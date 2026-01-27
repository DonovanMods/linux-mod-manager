package main

import (
	"bytes"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestProfileCmd_Structure(t *testing.T) {
	assert.Equal(t, "profile", profileCmd.Use)
	assert.NotEmpty(t, profileCmd.Short)

	// Check subcommands exist
	var subCmds []string
	for _, cmd := range profileCmd.Commands() {
		subCmds = append(subCmds, cmd.Name())
	}

	assert.Contains(t, subCmds, "list")
	assert.Contains(t, subCmds, "create")
	assert.Contains(t, subCmds, "delete")
	assert.Contains(t, subCmds, "switch")
	assert.Contains(t, subCmds, "export")
	assert.Contains(t, subCmds, "import")
}

func TestProfileListCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "list"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestProfileCreateCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "create", "myprofile"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestProfileCreateCmd_NoName(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "create"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestSelectPrimaryFile(t *testing.T) {
	tests := []struct {
		name     string
		files    []domain.DownloadableFile
		expected string
	}{
		{
			name: "returns primary file when available",
			files: []domain.DownloadableFile{
				{ID: "1", FileName: "optional.zip", IsPrimary: false},
				{ID: "2", FileName: "main.zip", IsPrimary: true},
				{ID: "3", FileName: "update.zip", IsPrimary: false},
			},
			expected: "2",
		},
		{
			name: "returns first file when no primary",
			files: []domain.DownloadableFile{
				{ID: "1", FileName: "first.zip", IsPrimary: false},
				{ID: "2", FileName: "second.zip", IsPrimary: false},
			},
			expected: "1",
		},
		{
			name: "returns first primary when multiple primaries",
			files: []domain.DownloadableFile{
				{ID: "1", FileName: "first.zip", IsPrimary: false},
				{ID: "2", FileName: "primary1.zip", IsPrimary: true},
				{ID: "3", FileName: "primary2.zip", IsPrimary: true},
			},
			expected: "2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := selectPrimaryFile(tt.files)
			assert.NotNil(t, result)
			assert.Equal(t, tt.expected, result.ID)
		})
	}
}

func TestSelectPrimaryFile_EmptySlice(t *testing.T) {
	var files []domain.DownloadableFile
	result := selectPrimaryFile(files)
	assert.Nil(t, result)
}
