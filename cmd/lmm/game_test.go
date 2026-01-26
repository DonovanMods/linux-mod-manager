package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGameCmd_Structure(t *testing.T) {
	assert.Equal(t, "game", gameCmd.Use)
	assert.NotEmpty(t, gameCmd.Short)
}

func TestGameSetDefaultCmd_Structure(t *testing.T) {
	assert.Equal(t, "set-default <game-id>", gameSetDefaultCmd.Use)
	assert.NotEmpty(t, gameSetDefaultCmd.Short)
	assert.NotEmpty(t, gameSetDefaultCmd.Long)
}

func TestGameShowDefaultCmd_Structure(t *testing.T) {
	assert.Equal(t, "show-default", gameShowDefaultCmd.Use)
	assert.NotEmpty(t, gameShowDefaultCmd.Short)
}

func TestGameClearDefaultCmd_Structure(t *testing.T) {
	assert.Equal(t, "clear-default", gameClearDefaultCmd.Use)
	assert.NotEmpty(t, gameClearDefaultCmd.Short)
}

func TestGameSetDefault_NoArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	gameCmdCopy.AddCommand(gameSetDefaultCmd)
	cmd.AddCommand(gameCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "set-default"})

	err := cmd.Execute()
	assert.Error(t, err)
	// Cobra should complain about missing argument
}

func TestGameSetDefault_GameNotFound(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	setDefaultCmdCopy := &cobra.Command{
		Use:  "set-default <game-id>",
		Args: cobra.ExactArgs(1),
		RunE: runGameSetDefault,
	}
	gameCmdCopy.AddCommand(setDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "set-default", "non-existent-game"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}

func TestGameShowDefault_NoDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	showDefaultCmdCopy := &cobra.Command{
		Use:  "show-default",
		Args: cobra.NoArgs,
		RunE: runGameShowDefault,
	}
	gameCmdCopy.AddCommand(showDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "show-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "No default game set")
}

func TestGameShowDefault_WithDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	// Set a default game in config
	cfg := &config.Config{DefaultGame: "test-game"}
	require.NoError(t, cfg.Save(tmpDir))

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	showDefaultCmdCopy := &cobra.Command{
		Use:  "show-default",
		Args: cobra.NoArgs,
		RunE: runGameShowDefault,
	}
	gameCmdCopy.AddCommand(showDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "show-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "test-game")
}

func TestGameClearDefault_NoDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	clearDefaultCmdCopy := &cobra.Command{
		Use:  "clear-default",
		Args: cobra.NoArgs,
		RunE: runGameClearDefault,
	}
	gameCmdCopy.AddCommand(clearDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "clear-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "No default game was set")
}

func TestGameClearDefault_WithDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	// Set a default game in config
	cfg := &config.Config{DefaultGame: "test-game"}
	require.NoError(t, cfg.Save(tmpDir))

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	clearDefaultCmdCopy := &cobra.Command{
		Use:  "clear-default",
		Args: cobra.NoArgs,
		RunE: runGameClearDefault,
	}
	gameCmdCopy.AddCommand(clearDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "clear-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "Cleared default game")
	assert.Contains(t, buf.String(), "test-game")

	// Verify it was actually cleared
	loadedCfg, err := config.Load(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, loadedCfg.DefaultGame)
}

func TestRequireGame_WithFlag(t *testing.T) {
	gameID = "test-game"
	err := requireGame(nil)
	assert.NoError(t, err)
	assert.Equal(t, "test-game", gameID)
}

func TestRequireGame_WithDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")
	gameID = "" // No flag

	// Set a default game in config
	cfg := &config.Config{DefaultGame: "default-game"}
	require.NoError(t, cfg.Save(tmpDir))

	err := requireGame(nil)
	assert.NoError(t, err)
	assert.Equal(t, "default-game", gameID)
}

func TestRequireGame_NoGameNoDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")
	gameID = "" // No flag

	err := requireGame(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
	assert.Contains(t, err.Error(), "game set-default")
}

func TestConfigDefaultGame_Persistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Save config with default game
	cfg := &config.Config{DefaultGame: "my-game"}
	require.NoError(t, cfg.Save(tmpDir))

	// Verify file was created
	configPath := filepath.Join(tmpDir, "config.yaml")
	_, err := os.Stat(configPath)
	require.NoError(t, err)

	// Load and verify
	loaded, err := config.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "my-game", loaded.DefaultGame)
}
