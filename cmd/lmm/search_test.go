package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// TestTruncate tests the string truncation helper function
func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "no truncation needed",
			input:    "short",
			maxLen:   10,
			expected: "short",
		},
		{
			name:     "exact length",
			input:    "exactly10!",
			maxLen:   10,
			expected: "exactly10!",
		},
		{
			name:     "needs truncation",
			input:    "this is a long string that needs truncation",
			maxLen:   20,
			expected: "this is a long st...",
		},
		{
			name:     "very short maxLen",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "maxLen equals 3",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "maxLen of 4",
			input:    "hello world",
			maxLen:   4,
			expected: "h...",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSearchCmd_Structure tests the search command structure
func TestSearchCmd_Structure(t *testing.T) {
	assert.Equal(t, "search <query>", searchCmd.Use)
	assert.NotEmpty(t, searchCmd.Short)
	assert.NotEmpty(t, searchCmd.Long)

	// Check flags exist
	assert.NotNil(t, searchCmd.Flags().Lookup("source"))
	assert.NotNil(t, searchCmd.Flags().Lookup("limit"))
}

// TestSearchCmd_NoGame tests search without game flag
func TestSearchCmd_NoGame(t *testing.T) {
	// Reset flags
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(searchCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search", "test-query"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestSearchCmd_NoQuery tests search without query argument
func TestSearchCmd_NoQuery(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(searchCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

// TestSearchCmd_DefaultFlags tests that default flag values are set
func TestSearchCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := searchCmd.Flags().Lookup("source")
	assert.Equal(t, "", sourceFlag.DefValue)

	limitFlag := searchCmd.Flags().Lookup("limit")
	assert.Equal(t, "10", limitFlag.DefValue)
}
