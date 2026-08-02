package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestListCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(listCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(listCmd); rootCmd.AddCommand(listCmd) })

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

// TestListCmd_DocMentionsLoadOrder guards #201: the help text must describe
// the mod ordering it actually shows (the profile's load order, which
// decides merge precedence) rather than staying silent about it or, worse,
// claiming the old install order.
func TestListCmd_DocMentionsLoadOrder(t *testing.T) {
	assert.Contains(t, listCmd.Long, "load order")
	assert.NotContains(t, strings.ToLower(listCmd.Long), "install order",
		"list must not claim install order - it shows profile load order")
}

func TestStatusCmd_Structure(t *testing.T) {
	assert.Equal(t, "status", statusCmd.Use)
	assert.NotEmpty(t, statusCmd.Short)
}
