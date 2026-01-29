package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestListCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(listCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestListCmd_Structure(t *testing.T) {
	assert.Equal(t, "list", listCmd.Use)
	assert.NotEmpty(t, listCmd.Short)
	assert.NotEmpty(t, listCmd.Long)

	// Check flags
	assert.NotNil(t, listCmd.Flags().Lookup("profile"))
	assert.NotNil(t, listCmd.Flags().Lookup("profiles"))
}

func TestStatusCmd_Structure(t *testing.T) {
	assert.Equal(t, "status", statusCmd.Use)
	assert.NotEmpty(t, statusCmd.Short)
}
