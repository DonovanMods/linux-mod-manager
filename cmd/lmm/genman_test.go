package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// committedManDir is the repo-tracked man page directory, relative to this
// package's working directory when tests run (cmd/lmm).
const committedManDir = "../../docs/man/man1"

// TestGenManTree_ProducesPageForEveryVisibleCommand pins gen-man's basic
// contract: a page per visible command in the tree, including the five
// previously-undocumented families (#104), a nested subcommand, and no page
// for hidden commands (gen-man itself).
func TestGenManTree_ProducesPageForEveryVisibleCommand(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	wantPages := []string{
		"lmm.1",
		"lmm-tui.1",
		"lmm-import.1",
		"lmm-source.1",
		"lmm-auth.1",
		"lmm-uninstall.1",
		"lmm-game-add.1", // nested: game -> game add
	}
	for _, page := range wantPages {
		_, err := os.Stat(filepath.Join(dir, page))
		assert.NoError(t, err, "expected %s to be generated", page)
	}

	_, err := os.Stat(filepath.Join(dir, "lmm-gen-man.1"))
	assert.True(t, os.IsNotExist(err), "gen-man is Hidden and must not get its own page")
}

// TestGenManTree_MatchesCommittedPages is the drift guard: docs/man/man1 must
// always be byte-identical to what the current command tree generates. When
// help text changes without regenerating, this test fails and tells the
// developer to run `make man` (issue #104 - 19 releases of undetected drift
// with the old hand-written pages).
func TestGenManTree_MatchesCommittedPages(t *testing.T) {
	committed, err := os.ReadDir(committedManDir)
	require.NoError(t, err, "reading committed man page directory")

	dir := t.TempDir()
	require.NoError(t, genManTree(dir))

	generated, err := os.ReadDir(dir)
	require.NoError(t, err)

	committedNames := direntNames(committed)
	generatedNames := direntNames(generated)
	require.ElementsMatch(t, committedNames, generatedNames,
		"docs/man/man1 is out of sync with the generated command tree; run `make man` to regenerate")

	for _, name := range committedNames {
		wantBytes, err := os.ReadFile(filepath.Join(committedManDir, name))
		require.NoError(t, err)
		gotBytes, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, string(wantBytes), string(gotBytes),
			"docs/man/man1/%s is stale; run `make man` to regenerate", name)
	}
}

func direntNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}
