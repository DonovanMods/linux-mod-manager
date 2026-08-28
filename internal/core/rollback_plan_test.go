package core_test

// Tests for Service.PlanRollback (v2 Phase 2 Unit I Task 10, #289): the
// pure, read-only half of the pre-extraction CLI's doUpdateRollback,
// extracted so cmd/lmm can render a plan instead of duplicating core's four
// pre-checks (GetInstalledMod, PreviousVersion == "", cache existence, lock
// state via a direct profile load). Reuses newFlowsTestService
// (flows_test.go) and seedUpdatableMod/seedRollbackReadyMod
// (flows_update_test.go/flows_rollback_test.go) - all in this same
// core_test package.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestService_PlanRollback_NotInstalled_ReturnsError guards PlanRollback's
// first guard: a mod ID absent from the profile returns GetInstalledMod's
// raw error (domain.ErrModNotFound) - the CLI wraps it as "mod not found:
// %s", matching doUpdateRollback's pre-extraction text.
func TestService_PlanRollback_NotInstalled_ReturnsError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	plan, err := svc.PlanRollback(context.Background(), game, "default", "src", "mod1")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Nil(t, plan)
}

// TestService_PlanRollback_NoPreviousVersion_ReturnsError guards
// PlanRollback's second guard: a mod that has never been updated (or has
// already been rolled back once) has no PreviousVersion, and PlanRollback
// must fail with the exact pre-extraction error text - there is nothing
// left to plan.
func TestService_PlanRollback_NoPreviousVersion_ReturnsError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mod := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	require.Empty(t, mod.PreviousVersion)

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.Error(t, err)
	assert.Equal(t, "no previous version available for rollback", err.Error())
	assert.Nil(t, plan)
}

// TestService_PlanRollback_Unlocked covers the happy path: FromVersion/
// ToVersion/Mod are populated from the installed row, Locked/CacheMissing
// are both false when nothing is wrong.
func TestService_PlanRollback_Unlocked(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, mod.ID, plan.Mod.ID)
	assert.Equal(t, "2.0", plan.FromVersion)
	assert.Equal(t, "1.0", plan.ToVersion)
	assert.False(t, plan.Locked)
	assert.Empty(t, plan.LockedVersion)
	assert.Empty(t, plan.Refusal)
	assert.False(t, plan.CacheMissing)
}

// TestService_PlanRollback_Locked covers the third pre-extraction check:
// Locked/LockedVersion/Refusal are populated from the profile ref's lock
// state, mirroring TestService_PlanUpdate_VersionBumpAvailable_Locked's
// assertions for the update-side plan.
func TestService_PlanRollback_Locked(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})
	require.NoError(t, svc.NewProfileManager().SetModLock("g1", "default", "src", "mod1", "2.0"))

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.Locked)
	assert.Equal(t, "2.0", plan.LockedVersion)
	require.NotEmpty(t, plan.Refusal)
	assert.Contains(t, plan.Refusal, "locked at v2.0")
	assert.Contains(t, plan.Refusal, "lmm mod lock")
}

// TestService_PlanRollback_CacheMissing covers the fourth pre-extraction
// check: CacheMissing is set when ToVersion's cache entry has been pruned
// or manually deleted since the update - a valid plan is still returned
// (unlike the no-previous-version guard above), since ApplyRollback's own
// re-check is what actually refuses the rollback.
func TestService_PlanRollback_CacheMissing(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})
	require.NoError(t, svc.GetGameCache(game).Delete("g1", "src", "mod1", "1.0"))

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.CacheMissing)
}

// TestService_PlanRollback_PerformsZeroMutations mirrors
// TestService_PlanUpdate_PerformsZeroMutations: a Plan reads the DB, cache,
// and profile - it must never write any of them.
func TestService_PlanRollback_PerformsZeroMutations(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	beforeMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	require.NotNil(t, plan)

	afterMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, beforeMods, afterMods, "DB rows must be untouched after planning")
}

// TestService_ApplyRollback_ErrStalePlan guards Ruling 5: a plan computed
// against one installed-mod snapshot must be refused once that snapshot has
// since changed - mirroring TestService_ApplyUpdate_ErrStalePlan exactly, a
// second mod is installed into the SAME profile between PlanRollback and
// ApplyRollback.
func TestService_ApplyRollback_ErrStalePlan(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)

	// Installed-mod set changes after the plan was computed.
	seedInstalledMod(t, svc, game, "src", "mod2", "1.0", true, map[string][]byte{"mod2.esp": []byte("other")})

	_, err = svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrStalePlan)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version, "a stale plan must never apply")
}
