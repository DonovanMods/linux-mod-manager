package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
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
	// Reset flags. configDir must point at an empty tempdir so requireGame
	// does not pick up a default-game from the user's real ~/.config/lmm.
	gameID = ""
	configDir = t.TempDir()

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

func TestSearchCmdStructure(t *testing.T) {
	assert.Equal(t, "search <query>", searchCmd.Use)
	flag := searchCmd.Flags().Lookup("source")
	if assert.NotNil(t, flag) {
		assert.Contains(t, flag.Usage, "all configured sources",
			"help text must reflect the new aggregate default")
	}
}

func TestCapabilityGapNotice(t *testing.T) {
	err := fmt.Errorf("source %q: searching: %w", "id-only", source.ErrNotSupported)
	notice, ok := capabilityGapNotice("id-only", err)
	assert.True(t, ok)
	assert.Contains(t, notice, "does not support searching")
	assert.Contains(t, notice, "lmm install --source id-only")
	assert.NotContains(t, notice, "operation not supported by this source",
		"the raw wrapped error must not leak into the notice")

	_, ok = capabilityGapNotice("x", errors.New("network down"))
	assert.False(t, ok)
}

// TestNoSourcesConfiguredErr tests the no-sources-configured guard
func TestNoSourcesConfiguredErr(t *testing.T) {
	tests := []struct {
		name    string
		game    *domain.Game
		wantErr bool
		wantMsg string
	}{
		{
			name: "empty sources returns error",
			game: &domain.Game{
				ID:        "test-game",
				Name:      "Test Game",
				SourceIDs: map[string]string{},
			},
			wantErr: true,
			wantMsg: "no mod sources configured",
		},
		{
			name: "non-empty sources returns nil",
			game: &domain.Game{
				ID:   "test-game",
				Name: "Test Game",
				SourceIDs: map[string]string{
					"nexusmods": "skyrimspecialedition",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := noSourcesConfiguredErr(tt.game)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantMsg)
				assert.Contains(t, err.Error(), "add sources with 'lmm game add' or edit games.yaml")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
