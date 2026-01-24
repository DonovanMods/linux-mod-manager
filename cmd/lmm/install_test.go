package main

import (
	"bytes"
	"testing"

	"lmm/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// TestInstallCmd_Structure tests the install command structure
func TestInstallCmd_Structure(t *testing.T) {
	assert.Equal(t, "install <query>", installCmd.Use)
	assert.NotEmpty(t, installCmd.Short)
	assert.NotEmpty(t, installCmd.Long)

	// Check flags exist
	assert.NotNil(t, installCmd.Flags().Lookup("source"))
	assert.NotNil(t, installCmd.Flags().Lookup("profile"))
	assert.NotNil(t, installCmd.Flags().Lookup("version"))
	assert.NotNil(t, installCmd.Flags().Lookup("id"))
	assert.NotNil(t, installCmd.Flags().Lookup("file"))
	assert.NotNil(t, installCmd.Flags().Lookup("yes"))
}

// TestInstallCmd_NoGame tests install without game flag
func TestInstallCmd_NoGame(t *testing.T) {
	// Reset flags
	gameID = ""
	installModID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "test mod"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestInstallCmd_NoQueryOrID tests install without query or --id
func TestInstallCmd_NoQueryOrID(t *testing.T) {
	gameID = "test-game"
	installModID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "search query or --id is required")
}

// TestInstallCmd_DefaultFlags tests that default flag values are set
func TestInstallCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := installCmd.Flags().Lookup("source")
	assert.Equal(t, "nexusmods", sourceFlag.DefValue)

	profileFlag := installCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	versionFlag := installCmd.Flags().Lookup("version")
	assert.Equal(t, "", versionFlag.DefValue)

	idFlag := installCmd.Flags().Lookup("id")
	assert.Equal(t, "", idFlag.DefValue)

	fileFlag := installCmd.Flags().Lookup("file")
	assert.Equal(t, "", fileFlag.DefValue)

	yesFlag := installCmd.Flags().Lookup("yes")
	assert.Equal(t, "false", yesFlag.DefValue)
}

// TestInstallCmd_GameNotFound tests install with non-existent game
func TestInstallCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"
	installModID = "12345"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "--id", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")

	// Reset
	installModID = ""
}

// TestFormatSize tests the formatSize function
func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.00 MB"},
		{1572864, "1.50 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatSize(tt.bytes)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestProgressBar tests the progressBar function
func TestProgressBar(t *testing.T) {
	tests := []struct {
		percentage float64
		width      int
		expected   int // number of filled characters
	}{
		{0, 10, 0},
		{50, 10, 5},
		{100, 10, 10},
		{25, 20, 5},
		{110, 10, 10}, // capped at 100%
	}

	for _, tt := range tests {
		bar := progressBar(tt.percentage, tt.width)
		assert.Equal(t, tt.width, len([]rune(bar)))
	}
}

// TestFilterAndSortFiles tests file filtering and sorting
func TestFilterAndSortFiles(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "1", FileName: "optional.zip", Category: "OPTIONAL"},
		{ID: "2", FileName: "main.zip", Category: "MAIN"},
		{ID: "3", FileName: "archived.zip", Category: "ARCHIVED"},
		{ID: "4", FileName: "update.zip", Category: "UPDATE"},
		{ID: "5", FileName: "main2.zip", Category: "MAIN"},
		{ID: "6", FileName: "old.zip", Category: "OLD_VERSION"},
	}

	// Without archived
	filtered := filterAndSortFiles(files, false)
	assert.Len(t, filtered, 4) // excludes ARCHIVED and OLD_VERSION

	// Check order: MAIN, MAIN, OPTIONAL, UPDATE
	assert.Equal(t, "MAIN", filtered[0].Category)
	assert.Equal(t, "MAIN", filtered[1].Category)
	assert.Equal(t, "OPTIONAL", filtered[2].Category)
	assert.Equal(t, "UPDATE", filtered[3].Category)

	// With archived
	withArchived := filterAndSortFiles(files, true)
	assert.Len(t, withArchived, 6) // includes all

	// ARCHIVED should be at the end
	assert.Equal(t, "ARCHIVED", withArchived[4].Category)
	assert.Equal(t, "OLD_VERSION", withArchived[5].Category)
}

// TestFileCategoryPriority tests category priority ordering
func TestFileCategoryPriority(t *testing.T) {
	assert.Less(t, fileCategoryPriority("MAIN"), fileCategoryPriority("OPTIONAL"))
	assert.Less(t, fileCategoryPriority("OPTIONAL"), fileCategoryPriority("UPDATE"))
	assert.Less(t, fileCategoryPriority("UPDATE"), fileCategoryPriority("MISCELLANEOUS"))
	assert.Less(t, fileCategoryPriority("MISCELLANEOUS"), fileCategoryPriority("ARCHIVED"))

	// Case insensitive
	assert.Equal(t, fileCategoryPriority("main"), fileCategoryPriority("MAIN"))
	assert.Equal(t, fileCategoryPriority("Main"), fileCategoryPriority("MAIN"))
}

// TestInstallCmd_ShowArchivedFlag tests the show-archived flag exists
func TestInstallCmd_ShowArchivedFlag(t *testing.T) {
	flag := installCmd.Flags().Lookup("show-archived")
	assert.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}
