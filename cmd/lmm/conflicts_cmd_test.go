package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupConflictsCmdTest points the package-global configDir/dataDir at temp
// dirs and opens a *core.Service against them (with a CacheDir matching
// getServiceConfig's default, dataDir/cache, since no config.yaml is
// written), then registers a game via svc.SaveGame so a later, independently
// opened service — the one initService builds inside the real command path —
// can resolve it. Unlike setupConflictsTest (conflicts_test.go), which hands
// doConflicts an isolated *core.Service the command path never touches, this
// exercises runConflicts for real: requireGame, withGameService, and
// resolveProfile all run.
//
// Callers must svc.Close() after seeding (SaveInstalledMod/DeployProfile
// etc.) and before driving rootCmd, so the command path's own initService
// call opens a fresh, unlocked handle on the same on-disk DB.
func setupConflictsCmdTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	cacheDir := filepath.Join(dataDir, "cache")

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: cacheDir})
	require.NoError(t, err)

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	oldGameID, oldProfile, oldJSON := gameID, conflictsProfile, jsonOutput
	gameID = "g1"
	conflictsProfile = ""
	jsonOutput = false
	t.Cleanup(func() {
		gameID, conflictsProfile, jsonOutput = oldGameID, oldProfile, oldJSON
		rootCmd.SetArgs(nil)
	})

	return svc, game
}

// TestRunConflicts_ViaCommand_NoConflicts drives the real `conflicts`
// command tree (requireGame -> withGameService -> resolveProfile ->
// doConflicts) end to end against two non-overlapping installed, deployed
// mods and checks the no-conflicts text path.
func TestRunConflicts_ViaCommand_NoConflicts(t *testing.T) {
	svc, game := setupConflictsCmdTest(t)
	seedConflictMod(t, svc, game, "a", "Mod A", true, map[string][]byte{"a-only.esp": []byte("A")})
	seedConflictMod(t, svc, game, "b", "Mod B", true, map[string][]byte{"b-only.esp": []byte("B")})
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Close())

	rootCmd.SetArgs([]string{"conflicts", "--game", game.ID})
	out := captureStdout(t, func() error { return rootCmd.ExecuteContext(context.Background()) })

	assert.Equal(t, "No conflicts found.\n", out)
}

// TestRunConflicts_ViaCommand_RealConflict_Text drives the real command tree
// against two mods that both ship shared.esp, deployed so Mod B (later in
// load order) owns the path, and checks the rendered conflict text.
func TestRunConflicts_ViaCommand_RealConflict_Text(t *testing.T) {
	svc, game := setupConflictsCmdTest(t)
	seedTwinConflictFixture(t, svc, game)
	require.NoError(t, svc.Close())

	rootCmd.SetArgs([]string{"conflicts", "--game", game.ID})
	out := captureStdout(t, func() error { return rootCmd.ExecuteContext(context.Background()) })

	assert.Contains(t, out, "Found 1 conflicting file(s):")
	assert.Contains(t, out, "shared.esp")
	assert.Contains(t, out, "Owner: Mod B")
	assert.Contains(t, out, "Also in: Mod A")
	assert.Contains(t, out, "Winner: Mod B")
	assert.NotContains(t, out, "stale", "owner and load-order winner agree, so no stale suffix")
}

// TestRunConflicts_ViaCommand_JSON drives the real command tree with --json
// and asserts the decoded document's shape and field values, not just that
// it parses.
func TestRunConflicts_ViaCommand_JSON(t *testing.T) {
	svc, game := setupConflictsCmdTest(t)
	seedTwinConflictFixture(t, svc, game)
	require.NoError(t, svc.Close())

	rootCmd.SetArgs([]string{"conflicts", "--game", game.ID, "--json"})
	out := captureStdout(t, func() error { return rootCmd.ExecuteContext(context.Background()) })

	var decoded conflictsJSONOutput
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))

	assert.Equal(t, game.ID, decoded.GameID)
	assert.Equal(t, "default", decoded.Profile)
	require.Len(t, decoded.Conflicts, 1)
	assert.Equal(t, "shared.esp", decoded.Conflicts[0].Path)
	assert.Equal(t, "Mod B", decoded.Conflicts[0].Owner)
	assert.Equal(t, []string{"Mod A"}, decoded.Conflicts[0].AlsoIn)
	assert.Equal(t, "Mod B", decoded.Conflicts[0].Winner)
	assert.False(t, decoded.Conflicts[0].Stale)
}
