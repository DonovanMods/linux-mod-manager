// Package core: this file is the acquire / decide / commit engine every
// install-shaped Apply runs its items through (#602, design
// docs/plans/2026-11-10-collections-design.md §1).
//
// The problem it solves: an Apply that downloads then writes used to treat
// each item independently - acquire item 1, write it, acquire item 2, write
// it - so a refusal discovered only after item N's download (an archive
// that needs a mod loader the game does not declare; from F2 on, a FOMOD
// with unanswered questions) left items 1..N-1 written. The user got a
// half-applied result, and the re-run failed checkPlanFresh with
// ErrStalePlan because the earlier items had moved the snapshot.
//
// The engine runs three phases instead:
//
//  1. ACQUIRE every item: download (or find cached), verify, extract and
//     lay out (acquire.go). Nothing is written to the cache, the DB or a
//     profile.
//  2. DECIDE: every acquire refusal the policy says blocks is collected,
//     and if there is any the Apply returns ONE error covering all of them
//     (*AcquireRefusedError), with nothing written. The plan is still
//     fresh, and the archives already fetched are retained
//     (retained_download.go's second reason), so the re-run is cheap.
//  3. COMMIT the items in order: cache, DB rows, profile, deploy - each
//     flow's own per-item code, under its existing Ruling 16 pairing.
//     An item whose acquire failed for a reason that does not block (a
//     download error, in an ordinary Apply) settles here as today's
//     per-item counted failure, in its place in the order.
//
// "NOTHING WRITTEN", precisely: no cache entry, no installed_mods row, no
// profile, no deployed file and no hook run. Two things acquire does
// persist, both deliberately. The archives a refused Apply kept for its
// re-run (retained_download.go), and - when a download fails because its
// source will only serve it by hand - the manual-only flag on that mod
// (noteDownloadOutcome, #543): a true fact about the SOURCE, learned once,
// which no installed row, profile or plan snapshot reads, so it leaves the
// plan fresh. A run refused at decide, or by a failing begin (an
// install.before_all hook), keeps its downloads; a cancelled one keeps
// nothing. Every item is released exactly once, a panicking commit
// included (runAcquire's deferred releaseAll), so no acquire tree or
// download staging directory outlives the run.
//
// MODE SELECTION. An install-shaped Apply runs PHASED, as above. A flow runs
// INTERLEAVED - acquire item i, settle it, then item i+1 - only by explicit
// declaration (acquireInterleaved), and exactly one flow declares it: the
// update batch, whose items are each re-planned against the state the
// previous items left (#324). Phasing it would plan item N against the state
// before the batch, which is wrong for an update. Any new install-shaped
// Apply is phased unless it can make the same case.
// TestAcquireModeSelection pins this.
//
// ON THE ENGINE TODAY: ApplyImport, ApplyInstall's batch path (a mod with
// dependencies, and PlanInstallMany's multi-select) and, interleaved, the
// update batch. Not yet: the converge loops of ApplyProfileSwitch and
// ApplyProfileApply, verify --fix's redownload, deploy's self-heal and
// rollback still fetch one file at a time through downloadMod (acquire and
// commit back to back, acquire.go), each with its own per-item loop. A
// single-mod install's STRICT path is one item, so a refusal there already
// writes nothing.
//
// WHAT BLOCKS. In an ordinary Apply only an ANSWERABLE refusal blocks: one
// the user answers and then re-runs the same command (answerableRefusal).
// Unanswerable failures keep their per-item counting, because blocking a
// whole profile import on one mod's flaky CDN would be a regression. In
// all-or-nothing mode (acquirePolicy.blockAll: a collection apply, C2,
// through ProfileImportOptions.AllOrNothing) every acquire failure blocks,
// because a half-applied collection is the outcome that looks like it
// worked.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// acquireMode is how the engine orders acquire against commit.
type acquireMode int

const (
	// acquirePhased acquires every item before committing any: the default.
	acquirePhased acquireMode = iota
	// acquireInterleaved settles each item before acquiring the next. Only
	// a flow whose items must see their predecessors' writes declares it
	// (the update batch, #324).
	acquireInterleaved
)

// acquirePolicy is one Apply's answer to "what does the engine do with an
// acquire refusal".
type acquirePolicy struct {
	mode acquireMode
	// blockAll makes every acquire failure block, not only answerable
	// ones (all-or-nothing; meaningless when interleaved).
	blockAll bool
}

// blocks reports whether an acquire failure stops the Apply at decide.
func (p acquirePolicy) blocks(err error) bool {
	return p.blockAll || answerableRefusal(err)
}

// answerableRefusal reports whether an acquire refusal is one the user
// answers before re-running the same Apply - so collecting it up front,
// with nothing written, is what helps them. Today that is the loader
// precondition (answered with `lmm game edit --loader`, or the web UI's game
// editor); F2 adds FOMOD questions.
func answerableRefusal(err error) bool {
	var loaderErr *LoaderRequiredError
	return errors.As(err, &loaderErr)
}

// AcquireRefusal is one item an install-shaped Apply refused in its acquire
// phase: the mod, as far as acquire resolved it, and why.
type AcquireRefusal struct {
	// SourceID and ModID identify the item.
	SourceID string
	ModID    string
	// Name is the mod's display name, empty when acquire refused before
	// the source named it.
	Name string
	// Err is the refusal itself.
	Err error
}

// AcquireRefusedError is the decide step's one error: the Apply refused
// with NOTHING written - no cache entry, no DB row, no profile change - so
// its plan is still fresh, and the archives already fetched are kept for the
// re-run (retained_download.go).
//
// It unwraps to the refusals' own errors, so a frontend that already
// recognises one (errors.As a *LoaderRequiredError, to print its setup
// steps) keeps working. When every refusal is the loader precondition they
// are merged into ONE *LoaderRequiredError naming every mod that needs the
// loader, since the remedy is the same single game edit.
type AcquireRefusedError struct {
	// Refusals lists every refused item, in item order.
	Refusals []AcquireRefusal
	err      error
}

// Error is the combined refusal's message.
func (e *AcquireRefusedError) Error() string { return e.err.Error() }

// Unwrap exposes the combined refusal (see the type's doc comment).
func (e *AcquireRefusedError) Unwrap() error { return e.err }

// newAcquireRefusedError combines refusals into the decide step's error.
func newAcquireRefusedError(refusals []AcquireRefusal) *AcquireRefusedError {
	return &AcquireRefusedError{Refusals: refusals, err: combineRefusals(refusals)}
}

func combineRefusals(refusals []AcquireRefusal) error {
	if merged := mergeLoaderRequirements(refusals); merged != nil {
		return merged
	}
	if len(refusals) == 1 {
		return refusals[0].Err
	}
	errs := make([]error, 0, len(refusals))
	for _, r := range refusals {
		errs = append(errs, fmt.Errorf("%s:%s: %w", r.SourceID, r.ModID, r.Err))
	}
	return errors.Join(errs...)
}

// mergeLoaderRequirements folds refusals that are ALL the same game's same
// loader requirement into one, naming every mod; nil otherwise. The first
// refusal's evidence, version and setup steps stand for the rest.
func mergeLoaderRequirements(refusals []AcquireRefusal) *LoaderRequiredError {
	var first *LoaderRequiredError
	names := make([]string, 0, len(refusals))
	for _, r := range refusals {
		var le *LoaderRequiredError
		if !errors.As(r.Err, &le) {
			return nil
		}
		if first == nil {
			first = le
		} else if le.GameID != first.GameID || le.Kind != first.Kind {
			return nil
		}
		names = append(names, cmpName(le.ModName, r))
	}
	if first == nil {
		return nil
	}
	if len(refusals) == 1 {
		return first
	}
	merged := *first
	merged.Mods = names
	merged.ModName = joinNames(names)
	return &merged
}

// cmpName is the name a loader refusal names its mod by, falling back to
// the item's own identity.
func cmpName(modName string, r AcquireRefusal) string {
	switch {
	case modName != "":
		return modName
	case r.Name != "":
		return r.Name
	default:
		return r.SourceID + ":" + r.ModID
	}
}

// joinNames spells a list of names as prose: "A", "A and B", "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// acquireFailure is one item's failed acquire.
type acquireFailure struct {
	// refusal identifies the item and carries the error the policy
	// classifies.
	refusal AcquireRefusal
	// settle records it as the flow's per-item failure (counting it and
	// emitting its events) when it does not block. May be nil.
	settle func()
	// release frees what the item acquired before it failed - a mod's
	// earlier files, when a later one refused - retaining it when blocked,
	// exactly as a clean item's release does. May be nil.
	release func(blocked bool)
	// abort, when set, stops the whole Apply with this error (a
	// cancellation the flow refuses to count as a per-item failure).
	abort error
}

// acquireRun is one Apply's items, as the engine drives them.
type acquireRun[T any] struct {
	policy acquirePolicy
	n      int
	// key, when set, names item i; an item whose key an earlier item
	// already had is skipped - never acquired, committed or reported - so
	// the first occurrence wins deterministically. Two items for one mod
	// would otherwise share one acquire tree (acquireTreePath) and one
	// cache entry.
	key func(i int) string
	// acquire fetches item i and writes nothing; a nil failure means the
	// staged value is ready to commit.
	acquire func(ctx context.Context, i int) (T, *acquireFailure)
	// begin runs once, after decide and before the first item settles
	// (the profile save an import makes first). May be nil.
	begin func(ctx context.Context) error
	// commit writes item i. A non-nil error stops the Apply with it.
	commit func(ctx context.Context, i int, staged T) error
	// release frees item i's staging; blocked asks it to retain what a
	// re-run would otherwise fetch again.
	release func(staged T, blocked bool)
}

type acquiredItem[T any] struct {
	index   int
	staged  T
	failure *acquireFailure
	done    bool
}

// acquireItems is the set of items one run has acquired, each released
// exactly once: when it settles, when the run is refused or stopped, or -
// whatever else happens, a panic in a commit included - by runAcquire's
// deferred releaseAll, so no acquire tree or staging directory outlives the
// run.
type acquireItems[T any] struct {
	r     *acquireRun[T]
	items []*acquiredItem[T]
}

func (a *acquireItems[T]) release(it *acquiredItem[T], blocked bool) {
	if it.done {
		return
	}
	it.done = true
	switch {
	case it.failure == nil:
		a.r.release(it.staged, blocked)
	case it.failure.release != nil:
		it.failure.release(blocked)
	}
}

func (a *acquireItems[T]) releaseAll(blocked bool) {
	for _, it := range a.items {
		a.release(it, blocked)
	}
}

// settle commits a clean item, or records a failed one as the flow's
// per-item failure, and releases it either way.
func (a *acquireItems[T]) settle(ctx context.Context, it *acquiredItem[T]) error {
	defer a.release(it, false)
	if it.failure != nil {
		if it.failure.settle != nil {
			it.failure.settle()
		}
		return nil
	}
	return a.r.commit(ctx, it.index, it.staged)
}

// runAcquire drives r through the engine (see the file's doc comment for
// the phases and the mode selection).
func runAcquire[T any](ctx context.Context, r acquireRun[T]) error {
	set := &acquireItems[T]{r: &r}
	defer set.releaseAll(false)

	seen := make(map[string]bool)
	duplicate := func(i int) bool {
		if r.key == nil {
			return false
		}
		k := r.key(i)
		if seen[k] {
			return true
		}
		seen[k] = true
		return false
	}

	if r.policy.mode == acquireInterleaved {
		return runInterleaved(ctx, r, set, duplicate)
	}

	var refusals []AcquireRefusal
	for i := 0; i < r.n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if duplicate(i) {
			continue
		}
		staged, failure := r.acquire(ctx, i)
		set.items = append(set.items, &acquiredItem[T]{index: i, staged: staged, failure: failure})
		if failure == nil {
			continue
		}
		if failure.abort != nil {
			return failure.abort
		}
		if r.policy.blocks(failure.refusal.Err) {
			refusals = append(refusals, failure.refusal)
		}
	}
	// A cancellation during the last item's acquire must stop the Apply as
	// a cancellation, not surface as that item's refusal.
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(refusals) > 0 {
		set.releaseAll(true)
		return newAcquireRefusedError(refusals)
	}

	if r.begin != nil {
		if err := r.begin(ctx); err != nil {
			// Nothing was written and the downloads are sound: a failing
			// install.before_all is fixed and re-run, and the re-run must
			// not fetch everything again.
			set.releaseAll(true)
			return err
		}
	}
	for _, it := range set.items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := set.settle(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

// runInterleaved settles each item before acquiring the next.
func runInterleaved[T any](ctx context.Context, r acquireRun[T], set *acquireItems[T], duplicate func(int) bool) error {
	if r.begin != nil {
		if err := r.begin(ctx); err != nil {
			return err
		}
	}
	for i := 0; i < r.n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if duplicate(i) {
			continue
		}
		staged, failure := r.acquire(ctx, i)
		it := &acquiredItem[T]{index: i, staged: staged, failure: failure}
		set.items = append(set.items, it)
		if failure != nil && failure.abort != nil {
			// Interleaved, the items before this one are written: the
			// failure is recorded like theirs before the run stops.
			if failure.settle != nil {
				failure.settle()
			}
			return failure.abort
		}
		if err := set.settle(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

// releaseAcquired releases a set of acquired files, retaining their network
// downloads first when the Apply was blocked.
func (s *Service) releaseAcquired(files []*acquiredFile, blocked bool) {
	for _, a := range files {
		if blocked {
			s.retainBlocked(a)
		}
		a.release()
	}
}

// releaseFilesFunc is acquireFailure.release for the files an item acquired
// before one of its later files failed.
func (s *Service) releaseFilesFunc(files []*acquiredFile) func(blocked bool) {
	if len(files) == 0 {
		return nil
	}
	return func(blocked bool) { s.releaseAcquired(files, blocked) }
}
