package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// TestUninstallCmd_Structure tests the uninstall command structure
func TestUninstallCmd_Structure(t *testing.T) {
	assert.Equal(t, "uninstall <mod-id>", uninstallCmd.Use)
	assert.NotEmpty(t, uninstallCmd.Short)
	assert.NotEmpty(t, uninstallCmd.Long)

	// Check flags exist
	assert.NotNil(t, uninstallCmd.Flags().Lookup("source"))
	assert.NotNil(t, uninstallCmd.Flags().Lookup("profile"))
	assert.NotNil(t, uninstallCmd.Flags().Lookup("keep-cache"))
}

// TestUninstallCmd_NoGame tests uninstall without game flag
func TestUninstallCmd_NoGame(t *testing.T) {
	// Reset flags
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(uninstallCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"uninstall", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestUninstallCmd_NoModID tests uninstall without mod-id argument
func TestUninstallCmd_NoModID(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(uninstallCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"uninstall"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

// TestUninstallCmd_DefaultFlags tests that default flag values are set
func TestUninstallCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := uninstallCmd.Flags().Lookup("source")
	assert.Equal(t, "", sourceFlag.DefValue) // empty = auto-detect from game config

	profileFlag := uninstallCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	keepCacheFlag := uninstallCmd.Flags().Lookup("keep-cache")
	assert.Equal(t, "false", keepCacheFlag.DefValue)
}

// TestUninstallCmd_GameNotFound tests uninstall with non-existent game
func TestUninstallCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(uninstallCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"uninstall", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}
