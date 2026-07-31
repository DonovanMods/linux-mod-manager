package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMatchImportedFile pins #139 item 2's matching rules: an imported
// archive resolves to a source file by exact FileName match first; a
// version-match fallback applies only when explicitly allowed (the --id
// archive path, where the user asserted the mod identity); any ambiguity -
// several candidates - resolves to nil rather than a guess, because wrong
// provenance is worse than none.
func TestMatchImportedFile(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "10", FileName: "mod-1.0.zip", Version: "1.0"},
		{ID: "20", FileName: "mod-2.0.zip", Version: "2.0"},
		{ID: "21", FileName: "mod-2.0-optional.zip", Version: "2.0"},
	}
	dupName := []domain.DownloadableFile{
		{ID: "1", FileName: "mod.zip", Version: "1.0"},
		{ID: "2", FileName: "mod.zip", Version: "2.0"},
	}

	tests := []struct {
		name                 string
		files                []domain.DownloadableFile
		archive              string
		version              string
		allowVersionFallback bool
		wantID               string
	}{
		{"exact filename match wins over version", files, "mod-1.0.zip", "2.0", true, "10"},
		{"exact filename match ambiguous resolves nothing", dupName, "mod.zip", "1.0", true, ""},
		{"version fallback resolves the sole version file", files, "renamed.zip", "1.0", true, "10"},
		{"version fallback ambiguous resolves nothing", files, "renamed.zip", "2.0", true, ""},
		{"version fallback disabled resolves nothing", files, "renamed.zip", "1.0", false, ""},
		{"unknown version never falls back", files, "renamed.zip", "unknown", true, ""},
		{"empty version never falls back", files, "renamed.zip", "", true, ""},
		{"no files resolves nothing", nil, "mod.zip", "1.0", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchImportedFile(tt.files, tt.archive, tt.version, tt.allowVersionFallback)
			if tt.wantID == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}
}
