package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestUpdateCmd_NoGame(t *testing.T) {
	// Reset flags
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(updateCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"update"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestUpdateCmd_Structure(t *testing.T) {
	assert.Equal(t, "update [mod-id]", updateCmd.Use)
	assert.NotEmpty(t, updateCmd.Short)
	assert.NotEmpty(t, updateCmd.Long)

	// Check flags exist
	assert.NotNil(t, updateCmd.Flags().Lookup("source"))
	assert.NotNil(t, updateCmd.Flags().Lookup("profile"))
	assert.NotNil(t, updateCmd.Flags().Lookup("all"))
}
