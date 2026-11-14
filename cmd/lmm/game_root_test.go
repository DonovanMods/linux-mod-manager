package main

// #629 at the CLI: a file the bethesda adapter routes to the game root is
// deployed beside the game's executable, and every command that lists
// deployed files names it "<game root>/...", never as the Data file of the
// same name.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGameRootCLI is a Skyrim-shaped game (mod_path = install/Data, the
// bethesda adapter) with two installed mods that both ship d3d11.dll, and
// one of them an enbseries/ folder and a texture.
func setupGameRootCLI(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	app.RegisterAdapters(svc)
	game.InstallPath = filepath.Dir(game.ModPath)
	game.ModPath = filepath.Join(game.ModPath, "Data")
	game.Adapter = "bethesda"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	mods := map[string]map[string]string{
		"enb":  {"d3d11.dll": "enb d3d11", "enbseries/effect.fx": "shader", "textures/sky.dds": "sky"},
		"enb2": {"d3d11.dll": "other d3d11"},
	}
	for _, id := range []string{"enb", "enb2"} {
		for rel, content := range mods[id] {
			require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: id, Version: "1.0"}, rel, []byte(content)))
		}
		seedSyncInstalledMod(t, svc, game, "src", id, id, "1.0", "default", true, nil)
		require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}
	return svc, game
}

func TestCLI_GameRootFilesAreNamedByTheirRoot(t *testing.T) {
	ctx := context.Background()
	svc, game := setupGameRootCLI(t)

	setFlag(t, &verbose, true)
	setFlag(t, &deployDryRun, true)
	stdout := captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "+ <game root>/d3d11.dll")
	assert.Contains(t, stdout, "+ <game root>/enbseries/effect.fx")
	assert.Contains(t, stdout, "+ textures/sky.dds")
	setFlag(t, &deployDryRun, false)
	setFlag(t, &verbose, false)

	_ = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	got, err := os.ReadFile(filepath.Join(game.InstallPath, "d3d11.dll"))
	require.NoError(t, err)
	assert.Equal(t, "other d3d11", string(got), "deployed beside the game's executable")
	_, err = os.Lstat(filepath.Join(game.ModPath, "d3d11.dll"))
	assert.True(t, os.IsNotExist(err), "not into Data/")

	stdout = captureStdout(t, func() error { return doConflicts(ctx, svc, game) })
	assert.Contains(t, stdout, "  <game root>/d3d11.dll\n")

	setFlag(t, &modSource, "") // doModFiles resolves it from the installed mod
	stdout = captureStdout(t, func() error { return doModFiles(ctx, svc, game, "enb") })
	assert.Contains(t, stdout, "  <game root>/enbseries/effect.fx\n")
	assert.Contains(t, stdout, "  textures/sky.dds\n")

	// The user swaps the ENB shader for their own: verify names it under
	// the root, and the purge keeps it, saying where it is.
	shader := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	require.NoError(t, os.Remove(shader))
	require.NoError(t, os.WriteFile(shader, []byte("mine"), 0o644))
	stdout, _ = captureStdoutErr(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "<game root>/enbseries/effect.fx")
	assert.NotContains(t, stdout, "\x00")

	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, true)
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Contains(t, stdout, "<game root>/enbseries/effect.fx")
	_, err = os.Lstat(filepath.Join(game.InstallPath, "d3d11.dll"))
	assert.True(t, os.IsNotExist(err), "purge removes the game-root file")
	assert.FileExists(t, shader, "and keeps the one that is yours")
}

// #632 at the CLI: the game ships a d3d11.dll of its own and both mods ship
// one too. The deploy sets the game's file aside and deploys the load-order
// winner without claiming the user replaced anything, conflicts lists the
// path, verify finds lmm's own file there, and the purge puts the game's
// file back.
func TestCLI_TwoModsSharingAGameRootFileOverTheGamesOwn(t *testing.T) {
	ctx := context.Background()
	svc, game := setupGameRootCLI(t)
	stock := filepath.Join(game.InstallPath, "d3d11.dll")
	require.NoError(t, os.WriteFile(stock, []byte("the game's own"), 0o644))

	out := captureCombined(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.NotContains(t, out, "replaced lmm's link", "lmm never deployed that path")
	assert.NotContains(t, out, "was not replaced")
	got, err := os.ReadFile(stock)
	require.NoError(t, err)
	assert.Equal(t, "other d3d11", string(got), "the load-order winner's file is at the game root")

	out = captureStdout(t, func() error { return doConflicts(ctx, svc, game) })
	assert.Contains(t, out, "  <game root>/d3d11.dll\n")

	out = captureCombined(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.NotContains(t, out, "replaced lmm's link")

	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, true)
	_ = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	got, err = os.ReadFile(stock)
	require.NoError(t, err, "purge puts the game's own d3d11.dll back")
	assert.Equal(t, "the game's own", string(got))
}
