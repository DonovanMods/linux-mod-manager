package main

// #635 at the CLI: a Witcher 3 mod of the witcher3tl type (the game
// directory) deploys its mods/ folder into the mod_path and its bin/ folder
// at the game root, and deploy names the root file "<game root>/...".

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_Witcher3TopLevelTypeDeploysFromTheGameRoot(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	app.RegisterAdapters(svc)
	game.InstallPath = game.ModPath
	game.ModPath = filepath.Join(game.InstallPath, "mods")
	game.Adapter = "witcher3"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	for rel, content := range map[string]string{
		"mods/modMenu/content/blob0.bundle":                   "bundle",
		"bin/config/r4game/user_config_matrix/pc/modMenu.xml": "menu",
	} {
		require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: "menu", Version: "1.0"}, rel, []byte(content)))
	}
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "menu", SourceID: "src", Name: "Menu", Version: "1.0", GameID: game.ID, ModType: "witcher3tl"},
		ProfileName: "default", Enabled: true, UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "menu", Version: "1.0", ModType: "witcher3tl"}))

	setFlag(t, &verbose, true)
	setFlag(t, &deployDryRun, true)
	stdout := captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "+ <game root>/bin/config/r4game/user_config_matrix/pc/modMenu.xml")
	assert.Contains(t, stdout, "+ modMenu/content/blob0.bundle", "the archive's mods/ folder is the mod_path")
	setFlag(t, &deployDryRun, false)
	setFlag(t, &verbose, false)

	_ = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	got, err := os.ReadFile(filepath.Join(game.InstallPath, "bin", "config", "r4game", "user_config_matrix", "pc", "modMenu.xml"))
	require.NoError(t, err)
	assert.Equal(t, "menu", string(got))
	got, err = os.ReadFile(filepath.Join(game.ModPath, "modMenu", "content", "blob0.bundle"))
	require.NoError(t, err)
	assert.Equal(t, "bundle", string(got))
}
