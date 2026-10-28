package main

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

// TestDoPurge_RemovesStrandedFilesWithNoModLeft is #466 review F2 (#451):
// after a games.yaml hand edit moved mod_path, every mod was uninstalled,
// which leaves the files - and their records - under the old mod_path. The
// purge the refusal names used to say "No mods installed" and do nothing,
// so the refusal could never be cleared. It now names the files, removes
// them, and the move is allowed.
func TestDoPurge_RemovesStrandedFilesWithNoModLeft(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	root := t.TempDir()
	game.InstallPath, game.ModPath = root, filepath.Join(root, "Data")
	game.LinkMethod, game.LinkMethodExplicit = domain.LinkCopy, true
	require.NoError(t, svc.SaveGame(ctx, game))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "m", "1.0", "plugin.esp", []byte("mod")))
	seedSyncInstalledMod(t, svc, game, "src", "m", "Mod M", "1.0", "default", true, nil)
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "m", Version: "1.0"}))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(root, "Data", "plugin.esp")
	require.FileExists(t, deployed)

	// The hand edit, then the uninstall that leaves the stranded file.
	gamesFile := filepath.Join(configDir, "games.yaml")
	data, err := os.ReadFile(gamesFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(gamesFile, []byte(strings.ReplaceAll(string(data), game.ModPath, game.ModPath+"2")), 0o644))
	_, err = svc.ReloadGames()
	require.NoError(t, err)
	moved, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	_, err = svc.UninstallMod(ctx, moved, "default", "src", "m", core.UninstallOptions{})
	require.NoError(t, err)
	require.FileExists(t, deployed, "an uninstall does not know the old mod_path")

	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, false)
	setFlag(t, &purgeDryRun, true)
	preview := captureStdout(t, func() error { return doPurge(ctx, svc, moved) })
	assert.Contains(t, preview, "Would also remove 1 file(s) deployed under an earlier mod_path:\n  - "+deployed+"\n")
	require.FileExists(t, deployed, "a dry run removes nothing")

	setFlag(t, &purgeDryRun, false)
	setFlag(t, &purgeYes, true)
	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, moved) })

	assert.NotContains(t, stdout, "No mods installed")
	assert.Contains(t, stdout, "Removed 1 file(s) deployed under an earlier mod_path")
	assert.NoFileExists(t, deployed)
	problem, err := svc.ModPathProblem(ctx, moved)
	require.NoError(t, err)
	assert.Nil(t, problem, "nothing is recorded under the old mod_path any more")
	other := filepath.Join(root, "Data3")
	_, err = svc.EditGame(ctx, game.ID, core.GameEdit{ModPath: &other})
	assert.NoError(t, err, "and the mod_path can move again")
}
