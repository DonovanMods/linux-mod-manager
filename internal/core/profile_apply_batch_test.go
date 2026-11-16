package core_test

// #663: `lmm profile apply`, `lmm profile switch` and `lmm profile sync`
// write their profile refs a batch at a time (#661's profileRefBatch), and
// apply's per-item ref write is completed under a cancellation as switch's
// always was (Ruling 16). These tests hold the three flows to it: a
// cancellation between an item's DB row and its ref, the save count (a
// ratchet that counts, never times), the lock refusal in the middle of a
// batch, and the document each flow ends with, which must be the per-item
// writes' to the byte.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelAtNthProfileWrite cancels itself when core reaches its nth profile
// ref write of a run - asked from a ProfileManager write's own guard
// (upsertModRef, AddMod, RemoveMod: before the write, where develop's
// apply asked it), from the batch's (before the write; a sync's, which
// runs under no completeProfileWrite), or from completeProfileWrite's re-check (after
// it). Each is reached once per item, so the cancellation lands on item n,
// after its DB row (apply, switch) and around its ref.
type cancelAtNthProfileWrite struct {
	context.Context
	cancel context.CancelFunc
	n      int32
	seen   atomic.Int32
	fired  atomic.Bool
}

func newCancelAtNthProfileWrite(n int32) *cancelAtNthProfileWrite {
	inner, cancel := context.WithCancel(context.Background())
	return &cancelAtNthProfileWrite{Context: inner, cancel: cancel, n: n}
}

func (c *cancelAtNthProfileWrite) Err() error {
	if pc, _, _, ok := runtime.Caller(1); ok {
		name := runtime.FuncForPC(pc).Name()
		for _, suffix := range []string{
			"core.completeProfileWrite",
			"core.(*ProfileManager).upsertModRef",
			"core.(*ProfileManager).AddMod",
			"core.(*ProfileManager).RemoveMod",
			"core.(*profileRefBatch).change",
		} {
			if strings.HasSuffix(name, suffix) {
				if c.seen.Add(1) == c.n && c.fired.CompareAndSwap(false, true) {
					c.cancel()
				}
				break
			}
		}
	}
	return c.Context.Err()
}

// applyFixture is an acquireFixture whose profile "default" (the active
// one) lists n plain mods, none installed.
func applyFixture(t *testing.T, prefix string, n int) (*acquireFixture, []string) {
	t.Helper()
	ids := modIDs(prefix, n)
	mods := make([]acquireMod, len(ids))
	refs := make([]domain.ModReference, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
		refs[i] = domain.ModReference{SourceID: "repo", ModID: id, Version: "1.0.0"}
	}
	fx := newAcquireFixture(t, mods...)
	require.NoError(t, config.SaveProfile(fx.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: fx.game.ID, IsDefault: true, Mods: refs}))
	return fx, ids
}

// TestApplyProfileApply_CancelledBetweenRowAndRef_EveryRowHasItsRef is
// #663's Ruling 16 gap: develop's apply wrote each item's ref with the
// flow's own ctx, so a cancellation landing after the item's DB row left
// the row without the version and files it installed recorded in the
// profile. The ref is now completed whatever the ctx says.
func TestApplyProfileApply_CancelledBetweenRowAndRef_EveryRowHasItsRef(t *testing.T) {
	fx, _ := applyFixture(t, "r", 8)
	plan, err := fx.svc.PlanProfileApply(context.Background(), fx.game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 8)

	ctx := newCancelAtNthProfileWrite(3)
	t.Cleanup(ctx.cancel)
	_, err = fx.svc.ApplyProfileApply(ctx, fx.game, plan, core.ProfileApplyOptions{}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, ctx.fired.Load(), "the cancellation landed on the third item's ref write")
	assert.Equal(t, 3, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "default"), "three rows written, each with its ref")
}

// TestApplyProfileSwitch_CancelledBetweenRowAndRef_EveryRowHasItsRef
// pins that switch has no such gap - its ref write was already completed
// under completeProfileWrite - and keeps it closed under the batch.
func TestApplyProfileSwitch_CancelledBetweenRowAndRef_EveryRowHasItsRef(t *testing.T) {
	fx, _ := switchFixture(t, "w", 8)
	plan, err := fx.svc.PlanProfileSwitch(context.Background(), fx.game, "target")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 8)

	ctx := newCancelAtNthProfileWrite(3)
	t.Cleanup(ctx.cancel)
	_, err = fx.svc.ApplyProfileSwitch(ctx, fx.game, plan, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, ctx.fired.Load())
	assert.Equal(t, 3, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "target"))
}

// switchFixture is an acquireFixture with an empty active profile
// "default" and a profile "target" listing n plain mods, none installed.
func switchFixture(t *testing.T, prefix string, n int) (*acquireFixture, []string) {
	t.Helper()
	ids := modIDs(prefix, n)
	mods := make([]acquireMod, len(ids))
	refs := make([]domain.ModReference, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
		refs[i] = domain.ModReference{SourceID: "repo", ModID: id, Version: "1.0.0"}
	}
	fx := newAcquireFixture(t, mods...)
	dir := fx.svc.ConfigDir()
	require.NoError(t, config.SaveProfile(dir, &domain.Profile{Name: "default", GameID: fx.game.ID, IsDefault: true}))
	require.NoError(t, config.SaveProfile(dir, &domain.Profile{Name: "target", GameID: fx.game.ID, Mods: refs}))
	return fx, ids
}

// syncFixture is an applyFixture whose n mods are installed, then dropped
// from the profile document, so a sync has n refs to add.
func syncFixture(t *testing.T, prefix string, n int) (*acquireFixture, []string) {
	t.Helper()
	fx, ids := applyFixture(t, prefix, n)
	plan, err := fx.svc.PlanProfileApply(context.Background(), fx.game, "default")
	require.NoError(t, err)
	_, err = fx.svc.ApplyProfileApply(context.Background(), fx.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, config.SaveProfile(fx.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: fx.game.ID, IsDefault: true}))
	return fx, ids
}

// TestApplyProfileSync_CancelledMidBatch_WhatItCountedIsOnDisk: a sync
// writes no DB row, so Ruling 16 has no pair to split; what it owes a
// cancellation is that every ref it counted is on disk - the batch is
// saved when the loop stops - and nothing it did not count is.
func TestApplyProfileSync_CancelledMidBatch_WhatItCountedIsOnDisk(t *testing.T) {
	fx, ids := syncFixture(t, "y", 8)
	plan, err := fx.svc.PlanProfileSync(context.Background(), fx.game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToAdd, 8)

	ctx := newCancelAtNthProfileWrite(4)
	t.Cleanup(ctx.cancel)
	result, err := fx.svc.ApplyProfileSync(ctx, fx.game, plan, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, ctx.fired.Load())
	saved, err := config.LoadProfile(fx.svc.ConfigDir(), fx.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, 3, result.Added, "three refs added before the cancellation")
	assert.Len(t, saved.Mods, result.Added, "every ref the sync counted is on disk, and no other")
	for _, ref := range saved.Mods {
		assert.Contains(t, ids, ref.ModID)
	}
}

// TestApplyProfileApply_ProfileSavesAreBatched is #663's ratchet: a
// 300-item fresh apply saves its profile once per batch of refs (and a
// few times around the loop), not once per item.
func TestApplyProfileApply_ProfileSavesAreBatched(t *testing.T) {
	fx, _ := applyFixture(t, "a", 300)
	plan, err := fx.svc.PlanProfileApply(context.Background(), fx.game, "default")
	require.NoError(t, err)
	before := fx.svc.ProfileSavesForTest()
	result, err := fx.svc.ApplyProfileApply(context.Background(), fx.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 300, result.Installed)
	assert.LessOrEqual(t, fx.svc.ProfileSavesForTest()-before, maxBatchedSaves(300, 2))
	assert.Equal(t, 300, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "default"))
}

// TestApplyProfileSwitch_ProfileSavesAreBatched is the ratchet over a
// switch: the target's refs a batch at a time, then the active mark on
// both profiles.
func TestApplyProfileSwitch_ProfileSavesAreBatched(t *testing.T) {
	fx, _ := switchFixture(t, "s", 300)
	plan, err := fx.svc.PlanProfileSwitch(context.Background(), fx.game, "target")
	require.NoError(t, err)
	before := fx.svc.ProfileSavesForTest()
	result, err := fx.svc.ApplyProfileSwitch(context.Background(), fx.game, plan, nil)
	require.NoError(t, err)
	require.Equal(t, 300, result.Installed)
	assert.LessOrEqual(t, fx.svc.ProfileSavesForTest()-before, maxBatchedSaves(300, 3))
	assert.Equal(t, 300, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "target"))
}

// TestApplyProfileSync_ProfileSavesAreBatched is the ratchet over a sync
// that adds 300 refs.
func TestApplyProfileSync_ProfileSavesAreBatched(t *testing.T) {
	fx, _ := syncFixture(t, "y", 300)
	plan, err := fx.svc.PlanProfileSync(context.Background(), fx.game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToAdd, 300)
	before := fx.svc.ProfileSavesForTest()
	result, err := fx.svc.ApplyProfileSync(context.Background(), fx.game, plan, nil)
	require.NoError(t, err)
	require.Equal(t, 300, result.Added)
	assert.LessOrEqual(t, fx.svc.ProfileSavesForTest()-before, maxBatchedSaves(300, 1))
	assert.Equal(t, 300, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "default"))
}

// mixedRefs is the equivalence fixture's document: plain mods, some marked
// as a collection's, FOMOD mods with recorded choices, a ref locked at the
// version it installs, a ref locked at no version (an install refuses to
// record over it, #143/#294), a disabled ref and one the source does not
// have - interleaved, so every batch holds several kinds.
func mixedRefs() []domain.ModReference {
	var refs []domain.ModReference
	for i := range 40 {
		id := fmt.Sprintf("p%02d", i)
		ref := domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}
		switch i % 8 {
		case 1:
			ref.FromCollection = true
		case 2:
			ref.ModID = fmt.Sprintf("f%02d", i)
			ref.Fomod = answer("2K")
		case 3:
			ref.Locked = true
		case 5:
			ref.Disabled = true
		}
		refs = append(refs, ref)
		if i == 13 {
			refs = append(refs, domain.ModReference{SourceID: "src", ModID: "locked-elsewhere", Locked: true})
		}
		if i == 22 {
			refs = append(refs, domain.ModReference{SourceID: "src", ModID: "gone", Version: "1.0"})
		}
	}
	return refs
}

// newMixedFixture is a fomodFixture serving every mod mixedRefs names
// but "gone", with the active profile "default" empty.
func newMixedFixture(t *testing.T) *fomodFixture {
	t.Helper()
	f := newFomodFixture(t)
	for _, ref := range mixedRefs() {
		switch {
		case ref.ModID == "gone":
		case strings.HasPrefix(ref.ModID, "f"):
			f.serve(t, ref.ModID, "1.0", "4K", ref.ModID)
		default:
			registerDownloadableMod(t, f.mock, &domain.Mod{ID: ref.ModID, SourceID: "src", Name: "Mod " + ref.ModID, Version: "1.0", GameID: "g1"},
				ref.ModID+".esp", "payload "+ref.ModID)
		}
	}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: "g1", IsDefault: true}))
	return f
}

func profileBytes(t *testing.T, svc *core.Service, gameID, profile string) string {
	t.Helper()
	path, err := config.ProfilePath(svc.ConfigDir(), gameID, profile)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestApplyProfileApply_BatchedDocumentIsThePerItemOne holds apply to the
// per-item writes: the mixed document applied with a batch of 1 and in
// batches leaves the same profile file to the byte, and the same warnings.
func TestApplyProfileApply_BatchedDocumentIsThePerItemOne(t *testing.T) {
	run := func(items int) (string, []string) {
		f := newMixedFixture(t)
		f.svc.SetRefBatchItemsForTest(items)
		require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: "g1", IsDefault: true, Mods: mixedRefs()}))
		plan, err := f.svc.PlanProfileApply(context.Background(), f.game, "default")
		require.NoError(t, err)
		result, err := f.svc.ApplyProfileApply(context.Background(), f.game, plan, core.ProfileApplyOptions{}, nil)
		require.ErrorAs(t, err, new(*core.ProfileApplyIncompleteError), "the mod the source does not have fails")
		return profileBytes(t, f.svc, "g1", "default"), result.Warnings
	}
	perItem, perItemWarnings := run(1)
	batched, batchedWarnings := run(0)
	assert.Equal(t, perItem, batched)
	assert.Equal(t, perItemWarnings, batchedWarnings)
	assert.Contains(t, batched, "name: 2K", "sanity: the FOMOD refs' recorded choices are in the document")
}

// TestApplyProfileSwitch_BatchedDocumentIsThePerItemOne is the same for a
// switch into the mixed document.
func TestApplyProfileSwitch_BatchedDocumentIsThePerItemOne(t *testing.T) {
	run := func(items int) (string, []string) {
		f := newMixedFixture(t)
		f.svc.SetRefBatchItemsForTest(items)
		require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "target", GameID: "g1", Mods: mixedRefs()}))
		plan, err := f.svc.PlanProfileSwitch(context.Background(), f.game, "target")
		require.NoError(t, err)
		result, err := f.svc.ApplyProfileSwitch(context.Background(), f.game, plan, nil)
		require.Error(t, err, "the mod the source does not have fails")
		require.NotEmpty(t, result.Failed)
		return profileBytes(t, f.svc, "g1", "target") + "\n---\n" + profileBytes(t, f.svc, "g1", "default"), result.Warnings
	}
	perItem, perItemWarnings := run(1)
	batched, batchedWarnings := run(0)
	assert.Equal(t, perItem, batched)
	assert.Equal(t, perItemWarnings, batchedWarnings)
}

// TestApplyProfileSync_BatchedDocumentIsThePerItemOne is the same for a
// sync with all three buckets: refs to add (installed, not listed), to
// remove (listed, no row, not disabled), and to update (listed without
// the files their row installed) - one of them locked at another version,
// which refuses.
func TestApplyProfileSync_BatchedDocumentIsThePerItemOne(t *testing.T) {
	run := func(items int) (string, []string) {
		f := newMixedFixture(t)
		ctx := context.Background()
		require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: "g1", IsDefault: true, Mods: mixedRefs()}))
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		_, _ = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)

		// The document drifts from the rows.
		doc, err := config.LoadProfile(f.svc.ConfigDir(), "g1", "default")
		require.NoError(t, err)
		var kept []domain.ModReference
		for i, ref := range doc.Mods {
			switch {
			case i%3 == 0 && !ref.Disabled:
				continue // to add
			case i%3 == 1:
				ref.FileIDs = nil // to update
				if i == 4 {
					ref.Locked, ref.Version = true, "0.9" // refuses
				}
			}
			kept = append(kept, ref)
		}
		kept = append(kept, domain.ModReference{SourceID: "src", ModID: "stray-1"}, domain.ModReference{SourceID: "src", ModID: "stray-2", Version: "2"})
		doc.Mods = kept
		require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), doc))

		f.svc.SetRefBatchItemsForTest(items)
		syncPlan, err := f.svc.PlanProfileSync(ctx, f.game, "default")
		require.NoError(t, err)
		require.NotEmpty(t, syncPlan.ToAdd)
		require.NotEmpty(t, syncPlan.ToRemove)
		require.NotEmpty(t, syncPlan.ToUpdate)
		result, err := f.svc.ApplyProfileSync(ctx, f.game, syncPlan, nil)
		require.NoError(t, err)
		return profileBytes(t, f.svc, "g1", "default"), result.Warnings
	}
	perItem, perItemWarnings := run(1)
	batched, batchedWarnings := run(0)
	assert.Equal(t, perItem, batched)
	assert.Equal(t, perItemWarnings, batchedWarnings)
	require.NotEmpty(t, batchedWarnings, "the locked ref refused its update")
}

// TestApplyProfileApply_LockRefusalMidBatch is #143/#294's refusal inside
// a batch: a ref locked at no version sits between plain mods; its install
// is recorded as a warning and its ref left as it was, while the refs on
// either side of it in the same batch are written.
func TestApplyProfileApply_LockRefusalMidBatch(t *testing.T) {
	fx, ids := applyFixture(t, "k", 6)
	doc, err := config.LoadProfile(fx.svc.ConfigDir(), fx.game.ID, "default")
	require.NoError(t, err)
	doc.Mods[3].Version, doc.Mods[3].Locked = "", true
	require.NoError(t, config.SaveProfile(fx.svc.ConfigDir(), doc))

	plan, err := fx.svc.PlanProfileApply(context.Background(), fx.game, "default")
	require.NoError(t, err)
	before := fx.svc.ProfileSavesForTest()
	result, err := fx.svc.ApplyProfileApply(context.Background(), fx.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 6, result.Installed, "the refusal does not fail the install")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "could not update profile: ")
	assert.Contains(t, result.Warnings[0], "is locked at v")
	assert.LessOrEqual(t, fx.svc.ProfileSavesForTest()-before, int64(3), "one batch, not a save per item")

	saved, err := config.LoadProfile(fx.svc.ConfigDir(), fx.game.ID, "default")
	require.NoError(t, err)
	for i, id := range ids {
		ref := saved.FindRef("repo", id)
		require.NotNil(t, ref)
		if i == 3 {
			assert.Empty(t, ref.Version, "the locked ref is left unwritten")
			assert.Empty(t, ref.FileIDs)
			assert.True(t, ref.Locked)
			continue
		}
		assert.Equal(t, []string{id + "-main"}, ref.FileIDs, "%s's ref is written", id)
	}
}

// TestApplyProfileSwitch_LockRefusalMidBatch is the same through a switch.
func TestApplyProfileSwitch_LockRefusalMidBatch(t *testing.T) {
	fx, ids := switchFixture(t, "q", 6)
	doc, err := config.LoadProfile(fx.svc.ConfigDir(), fx.game.ID, "target")
	require.NoError(t, err)
	doc.Mods[2].Version, doc.Mods[2].Locked = "", true
	require.NoError(t, config.SaveProfile(fx.svc.ConfigDir(), doc))

	plan, err := fx.svc.PlanProfileSwitch(context.Background(), fx.game, "target")
	require.NoError(t, err)
	before := fx.svc.ProfileSavesForTest()
	result, err := fx.svc.ApplyProfileSwitch(context.Background(), fx.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 6, result.Installed)
	assert.LessOrEqual(t, fx.svc.ProfileSavesForTest()-before, int64(3), "one batch and the active mark, not a save per item")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "is locked at v")

	saved, err := config.LoadProfile(fx.svc.ConfigDir(), fx.game.ID, "target")
	require.NoError(t, err)
	for i, id := range ids {
		ref := saved.FindRef("repo", id)
		require.NotNil(t, ref)
		if i == 2 {
			assert.Empty(t, ref.Version)
			assert.True(t, ref.Locked)
			continue
		}
		assert.Equal(t, []string{id + "-main"}, ref.FileIDs, "%s's ref is written", id)
	}
	assert.True(t, saved.IsDefault, "the switch's active mark, written after the batch, is not overwritten by it")
}

// TestProfileApplyBookkeepingTiming is #663's measurement, not a test:
// with LMM_PERF_663 set it times a fresh apply, a switch and a sync of n
// plain mods from a local manifest source and logs each run's duration
// and profile saves. Run it without -race:
//
//	LMM_PERF_663=1 go test ./internal/core -run TestProfileApplyBookkeepingTiming -v
func TestProfileApplyBookkeepingTiming(t *testing.T) {
	if os.Getenv("LMM_PERF_663") == "" {
		t.Skip("set LMM_PERF_663=1 to measure")
	}
	for _, n := range []int{150, 300, 600} {
		fx, _ := applyFixture(t, "a", n)
		plan, err := fx.svc.PlanProfileApply(context.Background(), fx.game, "default")
		require.NoError(t, err)
		before, start := fx.svc.ProfileSavesForTest(), time.Now()
		_, err = fx.svc.ApplyProfileApply(context.Background(), fx.game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		t.Logf("apply items=%d %.2fs saves=%d", n, time.Since(start).Seconds(), fx.svc.ProfileSavesForTest()-before)

		require.NoError(t, config.SaveProfile(fx.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: fx.game.ID, IsDefault: true}))
		syncPlan, err := fx.svc.PlanProfileSync(context.Background(), fx.game, "default")
		require.NoError(t, err)
		before, start = fx.svc.ProfileSavesForTest(), time.Now()
		_, err = fx.svc.ApplyProfileSync(context.Background(), fx.game, syncPlan, nil)
		require.NoError(t, err)
		t.Logf("sync  items=%d %.2fs saves=%d", n, time.Since(start).Seconds(), fx.svc.ProfileSavesForTest()-before)

		sx, _ := switchFixture(t, "s", n)
		switchPlan, err := sx.svc.PlanProfileSwitch(context.Background(), sx.game, "target")
		require.NoError(t, err)
		before, start = sx.svc.ProfileSavesForTest(), time.Now()
		_, err = sx.svc.ApplyProfileSwitch(context.Background(), sx.game, switchPlan, nil)
		require.NoError(t, err)
		t.Logf("switch items=%d %.2fs saves=%d", n, time.Since(start).Seconds(), sx.svc.ProfileSavesForTest()-before)
	}
}
