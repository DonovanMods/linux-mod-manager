package main

// TestFileSelectionTwins_* are Task 4 (#287) characterization tests, run
// against the pre-lift code: they prove cmd/lmm's file-selection policy
// (filterAndSortFiles/selectPrimaryFile/selectFilesToDownload) and
// internal/core's independently-duplicated twin
// (filterAndSortInstallFiles/its inline primary-file pick/
// selectVersionedDeployFiles) produce identical results AND identical error
// strings for the same inputs, via internal/core's TEMPORARY
// TwinTest*-prefixed shims (selection_twin_shim.go). Once that's proven,
// Task 4 collapses the twins into one exported implementation in
// internal/core/selection.go and this file - along with the shim it
// exercises - is deleted; it is not part of the lasting test suite.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileSelectionTwins_FilterAndSort(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "a", Category: "OPTIONAL"},
		{ID: "b", Category: "MAIN"},
		{ID: "c", Category: "ARCHIVED"},
		{ID: "d", Category: "OLD_VERSION"},
		{ID: "e", Category: "DELETED"},
		{ID: "f", Category: "UPDATE"},
		{ID: "g", Category: "MISCELLANEOUS"},
		{ID: "h", Category: "custom"},
	}

	for _, showArchived := range []bool{false, true} {
		cmdGot := filterAndSortFiles(files, showArchived)
		coreGot := core.TwinTestFilterAndSortFiles(files, showArchived)
		assert.Equal(t, cmdGot, coreGot, "showArchived=%v", showArchived)
	}
}

func TestFileSelectionTwins_PrimaryFile(t *testing.T) {
	cases := [][]domain.DownloadableFile{
		nil,
		{},
		{{ID: "only"}},
		{{ID: "first"}, {ID: "second", IsPrimary: true}, {ID: "third"}},
		{{ID: "first"}, {ID: "second"}},
	}

	for i, files := range cases {
		cmdGot := selectPrimaryFile(files)
		coreGot := core.TwinTestSelectPrimaryFile(files)
		assert.Equal(t, cmdGot, coreGot, "case %d", i)
	}
}

func TestFileSelectionTwins_SelectFilesForVersion(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "10", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Version: "1.0", Category: "ARCHIVED"},
		{ID: "11", Version: "1.0", Category: "ARCHIVED"},
		{ID: "12", Version: "2.0", Category: "OPTIONAL"},
	}

	cases := []struct {
		name          string
		storedFileIDs []string
		version       string
	}{
		{"stored IDs present at version (fast path)", []string{"9", "12"}, "1.0"},
		{"stored IDs gone, heals to recorded version", []string{"999"}, "1.0"},
		{"no file at version, with stored IDs", []string{"999"}, "0.5"},
		{"no file at version, without stored IDs", nil, "3.0"},
		{"stored present upstream, version gone (distinct wording)", []string{"12"}, "3.0"},
		{"legacy: empty version", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmdGot, cmdErr := selectFilesToDownload(files, tc.storedFileIDs, tc.version)
			coreGot, coreErr := core.TwinTestSelectFilesForVersion(files, tc.storedFileIDs, tc.version)

			if cmdErr != nil || coreErr != nil {
				require.Error(t, cmdErr)
				require.Error(t, coreErr)
				assert.Equal(t, cmdErr.Error(), coreErr.Error(), "error strings must be byte-identical")
				return
			}
			assert.Equal(t, cmdGot, coreGot)
		})
	}
}

// TestFileSelectionTwins_CategoryPriorityTieBreak mirrors the fixture in
// cmd/lmm/profile_test.go's TestSelectFilesToDownload_CategoryPriorityTieBreak
// and internal/core/flows_selection_test.go's
// TestSelectVersionedDeployFiles_CategoryPriorityTieBreak - proving those two
// existing, independently-maintained tests exercise the same behavior before
// their subjects are merged.
func TestFileSelectionTwins_CategoryPriorityTieBreak(t *testing.T) {
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

	for _, version := range []string{"2.0", "3.0", "4.0", "5.0", "6.0"} {
		cmdGot, cmdErr := selectFilesToDownload(files, nil, version)
		coreGot, coreErr := core.TwinTestSelectFilesForVersion(files, nil, version)
		require.NoError(t, cmdErr)
		require.NoError(t, coreErr)
		assert.Equal(t, cmdGot, coreGot, "version=%s", version)
	}
}
