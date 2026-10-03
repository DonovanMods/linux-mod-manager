package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

// TestMatchArchiveName pins #530's name rules: exact first, then
// case-insensitive, then without the suffix a browser adds to a repeated
// download - and never a match that normalisation moved from one listed file
// to another.
func TestMatchArchiveName(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "1", FileName: "Auctionator-339-1-g23f0261.zip"},
		{ID: "2", FileName: "Mod-1.2.zip"},
		{ID: "3", FileName: "Mod-1.2-1.zip"},
		{ID: "4", FileName: "Other-2.0.zip"},
		{ID: "5", FileName: "pack-1.0.tar.gz"},
		{ID: "6", FileName: "Dupe.zip"},
		{ID: "7", FileName: "dupe.zip"},
	}
	tests := []struct {
		name       string
		archive    string
		wantID     string
		normalized bool
	}{
		{"exact", "Mod-1.2.zip", "2", false},
		{"case-insensitive", "mod-1.2.ZIP", "2", false},
		{"browser (1) on a git-describe name", "Auctionator-339-1-g23f0261 (1).zip", "1", true},
		{"browser (2) with a space", "Mod-1.2 (2).zip", "2", true},
		{"browser (3) without a space", "Other-2.0(3).zip", "4", true},
		{"a real -1 name that is listed is not stripped", "Mod-1.2-1.zip", "3", false},
		{"-1 stripped when the raw name is not listed", "Other-2.0-1.zip", "4", true},
		{"_1 likewise", "Other-2.0_1.zip", "4", true},
		{"a suffix before a double extension", "pack-1.0 (1).tar.gz", "5", true},
		{"a stripped name that is not listed matches nothing", "Unknown (1).zip", "", false},
		{"several files of one name match nothing", "DUPE.zip", "", false},
		{"the suffix alone is never a name", "(1).zip", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, normalized := matchArchiveName(files, tt.archive)
			if tt.wantID == "" {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.Equal(t, tt.wantID, got.ID)
			}
			assert.Equal(t, tt.normalized, normalized)
		})
	}
}
