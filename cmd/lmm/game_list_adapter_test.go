package main

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// columnStarts returns the byte offset each column begins at, read off the
// table's dashed separator line. tabwriter aligns every line of a table to
// the same offsets, and the separator is the one line whose cells hold no
// interior space, so it is the ruler: slicing a row at those offsets
// recovers its cells exactly - including an EMPTY cell, which a split on
// runs of whitespace cannot see.
func columnStarts(separator string) []int {
	var starts []int
	inCell := false
	for i, r := range separator {
		switch {
		case r != ' ' && !inCell:
			starts = append(starts, i)
			inCell = true
		case r == ' ':
			inCell = false
		}
	}
	return starts
}

// cellsAt slices one rendered line into cells at the given column offsets.
func cellsAt(line string, starts []int) []string {
	cells := make([]string, 0, len(starts))
	for i, start := range starts {
		if start >= len(line) {
			cells = append(cells, "")
			continue
		}
		end := len(line)
		if i+1 < len(starts) && starts[i+1] < end {
			end = starts[i+1]
		}
		cells = append(cells, strings.TrimSpace(line[start:end]))
	}
	return cells
}

func gameListCells(t *testing.T, out string) (header []string, rows [][]string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "want a header, a separator and at least one row")
	starts := columnStarts(lines[1])
	header = cellsAt(lines[0], starts)
	for _, line := range lines[2:] {
		rows = append(rows, cellsAt(line, starts))
	}
	return header, rows
}

// TestDoGameList_RowArityMatchesHeader is C1's regression test (#411): the
// ADAPTER column was added to the header and the separator but never to the
// row Fprintf, so every cell after MOD PATH shifted one column left and
// SOURCES rendered blank. It pins the arity of the table - a column added to
// the header without a matching cell fails here - and the ADAPTER cell's
// position and value.
func TestDoGameList_RowArityMatchesHeader(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "alpha",
		Name:        "Alpha",
		InstallPath: "/games/alpha",
		ModPath:     "/games/alpha/mods",
		Adapter:     "generic-files",
		DeployMode:  domain.DeployExtract,
		SourceIDs:   map[string]string{"nexusmods": "alpha"},
	}))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "beta",
		Name:        "Beta",
		InstallPath: "/games/beta",
		ModPath:     "/games/beta/mods",
		DeployMode:  domain.DeployCompile,
		ConvertPaks: true,
		SourceIDs:   map[string]string{"icarus": "icarus"},
	}))

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	header, rows := gameListCells(t, out)
	assert.Equal(t, []string{"ID", "NAME", "INSTALL PATH", "MOD PATH", "ADAPTER", "DEPLOY MODE", "CONVERT PAKS", "SOURCES"}, header)
	require.Len(t, rows, 2)
	for i, row := range rows {
		require.Len(t, row, len(header), "row %d has a different cell count than the header: %q", i, row)
	}

	// "beta" fills every one of the eight cells, so a plain split on runs of
	// whitespace is unambiguous there: it counts the cells the row Fprintf
	// actually emitted, independent of the ruler above.
	betaLine := strings.Split(strings.TrimRight(out, "\n"), "\n")[3]
	assert.Len(t, regexp.MustCompile(` {2,}`).Split(strings.TrimSpace(betaLine), -1), len(header),
		"the row Fprintf emits a different number of cells than the header: %q", betaLine)

	adapterCol := 4
	assert.Equal(t, "generic-files", rows[0][adapterCol], "explicit adapter belongs under ADAPTER")
	assert.Equal(t, "extract", rows[0][adapterCol+1], "DEPLOY MODE follows ADAPTER")
	assert.Equal(t, "nexusmods:alpha", rows[0][len(header)-1], "SOURCES is the last cell")

	// A game with no `adapter:` key IS the identity, so the column names it.
	assert.Equal(t, "generic-files", rows[1][adapterCol])
	assert.Equal(t, "compile", rows[1][adapterCol+1])
	assert.Equal(t, "on", rows[1][adapterCol+2])
	assert.Equal(t, "icarus:icarus", rows[1][len(header)-1])
}
