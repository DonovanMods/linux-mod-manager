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

// --- v2 Phase 2 Unit M Task 25: PlanUninstall / ApplyUninstall ---

// TestPlanUninstall_ExplicitSource_ResolvesThatSourcesCopy pins the -s
// branch: the named source's copy is the one planned, and the plan echoes
// the options the CLI passed.
func TestPlanUninstall_ExplicitSource_ResolvesThatSourcesCopy(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "other", "1", "Other One", "1.0", true, map[string][]byte{"other.esp": []byte("o")})
	seedProfileWithMod(t, svc, "g1", "default", "other", "1", "1.0")

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "other", "1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, "other", plan.Mod.SourceID)
	assert.Equal(t, "Other One", plan.Mod.Name)
	assert.Equal(t, "default", plan.Mod.ProfileName, "ApplyUninstall reads the profile back off the plan's mod")
	assert.True(t, plan.KeepCache)
}

// TestPlanUninstall_BareID_TakesTheFirstIDMatchAcrossSources pins the
// cross-source disambiguation the CLI used to do inline: with no -s, every
// installed mod is scanned by ID and the FIRST hit wins. The expected mod is
// derived from GetInstalledMods' own order rather than hardcoded, because
// "first" is defined by that order, not by this fixture's seeding order.
func TestPlanUninstall_BareID_TakesTheFirstIDMatchAcrossSources(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "other", "1", "Other One", "1.0", true, map[string][]byte{"other.esp": []byte("o")})
	seedProfileWithMod(t, svc, "g1", "default", "other", "1", "1.0")

	all, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	var want *domain.InstalledMod
	for i := range all {
		if all[i].ID == "1" {
			want = &all[i]
			break
		}
	}
	require.NotNil(t, want, "fixture: two sources both carry mod ID 1")

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "1", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, want.SourceID, plan.Mod.SourceID, "first ID match wins, as the pre-lift CLI did")
	assert.Equal(t, want.Name, plan.Mod.Name)
}

// TestPlanUninstall_NotFound_PreservesHistoricalErrorText pins both
// not-found wordings the pre-lift cmd/lmm/uninstall.go produced, verbatim.
func TestPlanUninstall_NotFound_PreservesHistoricalErrorText(t *testing.T) {
	svc, game := newDeployableService(t)

	_, err := svc.PlanUninstall(context.Background(), game, "default", "", "nope", core.UninstallOptions{})
	require.Error(t, err)
	assert.Equal(t, "mod nope not found in profile default", err.Error())

	_, err = svc.PlanUninstall(context.Background(), game, "default", "src", "nope", core.UninstallOptions{})
	require.Error(t, err)
	assert.Equal(t, "mod nope not found in profile default (source: src)", err.Error())
}

// TestPlanUninstall_BareID_ReadFailure_PreservesHistoricalErrorText pins the
// other pre-lift text on that branch: a failing installed-mods read reached
// the user as "listing installed mods: …".
func TestPlanUninstall_BareID_ReadFailure_PreservesHistoricalErrorText(t *testing.T) {
	svc, game := newDeployableService(t)
	require.NoError(t, svc.Close(), "closing the DB early forces the scan's read to fail")

	_, err := svc.PlanUninstall(context.Background(), game, "default", "", "1", core.UninstallOptions{})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "listing installed mods:"),
		"want the pre-lift prefix %q, got %q", "listing installed mods:", err.Error())
}

// TestPlanUninstall_FilesMatchWhatApplyUninstallThenRemoved is this half of
// the task's central contract: the paths a plan says would leave the game
// directory are exactly the ones the Apply then removes, proven by diffing
// the deployed tree across the Apply rather than recomputing them twice.
func TestPlanUninstall_FilesMatchWhatApplyUninstallThenRemoved(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	before := deployedTree(t, game.ModPath)
	require.NotEmpty(t, before, "fixture: the mod must actually be deployed first")

	plan, err := svc.PlanUninstall(ctx, game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"one.esp"}, plan.Files)

	_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
	require.NoError(t, err)

	after := deployedTree(t, game.ModPath)
	removed := map[string]bool{}
	for p := range before {
		if !after[p] {
			removed[p] = true
		}
	}
	want := map[string]bool{}
	for _, f := range plan.Files {
		want[f] = true
	}
	assert.Equal(t, want, removed, "plan.Files must equal what ApplyUninstall actually removed")
}

// TestPlanUninstall_NothingDeployed_ListsNoFiles mirrors Task 24's Minor #1
// for uninstall: a cached-but-never-deployed mod has nothing to remove from
// the game directory, so its plan lists no files.
func TestPlanUninstall_NothingDeployed_ListsNoFiles(t *testing.T) {
	svc, game := newDeployableService(t)

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan.Files)
}

// TestPlanUninstall_AbsentCacheEntry_FallsBackToTrackedDeployedPaths pins
// #260's removal rule in plan form: with the cache entry gone, the DB's
// tracked deployed paths are what an uninstall would remove - and the plan
// still narrows them to what is actually on disk.
func TestPlanUninstall_AbsentCacheEntry_FallsBackToTrackedDeployedPaths(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	// A copy deploy owns real files, so erasing the cache entry leaves the
	// deployment fully on disk with only the DB remembering it is ours.
	require.NoError(t, os.RemoveAll(svc.GetGameCache(game).ModPath(game.ID, "src", "1", "1.0")))

	plan, err := svc.PlanUninstall(ctx, game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"one.esp"}, plan.Files, "the DB's tracked paths carry the removal set")
}

// TestPlanUninstall_HooksListedInRunOrder pins the hook readout: only
// configured hooks, in run order, and none at all under SkipHooks.
func TestPlanUninstall_HooksListedInRunOrder(t *testing.T) {
	svc, game := newDeployableService(t)
	script := filepath.Join(t.TempDir(), "hook.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o755))
	seedHooks(t, svc, game, "default", domain.GameHooks{
		Uninstall: domain.HookConfig{BeforeAll: script, AfterEach: script},
	})

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"uninstall.before_all", "uninstall.after_each"}, plan.Hooks)

	plan, err = svc.PlanUninstall(context.Background(), game, "default", "src", "1", core.UninstallOptions{SkipHooks: true})
	require.NoError(t, err)
	assert.Empty(t, plan.Hooks, "--no-hooks runs none of them")
}

// TestPlanUninstall_ChangesNothing pins the side-effect-free half of
// Plan/Apply: planning touches neither the game tree, the cache tree, nor
// the installed-mod rows.
func TestPlanUninstall_ChangesNothing(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	gameBefore := deployedTree(t, game.ModPath)
	cacheBefore := deployedTree(t, svc.GetGameCachePath(game))
	modsBefore, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)

	_, err = svc.PlanUninstall(ctx, game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)

	assert.Equal(t, gameBefore, deployedTree(t, game.ModPath))
	assert.Equal(t, cacheBefore, deployedTree(t, svc.GetGameCachePath(game)))
	modsAfter, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, modsBefore, modsAfter)
}

// TestApplyUninstall_StalePlan_ReturnsErrStalePlan pins Ruling 5 for
// uninstall: an installed-mod set that moved since the plan was computed is
// refused, not silently applied.
func TestApplyUninstall_StalePlan_ReturnsErrStalePlan(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()

	plan, err := svc.PlanUninstall(ctx, game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")

	_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// TestApplyUninstall_NilPlan_IsRefused guards the one shape no caller should
// reach: Apply without a precondition to check.
func TestApplyUninstall_NilPlan_IsRefused(t *testing.T) {
	svc, game := newDeployableService(t)
	_, err := svc.ApplyUninstall(context.Background(), game, nil, core.UninstallOptions{})
	require.Error(t, err)
}
