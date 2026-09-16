package core_test

// Deletion safety for purge and uninstall where the game's adapter is
// refused (#413 fix round 4, F2). Those two flows are exempt from the
// refusal so a user can always undo a deployment - but an exemption that
// removes by the cache entry's listing, under the identity's routing, also
// removed what the refused adapter would have protected: a user's own
// symlinked BepInEx/config file, which carries no deployed_files row. With
// the adapter refused, a removal takes only what lmm RECORDED deploying.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rootedWithConfigs is a BepInEx/-rooted archive shipping two configs: the
// bepinex adapter seeds both once, with no deployed_files row, and never
// removes either.
var rootedWithConfigs = map[string]string{
	"BepInEx/plugins/Rooted.dll": "rooted",
	"BepInEx/config/rooted.cfg":  "key = value\n",
	"BepInEx/config/linked.cfg":  "other = value\n",
}

// newGameRootWithUserConfigs is the review's P2: a game at its root on
// bepinex with the archive above imported, then the user's own changes -
// rooted.cfg edited in place, linked.cfg replaced by a symlink into their
// dotfiles. It returns the dotfile the link points at.
func newGameRootWithUserConfigs(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game := newGameRootBepInExGame(t)
	return userConfigsOn(t, svc, game)
}

// userConfigsOn is newGameRootWithUserConfigs for a game the caller built.
func userConfigsOn(t *testing.T, svc *core.Service, game *domain.Game) (*core.Service, *domain.Game, string) {
	t.Helper()
	importInto(t, svc, game, "Rooted-1.0.0.zip", rootedWithConfigs)

	cfg := filepath.Join(game.InstallPath, "BepInEx", "config")
	require.FileExists(t, filepath.Join(cfg, "linked.cfg"), "fixture: the config was seeded")
	f, err := os.OpenFile(filepath.Join(cfg, "rooted.cfg"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("user edit\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	dotfile := writeGameFile(t, t.TempDir(), "dotfiles/linked.cfg", "mine\n")
	require.NoError(t, os.Remove(filepath.Join(cfg, "linked.cfg")))
	require.NoError(t, os.Symlink(dotfile, filepath.Join(cfg, "linked.cfg")))
	return svc, game, dotfile
}

// refuse saves game with edit applied and returns the stored game, which
// AdapterFor must now refuse.
func refuse(t *testing.T, svc *core.Service, game *domain.Game, edit func(*domain.Game)) *domain.Game {
	t.Helper()
	changed := *game
	edit(&changed)
	require.NoError(t, svc.SaveGame(context.Background(), &changed))
	stored, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	_, err = svc.AdapterFor(stored)
	require.Error(t, err, "fixture: the adapter is refused")
	return stored
}

// refusals are the two ways a game at its root is refused: an adapter name
// this build does not have, and bepinex combined with deploy_mode: compile.
var refusals = map[string]func(*domain.Game){
	"unknown adapter":           func(g *domain.Game) { g.Adapter = "bepinx" },
	"bepinex with compile mode": func(g *domain.Game) { g.Adapter = "bepinex"; g.DeployMode = domain.DeployCompile },
}

// requireUserConfigsKept checks both of the user's configs are where they
// left them.
func requireUserConfigsKept(t *testing.T, game *domain.Game, dotfile string) {
	t.Helper()
	cfg := filepath.Join(game.InstallPath, "BepInEx", "config")
	link := filepath.Join(cfg, "linked.cfg")
	requireLink(t, link)
	target, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, dotfile, target)
	edited, err := os.ReadFile(filepath.Join(cfg, "rooted.cfg"))
	require.NoError(t, err)
	assert.Contains(t, string(edited), "user edit")
	assert.FileExists(t, dotfile)
}

// TestRemoval_ARefusedAdapterRemovesOnlyWhatLmmRecorded: purge and
// uninstall of a refused game take the plugin lmm recorded deploying and
// leave both configs - neither has a record - exactly as the user left them.
func TestRemoval_ARefusedAdapterRemovesOnlyWhatLmmRecorded(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		t.Run(name+"/purge", func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			game = refuse(t, svc, game, edit)
			plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			res, err := svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, 1, res.Purged)

			requireUserConfigsKept(t, game, dotfile)
			assert.NoFileExists(t, filepath.Join(game.InstallPath, "BepInEx", "plugins", "Rooted.dll"))
		})

		// `lmm mod disable` is a removal no adapter refusal gates at all.
		t.Run(name+"/disable", func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			game = refuse(t, svc, game, edit)
			mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
			require.NoError(t, err)
			require.Len(t, mods, 1)
			_, err = svc.DisableMod(ctx, game, "default", mods[0].SourceID, mods[0].ID)
			require.NoError(t, err)

			requireUserConfigsKept(t, game, dotfile)
			assert.NoFileExists(t, filepath.Join(game.InstallPath, "BepInEx", "plugins", "Rooted.dll"))
		})

		t.Run(name+"/uninstall", func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			game = refuse(t, svc, game, edit)
			mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
			require.NoError(t, err)
			require.Len(t, mods, 1)
			plan, err := svc.PlanUninstall(ctx, game, "default", mods[0].SourceID, mods[0].ID, core.UninstallOptions{})
			require.NoError(t, err)
			assert.Equal(t, []string{"BepInEx/plugins/Rooted.dll"}, plan.Files, "the dry run names exactly what the uninstall removes")
			_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
			require.NoError(t, err)

			requireUserConfigsKept(t, game, dotfile)
			assert.NoFileExists(t, filepath.Join(game.InstallPath, "BepInEx", "plugins", "Rooted.dll"))
		})
	}
}

// TestRemoval_AResolvingAdapterPlansOnlyWhatItRemoves is the pre-existing
// half the review noted: with bepinex resolving, uninstall never removed a
// seeded config, but its dry run listed both.
func TestRemoval_AResolvingAdapterPlansOnlyWhatItRemoves(t *testing.T) {
	ctx := context.Background()
	svc, game, dotfile := newGameRootWithUserConfigs(t)
	mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	plan, err := svc.PlanUninstall(ctx, game, "default", mods[0].SourceID, mods[0].ID, core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"BepInEx/plugins/Rooted.dll"}, plan.Files)

	deploy, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{Purge: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"BepInEx/plugins/Rooted.dll"}, deploy.Purge, "deploy --purge's preview is the same removal")

	_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
	require.NoError(t, err)
	requireUserConfigsKept(t, game, dotfile)
}

// TestRemoval_ACompileRefusalStillRoutesThroughBepInEx: a game refused only
// because bepinex cannot compile is still laid out by bepinex - that is
// what deployed it - so its rule that a purge never removes a
// BepInEx/config file holds even for a config path lmm has a record of.
func TestRemoval_ACompileRefusalStillRoutesThroughBepInEx(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := newGameRootWithUserConfigs(t)
	mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	recorded := filepath.Join(game.InstallPath, "BepInEx", "config", "recorded.cfg")
	target := writeGameFile(t, t.TempDir(), "recorded.cfg", "x")
	require.NoError(t, os.Symlink(target, recorded))
	require.NoError(t, svc.ExecForTest(ctx,
		`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, ?, ?, ?, ?)`,
		game.ID, "default", "BepInEx/config/recorded.cfg", mods[0].SourceID, mods[0].ID))
	cache := svc.GetGameCache(game)
	require.NoError(t, cache.Store(game.ID, mods[0].SourceID, mods[0].ID, mods[0].Version, "BepInEx/config/recorded.cfg", []byte("x")))

	game = refuse(t, svc, game, refusals["bepinex with compile mode"])
	plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	requireLink(t, recorded)
}

// TestRemoval_AnOffRootRefusalStillRemovesTheV1Deployment: the named
// adapter is NOT used where the refusal is about the mod_path itself - the
// v1 deployment under BepInEx/plugins was laid out by the identity, so its
// nested BepInEx/config link is an ordinary recorded deployment.
func TestRemoval_AnOffRootRefusalStillRemovesTheV1Deployment(t *testing.T) {
	ctx := context.Background()
	svc, game := newRefusedBepInExGame(t, nil)
	plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(game.ModPath, "BepInEx", "config", "rooted.cfg"))
	assert.ErrorIs(t, err, fs.ErrNotExist)
}
