package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// beginOp acquires the Service's single mutation slot, waiting until it is
// free or ctx is done. It returns the release func on success; release is
// idempotent, so a second call is a no-op rather than consuming the next
// caller's slot.
//
// Since #317 the slot has TWO halves, taken in this order and released
// together: the in-process semaphore below, then - when the Service was
// given a ServiceConfig.OpLockPath - an advisory flock on that file, which
// is what stops a CLI mutation interleaving with a running `lmm serve`'s.
// Ordering matters: the cheap in-process wait happens first, so a process
// with several goroutines queues internally instead of every one of them
// holding an open descriptor and polling the kernel. A contended
// cross-process lock fails with OperationInProgressError after a bounded
// wait rather than queueing - a second lmm invocation says who holds it
// instead of appearing to hang. The Ruling 16 pairing is unchanged: the
// release func drops both halves, and the completeProfileWrite /
// completeDBWrite chains still run inside it.
//
// Reads never call this, so a query is never blocked by another process.
//
// Concurrency contract (see the Service doc comment): query methods may
// run concurrently with each other and with at most one in-flight
// mutation; mutating operations are serialized service-wide. Exported
// mutating methods acquire the slot; their unexported implementations do
// not, so flows can compose them without re-entering the semaphore.
//
// beginOp panics if s.opSem is nil - a Service built as a struct literal
// (common in internal tests that only need a few fields) rather than via
// NewService - so the failure is an immediate, clear panic instead of a
// hang until the test's 10-minute timeout.
func (s *Service) beginOp(ctx context.Context) (release func(), err error) {
	release, err = s.acquireOp(ctx)
	if err != nil {
		return nil, err
	}
	// #431 (fix round 2): a profile-document backfill still owed runs
	// FIRST, inside this slot. The mutation about to run can write the very
	// flag values the backfill reads as evidence - `lmm purge` clears
	// deployed on every row - so it must not get there before the evidence
	// has been read. Normally app.Open has already discharged it and this is
	// one atomic load; it only does work when that open could not (the lock
	// was held) or when a profile kept for later has changed. A failure
	// refuses the mutation rather than letting it run over unread evidence.
	if s.backfillPending.Load() {
		if _, err := s.dischargeProfileBackfill(ctx); err != nil {
			release()
			return nil, fmt.Errorf("recording mods disabled before the upgrade in their profile files: %w", err)
		}
	}
	// #466 review D3: what a flow's removals kept as the user's is that
	// flow's to know. A flow that never drains its originals store must not
	// hand the memory to the next one, which would write it back.
	releaseSlot := release
	return func() {
		s.forgetKeptFiles()
		releaseSlot()
	}, nil
}

// acquireOp is beginOp's slot acquisition alone - the in-process semaphore
// and the cross-process lock, waiting as a mutation does.
func (s *Service) acquireOp(ctx context.Context) (release func(), err error) {
	return s.acquireOpWithin(ctx, opLockWait)
}

// tryAcquireOp takes the slot only if it is free right now, and otherwise
// fails at once - with OperationInProgressError when another process holds
// the lock. For the one caller that must never make a command wait: the
// backfill an open runs (F5), which a mutation's own slot finishes anyway.
func (s *Service) tryAcquireOp(ctx context.Context) (release func(), err error) {
	return s.acquireOpWithin(ctx, 0)
}

// acquireOpWithin takes the in-process semaphore and then the cross-process
// lock, giving up on the lock after wait. A zero wait does not queue for
// the semaphore either.
func (s *Service) acquireOpWithin(ctx context.Context, wait time.Duration) (release func(), err error) {
	if s.opSem == nil {
		panic("core: beginOp called on a Service with a nil opSem; construct it via NewService, not a struct literal")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if wait == 0 {
		select {
		case s.opSem <- struct{}{}:
		default:
			return nil, ErrOperationInProgress
		}
	} else {
		select {
		case s.opSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// #336: every mutation invalidates the verify memo. Done HERE, at the
	// gate, rather than in each flow: a per-flow guess about what a given
	// mutation could have changed is a guess a flow added later gets wrong,
	// and a stale health verdict is exactly the kind of wrong that looks
	// right.
	s.dropVerifyMemo()

	var lock *opLock
	if s.opLockPath != "" {
		var err error
		if lock, err = acquireOpLock(ctx, s.opLockPath, wait); err != nil {
			<-s.opSem // never hold the in-process slot for a mutation that is not happening
			return nil, err
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			lock.release() // nil-safe: no lock path configured
			<-s.opSem
		})
	}, nil
}

// completeProfileWrite runs write - a profile-file mutation that COMPLETES a
// DB mutation the caller has ALREADY applied - under a context that cannot be
// cancelled, then reports the caller's own cancellation if there was one.
//
// The DB row and the profile ref are a two-step commit: an install writes the
// installed_mods row and then the profile's ModReference; an uninstall or a
// `purge --uninstall` deletes the row and then the ref. A Ctrl-C landing
// between the two steps used to abort the second one, leaving a mod in the
// database but absent from its profile YAML (or a ref pointing at a row that
// is already gone) - the drift Phase 1's "completion and recovery never
// inherit cancellation" rule exists to prevent. So write always runs under
// context.WithoutCancel(ctx) and always finishes.
//
// The cancellation is not swallowed: ctx.Err() is re-checked immediately
// afterwards and takes precedence over write's own error, so the caller still
// ends the run with context.Canceled and processes no further items. Callers
// tell the two apart by re-checking ctx.Err() in their error branch:
//
//	if err := completeProfileWrite(ctx, func(ctx context.Context) error {
//		return pm.UpsertMod(ctx, gameID, profile, ref)
//	}); err != nil {
//		if cerr := ctx.Err(); cerr != nil {
//			return result, cerr // fatal: stop the run right here
//		}
//		// ... this site's existing warning/note handling for err
//	}
//
// A non-cancellation failure is returned unchanged, so every site's existing
// warning/note text is byte-for-byte what it was (v2 Phase 3 Ruling 16).
func completeProfileWrite(ctx context.Context, write func(context.Context) error) error {
	err := write(context.WithoutCancel(ctx))
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// completeRename runs write - the profile-RENAME write chain - under a
// context that cannot be cancelled, then reports the caller's own
// cancellation if there was one.
//
// Rename is the one flow completeProfileWrite cannot express. It has three
// writes and no half that can safely finish independently: a rename
// that name the profile, and the removal of the old profile file. Stop
// after the first and the config directory holds two copies of one profile,
// both flagged default; stop after the second and the DB names a profile
// whose old file is still on disk. Only the whole chain is a consistent
// state, so the whole chain is what runs under context.WithoutCancel -
// exactly the property Ruling 16 asks for ("the DB half and the profile
// -file half must never end a cancelled run disagreeing"), applied to a
// three-step chain instead of a pair. Everything that can REFUSE a rename
// (an unknown source profile, an occupied target name, an invalid name)
// runs before the chain is entered, where cancellation is still free.
//
// ctx.Err() is re-checked immediately afterwards and takes precedence over
// write's own error, the same cancellation-precedence contract both
// siblings above give their callers.
func completeRename(ctx context.Context, write func(context.Context) error) error {
	err := write(context.WithoutCancel(ctx))
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}
