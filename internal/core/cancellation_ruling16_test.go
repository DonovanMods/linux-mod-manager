package core_test

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errCallerIsCompletingProfileWrite reports whether the caller of the current
// Err() method (one frame further up, i.e. two frames from HERE) is the place
// a completing profile write observes cancellation - core.completeProfileWrite
// AFTER the write has run (v2 Phase 3 Ruling 16), or, before that ruling, the
// ProfileManager mutator's OWN guard, which fired BEFORE the write. Naming
// both is what lets one test bracket the fix: the trigger lands at the same
// point in the flow either way, and only the resulting on-disk state differs.
func errCallerIsCompletingProfileWrite() bool {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return false
	}
	name := runtime.FuncForPC(pc).Name()
	return strings.HasSuffix(name, "core.completeProfileWrite") ||
		strings.HasSuffix(name, "core.(*ProfileManager).RemoveMod")
}

// cancelAtCompletingProfileWrite is live until core reaches the first
// completing profile write of a run (see errCallerIsCompletingProfileWrite),
// then cancels itself - putting the cancellation exactly in the window
// Ruling 16 is about: after the DB mutation has committed, before the profile
// file agrees with it. Safe for the concurrent Err() calls database/sql's own
// (*Rows).awaitDone watcher makes: those never match the caller test, and the
// fired flag is atomic.
type cancelAtCompletingProfileWrite struct {
	context.Context
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *cancelAtCompletingProfileWrite) Err() error {
	if errCallerIsCompletingProfileWrite() && c.fired.CompareAndSwap(false, true) {
		c.cancel()
	}
	return c.Context.Err()
}

// TestService_PurgeProfile_CancellationBetweenRecordDeleteAndProfileRefRemoval
// is the class-(A) shape test for v2 Phase 3 Ruling 16: a `purge --uninstall`
// deletes each mod's DB record and then its profile ref, and a cancellation
// arriving between those two steps must not leave them disagreeing.
//
// One test covers the shape, not the eleven sites that share it: the property
// is completeProfileWrite's, and every site calls it the same way.
//
// Against 85a3ed0 this fails on the profile-ref assertion - RemoveMod's own
// ctx.Err() guard refused the write, so mod A's record was gone while its ref
// remained (the call still ended in context.Canceled there, via the next
// iteration's loop-top check, so that assertion alone would not have caught
// the drift).
func TestService_PurgeProfile_CancellationBetweenRecordDeleteAndProfileRefRemoval(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")
	installSeededMod(t, svc, game, "a")
	installSeededMod(t, svc, game, "b")

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 2)
	first, second := mods[0], mods[1]

	inner, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelAtCompletingProfileWrite{Context: inner, cancel: cancel}

	result, err := svc.PurgeProfile(ctx, game, "default", mods, core.PurgeOptions{Uninstall: true}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "the run must end with the cancellation, not absorb it")
	require.True(t, ctx.fired.Load(), "the cancellation must have landed on a completing profile write")
	require.NotNil(t, result)

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)

	_, err = svc.GetInstalledMod(context.Background(), first.SourceID, first.ID, "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "the record delete committed before the cancellation")
	assert.Nil(t, profile.FindRef(first.SourceID, first.ID),
		"Ruling 16 (A): the profile write completing that delete must finish under context.WithoutCancel")
	assert.Empty(t, result.Notes, "the cancellation must not be reported as a per-mod business note")

	_, err = svc.GetInstalledMod(context.Background(), second.SourceID, second.ID, "g1", "default")
	assert.NoError(t, err, "the next mod must not be processed after the cancellation")
	assert.NotNil(t, profile.FindRef(second.SourceID, second.ID),
		"the next mod must not be processed after the cancellation")
}

// TestService_ApplyRelinkMod_CancellationBetweenDBDeleteAndProfileMove is the
// residual class-(A) shape for v2 Phase 3 Ruling 16, left open at fix wave
// round 1's "Left for the coordinator" (deviation 3): a re-link deletes the
// old DB row, then moves the profile ref, then saves the new DB row - one
// three-step commit once the old row is gone. Reuses
// cancelAtCompletingProfileWrite unmodified: the re-link branch's FIRST
// completeProfileWrite call is the pair's RemoveMod(old ref), which runs
// immediately after the DB delete with no core-originated ctx.Err() check in
// between, so the harness's existing trigger point already lands exactly
// where this test needs it.
//
// Against 611b00d this fails on the new-DB-row assertion: the profile pair
// finishes under WithoutCancel (round 1's fix), but the run then returns
// before the new DB row is ever saved - the profile agrees with the NEW
// identity while the DB has NEITHER identity's row.
func TestService_ApplyRelinkMod_CancellationBetweenDBDeleteAndProfileMove(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	game.SourceIDs["src"] = game.ID // relink target must be a configured source

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "src", "b")
	require.NoError(t, err)

	inner, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelAtCompletingProfileWrite{Context: inner, cancel: cancel}

	result, err := svc.ApplyRelinkMod(ctx, game, plan, core.RelinkOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "the run must end with the cancellation, not absorb it")
	require.True(t, ctx.fired.Load(), "the cancellation must have landed on a completing profile write")
	assert.Nil(t, result)

	_, err = svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "the old identity's DB row must be gone")

	moved, err := svc.GetInstalledMod(context.Background(), "src", "b", game.ID, "default")
	require.NoError(t, err, "Ruling 16 (A): the new DB row must be saved even though the run was cancelled")
	assert.Equal(t, "1.5", moved.Version)

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Nil(t, prof.FindRef("src", "a"), "the old profile ref must be removed")
	require.NotNil(t, prof.FindRef("src", "b"), "the new profile ref must exist, agreeing with the DB")
}

// TestService_ApplyRelinkMod_CancelledBeforeApply_LeavesEverythingUntouched
// is the bracketing counterpart: a cancellation that lands before the run
// ever starts (beginOp's own guard) must not touch the old DB row, the new
// identity, or the profile at all.
func TestService_ApplyRelinkMod_CancelledBeforeApply_LeavesEverythingUntouched(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	game.SourceIDs["src"] = game.ID

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "src", "b")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.ApplyRelinkMod(ctx, game, plan, core.RelinkOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)

	_, err = svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	assert.NoError(t, err, "a cancellation before the run starts must not touch the old record")

	_, err = svc.GetInstalledMod(context.Background(), "src", "b", game.ID, "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "the new identity must not have been created")

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NotNil(t, prof.FindRef("src", "a"), "the old profile ref must be untouched")
	assert.Nil(t, prof.FindRef("src", "b"))
}
