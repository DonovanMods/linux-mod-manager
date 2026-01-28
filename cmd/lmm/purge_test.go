package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPurgeCmd_Structure tests the purge command structure
func TestPurgeCmd_Structure(t *testing.T) {
	assert.Equal(t, "purge", purgeCmd.Use)
	assert.NotEmpty(t, purgeCmd.Short)
	assert.NotEmpty(t, purgeCmd.Long)

	// Check flags exist
	assert.NotNil(t, purgeCmd.Flags().Lookup("profile"))
	assert.NotNil(t, purgeCmd.Flags().Lookup("uninstall"))
	assert.NotNil(t, purgeCmd.Flags().Lookup("yes"))
}

// TestPurgeCmd_NoGame tests purge without game flag
func TestPurgeCmd_NoGame(t *testing.T) {
	// Reset global state
	gameID = ""
	configDir = t.TempDir() // Use temp dir to avoid loading real default game

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(purgeCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"purge"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestPurgeCmd_DefaultFlags tests that default flag values are set
func TestPurgeCmd_DefaultFlags(t *testing.T) {
	profileFlag := purgeCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	uninstallFlag := purgeCmd.Flags().Lookup("uninstall")
	assert.Equal(t, "false", uninstallFlag.DefValue)

	yesFlag := purgeCmd.Flags().Lookup("yes")
	assert.Equal(t, "false", yesFlag.DefValue)
}

// TestPurgeCmd_GameNotFound tests purge with non-existent game
func TestPurgeCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"
	purgeYes = true

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(purgeCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"purge"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}

// TestPurgeCmd_NoModsToPurge tests purge with no installed mods
func TestPurgeCmd_NoModsToPurge(t *testing.T) {
	// Set up temp directories
	tempDir := t.TempDir()
	cfgDir := filepath.Join(tempDir, "config")
	dtaDir := filepath.Join(tempDir, "data")

	require.NoError(t, os.MkdirAll(cfgDir, 0755))
	require.NoError(t, os.MkdirAll(dtaDir, 0755))

	// Create a minimal games.yaml
	gamesYaml := `games:
  test-game:
    name: Test Game
    install_path: /tmp/test-game
    mod_path: /tmp/test-game/mods
    sources:
      nexusmods: testgame
`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "games.yaml"), []byte(gamesYaml), 0644))

	// Set global state
	configDir = cfgDir
	dataDir = dtaDir
	gameID = "test-game"
	purgeYes = true
	purgeProfile = ""
	purgeUninstall = false

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(purgeCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"purge"})

	err := cmd.Execute()
	assert.NoError(t, err)
}
