package thunderstore_test

// The PACKAGE reads (#409, design §3.2/§3.3): GetMod, GetModFiles and
// GetDownloadURL over the same local index Search uses, with no request of
// any kind.
//
// The claim these tests exist for is "a version IS a file". Thunderstore
// publishes no file list - a package version is one zip - so the thing that
// makes ResolveVersionFiles (#96), `lmm mod files`, the rollback flow and
// the SPA's versions table work here with ZERO source-specific handling is
// that GetModFiles returns one DownloadableFile per version.

import (
	"net/http"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetModMapsTheWholePackage is §3.2's field mapping, read off the
// detail store rather than off a search row - the two must agree, and the
// detail store is the one that carries every version.
func TestGetModMapsTheWholePackage(t *testing.T) {
	s := searchable(t)

	mod, err := s.src.GetMod(t.Context(), testCommunity, "RugbugRedfern-Skinwalkers")
	require.NoError(t, err)

	assert.Equal(t, "RugbugRedfern-Skinwalkers", mod.ID, "the id IS the full_name")
	assert.Equal(t, "thunderstore", mod.SourceID)
	assert.Equal(t, testCommunity, mod.GameID, "the community core already translated")
	assert.Equal(t, "Skinwalkers", mod.Name)
	assert.Equal(t, "RugbugRedfern", mod.Author)
	assert.Equal(t, "3.0.2", mod.Version, "versions[0] is the newest")
	assert.Equal(t, "Monsters mimic the voices of players they have heard.", mod.Description)
	assert.Equal(t, "https://thunderstore.io/c/"+testCommunity+"/p/RugbugRedfern/Skinwalkers/", mod.SourceURL)
	assert.Equal(t,
		"https://gcdn.thunderstore.io/live/repository/icons/RugbugRedfern-Skinwalkers-3.0.2.png",
		mod.PictureURL)
	assert.Equal(t, "Mods, BepInEx, Client-side", mod.Category)
	assert.Equal(t, time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC), mod.UpdatedAt)
}

// TestGetModUnderscoresBecomeSpacesAndDeprecationRidesInCategories pins the
// two mappings that are not identity: a package name is displayed with its
// underscores as spaces, and is_deprecated gets no new domain.Mod field -
// it rides in Category, which every existing renderer already prints.
func TestGetModUnderscoresBecomeSpacesAndDeprecationRidesInCategories(t *testing.T) {
	s := searchable(t)

	mod, err := s.src.GetMod(t.Context(), testCommunity, "Ghostbird-Skinwalker_Sounds")
	require.NoError(t, err)
	assert.Equal(t, "Skinwalker Sounds", mod.Name)
	assert.Equal(t, "Mods, Deprecated", mod.Category)
}

// TestGetModAgreesWithTheSearchRow is the consistency rule: a search result
// and a detail read describe the SAME mod, or `lmm install <what you just
// searched for>` addresses something else.
func TestGetModAgreesWithTheSearchRow(t *testing.T) {
	s := searchable(t)

	hits := s.search(source.SearchQuery{Query: "shiploot"})
	require.NotEmpty(t, hits.Mods)
	row := hits.Mods[0]

	mod, err := s.src.GetMod(t.Context(), testCommunity, row.ID)
	require.NoError(t, err)
	assert.Equal(t, row, *mod, "a row and a detail read are the same document")
}

// TestGetModForAPackageTheIndexDoesNotHold is the honest answer for a
// package that has left the community (or never was in it): not found, not
// a source failure - a caller distinguishes them.
func TestGetModForAPackageTheIndexDoesNotHold(t *testing.T) {
	s := searchable(t)

	_, err := s.src.GetMod(t.Context(), testCommunity, "Nobody-NoSuchPackage")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Contains(t, err.Error(), "Nobody-NoSuchPackage")
}

// TestGetModFilesIsOneFilePerVersionNewestFirst is §3.3, the headline.
func TestGetModFilesIsOneFilePerVersionNewestFirst(t *testing.T) {
	s := searchable(t)
	mod := s.mod(t, "RugbugRedfern-Skinwalkers")

	files, err := s.src.GetModFiles(t.Context(), mod)
	require.NoError(t, err)
	require.Len(t, files, 5, "every version is a file; the list is not capped")

	versions := make([]string, 0, len(files))
	for _, f := range files {
		versions = append(versions, f.Version)
	}
	assert.Equal(t, []string{"3.0.2", "3.0.1", "3.0.0", "2.1.0", "2.0.0"}, versions,
		"newest first, exactly as Thunderstore orders them")

	newest := files[0]
	assert.Equal(t, "3.0.2", newest.ID, "the file id IS the version")
	assert.Equal(t, "Skinwalkers 3.0.2", newest.Name)
	assert.Equal(t, "RugbugRedfern-Skinwalkers-3.0.2.zip", newest.FileName)
	assert.Equal(t, int64(240128), newest.Size)
	assert.True(t, newest.IsPrimary, "the newest version is the primary file")
	assert.Equal(t, "MAIN", newest.Category)
	assert.Empty(t, newest.SHA256, "Thunderstore publishes no checksum")

	for _, f := range files[1:] {
		assert.False(t, f.IsPrimary, "exactly one file is primary")
	}
}

// TestGetModFilesMakesNoRequest is what "the local index answers every id"
// means in practice: the detail read is a positioned read of a file on
// disk, so a version list costs nothing upstream.
func TestGetModFilesMakesNoRequest(t *testing.T) {
	s := searchable(t)
	before, _, _, _ := s.srv.counts()
	mod := s.mod(t, "notnotnotswipez-MoreCompany")

	files, err := s.src.GetModFiles(t.Context(), mod)
	require.NoError(t, err)
	require.Len(t, files, 2)

	after, _, _, _ := s.srv.counts()
	assert.Equal(t, before, after, "no request may be made to list a package's versions")
}

// TestExactFileSizesIsDeclared is the opt-in that lets core fail a short or
// truncated download loudly: file_size is exact, and Thunderstore publishes
// no checksum to check instead.
func TestExactFileSizesIsDeclared(t *testing.T) {
	s := searchable(t)
	sizer, ok := any(s.src).(source.ExactFileSizer)
	require.True(t, ok, "the source must declare its sizes exact")
	assert.True(t, sizer.ExactFileSizes())
}

// TestGetDownloadURLIsBuiltFromAVALIDATEDVersion is §3.3's second half. The
// URL is pure string construction - no token, no expiry, no round trip -
// which makes the validation the only thing standing between a caller and a
// URL for a version that does not exist.
func TestGetDownloadURLIsBuiltFromAVALIDATEDVersion(t *testing.T) {
	s := searchable(t)
	mod := s.mod(t, "RugbugRedfern-Skinwalkers")

	url, err := s.src.GetDownloadURL(t.Context(), mod, "3.0.1")
	require.NoError(t, err)
	assert.Equal(t, s.srv.URL+"/package/download/RugbugRedfern/Skinwalkers/3.0.1/", url,
		"the PATH is Thunderstore's; the host is whatever this source was pointed at")

	for _, fileID := range []string{"9.9.9", "", "../../etc", "3.0.1/../../.."} {
		t.Run("refused: "+fileID, func(t *testing.T) {
			_, err := s.src.GetDownloadURL(t.Context(), mod, fileID)
			require.Error(t, err, "a caller must not be able to synthesise a URL for a version that does not exist")
		})
	}
}

// TestGetDownloadURLTargetsTheServedHost keeps the base URL honest: every
// test in this package runs against an httptest server, so the download URL
// a test sees must come from the same place the index did, not from the
// production constant.
func TestGetDownloadURLTargetsTheServedHost(t *testing.T) {
	s := searchable(t)
	s.srv.publishArchive("tinyhoot", "ShipLoot", "1.1.0", []byte("PK\x05\x06"))
	mod := s.mod(t, "tinyhoot-ShipLoot")

	url, err := s.src.GetDownloadURL(t.Context(), mod, "1.1.0")
	require.NoError(t, err)
	assert.Equal(t, s.srv.URL+"/package/download/tinyhoot/ShipLoot/1.1.0/", url)

	// And it is fetchable: the fixture server answers the path, which is
	// what makes the end-to-end install test possible at all.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestPackageReadsRefuseABadCommunity keeps the path-safety gate in front
// of every new entry point, not just the ones T1 built.
func TestPackageReadsRefuseABadCommunity(t *testing.T) {
	s := searchable(t)

	for _, community := range []string{"", "../../etc", "Valheim", "a/b"} {
		t.Run("community "+community, func(t *testing.T) {
			_, err := s.src.GetMod(t.Context(), community, "tinyhoot-ShipLoot")
			assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)

			mod := &domain.Mod{ID: "tinyhoot-ShipLoot", GameID: community}
			_, err = s.src.GetModFiles(t.Context(), mod)
			assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)

			_, err = s.src.GetDownloadURL(t.Context(), mod, "1.1.0")
			assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
		})
	}
}

// TestVersionsCapabilityIsDeclared: a frontend branches on this before
// offering a version picker, so it has to become true in the commit that
// makes it true.
func TestVersionsCapabilityIsDeclared(t *testing.T) {
	s := searchable(t)
	assert.True(t, s.src.Capabilities().Versions)
}

// mod is the domain.Mod for a package id, for the tests whose subject is
// what comes AFTER a metadata read.
func (s *sourceUnderTest) mod(t *testing.T, id string) *domain.Mod {
	t.Helper()
	mod, err := s.src.GetMod(t.Context(), testCommunity, id)
	require.NoError(t, err)
	return mod
}
