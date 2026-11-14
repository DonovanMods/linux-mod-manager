package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// selectedIDs is the IDs of selected, in order.
func selectedIDs(selected []*domain.DownloadableFile) []string {
	ids := make([]string, len(selected))
	for i, f := range selected {
		ids[i] = f.ID
	}
	return ids
}

// TestSelectUpdateDeployFiles_SupersededFileIsNoReplacementCandidate pins
// #643: a file the replacement map supersedes (a key, fileA -> fileC) is
// never also offered as the stand-in for another stored file, whatever the
// listing order and however it is labelled. An unchanged optional keeps
// itself, and nothing is replaced twice.
func TestSelectUpdateDeployFiles_SupersededFileIsNoReplacementCandidate(t *testing.T) {
	a := domain.DownloadableFile{ID: "fileA", Version: "1.0", Category: "MAIN"}
	b := domain.DownloadableFile{ID: "fileB", Version: "1.0", Category: "OPTIONAL"}
	c := domain.DownloadableFile{ID: "fileC", Version: "1.0", Category: "MAIN", IsPrimary: true}
	aPrimary := a
	aPrimary.IsPrimary = true
	cPlain := c
	cPlain.IsPrimary = false

	for name, files := range map[string][]domain.DownloadableFile{
		"listed A B C":                {a, b, c},
		"listed C B A":                {c, b, a},
		"listed B A C":                {b, a, c},
		"old main still primary":      {aPrimary, b, cPlain},
		"no categories, no primary":   {{ID: "fileA", Version: "1.0"}, {ID: "fileB", Version: "1.0"}, {ID: "fileC", Version: "1.0"}},
		"old main archived (control)": {{ID: "fileA", Version: "1.0", Category: "ARCHIVED"}, b, c},
	} {
		t.Run(name, func(t *testing.T) {
			selected, warnings, err := selectUpdateDeployFiles(files, "1.0", "1.0",
				[]string{"fileA", "fileB"}, []string{"fileC", "fileB"}, map[string]bool{"fileC": true})
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{"fileC", "fileB"}, selectedIDs(selected))
			assert.Empty(t, warnings, "an optional that keeps itself is not ambiguous")
		})
	}
}

// TestSelectUpdateDeployFiles_TwoUnchangedOptionalsBothKept is #643 with
// two optionals beside the superseded main: neither is swapped for the old
// main, and neither is dropped.
func TestSelectUpdateDeployFiles_TwoUnchangedOptionalsBothKept(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "fileA", Version: "1.0", Category: "MAIN"},
		{ID: "fileB", Version: "1.0", Category: "OPTIONAL"},
		{ID: "fileD", Version: "1.0", Category: "OPTIONAL"},
		{ID: "fileC", Version: "1.0", Category: "MAIN", IsPrimary: true},
	}
	selected, _, err := selectUpdateDeployFiles(files, "1.0", "1.0",
		[]string{"fileA", "fileB", "fileD"}, []string{"fileC", "fileB", "fileD"}, map[string]bool{"fileC": true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fileC", "fileB", "fileD"}, selectedIDs(selected))
}
