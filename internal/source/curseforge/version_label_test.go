package curseforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractVersion_BareLabels: a file whose version is not dotted (a single
// build number, a git-describe label) still shows it (#510), while dotted
// names keep winning and prose names are never taken as versions.
func TestExtractVersion_BareLabels(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		fileName    string
		want        string
	}{
		// Auctionator (CurseForge 6124), measured against the live API.
		{"dotted display name", "8.3.1.0", "Auctionator-8.3.1.zip", "8.3.1.0"},
		{"git describe label", "296-3-g85cf972", "Auctionator-296-3-g85cf972.zip", "296-3-g85cf972"},
		{"bare build number", "339", "Auctionator-339.zip", "339"},
		{"build number with commits", "339-1-g23f0261", "Auctionator-339-1-g23f0261.zip", "339-1-g23f0261"},
		{"v-prefixed bare label drops the v", "v12", "Thing-v12.zip", "12"},

		// Dotted extraction still comes first.
		{"minecraft filename", "", "jei-1.20.1-15.3.0.4.jar", "15.3.0.4"},
		{"minecraft display name", "jei-1.20.1-15.3.0.4", "jei-1.20.1-15.3.0.4.jar", "15.3.0.4"},
		{"dotted filename beats a bare display name", "339", "Auctionator-8.3.1.zip", "8.3.1"},

		// The file name is the fallback when the display name is not a label.
		{"prose display name, label in the file name", "Auctionator for Classic", "Auctionator-339-1-g23f0261.zip", "339-1-g23f0261"},
		{"index-only entry has only a file name", "", "Auctionator-339-1-g23f0261.zip", "339-1-g23f0261"},
		{"underscore prefix", "", "Some_Mod_42.zip", "42"},
		{"any extension", "", "Thing-7.7z", "7"},

		// Prose and unversioned names are not versions.
		{"prose display name only", "Auctionator for Classic", "Auctionator.zip", ""},
		{"display name with a digit inside prose", "Release 5 for WotLK", "Auctionator.zip", ""},
		{"bare file name", "", "thing.jar", ""},
		{"digit glued to the name", "", "Mod2-Pro.zip", ""},
		{"empty", "", "", ""},
		{"absurdly long label", "1234567890123456789012345678901234567890123456789", "x.zip", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractVersion(tc.displayName, tc.fileName))
		})
	}
}

// TestCheckUpdates_NonDottedLabelIsOffered is the Auctionator case: the
// installed "339" (file 8939586) is superseded by "339-1-g23f0261", whose
// label has no dotted version. The update check used to skip any candidate it
// could not extract a version from, so it was never offered (#510).
func TestCheckUpdates_NonDottedLabelIsOffered(t *testing.T) {
	old := wowFile(2956486, "8.3.1.0", ReleaseTypeRelease, "2020-01-01T00:00:00Z", typeRetail)
	v339 := wowFile(8939586, "339", ReleaseTypeRelease, "2026-09-21T00:00:00Z", typeRetail)
	v339g := wowFile(8960001, "339-1-g23f0261", ReleaseTypeRelease, "2026-09-26T00:00:00Z", typeRetail)
	mods := []Mod{{ID: 6124, Name: "Auctionator", LatestFiles: []File{old, v339g, v339}}}

	var batch, single atomic.Int32
	srv := flavorServer(t, mods, nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(6124, "Auctionator", "339", 8939586)})
	require.NoError(t, err)

	require.Len(t, updates, 1, "got %+v", updates)
	assert.Equal(t, "339-1-g23f0261", updates[0].NewVersion)
	assert.Equal(t, map[string]string{"8939586": "8960001"}, updates[0].FileIDReplacements)
}

// TestCheckUpdates_IndexOnlyNonDottedLabelIsOffered: the same for a file only
// latestFilesIndexes names, whose label has to come from its file name.
func TestCheckUpdates_IndexOnlyNonDottedLabelIsOffered(t *testing.T) {
	cur := wowFile(8939586, "339", ReleaseTypeRelease, "2026-09-21T00:00:00Z", typeRetail)
	mods := []Mod{{
		ID: 6124, Name: "Auctionator", LatestFiles: []File{cur},
		LatestFilesIndexes: []FileIndex{
			indexFor(cur, typeRetail),
			{FileID: 8960001, Filename: "Auctionator-339-1-g23f0261.zip", ReleaseType: ReleaseTypeRelease, GameVersionTypeID: typeRetail},
		},
	}}

	var batch, single atomic.Int32
	srv := flavorServer(t, mods, nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(6124, "Auctionator", "339", 8939586)})
	require.NoError(t, err)

	require.Len(t, updates, 1, "got %+v", updates)
	assert.Equal(t, "339-1-g23f0261", updates[0].NewVersion)
}

// TestModToDomain_NonDottedVersionIsShown: search and `mod show` read the
// newest file's version; Auctionator's is "339-1-g23f0261", not blank (#510).
func TestModToDomain_NonDottedVersionIsShown(t *testing.T) {
	old := wowFile(2956486, "8.3.1.0", ReleaseTypeRelease, "2020-01-01T00:00:00Z", typeRetail)
	v339g := wowFile(8960001, "339-1-g23f0261", ReleaseTypeRelease, "2026-09-26T00:00:00Z", typeRetail)

	got := modToDomain(Mod{ID: 6124, Name: "Auctionator", LatestFiles: []File{old, v339g}}, "1")
	assert.Equal(t, "339-1-g23f0261", got.Version)
}

// TestCurseForge_GetModFiles_NonDottedVersion: the version an install records
// comes from GetModFiles, so it follows the same rule (#510).
func TestCurseForge_GetModFiles_NonDottedVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": 8960001, "displayName": "339-1-g23f0261", "fileName": "Auctionator-339-1-g23f0261.zip", "releaseType": 1},
				{"id": 8939586, "displayName": "339", "fileName": "Auctionator-339.zip", "releaseType": 1},
				{"id": 2956486, "displayName": "8.3.1.0", "fileName": "Auctionator-8.3.1.zip", "releaseType": 1}
			],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 3, "totalCount": 3}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	files, err := cf.GetModFiles(context.Background(), &domain.Mod{ID: "6124", GameID: "1"})
	require.NoError(t, err)
	require.Len(t, files, 3)
	assert.Equal(t, "339-1-g23f0261", files[0].Version)
	assert.Equal(t, "339", files[1].Version)
	assert.Equal(t, "8.3.1.0", files[2].Version)
}
