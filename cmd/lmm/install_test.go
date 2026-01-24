package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// TestInstallCmd_Structure tests the install command structure
func TestInstallCmd_Structure(t *testing.T) {
	assert.Equal(t, "install <mod-id>", installCmd.Use)
	assert.NotEmpty(t, installCmd.Short)
	assert.NotEmpty(t, installCmd.Long)

	// Check flags exist
	assert.NotNil(t, installCmd.Flags().Lookup("source"))
	assert.NotNil(t, installCmd.Flags().Lookup("profile"))
	assert.NotNil(t, installCmd.Flags().Lookup("version"))
}

// TestInstallCmd_NoGame tests install without game flag
func TestInstallCmd_NoGame(t *testing.T) {
	// Reset flags
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestInstallCmd_NoModID tests install without mod-id argument
func TestInstallCmd_NoModID(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

// TestInstallCmd_DefaultFlags tests that default flag values are set
func TestInstallCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := installCmd.Flags().Lookup("source")
	assert.Equal(t, "nexusmods", sourceFlag.DefValue)

	profileFlag := installCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	versionFlag := installCmd.Flags().Lookup("version")
	assert.Equal(t, "", versionFlag.DefValue)
}

// TestInstallCmd_GameNotFound tests install with non-existent game
func TestInstallCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}
