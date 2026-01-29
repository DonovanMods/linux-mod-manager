package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestDeployCmd_Structure(t *testing.T) {
	assert.Equal(t, "deploy [mod-id]", deployCmd.Use)
	assert.NotEmpty(t, deployCmd.Short)
	assert.NotEmpty(t, deployCmd.Long)

	// Check flags exist
	assert.NotNil(t, deployCmd.Flags().Lookup("source"))
	assert.NotNil(t, deployCmd.Flags().Lookup("profile"))
	assert.NotNil(t, deployCmd.Flags().Lookup("method"))
	assert.NotNil(t, deployCmd.Flags().Lookup("purge"))
}

func TestDeployCmd_PurgeFlag(t *testing.T) {
	purgeFlag := deployCmd.Flags().Lookup("purge")
	assert.NotNil(t, purgeFlag)
	assert.Equal(t, "false", purgeFlag.DefValue)
	assert.Equal(t, "bool", purgeFlag.Value.Type())
}

func TestDeployCmd_NoGame(t *testing.T) {
	gameID = ""
	configDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(deployCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"deploy"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}
