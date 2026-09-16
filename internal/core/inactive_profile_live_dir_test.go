package core_test

// #445: a game has one game directory, and it holds the ACTIVE profile's
// deployment. `lmm deploy -p <other>` put a second profile's mods beside
// the active one's; it now refuses, naming `lmm profile switch`, and leaves
// the directory exactly as it was. `lmm purge -p <other>` removed whatever
// that profile's mods' cache entries named - the active profile's files
// included. It is now a recorded-only cleanup: it removes the paths that
// profile has deployed_files rows for and no other profile records, and
// nothing else - so a profile whose files were put into the live directory
// (an `import -p`, or a deploy before this fix) can still be cleared.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveDirFixture is a backfill fixture with profile "a" active and its mod
// deployed, and profile "b" - not active - listing a mod of its own and
// one it shares with a, whose row still claims the deployment a pre-upgrade
// switch left it.
func liveDirFixture(t *testing.T) *backfillFixture {
	t.Helper()
	ctx := context.Background()
	f := newBackfillFixture(t)
	f.row(t, "a", "shared", true, false)
	f.row(t, "a", "aonly", true, false)
	_, err := f.svc.DeployProfile(ctx, f.game, "a", core.DeployOptions{}, nil)
	require.NoError(t, err)
	f.row(t, "b", "bonly", true, false)
	f.row(t, "b", "shared", true, true)
	require.FileExists(t, filepath.Join(f.gameDir, "shared.esp"))
	require.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	return f
}

// requireRefusedForB checks err is #445's refusal for profile b and says
// how to proceed.
func requireRefusedForB(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, core.ErrProfileNotActive)
	assert.Contains(t, err.Error(), `"b"`)
	assert.Contains(t, err.Error(), `"a"`, "it names the profile that is active")
	assert.Contains(t, err.Error(), "lmm profile switch b")
}

func TestDeploy_ANonActiveProfileIsRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("plan", func(t *testing.T) {
		f := liveDirFixture(t)
		before := treeOf(t, f.gameDir)
		_, err := f.svc.PlanDeploy(ctx, f.game, "b", core.DeployOptions{})
		requireRefusedForB(t, err)
		_, err = f.svc.PlanDeploy(ctx, f.game, "b", core.DeployOptions{Purge: true})
		requireRefusedForB(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("apply", func(t *testing.T) {
		f := liveDirFixture(t)
		before := treeOf(t, f.gameDir)
		// A plan built by hand, or made before `a` became active, is
		// refused at the apply as well.
		_, err := f.svc.ApplyDeploy(ctx, f.game, &core.DeployPlan{Profile: "b"}, core.DeployOptions{Purge: true}, nil)
		requireRefusedForB(t, err)
		_, err = f.svc.DeployProfile(ctx, f.game, "b", core.DeployOptions{All: true}, nil)
		requireRefusedForB(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir), "nothing of b's reached the game directory")
		assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
	})

	t.Run("the active profile still deploys", func(t *testing.T) {
		f := liveDirFixture(t)
		result, err := f.svc.DeployProfile(ctx, f.game, "a", core.DeployOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, 2, result.Deployed)
	})

	t.Run("after a switch the other profile deploys", func(t *testing.T) {
		f := liveDirFixture(t)
		f.switchTo(t, "b")
		_, err := f.svc.DeployProfile(ctx, f.game, "b", core.DeployOptions{}, nil)
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
		assert.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
		_, err = f.svc.PlanDeploy(ctx, f.game, "a", core.DeployOptions{})
		require.ErrorIs(t, err, core.ErrProfileNotActive, "and a, no longer active, is refused")
	})
}

// mixedDirFixture is liveDirFixture after profile b's mods were deployed
// into the game directory while a stayed active - the state an
// `import -p b` or a pre-#445 `deploy -p b` leaves: b records bonly.esp,
// which only it records, and shared.esp, which a records too.
func mixedDirFixture(t *testing.T) *backfillFixture {
	t.Helper()
	ctx := context.Background()
	f := liveDirFixture(t)
	pm := f.svc.NewProfileManager()
	require.NoError(t, pm.SetDefault(ctx, f.game.ID, "b"))
	_, err := f.svc.DeployProfile(ctx, f.game, "b", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, f.game.ID, "a"))
	for _, name := range []string{"aonly.esp", "shared.esp", "bonly.esp"} {
		require.FileExists(t, filepath.Join(f.gameDir, name))
	}
	return f
}

func TestPurge_ANonActiveProfileClearsOnlyWhatItRecorded(t *testing.T) {
	ctx := context.Background()

	t.Run("the plan says so", func(t *testing.T) {
		f := mixedDirFixture(t)
		before := treeOf(t, f.gameDir)

		plan, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		assert.True(t, plan.RecordedOnly)
		assert.Equal(t, "a", plan.ActiveProfile)
		assert.Equal(t, []string{"bonly.esp"}, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "shared.esp", Profiles: []string{"a"}}}, plan.Kept)
		require.Len(t, plan.Mods, 1)
		assert.Equal(t, "bonly", plan.Mods[0].ID)
		assert.Empty(t, plan.Hooks, "a recorded-only purge runs no hooks")
		assert.Nil(t, plan.MergedArtifact)
		assert.Equal(t, before, treeOf(t, f.gameDir), "planning changes nothing")
	})

	t.Run("the apply removes only that", func(t *testing.T) {
		f := mixedDirFixture(t)
		plan, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)

		assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"), "b's own file is gone")
		assert.FileExists(t, filepath.Join(f.gameDir, "shared.esp"), "a path the active profile records too survives")
		assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
		assert.Equal(t, 1, result.Purged)
		assert.Equal(t, 1, result.RemovedPaths)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "shared.esp", Profiles: []string{"a"}}}, result.Kept)

		for profile, want := range map[string][]string{"b": {"shared.esp"}, "a": {"shared.esp"}} {
			rows, err := f.svc.GetDeployedFilesForMod(ctx, f.game.ID, profile, "src", "shared")
			require.NoError(t, err)
			assert.Equal(t, want, rows, "profile %s still records the path it shares", profile)
		}
		rows, err := f.svc.GetDeployedFilesForMod(ctx, f.game.ID, "b", "src", "bonly")
		require.NoError(t, err)
		assert.Empty(t, rows)
		bonly, err := f.svc.GetInstalledMod(ctx, "src", "bonly", f.game.ID, "b")
		require.NoError(t, err)
		assert.False(t, bonly.Deployed)
		assert.Equal(t, 2, f.refCount(t, "b"), "b's document is untouched")
		aRows, err := f.svc.GetInstalledMods(ctx, f.game.ID, "a")
		require.NoError(t, err)
		for _, row := range aRows {
			assert.True(t, row.Deployed, "the active profile's %s", row.ID)
		}

		again, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		assert.Empty(t, again.Remove, "nothing is left to clear")
	})

	t.Run("--uninstall is refused", func(t *testing.T) {
		f := mixedDirFixture(t)
		before := treeOf(t, f.gameDir)
		_, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{Uninstall: true})
		requireRefusedForB(t, err)
		plan, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{Uninstall: true}, nil)
		requireRefusedForB(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir))
		rows, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
		require.NoError(t, err)
		assert.Len(t, rows, 2)
	})

	t.Run("a plan that is not recorded-only is refused", func(t *testing.T) {
		f := mixedDirFixture(t)
		before := treeOf(t, f.gameDir)
		_, err := f.svc.ApplyPurge(ctx, f.game, &core.PurgePlan{Profile: "b"}, core.PurgeOptions{}, nil)
		requireRefusedForB(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("a path shared after the plan is still kept", func(t *testing.T) {
		f := mixedDirFixture(t)
		plan, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		require.NoError(t, f.svc.ExecForTest(ctx,
			`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, 'a', 'bonly.esp', 'src', 'bonly')`,
			f.game.ID))
		result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
		assert.Zero(t, result.Purged)
		assert.Contains(t, result.Kept, core.PurgeKeptPath{Path: "bonly.esp", Profiles: []string{"a"}})
	})

	t.Run("a profile that became active since is a stale plan", func(t *testing.T) {
		f := mixedDirFixture(t)
		plan, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		require.NoError(t, f.svc.NewProfileManager().SetDefault(ctx, f.game.ID, "b"))
		_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
	})

	t.Run("the convenience call agrees", func(t *testing.T) {
		f := mixedDirFixture(t)
		rows, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
		require.NoError(t, err)
		result, err := f.svc.PurgeProfile(ctx, f.game, "b", rows, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, result.RemovedPaths)
		assert.FileExists(t, filepath.Join(f.gameDir, "shared.esp"))
		assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
	})

	t.Run("the active profile still purges", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanPurge(ctx, f.game, "a", core.PurgeOptions{})
		require.NoError(t, err)
		assert.False(t, plan.RecordedOnly)
		result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, 2, result.Purged)
		assert.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	})
}

// TestDeploy_AGameWithNoProfileFilesDeploysDefault: with no profile file at
// all, "default" is the profile every frontend resolves, and the only one a
// deploy may act for.
func TestDeploy_AGameWithNoProfileFilesDeploysDefault(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "bare", Name: "Bare", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()

	_, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
	assert.NotErrorIs(t, err, core.ErrProfileNotActive)
	_, err = svc.PlanDeploy(ctx, game, "other", core.DeployOptions{})
	require.ErrorIs(t, err, core.ErrProfileNotActive)
}
