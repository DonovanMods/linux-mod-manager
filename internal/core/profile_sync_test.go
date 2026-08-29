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

// TestApplyProfileSync_UpsertMod_LockedRefRefusalIsSwallowed is Ruling 9: a
// LOCKED profile ref makes the toUpdate loop's UpsertMod refuse; the
// refusal is swallowed into a --verbose-only SyncUpdateNote event, the
// FileIDs backfill never happens, and the apply still counts the mod as
// processed and completes without error.
func TestApplyProfileSync_UpsertMod_LockedRefRefusalIsSwallowed(t *testing.T) {
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

	var noteDetail string
	for _, e := range *events {
		if se, ok := e.(core.StepEvent); ok && se.Phase == core.SyncUpdateNote {
			noteDetail = se.Detail
		}
	}
	assert.Contains(t, noteDetail, "Warning: could not update src:lock1: ")
	assert.Contains(t, noteDetail, "is locked at v")

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
