package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- v2 Phase 2 Unit M Task 25: PlanPurge / ApplyPurge ---

// TestPlanPurge_ListsEveryInstalledModAndEchoesOptions pins the plan's
// baseline shape: PlanPurge does the installed-mods read the CLI used to do
// itself, so the set a frontend counts in its confirmation prompt is the
// same object ApplyPurge goes on to purge.
func TestPlanPurge_ListsEveryInstalledModAndEchoesOptions(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{Uninstall: true})
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, "default", plan.Profile)
	assert.True(t, plan.Uninstall)

	want, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, want, plan.Mods, "the plan's mods ARE GetInstalledMods' set, in its order")
}

// TestPlanPurge_EmptyProfile_ListsNoMods pins the shape the CLI's "No mods
// installed" early-out reads off the plan.
func TestPlanPurge_EmptyProfile_ListsNoMods(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan.Mods)
	assert.Empty(t, plan.Hooks, "purgeMods returns before its before_all when there is nothing to purge")
}

// TestPlanPurge_ReadFailure_PreservesHistoricalErrorText pins the pre-lift
// wording of cmd/lmm/purge.go's own installed-mods read.
func TestPlanPurge_ReadFailure_PreservesHistoricalErrorText(t *testing.T) {
	svc, game := newDeployableService(t)
	require.NoError(t, svc.Close(), "closing the DB early forces the read PlanPurge opens with to fail")

	_, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "getting installed mods:"),
		"want the pre-lift prefix %q, got %q", "getting installed mods:", err.Error())
}

// TestPlanPurge_HooksListedInRunOrder pins the hook readout: only configured
// hooks, in run order, and none at all under SkipHooks.
func TestPlanPurge_HooksListedInRunOrder(t *testing.T) {
	svc, game := newDeployableService(t)
	script := filepath.Join(t.TempDir(), "hook.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o755))
	seedHooks(t, svc, game, "default", domain.GameHooks{
		Uninstall: domain.HookConfig{BeforeEach: script, AfterAll: script},
	})

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"uninstall.before_each", "uninstall.after_all"}, plan.Hooks)

	plan, err = svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{SkipHooks: true})
	require.NoError(t, err)
	assert.Empty(t, plan.Hooks)
}

// TestApplyPurge_PurgesExactlyThePlansMods is this half of the task's
// central contract: the mods the plan lists (the set a frontend counted in
// its prompt) are exactly the ones the Apply undeploys.
func TestApplyPurge_PurgesExactlyThePlansMods(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, deployedTree(t, game.ModPath), "fixture: both mods must be deployed first")

	plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Mods, 2)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, sink)
	require.NoError(t, err)
	var purged []string
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.PurgeModPurged {
			purged = append(purged, m.ModName)
		}
	}
	assert.Equal(t, 2, result.Purged)
	assert.Equal(t, []string{plan.Mods[0].Name, plan.Mods[1].Name}, purged)
	assert.Empty(t, deployedTree(t, game.ModPath), "every planned mod is undeployed")
}

// TestPlanPurge_ChangesNothing pins the side-effect-free half of Plan/Apply.
func TestPlanPurge_ChangesNothing(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	gameBefore := deployedTree(t, game.ModPath)
	cacheBefore := deployedTree(t, svc.GetGameCachePath(game))
	modsBefore, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)

	_, err = svc.PlanPurge(ctx, game, "default", core.PurgeOptions{Uninstall: true})
	require.NoError(t, err)

	assert.Equal(t, gameBefore, deployedTree(t, game.ModPath))
	assert.Equal(t, cacheBefore, deployedTree(t, svc.GetGameCachePath(game)))
	modsAfter, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, modsBefore, modsAfter)
}

// TestApplyPurge_StalePlan_ReturnsErrStalePlan pins Ruling 5 for purge.
func TestApplyPurge_StalePlan_ReturnsErrStalePlan(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()

	plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")

	_, err = svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// TestApplyPurge_NilPlan_IsRefused guards the one shape no caller should
// reach: Apply without a precondition to check.
func TestApplyPurge_NilPlan_IsRefused(t *testing.T) {
	svc, game := newDeployableService(t)
	_, err := svc.ApplyPurge(context.Background(), game, nil, core.PurgeOptions{}, nil)
	require.Error(t, err)
}
