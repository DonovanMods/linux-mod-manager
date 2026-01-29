package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusCmd_FullStructure tests the status command structure in detail
func TestStatusCmd_FullStructure(t *testing.T) {
	assert.Equal(t, "status", statusCmd.Use)
	assert.NotEmpty(t, statusCmd.Short)
	assert.NotEmpty(t, statusCmd.Long)

	// Status command should not require any flags
	assert.NotNil(t, statusCmd.RunE)
}

// TestStatusCmd_NoGames tests status when no games are configured
func TestStatusCmd_NoGames(t *testing.T) {
	// Use temp directories (empty, no games configured)
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(statusCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	// Should succeed even with no games
	err := cmd.Execute()
	assert.NoError(t, err)
}

// TestStatusCmd_WithGameFlag_NoGamesConfigured tests status with game flag when no games exist
func TestStatusCmd_WithGameFlag_NoGamesConfigured(t *testing.T) {
	// Use temp directories (empty, no games configured)
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "some-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(statusCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	// When no games are configured, status returns early with "No games configured"
	// regardless of the gameID flag - this is the actual behavior
	err := cmd.Execute()
	assert.NoError(t, err)
}

// TestStatusCmd_AcceptsGameFlag tests that status accepts the game flag
func TestStatusCmd_AcceptsGameFlag(t *testing.T) {
	// The status command should accept the --game flag from the root command
	// This is a persistent flag from rootCmd, so we test via the full command setup

	// Reset state
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	// Create a fresh root command for this test
	testRoot := &cobra.Command{Use: "lmm"}
	testRoot.PersistentFlags().StringVarP(&gameID, "game", "g", "", "game ID")
	testRoot.AddCommand(statusCmd)

	buf := new(bytes.Buffer)
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs([]string{"status", "--game", "test-game"})

	// Command succeeds (shows "No games configured"), but flag should be parsed
	err := testRoot.Execute()
	assert.NoError(t, err)
	assert.Equal(t, "test-game", gameID)
}

// TestStatusCmd_ShowsDefaultGame tests that status runs when default game is set
func TestStatusCmd_ShowsDefaultGame(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	// Set up a default game in config
	cfg := &config.Config{DefaultGame: "my-default-game"}
	err := cfg.Save(configDir)
	require.NoError(t, err)

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(statusCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	// Command succeeds (note: default game is only shown when games are configured)
	err = cmd.Execute()
	assert.NoError(t, err)
}

// TestStatusCmd_JSONOutput verifies status --json output structure (JSON contract / E2E shape).
// Encodes the same struct used by status --json and asserts round-trip and expected keys.
func TestStatusCmd_JSONOutput(t *testing.T) {
	out := statusJSONOutput{Games: []statusGameJSON{}}
	data, err := json.Marshal(out)
	require.NoError(t, err)

	var decoded struct {
		Games []interface{} `json:"games"`
	}
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err, "status JSON output must be valid JSON with 'games' key")
	assert.NotNil(t, decoded.Games)
	assert.Len(t, decoded.Games, 0)
}
