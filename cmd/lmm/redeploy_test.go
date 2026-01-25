package main

import (
	"bytes"
	"testing"

	"lmm/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestRedeployCmd_Structure(t *testing.T) {
	assert.Equal(t, "redeploy [mod-id]", redeployCmd.Use)
	assert.NotEmpty(t, redeployCmd.Short)
	assert.NotEmpty(t, redeployCmd.Long)

	// Check flags exist
	assert.NotNil(t, redeployCmd.Flags().Lookup("source"))
	assert.NotNil(t, redeployCmd.Flags().Lookup("profile"))
	assert.NotNil(t, redeployCmd.Flags().Lookup("method"))
}

func TestRedeployCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(redeployCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"redeploy"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestLinkMethodName(t *testing.T) {
	tests := []struct {
		method   domain.LinkMethod
		expected string
	}{
		{domain.LinkSymlink, "symlink"},
		{domain.LinkHardlink, "hardlink"},
		{domain.LinkCopy, "copy"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := linkMethodName(tt.method)
			assert.Equal(t, tt.expected, result)
		})
	}
}
