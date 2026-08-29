package core

// Unit tests for selection.go's file-selection policy: FilterAndSortFiles,
// primaryFile, and selectFilesForVersion. Some cases (pickVersionMatch's
// tie-break tail) are pure functions of their inputs that the end-to-end
// ApplyUpdate harness in flows_update_test.go cannot isolate without
// dragging in install/deploy fixtures that add nothing here.

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFilterAndSortFiles tests file filtering and sorting. Moved from
// cmd/lmm/install_test.go's identical test by the same name when #287
// deleted the cmd/lmm twin (filterAndSortFiles).
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
	filtered := FilterAndSortFiles(files, false)
	assert.Len(t, filtered, 4) // excludes ARCHIVED and OLD_VERSION

	// Check order: MAIN, MAIN, OPTIONAL, UPDATE
	assert.Equal(t, "MAIN", filtered[0].Category)
	assert.Equal(t, "MAIN", filtered[1].Category)
	assert.Equal(t, "OPTIONAL", filtered[2].Category)
	assert.Equal(t, "UPDATE", filtered[3].Category)

	// With archived
	withArchived := FilterAndSortFiles(files, true)
	assert.Len(t, withArchived, 6) // includes all

	// ARCHIVED should be at the end
	assert.Equal(t, "ARCHIVED", withArchived[4].Category)
	assert.Equal(t, "OLD_VERSION", withArchived[5].Category)
}

// TestInstallFileCategoryPriority tests category priority ordering. Moved
// from cmd/lmm/install_test.go's TestFileCategoryPriority when #287 deleted
// the cmd/lmm twin (fileCategoryPriority).
func TestInstallFileCategoryPriority(t *testing.T) {
	assert.Less(t, installFileCategoryPriority("MAIN"), installFileCategoryPriority("OPTIONAL"))
	assert.Less(t, installFileCategoryPriority("OPTIONAL"), installFileCategoryPriority("UPDATE"))
	assert.Less(t, installFileCategoryPriority("UPDATE"), installFileCategoryPriority("MISCELLANEOUS"))
	assert.Less(t, installFileCategoryPriority("MISCELLANEOUS"), installFileCategoryPriority("ARCHIVED"))

	// Case insensitive
	assert.Equal(t, installFileCategoryPriority("main"), installFileCategoryPriority("MAIN"))
	assert.Equal(t, installFileCategoryPriority("Main"), installFileCategoryPriority("MAIN"))
}

// TestPrimaryFile and TestPrimaryFile_EmptySlice moved from
// cmd/lmm/profile_test.go's TestSelectPrimaryFile[_EmptySlice] when #287
// deleted the cmd/lmm twin (selectPrimaryFile).
func TestPrimaryFile(t *testing.T) {
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
			result := primaryFile(tt.files)
			assert.NotNil(t, result)
			assert.Equal(t, tt.expected, result.ID)
		})
	}
}

func TestPrimaryFile_EmptySlice(t *testing.T) {
	var files []domain.DownloadableFile
	result := primaryFile(files)
	assert.Nil(t, result)
}

// TestSelectFilesForVersion_VersionAuthoritative is the #96 direct unit test
// for selectFilesForVersion's version parameter: drift heals to the
// recorded version, gone IDs heal to the recorded version, an unresolvable
// target hard-fails naming the version, and an empty version preserves the
// exact pre-#96 behavior. Moved from cmd/lmm/profile_test.go's
// TestSelectFilesToDownload_VersionAuthoritative when #287 unified the
// cmd/lmm and internal/core twins into this one function.
func TestSelectFilesForVersion_VersionAuthoritative(t *testing.T) {
	// file11 (another 1.0 file) and file12 (a non-primary file at an
	// unrelated version, 2.0) extend the base fixture to cover the fast
	// path and the stored-intersect-matches precedence (rule 3) without
	// disturbing any of the original four assertions below - each is
	// re-verified against the larger fixture inline.
	files := []domain.DownloadableFile{
		{ID: "10", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Version: "1.0", Category: "ARCHIVED"},
		{ID: "11", Version: "1.0", Category: "ARCHIVED"},
		{ID: "12", Version: "2.0", Category: "OPTIONAL"},
	}

	// Drift: stored ID exists upstream but is the wrong version - version wins.
	got, err := selectFilesForVersion(files, []string{"10"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "9", got[0].ID)

	// Gone IDs heal to the recorded version.
	got, err = selectFilesForVersion(files, []string{"999"}, "1.0")
	require.NoError(t, err)
	assert.Equal(t, "9", got[0].ID)

	// Unresolvable, no stored ID present upstream at all: extended #95
	// wording (rule 4a).
	_, err = selectFilesForVersion(files, []string{"999"}, "0.5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "0.5" not available`)

	// Legacy: empty version behaves exactly as before.
	got, err = selectFilesForVersion(files, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "10", got[0].ID)

	// Rule 2 (fast path): both stored IDs are returned as-is, without
	// re-resolution, once their combined effective version agrees with the
	// record - file9 (1.0) is the representative (neither stored file is
	// primary, so the first one carrying a version wins), and file12 (2.0,
	// an unrelated companion file's own version) rides along BOTH. This is
	// what distinguishes the fast path from rule 3's stored-intersect-
	// matches, which would keep only file9 (file12 isn't a 1.0 match).
	got, err = selectFilesForVersion(files, []string{"9", "12"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 2, "fast path must return the whole stored set, not just the version-matching subset")
	gotIDs := []string{got[0].ID, got[1].ID}
	assert.ElementsMatch(t, []string{"9", "12"}, gotIDs)

	// Rule 3 (stored ∩ matches): stored IDs 10 and 9 disagree in effective
	// version with the record (file10, the primary, is 1.5) so the fast
	// path does not fire; among the files that DO match "1.0" (9 and 11),
	// only the one that was ALSO a stored ID (9) is kept - file11 is
	// excluded even though it's a 1.0 match, because it was never a stored
	// ID for this mod.
	got, err = selectFilesForVersion(files, []string{"10", "9"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "9", got[0].ID)

	// Rule 5 (ErrVersionNotFound wrap): no stored IDs at all, and the
	// requested version matches nothing upstream.
	_, err = selectFilesForVersion(files, nil, "3.0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrVersionNotFound))
	assert.Contains(t, err.Error(), "edit the profile's version or reinstall")

	// Rule 4b (new split, #96 fix round 1): a stored ID (12) IS still
	// present upstream, but the recorded version (3.0) matches nothing -
	// this is a wrong version record on a file that's still there, not a
	// gone file, so it must NOT get the #95 "no longer available upstream"
	// wording; it gets a distinct ErrVersionNotFound wrap pointing at
	// verify/update instead of reinstall.
	_, err = selectFilesForVersion(files, []string{"12"}, "3.0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrVersionNotFound))
	assert.NotContains(t, err.Error(), "no longer available upstream", "a present-upstream stored ID must not get the gone-file wording")
	assert.Contains(t, err.Error(), `installed file(s) (ID(s): 12) do not match recorded version "3.0"`)
	assert.Contains(t, err.Error(), "run 'lmm verify --fix' to correct the version record, or 'lmm update' to adopt the current version")
}

// TestSelectFilesForVersion_CategoryPriorityTieBreak covers #144 item 5:
// when the target version offers no primary file and no stored ID narrows the
// choice, pickVersionMatch used to fall to matches[0] - upstream listing
// order - ignoring installFileCategoryPriority entirely. The category
// priority (MAIN > OPTIONAL > UPDATE > MISCELLANEOUS > unknown > archived)
// now breaks that tie, stable: first-listed wins among equal priorities, so
// category-less listings (custom sources) behave exactly as before.
//
// This fixture and case table used to be hand-duplicated in
// cmd/lmm/profile_test.go as TestSelectFilesToDownload_CategoryPriorityTieBreak
// (a drift guard for what was then a hand-mirrored twin of pickVersionMatch's
// tail); #287 unified the twins into selectFilesForVersion, so that copy is
// gone and this is the only version of the test.
func TestSelectFilesForVersion_CategoryPriorityTieBreak(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "misc20", Version: "2.0", Category: "MISCELLANEOUS"},
		{ID: "upd20", Version: "2.0", Category: "UPDATE"},
		{ID: "opt20", Version: "2.0", Category: "OPTIONAL"},
		{ID: "optA30", Version: "3.0", Category: "OPTIONAL"},
		{ID: "optB30", Version: "3.0", Category: "OPTIONAL"},
		{ID: "plain40a", Version: "4.0"},
		{ID: "plain40b", Version: "4.0"},
		{ID: "beta50", Version: "5.0", Category: "beta"},
		{ID: "upd50", Version: "5.0", Category: "update"},
		{ID: "opt60", Version: "6.0", Category: "OPTIONAL"},
		{ID: "miscPrim60", Version: "6.0", Category: "MISCELLANEOUS", IsPrimary: true},
	}

	cases := []struct {
		name    string
		version string
		wantID  string
	}{
		{"category priority beats listing order", "2.0", "opt20"},
		{"equal categories keep first-listed", "3.0", "optA30"},
		{"category-less files keep first-listed", "4.0", "plain40a"},
		{"known category beats unknown, case-insensitively", "5.0", "upd50"},
		{"a primary file still outranks category priority", "6.0", "miscPrim60"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectFilesForVersion(files, nil, tc.version)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantID, got[0].ID)
		})
	}
}

// TestGuardNoOpUpdateSelection_UninstalledMatchDowngradesLabellingClaim pins
// the #144 item-2 wording to a LOCALLY verifiable condition (PR #148 Copilot
// round): the "every file the source offers under the target version is
// already installed" claim must only be made when the guard can see that
// itself - every target-version match present in currentFileIDs. The shape
// below (an uninstalled target-version file B alongside an installed one
// that the repair re-picks) is unreachable through resolveUpdateSelection
// today precisely because of the invariant the guard exists to backstop;
// the guard must not parrot a claim whose truth rests on the very code it
// guards against.
func TestGuardNoOpUpdateSelection_UninstalledMatchDowngradesLabellingClaim(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "m", Version: "1.0", IsPrimary: true},
		{ID: "o", Version: "2.0"},
		{ID: "b", Version: "2.0"},
	}
	selected := []*domain.DownloadableFile{&files[0], &files[1]}

	_, err := guardNoOpUpdateSelection(files, "2.0", "1.0", []string{"m", "o"}, nil, selected)
	require.Error(t, err, "the no-op guard must still fail loudly")
	assert.NotContains(t, err.Error(), "every file the source offers",
		"with target-version file b uninstalled, the every-file-installed claim is false and must not be made")
	assert.NotContains(t, err.Error(), "labelling",
		"the labelling-quirk diagnosis belongs only to the locally-proven all-installed shape")
	assert.Contains(t, err.Error(), "use --file to pick one explicitly",
		"the generic remedy applies when another target-version file exists to pick")
}

func TestSelectFilesForVersion_EmptyFileList(t *testing.T) {
	_, err := selectFilesForVersion(nil, nil, "")
	require.ErrorIs(t, err, ErrNoDownloadableFiles)
}
