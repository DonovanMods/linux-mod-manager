package main

// #636 at the CLI: Baldur's Gate 3's .pak mods are deployed into the Mods
// folder in the game's Wine/Proton prefix, every command that lists
// deployed files names them "<prefix>/...", uninstall and purge take them
// away again, and a prefix lmm cannot resolve refuses the deploy.

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

const cliBG3Mods = "AppData/Local/Larian Studios/Baldur's Gate 3/Mods"

// setupPrefixCLI is a Baldur's Gate 3 game (mod_path = install/Data, the
// baldurs-gate-3 adapter) with an explicit prefix - or none, for
// withPrefix false - and one installed mod shipping a wrapped .pak and a
// loose file. The game is returned as GetGame hands it to a command.
func setupPrefixCLI(t *testing.T, withPrefix bool) (*core.Service, *domain.Game, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	app.RegisterAdapters(svc)
	game.InstallPath = filepath.Dir(game.ModPath)
	game.ModPath = filepath.Join(game.ModPath, "Data")
	game.Adapter = "baldurs-gate-3"
	prefix := ""
	if withPrefix {
		prefix = filepath.Join(t.TempDir(), "pfx")
		require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
		game.Prefix, game.PrefixUser = prefix, "steamuser"
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	for rel, content := range map[string]string{"Camera-1.0/Camera.pak": "pak", "Public/Camera/x.lsx": "loose"} {
		require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: "camera", Version: "1.0"}, rel, []byte(content)))
	}
	seedSyncInstalledMod(t, svc, game, "src", "camera", "camera", "1.0", "default", true, nil)
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "camera", Version: "1.0"}))
	got, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	return svc, got, prefix
}

func TestCLI_PrefixFilesDeployUninstallAndPurge(t *testing.T) {
	ctx := context.Background()
	svc, game, prefix := setupPrefixCLI(t, true)
	pak := filepath.Join(prefix, "drive_c", "users", "steamuser", filepath.FromSlash(cliBG3Mods), "Camera.pak")

	setFlag(t, &verbose, true)
	setFlag(t, &deployDryRun, true)
	stdout := captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "+ <prefix>/"+cliBG3Mods+"/Camera.pak")
	assert.Contains(t, stdout, "+ Public/Camera/x.lsx")
	setFlag(t, &deployDryRun, false)
	setFlag(t, &verbose, false)

	out := captureCombined(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, out, "modsettings.lsx", "the deploy says what lmm does not do yet")
	got, err := os.ReadFile(pak)
	require.NoError(t, err)
	assert.Equal(t, "pak", string(got), "the pak is in the prefix's Mods folder, flat")
	_, err = os.Lstat(filepath.Join(game.ModPath, "Camera-1.0"))
	assert.True(t, os.IsNotExist(err), "not into Data/")
	assert.FileExists(t, filepath.Join(game.ModPath, "Public", "Camera", "x.lsx"))

	setFlag(t, &modSource, "")
	stdout = captureStdout(t, func() error { return doModFiles(ctx, svc, game, "camera") })
	assert.Contains(t, stdout, "  <prefix>/"+cliBG3Mods+"/Camera.pak\n")

	setFlag(t, &uninstallSource, "")
	setFlag(t, &uninstallKeep, true)
	_ = captureCombined(t, func() error { return doUninstall(ctx, svc, game, "camera") })
	_, err = os.Lstat(pak)
	assert.True(t, os.IsNotExist(err), "uninstall removes the pak from the prefix")
	assert.DirExists(t, filepath.Dir(pak), "and leaves the game's Mods folder")
}

func TestCLI_PrefixFilesArePurged(t *testing.T) {
	ctx := context.Background()
	svc, game, prefix := setupPrefixCLI(t, true)
	pak := filepath.Join(prefix, "drive_c", "users", "steamuser", filepath.FromSlash(cliBG3Mods), "Camera.pak")
	_ = captureCombined(t, func() error { return doDeploy(ctx, svc, game, nil) })
	require.FileExists(t, pak)

	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, true)
	_ = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	_, err := os.Lstat(pak)
	assert.True(t, os.IsNotExist(err), "purge removes the pak from the prefix")
}

func TestCLI_UnresolvedPrefixRefusesTheDeploy(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupPrefixCLI(t, false)
	err := doDeploy(ctx, svc, game, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--prefix", "the refusal says what to set")
	_, statErr := os.Lstat(filepath.Join(game.ModPath, "Camera-1.0"))
	assert.True(t, os.IsNotExist(statErr), "nothing falls back to Data/")
}
