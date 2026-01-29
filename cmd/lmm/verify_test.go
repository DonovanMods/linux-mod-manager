package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyCommand_Exists(t *testing.T) {
	// Verify the command is registered
	cmd := rootCmd
	verifyCmd, _, err := cmd.Find([]string{"verify"})
	assert.NoError(t, err)
	assert.Equal(t, "verify", verifyCmd.Name())
}

func TestVerifyCommand_HasFixFlag(t *testing.T) {
	cmd := rootCmd
	verifyCmd, _, err := cmd.Find([]string{"verify"})
	assert.NoError(t, err)

	flag := verifyCmd.Flags().Lookup("fix")
	assert.NotNil(t, flag)
}

func TestVerifyCommand_AcceptsOptionalModID(t *testing.T) {
	cmd := rootCmd
	verifyCmd, _, err := cmd.Find([]string{"verify"})
	assert.NoError(t, err)

	// Command should accept 0 or 1 arguments
	assert.Contains(t, verifyCmd.Use, "[mod-id]")
}
