package core

// #662: stored file ids whose main files span more than one version are
// healed for a user's reference, and installed as they are for a
// collection's. selectStoredFiles is the one place both rules live.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healListing is a mod's listing: two main versions (2.0's current, 1.0's
// archived), a 1.0 optional patch and an update of its own version.
func healListing() []domain.DownloadableFile {
	return []domain.DownloadableFile{
		{ID: "m2", FileName: "main-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true},
		{ID: "m1", FileName: "main-1.0.zip", Version: "1.0", Category: "OLD_VERSION"},
		{ID: "p1", FileName: "patch-1.0.zip", Version: "1.0", Category: "OPTIONAL"},
		{ID: "u", FileName: "hotfix-0.3.zip", Version: "0.3", Category: "UPDATE"},
	}
}

func TestSelectStoredFiles_HealsToTheRecordedVersion(t *testing.T) {
	sel, err := selectStoredFiles(healListing(), []string{"m1", "m2", "p1", "u"}, "2.0", false, "2.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"m2", "p1", "u"}, selectedIDs(sel.files), "the 2.0 main file and every secondary file stay")
	assert.Equal(t, "2.0", sel.version)
	require.NotNil(t, sel.heal)
	assert.Equal(t, StoredFilesHeal{Version: "2.0", Dropped: []MixedVersion{
		{Version: "1.0", Files: []MixedVersionFile{{ID: "m1", FileName: "main-1.0.zip"}}},
	}}, *sel.heal)
	assert.Nil(t, sel.mixed)
}

func TestSelectStoredFiles_HealsToTheOlderRecordedVersion(t *testing.T) {
	sel, err := selectStoredFiles(healListing(), []string{"m2", "m1", "p1"}, "1.0", false, "2.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"m1", "p1"}, selectedIDs(sel.files))
	assert.Equal(t, "1.0", sel.version)
	require.NotNil(t, sel.heal)
	assert.Equal(t, "1.0", sel.heal.Version)
	assert.Empty(t, sel.heal.RecordedVersion)
	assert.Equal(t, []MixedVersion{{Version: "2.0", Files: []MixedVersionFile{{ID: "m2", FileName: "main-2.0.zip"}}}}, sel.heal.Dropped)
}

func TestSelectStoredFiles_NoMainAtTheRecordedVersionKeepsTheNewest(t *testing.T) {
	files := append(healListing(), domain.DownloadableFile{ID: "m3", FileName: "main-3.0.zip", Version: "3.0", Category: "MAIN"})
	sel, err := selectStoredFiles(files, []string{"m1", "m2", "p1"}, "0.3", false, "3.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"m2", "p1"}, selectedIDs(sel.files), "2.0 is the newest of the STORED main files")
	assert.Equal(t, "2.0", sel.version)
	require.NotNil(t, sel.heal)
	assert.Equal(t, "2.0", sel.heal.Version)
	assert.Equal(t, "0.3", sel.heal.RecordedVersion)
	assert.Contains(t, sel.heal.Sentence(), "keeping the newest, 2.0, since none is at the recorded version 0.3")
}

func TestSelectStoredFiles_OneMainVersionIsNotAHeal(t *testing.T) {
	sel, err := selectStoredFiles(healListing(), []string{"p1", "m2", "u"}, "2.0", false, "2.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"m2", "p1", "u"}, selectedIDs(sel.files), "secondary files of other versions go with any main version")
	assert.Equal(t, "2.0", sel.version)
	assert.Nil(t, sel.heal)
}

func TestSelectStoredFiles_UncategorisedFilesKeepTheOldRule(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "a", FileName: "mod-2.0.zip", Version: "2.0", IsPrimary: true},
		{ID: "b", FileName: "mod-1.0.zip", Version: "1.0"},
	}
	sel, err := selectStoredFiles(files, []string{"a", "b"}, "2.0", false, "2.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, selectedIDs(sel.files), "a file with no category is a main file")
	require.NotNil(t, sel.heal)
	assert.Equal(t, []MixedVersion{{Version: "1.0", Files: []MixedVersionFile{{ID: "b", FileName: "mod-1.0.zip"}}}}, sel.heal.Dropped)
}

func TestSelectStoredFiles_CollectionRefInstallsTheCuratorsFiles(t *testing.T) {
	sel, err := selectStoredFiles(healListing(), []string{"m1", "m2", "p1"}, "1.0", true, "2.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"m2", "m1", "p1"}, selectedIDs(sel.files), "exactly the curator's files, none dropped")
	assert.Equal(t, "1.0", sel.version, "the reference's own version, so the next converge sees no drift")
	assert.Nil(t, sel.heal)
	assert.Equal(t, []MixedVersion{
		{Version: "2.0", Files: []MixedVersionFile{{ID: "m2", FileName: "main-2.0.zip"}}},
		{Version: "1.0", Files: []MixedVersionFile{{ID: "m1", FileName: "main-1.0.zip"}}},
	}, sel.mixed)
}

func TestSelectStoredFiles_CollectionRefOptionalFirstKeepsBothFiles(t *testing.T) {
	// The curator's patch carries its own version and the reference was
	// stamped with it: both files still install.
	sel, err := selectStoredFiles(healListing(), []string{"p1", "m2"}, "1.0", true, "2.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"m2", "p1"}, selectedIDs(sel.files))
	assert.Equal(t, "1.0", sel.version)
	assert.Nil(t, sel.mixed)
}

func TestSelectStoredFiles_CollectionRefWithAGoneFileResolvesAsBefore(t *testing.T) {
	_, err := selectStoredFiles(healListing(), []string{"gone"}, "9.9", true, "2.0")
	require.ErrorIs(t, err, ErrStoredFilesUnavailable)
}

func TestStoredFilesHeal_Sentence(t *testing.T) {
	h := StoredFilesHeal{SourceID: "nexusmods", ModID: "42", Name: "Some Mod", Version: "2.0", Dropped: []MixedVersion{
		{Version: "1.0", Files: []MixedVersionFile{{ID: "m1", FileName: "main-1.0.zip"}, {ID: "m0"}}},
	}}
	assert.Equal(t, "Some Mod: its stored main files are of more than one version; keeping version 2.0 and dropping main-1.0.zip (1.0), m0 (1.0) (reinstall the mod at a dropped file's version to use it instead)", h.Sentence())
	h.Kept = []MixedVersion{{Version: "0.9", Files: []MixedVersionFile{{ID: "t", FileName: "textures-0.9.zip"}}}}
	assert.Equal(t, "Some Mod: its stored main files are of more than one version; keeping version 2.0 and dropping main-1.0.zip (1.0), m0 (1.0) (reinstall the mod at a dropped file's version to use it instead); keeping textures-0.9.zip (0.9), a separate component", h.Sentence())
	h.Dropped = nil
	assert.Equal(t, "Some Mod: its stored main files are of more than one version; keeping textures-0.9.zip (0.9), a separate component", h.Sentence())
	c := CuratorMixedFiles{SourceID: "nexusmods", ModID: "42", Versions: []MixedVersion{
		{Version: "1.0", Files: []MixedVersionFile{{ID: "m1", FileName: "a.zip"}}},
		{Version: "2.0", Files: []MixedVersionFile{{ID: "m2", FileName: "b.zip"}}},
	}}
	assert.Equal(t, "nexusmods:42: the collection's files mix main files of versions 1.0 (a.zip) and 2.0 (b.zip); installed as the curator chose them", c.Sentence())
}

func TestSelectStoredFiles_SeparateComponentsAreKept(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "c12", Name: "Core", FileName: "Core-1.2.zip", Version: "1.2", Category: "MAIN", IsPrimary: true},
		{ID: "c11", Name: "Core", FileName: "Core-1.1.zip", Version: "1.1", Category: "MAIN"},
		{ID: "t10", Name: "Textures", FileName: "Textures-1.0.zip", Version: "1.0", Category: "MAIN"},
		{ID: "t09", Name: "Textures", FileName: "Textures-0.9.zip", Version: "0.9", Category: "MAIN"},
	}
	// Core's copies keep the recorded 1.2; Textures has no 1.2, so its
	// newest copy stays - a separate component, kept and said so.
	sel, err := selectStoredFiles(files, []string{"c11", "c12", "t09", "t10"}, "1.2", false, "1.2")
	require.NoError(t, err)
	assert.Equal(t, []string{"c12", "t10"}, selectedIDs(sel.files))
	assert.Equal(t, "1.2", sel.version)
	require.NotNil(t, sel.heal)
	assert.Equal(t, []MixedVersion{
		{Version: "1.1", Files: []MixedVersionFile{{ID: "c11", FileName: "Core-1.1.zip"}}},
		{Version: "0.9", Files: []MixedVersionFile{{ID: "t09", FileName: "Textures-0.9.zip"}}},
	}, sel.heal.Dropped)
	assert.Equal(t, []MixedVersion{{Version: "1.0", Files: []MixedVersionFile{{ID: "t10", FileName: "Textures-1.0.zip"}}}}, sel.heal.Kept)

	// Nothing to drop is no heal: the set installs as it is, unnoted.
	sel, err = selectStoredFiles(files, []string{"t10", "c12"}, "1.2", false, "1.2")
	require.NoError(t, err)
	assert.Equal(t, []string{"c12", "t10"}, selectedIDs(sel.files))
	assert.Nil(t, sel.heal)
	assert.Equal(t, "1.2", sel.version)
}

// TestSelectStoredFiles_ComponentsTellingApartByDigits (#662 re-review):
// "Textures 2K" and "Textures 4K" are separate components, though their
// names differ only in digits; neither is dropped.
func TestSelectStoredFiles_ComponentsTellingApartByDigits(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "t2", Name: "Textures 2K", Version: "1.0", Category: "MAIN"},
		{ID: "t4", Name: "Textures 4K", Version: "1.1", Category: "MAIN", IsPrimary: true},
	}
	sel, err := selectStoredFiles(files, []string{"t2", "t4"}, "1.1", false, "1.1")
	require.NoError(t, err)
	assert.Equal(t, []string{"t2", "t4"}, selectedIDs(sel.files))
	assert.Nil(t, sel.heal)
	assert.Equal(t, "1.1", sel.version)
}

// TestSelectStoredFiles_KeepsAnOlderRulesRecordedVersion (#662 review): a
// record stamped by the pre-#662 rule (the first versioned file, here a
// patch) keeps its version and its files.
func TestSelectStoredFiles_KeepsAnOlderRulesRecordedVersion(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "p", FileName: "patch-0.5.zip", Version: "0.5", Category: "OPTIONAL"},
		{ID: "m", FileName: "mod-1.0.zip", Version: "1.0", Category: "MAIN"},
	}
	sel, err := selectStoredFiles(files, []string{"p", "m"}, "0.5", false, "1.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"p", "m"}, selectedIDs(sel.files))
	assert.Equal(t, "0.5", sel.version)
	assert.Nil(t, sel.heal)
}

func TestFileCopyKeys(t *testing.T) {
	assert.Nil(t, fileCopyKeys(healListing()), "every main file a copy of one: no keys needed")
	keys := fileCopyKeys([]domain.DownloadableFile{
		{ID: "c", FileName: "Core-1.2.zip", Version: "1.2", Category: "MAIN"},
		{ID: "t", FileName: "Textures-1.0.zip", Version: "1.0", Category: "MAIN"},
		{ID: "c11", FileName: "Core-1.1.zip", Version: "1.1", Category: "OLD_VERSION"},
		{ID: "p", FileName: "patch.zip", Category: "OPTIONAL"},
	})
	assert.Equal(t, map[string]string{"c": "c", "t": "t", "c11": "c", "p": "p"}, keys, "a group is named by its first file")
}
