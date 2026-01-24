package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestModCmd_Structure(t *testing.T) {
	assert.Equal(t, "mod", modCmd.Use)
	assert.NotEmpty(t, modCmd.Short)
}

func TestModSetUpdateCmd_Structure(t *testing.T) {
	assert.Equal(t, "set-update <mod-id>", modSetUpdateCmd.Use)
	assert.NotEmpty(t, modSetUpdateCmd.Short)
	assert.NotEmpty(t, modSetUpdateCmd.Long)

	// Check flags exist
	assert.NotNil(t, modSetUpdateCmd.Flags().Lookup("auto"))
	assert.NotNil(t, modSetUpdateCmd.Flags().Lookup("notify"))
	assert.NotNil(t, modSetUpdateCmd.Flags().Lookup("pin"))
}

func TestModSetUpdateCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	modCmdCopy.AddCommand(modSetUpdateCmd)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "set-update", "12345", "--auto"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestModSetUpdateCmd_NoPolicy(t *testing.T) {
	gameID = "test-game"
	modSetAuto = false
	modSetNotify = false
	modSetPin = false

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	setUpdateCmdCopy := &cobra.Command{
		Use:  "set-update <mod-id>",
		Args: cobra.ExactArgs(1),
		RunE: runModSetUpdate,
	}
	setUpdateCmdCopy.Flags().BoolVar(&modSetAuto, "auto", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetNotify, "notify", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetPin, "pin", false, "")
	modCmdCopy.AddCommand(setUpdateCmdCopy)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "set-update", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "specify a policy")
}

func TestModSetUpdateCmd_MultiplePolicies(t *testing.T) {
	gameID = "test-game"
	// Reset flags before test
	modSetAuto = false
	modSetPin = false
	modSetNotify = false

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	setUpdateCmdCopy := &cobra.Command{
		Use:  "set-update <mod-id>",
		Args: cobra.ExactArgs(1),
		RunE: runModSetUpdate,
	}
	setUpdateCmdCopy.Flags().BoolVar(&modSetAuto, "auto", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetNotify, "notify", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetPin, "pin", false, "")
	modCmdCopy.AddCommand(setUpdateCmdCopy)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	// Pass --auto and --pin flags via command line
	cmd.SetArgs([]string{"mod", "set-update", "12345", "--auto", "--pin"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "specify only one policy")

	// Reset flags after test
	modSetAuto = false
	modSetPin = false
}
