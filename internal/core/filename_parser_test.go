package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNexusModsFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     *core.ParsedFilename
	}{
		{
			name:     "standard nexusmods pattern",
			filename: "SkyUI-12604-5-2SE.zip",
			want:     &core.ParsedFilename{ModID: "12604", Version: "5.2SE", BaseName: "SkyUI"},
		},
		{
			name:     "pattern with underscores in name",
			filename: "SkyUI_5_2_SE-12604-5-2SE.zip",
			want:     &core.ParsedFilename{ModID: "12604", Version: "5.2SE", BaseName: "SkyUI_5_2_SE"},
		},
		{
			name:     "pattern with timestamp suffix",
			filename: "SKSE64-30379-2-2-6-1703618069.7z",
			want:     &core.ParsedFilename{ModID: "30379", Version: "2.2.6", BaseName: "SKSE64"},
		},
		{
			name:     "pattern with spaces replaced by dashes",
			filename: "Unofficial-Skyrim-Patch-266-4-3-0a.zip",
			want:     &core.ParsedFilename{ModID: "266", Version: "4.3.0a", BaseName: "Unofficial-Skyrim-Patch"},
		},
		{
			name:     "no pattern - simple name",
			filename: "my-cool-mod.zip",
			want:     nil,
		},
		{
			name:     "no pattern - no version after id",
			filename: "ModName-12345.zip",
			want:     nil,
		},
		{
			name:     "7z extension",
			filename: "TestMod-99999-1-0.7z",
			want:     &core.ParsedFilename{ModID: "99999", Version: "1.0", BaseName: "TestMod"},
		},
		{
			name:     "rar extension",
			filename: "TestMod-88888-2-1.rar",
			want:     &core.ParsedFilename{ModID: "88888", Version: "2.1", BaseName: "TestMod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := core.ParseNexusModsFilename(tt.filename)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectModName(t *testing.T) {
	t.Run("empty path returns archive basename", func(t *testing.T) {
		got := core.DetectModName("", "MyMod-123-1-0.zip")
		assert.Equal(t, "MyMod-123-1-0", got)
	})

	t.Run("single top-level directory uses directory name", func(t *testing.T) {
		dir := t.TempDir()
		modDir := filepath.Join(dir, "MyAwesomeMod")
		err := os.MkdirAll(modDir, 0755)
		require.NoError(t, err)
		// Add a file inside the directory
		err = os.WriteFile(filepath.Join(modDir, "readme.txt"), []byte("test"), 0644)
		require.NoError(t, err)

		got := core.DetectModName(dir, "archive-12345-1-0.zip")
		assert.Equal(t, "MyAwesomeMod", got)
	})

	t.Run("multiple entries falls back to archive basename", func(t *testing.T) {
		dir := t.TempDir()
		// Create multiple items at root level
		err := os.MkdirAll(filepath.Join(dir, "Data"), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("test"), 0644)
		require.NoError(t, err)

		got := core.DetectModName(dir, "CoolMod-99999-2-0.zip")
		assert.Equal(t, "CoolMod-99999-2-0", got)
	})

	t.Run("single file falls back to archive basename", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "plugin.dll"), []byte("binary"), 0644)
		require.NoError(t, err)

		got := core.DetectModName(dir, "SingleFile-11111-1-0.zip")
		assert.Equal(t, "SingleFile-11111-1-0", got)
	})

	t.Run("non-existent path falls back to archive basename", func(t *testing.T) {
		got := core.DetectModName("/nonexistent/path", "SomeMod-22222-1-0.zip")
		assert.Equal(t, "SomeMod-22222-1-0", got)
	})
}
