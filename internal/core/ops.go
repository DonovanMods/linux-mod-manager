package core

import (
	"context"
	"sync"
)

// beginOp acquires the Service's single mutation slot, waiting until it is
// free or ctx is done. It returns the release func on success; release is
// idempotent, so a second call is a no-op rather than consuming the next
// caller's slot.
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
	if s.opSem == nil {
		panic("core: beginOp called on a Service with a nil opSem; construct it via NewService, not a struct literal")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.opSem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.opSem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

// completeDBWrite runs write - a DB mutation that COMPLETES a profile-file
// mutation the caller has ALREADY applied - under a context that cannot be
// cancelled, then reports the caller's own cancellation if there was one.
//
// Mirrors completeProfileWrite exactly, direction reversed: a re-link
// (ApplyRelinkMod) moves a mod to a new source_id/mod_id identity by first
// completing its profile-ref move (two completeProfileWrite calls dropping
// the old ref and writing the new one, once the OLD DB row is already
// deleted), then saving the NEW installed_mods row. A Ctrl-C landing after
// the profile move but before that save would leave the profile pointing at
// an identity with no DB row behind it - the same drift completeProfileWrite
// prevents, on the other side of the pair. write always runs under
// context.WithoutCancel(ctx) and always finishes; ctx.Err() is re-checked
// immediately afterwards and takes precedence over write's own error, so the
// caller still ends the run with context.Canceled (v2 Phase 3 Ruling 16,
// fix wave round 1's residual - see completeProfileWrite's own comment for
// the shared cancellation-precedence contract callers rely on).
func completeDBWrite(ctx context.Context, write func(context.Context) error) error {
	err := write(context.WithoutCancel(ctx))
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}
