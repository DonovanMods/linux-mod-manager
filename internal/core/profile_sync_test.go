package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for PlanProfileSync/ApplyProfileSync - the core twin of the CLI's
// `lmm profile sync` (v2 Phase 2 Unit J Task 15, #290). cmd/lmm's
// TestDoProfileSync_* tests keep pinning the printed lines; these pin the
// classification, the end state, the event stream and the plan-staleness
// guard.

// newSyncTestService builds a service plus a game with an empty "default"
// profile - the shape every test below starts from.
func newSyncTestService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	return svc, game
}

// seedSyncInstalledMod saves an installed-mod DB row with full control over
// Name/ProfileName/FileIDs - the plan's diff and its Names map depend on all
// three, unlike the package's generic seedInstalledMod (which always uses
// "default"/"Test Mod"/no FileIDs).
func seedSyncInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version, profileName string, enabled bool, fileIDs []string) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: version, GameID: game.ID},
		ProfileName:  profileName,
		Enabled:      enabled,
		FileIDs:      fileIDs,
		UpdatePolicy: domain.UpdateNotify,
	}))
}

func TestPlanProfileSync_NoChanges(t *testing.T) {
	svc, game := newSyncTestService(t)

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)
	assert.False(t, plan.Missing)
	assert.Empty(t, plan.ToAdd)
	assert.Empty(t, plan.ToRemove)
	assert.Empty(t, plan.ToUpdate)
	assert.Equal(t, "g1", plan.GameID)
	assert.Equal(t, "default", plan.Profile)
}

// TestPlanProfileSync_ClassifiesAddRemoveUpdate covers all three buckets at
// once: enabled-but-unlisted (add), listed-but-not-enabled (remove), and
// listed-and-enabled but missing the profile-side FileIDs (update).
func TestPlanProfileSync_ClassifiesAddRemoveUpdate(t *testing.T) {
	svc, game := newSyncTestService(t)
	pm := svc.NewProfileManager()

	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	seedSyncInstalledMod(t, svc, game, "src", "upd1", "Upd One", "1.0", "default", true, []string{"main"})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "upd1", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "rem1", Version: "1.0"}))

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)

	require.Len(t, plan.ToAdd, 1)
	assert.Equal(t, "add1", plan.ToAdd[0].ModID)
	require.Len(t, plan.ToRemove, 1)
	assert.Equal(t, "rem1", plan.ToRemove[0].ModID)
	require.Len(t, plan.ToUpdate, 1)
	assert.Equal(t, "upd1", plan.ToUpdate[0].ModID)
	assert.Equal(t, []string{"main"}, plan.ToUpdate[0].FileIDs)
}

// TestPlanProfileSync_BucketsAreDeterministicallyOrdered pins Ruling 4
// (#298): ToAdd follows the order GetInstalledMods returned it in
// (installed_at, i.e. seed order here - deliberately NOT alphabetical, to
// prove this isn't accidentally sorted by ModID, and deliberately NOT
// matching profile order since ToAdd entries have none). ToUpdate and
// ToRemove are both subsets of the profile's own mod list, so both follow
// profile.Mods' own order (AddMod append order) - each is seeded with its
// installed_at order deliberately reversed relative to its profile order,
// so the assertion can only pass under profile-order semantics, not
// installed-order ones. Before Ruling 4, all three buckets were built by
// ranging Go maps keyed by domain.ModKey, so the order was whatever that
// run's map iteration produced.
func TestPlanProfileSync_BucketsAreDeterministicallyOrdered(t *testing.T) {
	svc, game := newSyncTestService(t)
	pm := svc.NewProfileManager()

	// ToAdd: enabled, installed, not in the profile - seeded in a
	// deliberately non-alphabetical order.
	seedSyncInstalledMod(t, svc, game, "src", "zulu", "Zulu", "1.0", "default", true, nil)
	seedSyncInstalledMod(t, svc, game, "src", "alpha", "Alpha", "1.0", "default", true, nil)
	seedSyncInstalledMod(t, svc, game, "src", "mike", "Mike", "1.0", "default", true, nil)

	// ToUpdate: enabled, installed with FileIDs, present in the profile
	// without FileIDs - installed before yankee, but listed after yankee
	// in the profile, so installed order and profile order disagree.
	seedSyncInstalledMod(t, svc, game, "src", "bravo", "Bravo", "1.0", "default", true, []string{"main"})
	seedSyncInstalledMod(t, svc, game, "src", "yankee", "Yankee", "1.0", "default", true, []string{"main"})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "yankee", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "bravo", Version: "1.0"}))

	// ToRemove: listed in the profile, not enabled in the DB - each was
	// installed-then-disabled (so it carries an installed_at) with
	// installed order and profile order likewise disagreeing: delta was
	// installed before romeo, but romeo is listed first in the profile.
	seedSyncInstalledMod(t, svc, game, "src", "delta", "Delta", "1.0", "default", false, nil)
	seedSyncInstalledMod(t, svc, game, "src", "romeo", "Romeo", "1.0", "default", false, nil)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "romeo", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "delta", Version: "1.0"}))

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)

	requireModIDOrder := func(t *testing.T, refs []domain.ModReference, want []string) {
		t.Helper()
		require.Len(t, refs, len(want))
		got := make([]string, len(refs))
		for i, r := range refs {
			got[i] = r.ModID
		}
		assert.Equal(t, want, got)
	}

	requireModIDOrder(t, plan.ToAdd, []string{"zulu", "alpha", "mike"})
	requireModIDOrder(t, plan.ToUpdate, []string{"yankee", "bravo"})
	requireModIDOrder(t, plan.ToRemove, []string{"romeo", "delta"})
}

// TestPlanProfileSync_Names_OnlyForAddAndUpdate pins that Names resolves a
// display name for ToAdd/ToUpdate entries only - doProfileSync never looked
// up a name for a ToRemove entry (it only ever had the profile ref).
func TestPlanProfileSync_Names_OnlyForAddAndUpdate(t *testing.T) {
	svc, game := newSyncTestService(t)
	pm := svc.NewProfileManager()

	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "rem1", Version: "1.0"}))

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)

	assert.Equal(t, "Add One", plan.Names[domain.ModKey("src", "add1")])
	_, hasRemoveName := plan.Names[domain.ModKey("src", "rem1")]
	assert.False(t, hasRemoveName, "a ToRemove entry must never get a Names lookup")
}

// TestPlanProfileSync_MissingProfile_ComputesAsEmptyWithoutCreating pins
// Missing: PlanProfileSync notes a nonexistent profile.yaml but does NOT
// create it (creation is ApplyProfileSync's job) - the diff is computed as
// if the profile were empty.
func TestPlanProfileSync_MissingProfile_ComputesAsEmptyWithoutCreating(t *testing.T) {
	svc, game := newSyncTestService(t)
	seedSyncInstalledMod(t, svc, game, "src", "auto1", "New Auto", "1.0", "newprof", true, nil)

	pm := svc.NewProfileManager()
	_, err := pm.Get(game.ID, "newprof")
	require.Error(t, err, "precondition: newprof must not exist yet")

	plan, err := svc.PlanProfileSync(context.Background(), game, "newprof")
	require.NoError(t, err)
	assert.True(t, plan.Missing)
	require.Len(t, plan.ToAdd, 1)
	assert.Equal(t, "auto1", plan.ToAdd[0].ModID)

	_, err = pm.Get(game.ID, "newprof")
	assert.Error(t, err, "PlanProfileSync must not have created the profile")
}

func TestApplyProfileSync_CreatesMissingProfileThenAdds(t *testing.T) {
	svc, game := newSyncTestService(t)
	seedSyncInstalledMod(t, svc, game, "src", "auto1", "New Auto", "1.0", "newprof", true, nil)

	plan, err := svc.PlanProfileSync(context.Background(), game, "newprof")
	require.NoError(t, err)
	require.True(t, plan.Missing)

	result, err := svc.ApplyProfileSync(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Added)

	pm := svc.NewProfileManager()
	profile, err := pm.Get(game.ID, "newprof")
	require.NoError(t, err, "ApplyProfileSync must have created the profile")
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "auto1", profile.Mods[0].ModID)
}

// TestApplyProfileSync_CreatesMissingProfile_EvenWithEmptyDiff pins that
// creation is NOT gated by the diff being non-empty (review Important #1 on
// Task 15, #290): the pre-lift engine created a missing profile
// unconditionally, before it ever computed a diff, so cmd's early-return on
// an empty plan must still be able to reach this creation.
func TestApplyProfileSync_CreatesMissingProfile_EvenWithEmptyDiff(t *testing.T) {
	svc, game := newSyncTestService(t)

	plan, err := svc.PlanProfileSync(context.Background(), game, "newprof")
	require.NoError(t, err)
	require.True(t, plan.Missing)
	assert.Empty(t, plan.ToAdd)
	assert.Empty(t, plan.ToRemove)
	assert.Empty(t, plan.ToUpdate)

	result, err := svc.ApplyProfileSync(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Zero(t, result.Added)

	pm := svc.NewProfileManager()
	profile, err := pm.Get(game.ID, "newprof")
	require.NoError(t, err, "ApplyProfileSync must create the profile even when nothing needs syncing")
	assert.Empty(t, profile.Mods)
}

// TestApplyProfileSync_AppliesAllThreeBuckets drives a plan with one entry
// in each bucket through Apply and checks the resulting profile.yaml and
// result counts.
func TestApplyProfileSync_AppliesAllThreeBuckets(t *testing.T) {
	svc, game := newSyncTestService(t)
	pm := svc.NewProfileManager()

	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	seedSyncInstalledMod(t, svc, game, "src", "upd1", "Upd One", "1.0", "default", true, []string{"main"})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "upd1", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "rem1", Version: "1.0"}))

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)

	result, err := svc.ApplyProfileSync(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Added)
	assert.Equal(t, 1, result.Removed)
	assert.Equal(t, 1, result.Updated)
	assert.Empty(t, result.Warnings)

	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)

	byID := map[string]domain.ModReference{}
	for _, m := range profile.Mods {
		byID[m.ModID] = m
	}
	_, hasRemoved := byID["rem1"]
	assert.False(t, hasRemoved, "rem1 must have been removed")
	require.Contains(t, byID, "add1")
	require.Contains(t, byID, "upd1")
	assert.Equal(t, []string{"main"}, byID["upd1"].FileIDs)
}

// TestApplyProfileSync_UpsertMod_LockedRefRefusalIsWarning is #294
// (Ruling 5), the Phase 3 behaviour fix ruling 9 deferred: a LOCKED profile
// ref makes the toUpdate loop's UpsertMod refuse; the refusal now surfaces
// as a SyncUpdateWarning event AND on Result.Warnings (so the CLI prints it
// unconditionally) instead of the --verbose-only SyncUpdateNote it used to
// be. The FileIDs backfill still never happens, and the apply still counts
// the mod as processed and completes without error.
func TestApplyProfileSync_UpsertMod_LockedRefRefusalIsWarning(t *testing.T) {
	svc, game := newSyncTestService(t)
	pm := svc.NewProfileManager()

	seedSyncInstalledMod(t, svc, game, "src", "lock1", "Locked One", "2.0", "default", true, []string{"main"})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "lock1", Version: "1.0", Locked: true}))

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToUpdate, 1)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyProfileSync(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Updated, "the refusal must not fail the loop's count")

	var warnDetail string
	var warnScope core.Scope
	for _, e := range *events {
		if se, ok := e.(core.StepEvent); ok && se.Phase == core.SyncUpdateWarning {
			warnDetail = se.Detail
			warnScope = se.Scope
		}
		if se, ok := e.(core.StepEvent); ok {
			assert.NotEqual(t, core.SyncUpdateNote, se.Phase, "#294: the refusal is no longer a --verbose-only note")
		}
	}
	assert.Contains(t, warnDetail, "could not update src:lock1: ")
	assert.Contains(t, warnDetail, "is locked at v")
	assert.NotContains(t, warnDetail, "Warning: ",
		"#294: the detail carries no baked-in prefix - the caller renders `Warning: %s`")
	assert.Equal(t, "Locked One", warnScope.ModName, "Sync* events must carry ModName, like the sibling Switch* events")
	require.Len(t, result.Warnings, 1, "#294: the refusal must also reach ProfileSyncResult.Warnings")
	assert.Equal(t, warnDetail, result.Warnings[0])

	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods[0].FileIDs, "the locked ref's FileIDs must NOT have been backfilled")
}

// TestApplyProfileSync_MergedPakSyncFailure_IsAWarning pins #197: the
// end-of-apply merged-pak sync's failure is recorded on Result.Warnings,
// never fails the apply, and is independent of the three loops' own
// verbose-gated notes.
func TestApplyProfileSync_MergedPakSyncFailure_IsAWarning(t *testing.T) {
	svc, game := newSyncTestService(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	game.SourceIDs = map[string]string{"fake-compiler": "external-icarus-id"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	svc.RegisterSource(&fakeCompilerSource{})

	const modID, version, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	seedSyncInstalledMod(t, svc, game, "fake-compiler", modID, "Bear Mount", version, "default", true, []string{fileID})

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToAdd, 1)

	result, err := svc.ApplyProfileSync(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "could not sync merged pak: ")
}

// TestApplyProfileSync_MissingProfileWithEmptyDiff_CreatesProfileWithoutSyncingMergedPak
// pins Unit J's review finding: the one case where the CLI reaches Apply
// with all three buckets empty is a MISSING profile (it calls Apply purely
// for the pm.Create side effect), and that path used to fall through to the
// merged-pak sync - whose warnings the CLI then discarded, and whose
// zero-sources branch UNINSTALLS any merged pak the game has. Applying an
// empty diff must not touch the merged pak at all, exactly as
// ApplyProfileApply's NoChanges guard already ensures.
func TestApplyProfileSync_MissingProfileWithEmptyDiff_CreatesProfileWithoutSyncingMergedPak(t *testing.T) {
	svc, game := newSyncTestService(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	require.NoError(t, svc.SaveGame(context.Background(), game))

	// A merged pak already exists for the game. "merged-pak"/"merged" mirror
	// core's private mergedPakModID/mergedPakVersion (same convention as
	// merged_pak_hooks_test.go).
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, domain.SourceMerged, "merged-pak", "merged", "zzz_LMM_Merged_P.pak", []byte("merged-bytes")))

	plan, err := svc.PlanProfileSync(context.Background(), game, "fresh")
	require.NoError(t, err)
	require.True(t, plan.Missing)
	require.Empty(t, plan.ToAdd)
	require.Empty(t, plan.ToRemove)
	require.Empty(t, plan.ToUpdate)

	result, err := svc.ApplyProfileSync(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Added)
	assert.Equal(t, 0, result.Removed)
	assert.Equal(t, 0, result.Updated)
	assert.Empty(t, result.Warnings, "no merged-pak sync ran, so there is nothing to warn about")

	_, gErr := svc.NewProfileManager().Get(game.ID, "fresh")
	require.NoError(t, gErr, "the missing profile is still created")

	assert.True(t, gameCache.Exists(game.ID, domain.SourceMerged, "merged-pak", "merged"),
		"an empty-diff sync must leave the merged pak alone")
}

// TestApplyProfileSync_StalePlan is ruling 5: a plan computed against an
// installed-mod set that has since changed is refused, so a frontend that
// held a plan across a mutation re-plans instead of applying a stale diff.
func TestApplyProfileSync_StalePlan(t *testing.T) {
	svc, game := newSyncTestService(t)

	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)

	plan, err := svc.PlanProfileSync(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToAdd, 1)

	// Something else disables the mod behind the plan's back.
	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", false, nil)

	_, err = svc.ApplyProfileSync(context.Background(), game, plan, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}
