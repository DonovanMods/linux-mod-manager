package core_test

// A game every deploy-direction flow refuses - an explicit `adapter:
// bepinex` whose mod_path is not its install path (#413 re-review P-b) -
// must still be one a user can get OUT of with lmm's own commands, in the
// order lmm gives them, with nothing left behind that lmm then fails to
// report (#413 final review F4).
//
// The state is reachable by hand: a v1 games.yaml points mod_path at
// <install>/BepInEx/plugins, and adding `adapter: bepinex` to it is the
// obvious edit for a user who has read that lmm now supports BepInEx.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedStateArchives are the archive shapes a v1 user deployed into
// BepInEx/plugins: a loose plugin, a plugin folder, and an archive rooted
// at BepInEx/ - the one whose as-packaged deployment nests a whole BepInEx/
// tree inside the plugins directory.
var refusedStateArchives = map[string]map[string]string{
	"LoosePlugin-1.0.0.zip": {"LoosePlugin.dll": "loose"},
	"CoolFolder-1.0.0.zip": {
		"CoolFolder/CoolFolder.dll": "cool",
		"CoolFolder/assets.bundle":  "bundle",
	},
	"Rooted-1.0.0.zip": {
		"BepInEx/plugins/Rooted.dll": "rooted",
		"BepInEx/config/rooted.cfg":  "key = value\n",
	},
}

// newRefusedBepInExGame is the v1 game with its archives deployed, then the
// hand edit that adds `adapter: bepinex`. The returned game is the one the
// Service now holds.
func newRefusedBepInExGame(t *testing.T, loader *domain.GameLoader) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := newPluginsModPathService(t, loader)
	// A log newer than every deploy this test makes, so the loader tier's
	// "has it run since?" question has nothing to say about a declared game.
	bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapNative, time.Now().Add(time.Hour))
	for archive, members := range refusedStateArchives {
		path := filepath.Join(t.TempDir(), archive)
		createImportTestZip(t, path, members)
		_, err := svc.ImportArchive(context.Background(), game, "default", path, core.ImportArchiveOptions{Force: true}, nil)
		require.NoError(t, err, archive)
	}
	nested := filepath.Join(game.ModPath, "BepInEx", "plugins", "Rooted.dll")
	_, err := os.Lstat(nested)
	require.NoError(t, err, "the v1 deployment nests the rooted archive's own BepInEx/ tree")

	refused := *game
	refused.Adapter = "bepinex"
	require.NoError(t, svc.SaveGame(context.Background(), &refused))
	stored, err := svc.GetGame(refused.ID)
	require.NoError(t, err)
	return svc, stored
}

// deployedModTree lists every non-directory entry under root that the
// fixture did not write, slash-separated and sorted.
func deployedModTree(t *testing.T, root string) []string {
	t.Helper()
	fixture := map[string]bool{
		"BepInEx/core/BepInEx.Preloader.dll": true,
		"BepInEx/LogOutput.log":              true,
		"run_bepinex.sh":                     true,
		"libdoorstop.so":                     true,
	}
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !fixture[filepath.ToSlash(rel)] {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	}))
	sort.Strings(out)
	return out
}

// TestRemoval_WorksWhereEveryOtherFlowIsRefused: purge and uninstall remove
// what lmm recorded deploying, which needs no layout at all, so the
// refusal that stops a deploy must not stop them - they are the first step
// of every way out of that state.
func TestRemoval_WorksWhereEveryOtherFlowIsRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("deploy is refused", func(t *testing.T) {
		svc, game := newRefusedBepInExGame(t, nil)
		_, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sets adapter: bepinex")
	})

	t.Run("purge", func(t *testing.T) {
		svc, game := newRefusedBepInExGame(t, nil)
		plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
		require.NoError(t, err, "purge --dry-run")
		assert.Len(t, plan.Mods, len(refusedStateArchives))

		res, err := svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, len(refusedStateArchives), res.Purged)
		assert.Empty(t, deployedModTree(t, game.InstallPath), "every deployed file is gone")
	})

	t.Run("purge --uninstall", func(t *testing.T) {
		svc, game := newRefusedBepInExGame(t, nil)
		opts := core.PurgeOptions{Uninstall: true}
		plan, err := svc.PlanPurge(ctx, game, "default", opts)
		require.NoError(t, err)
		_, err = svc.ApplyPurge(ctx, game, plan, opts, nil)
		require.NoError(t, err)
		mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
		require.NoError(t, err)
		assert.Empty(t, mods)
		assert.Empty(t, deployedModTree(t, game.InstallPath))
	})

	t.Run("uninstall", func(t *testing.T) {
		svc, game := newRefusedBepInExGame(t, nil)
		mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
		require.NoError(t, err)
		require.Len(t, mods, len(refusedStateArchives))
		for _, m := range mods {
			plan, err := svc.PlanUninstall(ctx, game, "default", m.SourceID, m.ID, core.UninstallOptions{})
			require.NoError(t, err, "uninstall --dry-run %s", m.Name)
			assert.NotEmpty(t, plan.Files, "the plan names what %s deployed", m.Name)
			_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
			require.NoError(t, err, "uninstall %s", m.Name)
		}
		assert.Empty(t, deployedModTree(t, game.InstallPath))
	})
}

// TestAdapterFor_TheOffRootRefusalPutsThePurgeFirst pins the refusal whole:
// every step a user needs, in the only order that leaves nothing behind -
// purge while the records still describe where the files are, then move
// mod_path - and the other way out.
func TestAdapterFor_TheOffRootRefusalPutsThePurgeFirst(t *testing.T) {
	svc, game := newPluginsModPathService(t, nil)
	refused := *game
	refused.Adapter = "bepinex"
	_, err := svc.AdapterFor(&refused)
	require.Error(t, err)
	root := game.InstallPath
	assert.Equal(t, fmt.Sprintf("game %[1]q sets adapter: bepinex but its mod_path (%[2]s) is not its install path, and a BepInEx layout is relative to the game root. "+
		"To have lmm lay BepInEx archives out, run `lmm purge --game %[1]s`, then set its mod_path to %[3]s in games.yaml, "+
		"then run `lmm deploy --game %[1]s` and `lmm verify --fix --game %[1]s`, which moves what is already imported under BepInEx/; "+
		"or, to deploy archives into %[2]s exactly as packaged, run `lmm game edit %[1]s --adapter generic-files`",
		"valheim", game.ModPath, root), err.Error())
}

// TestRefusedBepInExGame_FollowingTheRefusalLeavesNothingBehind is the whole
// walk: reach the refused state, run exactly the steps its message gives,
// in its order, and end with every plugin where BepInEx loads it, every
// config where BepInEx reads it, and a verify with nothing to say.
func TestRefusedBepInExGame_FollowingTheRefusalLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	for name, loader := range map[string]*domain.GameLoader{
		"installed, undeclared (the v1 configuration)": nil,
		"installed and declared":                       {Kind: domain.LoaderKindBepInEx},
	} {
		t.Run(name, func(t *testing.T) {
			svc, game := newRefusedBepInExGame(t, loader)
			_, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
			require.Error(t, err)
			msg := err.Error()
			steps := []string{"`lmm purge --game valheim`", "set its mod_path to " + game.InstallPath, "`lmm deploy --game valheim`", "`lmm verify --fix --game valheim`"}
			last := -1
			for _, step := range steps {
				at := strings.Index(msg, step)
				require.Greater(t, at, last, "%q must come after the step before it in: %s", step, msg)
				last = at
			}

			// 1. lmm purge --game valheim
			plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			_, err = svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)

			// 2. set its mod_path to the install path in games.yaml
			moved := *game
			moved.ModPath = game.InstallPath
			require.NoError(t, svc.SaveGame(ctx, &moved))
			game, err = svc.GetGame(game.ID)
			require.NoError(t, err)
			require.Equal(t, "bepinex", svc.AdapterName(game))

			// 3. lmm deploy --game valheim
			deployPlan, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
			require.NoError(t, err)
			_, err = svc.ApplyDeploy(ctx, game, deployPlan, core.DeployOptions{}, nil)
			require.NoError(t, err)

			// 4. lmm verify --fix --game valheim
			_, err = svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
			require.NoError(t, err)

			report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
			require.NoError(t, err)
			for _, f := range report.Result.Findings {
				assert.Equal(t, "ok", f.Status, "verify has nothing to report: %+v", f)
			}
			assert.Zero(t, report.Result.Issues)
			assert.Zero(t, report.Result.Warnings)

			tree := deployedModTree(t, game.InstallPath)
			assert.Equal(t, []string{
				"BepInEx/config/rooted.cfg",
				"BepInEx/plugins/CoolFolder/CoolFolder.dll",
				"BepInEx/plugins/CoolFolder/assets.bundle",
				"BepInEx/plugins/LoosePlugin-1.0.0/LoosePlugin.dll",
				"BepInEx/plugins/Rooted.dll",
			}, tree)
		})
	}
}
