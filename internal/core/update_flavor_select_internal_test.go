package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// TestSelectUpdateDeployFiles_FileIDReplacementPicksTheAdvertisedFlavor pins
// the contract the CurseForge update check (#504) relies on: when the source
// names the new file in FileIDReplacements, ApplyUpdate installs exactly that
// file even though another flavor's file carries the same version label and
// sorts ahead of it in the listing.
func TestSelectUpdateDeployFiles_FileIDReplacementPicksTheAdvertisedFlavor(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "6200031", Version: "1.12.27", IsPrimary: true, Category: "Release"}, // Classic build, same label, listed first
		{ID: "6200020", Version: "1.12.27", Category: "Release"},                  // the Retail file the check advertised
		{ID: "6100010", Version: "1.12.26", Category: "Release"},                  // the installed file, still listed
	}
	replacements := map[string]bool{"6200020": true} // effective IDs after FileIDReplacements {6100010 -> 6200020}

	selected, _, err := selectUpdateDeployFiles(files, "1.12.27", "1.12.26",
		[]string{"6100010"}, []string{"6200020"}, replacements)
	require.NoError(t, err)

	require.Len(t, selected, 1)
	assert.Equal(t, "6200020", selected[0].ID)
}
