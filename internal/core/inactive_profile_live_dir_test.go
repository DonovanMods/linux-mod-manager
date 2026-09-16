package core_test

// #445: a game has one game directory, and it holds the ACTIVE profile's
// deployment. `lmm deploy -p <other>` put a second profile's mods beside
// the active one's, and `lmm purge -p <other>` removed files from a
// directory that profile does not own. Both now refuse, naming
// `lmm profile switch`, and leave the directory exactly as it was.

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

func TestPurge_ANonActiveProfileIsRefused(t *testing.T) {
	ctx := context.Background()

	for _, uninstall := range []bool{false, true} {
		t.Run(map[bool]string{false: "purge", true: "purge --uninstall"}[uninstall], func(t *testing.T) {
			f := liveDirFixture(t)
			opts := core.PurgeOptions{Uninstall: uninstall}
			before := treeOf(t, f.gameDir)
			beforeDoc := mustRead(t, f.profilePath("b"))

			_, err := f.svc.PlanPurge(ctx, f.game, "b", opts)
			requireRefusedForB(t, err)
			_, err = f.svc.ApplyPurge(ctx, f.game, &core.PurgePlan{Profile: "b"}, opts, nil)
			requireRefusedForB(t, err)
			rows, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
			require.NoError(t, err)
			_, err = f.svc.PurgeProfile(ctx, f.game, "b", rows, opts, nil)
			requireRefusedForB(t, err)

			assert.Equal(t, before, treeOf(t, f.gameDir), "the active profile's files are all still there")
			assert.Equal(t, beforeDoc, mustRead(t, f.profilePath("b")), "and b's document is untouched")
			after, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
			require.NoError(t, err)
			assert.Len(t, after, 2, "and so are b's rows")
		})
	}

	t.Run("the active profile still purges", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanPurge(ctx, f.game, "a", core.PurgeOptions{})
		require.NoError(t, err)
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
