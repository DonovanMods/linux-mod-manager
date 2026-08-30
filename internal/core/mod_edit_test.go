package core_test

// Tests for Service.PlanRelinkMod/ApplyRelinkMod (v2 Phase 3 Task 10,
// #303): the core flow behind `lmm mod edit`, extracted from
// cmd/lmm/mod_edit.go's pre-Task-10 doModEdit. Reuses newModDetailTestService
// / seedModDetailInstalled (moddetail_test.go) - both already in this
// core_test package.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_PlanRelinkMod_NotInstalled_ReturnsError(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "missing", "", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Nil(t, plan)
}

// TestService_PlanRelinkMod_MetadataOnly guards the no-op-identity shape: no
// newSourceID/newModID means Relink is false and To mirrors From exactly.
func TestService_PlanRelinkMod_MetadataOnly(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.False(t, plan.Relink)
	assert.Equal(t, domain.ModReference{SourceID: "src", ModID: "a", Version: "1.5"}, plan.From)
	assert.Equal(t, domain.ModReference{SourceID: "src", ModID: "a"}, plan.To, "To.Version is always empty - see RelinkPlan's doc comment")
	assert.False(t, plan.Locked)
	assert.Empty(t, plan.Refusal)
	assert.False(t, plan.TargetInstalled)
}

// TestService_PlanRelinkMod_Relink_ComputesToFromNewIdentity guards the
// re-link shape: To takes the new source/id, From keeps the current one.
func TestService_PlanRelinkMod_Relink_ComputesToFromNewIdentity(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "curseforge", "99")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.Relink)
	assert.Equal(t, domain.ModReference{SourceID: "src", ModID: "a", Version: "1.5"}, plan.From)
	assert.Equal(t, domain.ModReference{SourceID: "curseforge", ModID: "99"}, plan.To)
}

// TestService_PlanRelinkMod_Relink_DefaultsOmittedHalf guards doModEdit's
// "whichever of the two you omit keeps its current value" contract: passing
// only a new mod ID (no new source) leaves To.SourceID at the mod's current
// source.
func TestService_PlanRelinkMod_Relink_DefaultsOmittedHalf(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "99")
	require.NoError(t, err)
	require.True(t, plan.Relink, "specifying only --source-id is still a re-link request")
	assert.Equal(t, "src", plan.To.SourceID)
	assert.Equal(t, "99", plan.To.ModID)
}

// TestService_PlanRelinkMod_TargetInstalled_Detected guards the
// TargetInstalled datum: re-linking onto an identity another installed mod
// already occupies is flagged (informational; ApplyRelinkMod does not
// refuse on it - see RelinkPlan's doc comment).
func TestService_PlanRelinkMod_TargetInstalled_Detected(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "b", SourceID: "src", Name: "Mod B", Version: "2.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "b", Version: "2.0"}))

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "src", "b")
	require.NoError(t, err)
	assert.True(t, plan.TargetInstalled)
}

// TestService_PlanRelinkMod_Locked_Relink_SetsRefusal guards #146: a
// re-link request against a locked ref precomputes the refusal. Since #294
// (Ruling 5) that text is the canonical sentence, not a hand-worded one of
// its own - specifically LockedRefUnlockOnlyRefusalError's, since a re-link
// is refused on the lock alone (unit Q review, I1) - and
// RelinkPlan.Refusal carries the SENTENCE (no
// ErrModLocked prefix), so prefixing the sentinel reproduces the canonical
// error byte-for-byte, which is exactly what doModEdit/ApplyRelinkMod
// return.
func TestService_PlanRelinkMod_Locked_Relink_SetsRefusal(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "curseforge", "99")
	require.NoError(t, err)
	assert.True(t, plan.Locked)
	assert.Equal(t, "1.5", plan.LockedVersion)
	require.NotEmpty(t, plan.Refusal)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	canonical := core.LockedRefUnlockOnlyRefusalError(installed.Mod, "default", &domain.ModReference{Version: "1.5"}).Error()
	assert.Equal(t, canonical, "mod is locked: "+plan.Refusal,
		"#294: every lock refusal is the canonical wording; Refusal is its sentence half")
	assert.NotContains(t, plan.Refusal, "re-linking would replace the locked ref",
		"#294: the hand-worded re-link refusal is gone")
	assert.NotContains(t, plan.Refusal, "move the lock",
		"unit Q review I1: a re-link is refused on the lock alone, so moving it is not a remedy")
}

// TestService_PlanRelinkMod_Locked_MetadataOnly_NoRefusal guards the
// counterpart: a metadata-only plan (no re-link) never populates Refusal,
// even when locked - ApplyRelinkMod re-derives the version-only guard
// itself from RelinkOptions, which PlanRelinkMod has no visibility into.
func TestService_PlanRelinkMod_Locked_MetadataOnly_NoRefusal(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)
	assert.True(t, plan.Locked)
	assert.Empty(t, plan.Refusal)
}

func TestService_ApplyRelinkMod_MetadataOnly_SavesChanges(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{Name: "Renamed"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.NoChanges)
	assert.Equal(t, []string{"name -> Renamed"}, result.Changes)
	assert.Equal(t, "Renamed", result.Mod.Name)

	saved, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", saved.Name)
}

// TestService_ApplyRelinkMod_NoChanges_ReturnsNoChangesTrue guards
// doModEdit's own no-op early return: no options and no re-link writes
// nothing and reports NoChanges.
func TestService_ApplyRelinkMod_NoChanges_ReturnsNoChangesTrue(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, result.NoChanges)
	assert.Empty(t, result.Changes)

	saved, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Mod A", saved.Name, "an unrequested edit must not touch the record")
}

// TestService_ApplyRelinkMod_Relink_MovesDBRowAndProfileRef guards the
// re-link's core sequence: the old DB row is gone, a new one exists under
// the new identity, and the profile ref moved with it.
func TestService_ApplyRelinkMod_Relink_MovesDBRowAndProfileRef(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	game.SourceIDs["src"] = game.ID // relink target must be a configured source

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "src", "b")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "b", result.Mod.ID)
	assert.Contains(t, result.Changes, "source -> src (was src)")
	assert.Contains(t, result.Changes, "id -> b (was a)")

	_, err = svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "the old identity's DB row must be gone")

	moved, err := svc.GetInstalledMod(context.Background(), "src", "b", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", moved.Version)

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Nil(t, prof.FindRef("src", "a"), "the old profile ref must be removed")
	require.NotNil(t, prof.FindRef("src", "b"), "the new profile ref must exist")
}

// TestService_ApplyRelinkMod_Relink_FetchesMetadataFromNonLocalSource
// guards doModEdit's metadata-refresh behavior: fields the caller did not
// explicitly override are filled in from the target source.
func TestService_ApplyRelinkMod_Relink_FetchesMetadataFromNonLocalSource(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	other := newMockSource("other")
	svc.RegisterSource(other)
	game.SourceIDs["other"] = game.ID
	other.AddMod(game.ID, &domain.Mod{ID: "99", SourceID: "other", GameID: game.ID, Name: "Fetched Name", Author: "Fetched Author", Version: "3.0"})

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "other", "99")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Fetched Name", result.Mod.Name)
	assert.Equal(t, "Fetched Author", result.Mod.Author)
	assert.Equal(t, "3.0", result.Mod.Version)
	assert.Contains(t, result.Changes, "name -> Fetched Name (from other)")
}

// TestService_ApplyRelinkMod_Relink_ExplicitOverrideBeatsFetchedMetadata
// guards the override precedence: an explicit --name survives a re-link's
// metadata refresh.
func TestService_ApplyRelinkMod_Relink_ExplicitOverrideBeatsFetchedMetadata(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	other := newMockSource("other")
	svc.RegisterSource(other)
	game.SourceIDs["other"] = game.ID
	other.AddMod(game.ID, &domain.Mod{ID: "99", SourceID: "other", GameID: game.ID, Name: "Fetched Name", Version: "3.0"})

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "other", "99")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{Name: "Explicit Name"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Explicit Name", result.Mod.Name)
	assert.NotContains(t, result.Changes, "name -> Fetched Name (from other)")
}

// TestService_ApplyRelinkMod_Relink_UnconfiguredTargetSource_Errors guards
// the pre-existing "source %q is not configured for %s" refusal.
func TestService_ApplyRelinkMod_Relink_UnconfiguredTargetSource_Errors(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "unconfigured", "99")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `source "unconfigured" is not configured for`)
	assert.Nil(t, result)

	_, err = svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err, "a refused re-link must not touch the old record")
}

// TestService_ApplyRelinkMod_Relink_Locked_Refuses guards #146: identical
// to PlanRelinkMod's own Refusal text, wrapped in ErrModLocked so callers
// can errors.Is it, and no state moves. Since #294 that text IS the
// canonical one, byte-for-byte - its unlock-only variant since the unit Q
// review's I1.
func TestService_ApplyRelinkMod_Relink_Locked_Refuses(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	installed, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "src", "b")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Equal(t, core.LockedRefUnlockOnlyRefusalError(installed.Mod, "default", &domain.ModReference{Version: "1.5"}).Error(), err.Error(),
		"#294 + unit Q review I1: the re-link refusal is the canonical text's unlock-only variant")
	assert.Nil(t, result)

	saved, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", saved.Version, "a refused re-link must not touch the DB row")
}

// TestService_ApplyRelinkMod_VersionOnly_Locked_MismatchedVersion_Refuses
// guards the metadata-only lock guard, re-derived independently from
// RelinkOptions.Version since PlanRelinkMod cannot see it.
func TestService_ApplyRelinkMod_VersionOnly_Locked_MismatchedVersion_Refuses(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)
	require.Empty(t, plan.Refusal, "a metadata-only plan never precomputes a refusal")

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{Version: "2.0"}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Contains(t, err.Error(), "move the lock with 'lmm mod lock -s src -p default a <version>'")
	assert.Nil(t, result)
}

// TestService_ApplyRelinkMod_VersionOnly_Locked_MatchingVersion_Allowed
// guards the realign allowance: a --version equal to the locked version is
// not a move, matching UpsertMod's own same-version allowance.
func TestService_ApplyRelinkMod_VersionOnly_Locked_MatchingVersion_Allowed(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "2.0")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", "1.5"))

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{Version: "1.5"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "1.5", result.Mod.Version)

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ref := prof.FindRef("src", "a")
	require.NotNil(t, ref)
	assert.True(t, ref.Locked, "realigning to the locked version must preserve the marker")
}

// TestService_ApplyRelinkMod_StalePlan_ReturnsErrStalePlan guards Ruling 5:
// a plan computed against an installed set that has since changed is
// refused rather than applied.
func TestService_ApplyRelinkMod_StalePlan_ReturnsErrStalePlan(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "", "")
	require.NoError(t, err)

	// Move the installed set out from under the plan.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "b", SourceID: "src", Name: "Mod B", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{Name: "Renamed"}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrStalePlan)
	assert.Nil(t, result)

	saved, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Mod A", saved.Name, "a stale plan must not be applied")
}

// TestService_ApplyRelinkMod_EmitsProgressEvents guards the sink contract:
// RelinkFetching/RelinkWarning fire at their documented points.
func TestService_ApplyRelinkMod_EmitsProgressEvents(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	other := newMockSource("other")
	svc.RegisterSource(other)
	game.SourceIDs["other"] = game.ID
	// No AddMod call: GetMod will fail, exercising the RelinkWarning path.

	plan, err := svc.PlanRelinkMod(context.Background(), game, "default", "src", "a", "other", "99")
	require.NoError(t, err)

	var phases []core.DeployPhase
	sink := func(e core.Event) {
		if se, ok := e.(core.StepEvent); ok {
			phases = append(phases, se.Phase)
		}
	}

	result, err := svc.ApplyRelinkMod(context.Background(), game, plan, core.RelinkOptions{}, sink)
	require.NoError(t, err)
	require.Contains(t, result.Warnings, "could not fetch metadata: mod not found")
	assert.Contains(t, phases, core.RelinkFetching)
	assert.Contains(t, phases, core.RelinkWarning)
}
