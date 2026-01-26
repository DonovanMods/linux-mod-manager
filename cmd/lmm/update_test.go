package main

import (
	"bytes"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

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
	assert.NotNil(t, updateCmd.Flags().Lookup("dry-run"))
}

func TestUpdateRollbackCmd_Structure(t *testing.T) {
	assert.Equal(t, "rollback <mod-id>", updateRollbackCmd.Use)
	assert.NotEmpty(t, updateRollbackCmd.Short)
	assert.NotEmpty(t, updateRollbackCmd.Long)

	// Check flags exist
	assert.NotNil(t, updateRollbackCmd.Flags().Lookup("source"))
	assert.NotNil(t, updateRollbackCmd.Flags().Lookup("profile"))
}

func TestUpdateRollbackCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	updateCmdCopy := &cobra.Command{Use: "update"}
	updateCmdCopy.AddCommand(updateRollbackCmd)
	cmd.AddCommand(updateCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"update", "rollback", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestPolicyToString(t *testing.T) {
	tests := []struct {
		policy   int
		expected string
	}{
		{0, "notify"}, // domain.UpdateNotify
		{1, "auto"},   // domain.UpdateAuto
		{2, "pinned"}, // domain.UpdatePinned
	}

	for _, tt := range tests {
		// Use internal knowledge that UpdatePolicy is an int
		result := policyToString(domain.UpdatePolicy(tt.policy))
		assert.Equal(t, tt.expected, result)
	}
}
