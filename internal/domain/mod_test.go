package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSourceLocal_IsExpectedValue(t *testing.T) {
	if SourceLocal != "local" {
		t.Errorf("SourceLocal = %q, want %q", SourceLocal, "local")
	}
}

func TestModKey(t *testing.T) {
	tests := []struct {
		sourceID string
		modID    string
		want     string
	}{
		{"nexusmods", "12345", "nexusmods:12345"},
		{"curseforge", "abc", "curseforge:abc"},
		{"local", "uuid-here", "local:uuid-here"},
		{"", "", ":"},
	}

	for _, tt := range tests {
		got := ModKey(tt.sourceID, tt.modID)
		if got != tt.want {
			t.Errorf("ModKey(%q, %q) = %q, want %q", tt.sourceID, tt.modID, got, tt.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0", "1.0.0", 0},
		{"2.0", "1.9.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"V1.2.3", "1.2.3", 0},
		{"1.0.0-beta", "1.0.0", 0},
		{"1.2", "1.10", -1},
		{"", "", 0},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		current string
		new     string
		want    bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.1", "1.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0", "2.0", true},
	}

	for _, tt := range tests {
		got := IsNewerVersion(tt.current, tt.new)
		if got != tt.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", tt.current, tt.new, got, tt.want)
		}
	}
}

func TestExtractVersionFromName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "BiggerBackpack-1.2.0", "1.2.0"},
		{"v prefix", "cool-mod-v2.1", "2.1"},
		{"last version wins", "jei-1.20.1-15.3.0", "15.3.0"},
		{"prerelease suffix", "mod-1.0.0-beta2", "1.0.0-beta2"},
		{"no version", "JustAName", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractVersionFromName(tt.in))
		})
	}
}

func TestEffectiveInstalledVersion(t *testing.T) {
	f := func(id, version string, primary bool) *DownloadableFile {
		return &DownloadableFile{ID: id, Version: version, IsPrimary: primary}
	}
	tests := []struct {
		name     string
		modVer   string
		selected []*DownloadableFile
		want     string
	}{
		{"no files falls back to mod version", "1.5", nil, "1.5"},
		{"single file with version wins", "1.5", []*DownloadableFile{f("10", "1.0", false)}, "1.0"},
		{"file without version falls back", "1.5", []*DownloadableFile{f("10", "", true)}, "1.5"},
		{"primary file version preferred over earlier non-primary", "1.5",
			[]*DownloadableFile{f("10", "0.9-patch", false), f("11", "1.0", true)}, "1.0"},
		{"first non-empty when no primary has a version", "1.5",
			[]*DownloadableFile{f("10", "", true), f("11", "1.0", false)}, "1.0"},
		{"nil entries skipped", "1.5", []*DownloadableFile{nil, f("10", "1.0", false)}, "1.0"},
		// #662: an optional, update or miscellaneous file may carry its own
		// version beside any main file, so the main file names the mod's
		// version wherever it is listed.
		{"a main file's version over an earlier optional's", "1.5",
			[]*DownloadableFile{cat(f("10", "0.5", false), "OPTIONAL"), cat(f("11", "1.0", false), "MAIN")}, "1.0"},
		{"a main file's version over a primary optional's", "1.5",
			[]*DownloadableFile{cat(f("10", "0.5", true), "UPDATE"), cat(f("11", "1.0", false), "")}, "1.0"},
		{"the primary main file's over another main's", "1.5",
			[]*DownloadableFile{cat(f("10", "0.9", false), "MAIN"), cat(f("11", "1.0", true), "MAIN")}, "1.0"},
		{"only non-main files: the first versioned one", "1.5",
			[]*DownloadableFile{cat(f("10", "0.5", false), "MISCELLANEOUS"), cat(f("11", "0.6", false), "OPTIONAL")}, "0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EffectiveInstalledVersion(tt.modVer, tt.selected))
		})
	}
}

// cat sets f's source category.
func cat(f *DownloadableFile, category string) *DownloadableFile {
	f.Category = category
	return f
}

// TestIsSecondaryFileCategory is #662's classification: only the
// categories that name a file beside a main one are secondary; an empty,
// unknown or archived category counts as main.
func TestIsSecondaryFileCategory(t *testing.T) {
	for _, c := range []string{"OPTIONAL", "UPDATE", "MISCELLANEOUS", "optional", "Update"} {
		assert.True(t, IsSecondaryFileCategory(c), c)
	}
	for _, c := range []string{"", "MAIN", "OLD_VERSION", "ARCHIVED", "DELETED", "Release", "Beta", "EXMODZ", "something"} {
		assert.False(t, IsSecondaryFileCategory(c), c)
	}
}
