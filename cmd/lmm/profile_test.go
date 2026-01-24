package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestProfileCmd_Structure(t *testing.T) {
	assert.Equal(t, "profile", profileCmd.Use)
	assert.NotEmpty(t, profileCmd.Short)

	// Check subcommands exist
	var subCmds []string
	for _, cmd := range profileCmd.Commands() {
		subCmds = append(subCmds, cmd.Name())
	}

	assert.Contains(t, subCmds, "list")
	assert.Contains(t, subCmds, "create")
	assert.Contains(t, subCmds, "delete")
	assert.Contains(t, subCmds, "switch")
	assert.Contains(t, subCmds, "export")
	assert.Contains(t, subCmds, "import")
}

func TestProfileListCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "list"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestProfileCreateCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "create", "myprofile"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestProfileCreateCmd_NoName(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "create"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}
