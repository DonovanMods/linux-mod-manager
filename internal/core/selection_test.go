package core

// Unit tests for selection.go's file-selection policy: FilterAndSortFiles,
// PrimaryFile, and SelectFilesForVersion. Some cases (pickVersionMatch's
// tie-break tail) are pure functions of their inputs that the end-to-end
// ApplyUpdate harness in flows_update_test.go cannot isolate without
// dragging in install/deploy fixtures that add nothing here.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
// tail); #287 unified the twins into SelectFilesForVersion, so that copy is
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
			got, err := SelectFilesForVersion(files, nil, tc.version)
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
