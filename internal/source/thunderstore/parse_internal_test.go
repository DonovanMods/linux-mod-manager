package thunderstore

// T1 review #4: how many times one search parses index.json.
//
// Package-INTERNAL, because the claim is about which function reads the
// file and the only honest way to prove a read did not happen is to take
// the file away between two calls that used to both need it. 8.22 MB and
// 97 ms of that parse, on the largest community, used to be spent
// producing an answer the next call immediately recomputed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const parseTestCommunity = "lethal-company"

// TestTheRowsVerifyParsedAreTheRowsTheResidentCopyIsBuiltFrom is the fix
// stated where it can be seen: usable() has just read and parsed
// index.json to check the generation, so residentFor() must build the
// search copy out of THOSE rows rather than reading the same file again.
//
// Proved by deleting index.json in between. Before the fix residentFor
// re-read it and failed; the rows it needs were on the stack one frame up
// the whole time.
func TestTheRowsVerifyParsedAreTheRowsTheResidentCopyIsBuiltFrom(t *testing.T) {
	dir := t.TempDir()
	st := newStore(dir)
	writeParseTestIndex(t, st)

	src := New(Options{CacheDir: dir, BaseURL: "http://127.0.0.1:1"})
	wm, rows, ok := src.usable(parseTestCommunity)
	require.True(t, ok, "the fixture must read as a usable index")
	require.NotNil(t, rows, "usable parsed index.json to check it - those rows are the answer")

	require.NoError(t, os.Remove(filepath.Join(st.dir(parseTestCommunity), indexFileName)))

	idx, err := src.residentFor(parseTestCommunity, wm, rows)
	require.NoError(t, err, "the resident copy must be built from the rows already parsed")
	assert.Len(t, idx.rows, 2)
}

// TestUsableReportsNoRowsOnceAResidentCopyExists is the other half of the
// same signature: once the loaded copy matches the watermark there is
// nothing to re-parse, so usable reads no file at all and hands back no
// rows - 808555f6's fast path, kept.
func TestUsableReportsNoRowsOnceAResidentCopyExists(t *testing.T) {
	dir := t.TempDir()
	st := newStore(dir)
	writeParseTestIndex(t, st)

	src := New(Options{CacheDir: dir, BaseURL: "http://127.0.0.1:1"})
	wm, rows, ok := src.usable(parseTestCommunity)
	require.True(t, ok)
	_, err := src.residentFor(parseTestCommunity, wm, rows)
	require.NoError(t, err)

	// index.json is now unparseable. state() still sees a non-empty file
	// beside a watermark, and the resident copy - loaded out of the bytes
	// that WERE there - means nothing needs re-reading, so the answer is
	// still yes and nothing was parsed to reach it.
	require.NoError(t, os.WriteFile(
		filepath.Join(st.dir(parseTestCommunity), indexFileName), []byte("not json at all"), 0o644))
	again, rowsAgain, ok := src.usable(parseTestCommunity)
	require.True(t, ok)
	assert.Equal(t, wm.Generation, again.Generation)
	assert.Nil(t, rowsAgain, "nothing was parsed, so there are no rows to hand on")
}

// writeParseTestIndex builds a two-package index through the builder, so
// the fixture is exactly what a real refresh writes - generation stamps
// and all.
func writeParseTestIndex(t *testing.T, st *store) {
	t.Helper()
	b, err := newBuilder(st.dir(parseTestCommunity))
	require.NoError(t, err)
	for _, name := range []string{"Owner-First", "Owner-Second"} {
		require.NoError(t, b.add(
			packageRecord{FullName: name, Versions: []versionRow{}},
			indexRow{FullName: name, Categories: []string{"Mods"}, LatestVersion: "1.0.0"},
		))
	}
	_, err = b.commit(watermark{LastModified: "Wed, 10 Sep 2026 12:00:00 GMT", FetchedAt: 1757505600})
	require.NoError(t, err)

	// Sanity: the fixture is a real index, not a directory of files.
	data, err := os.ReadFile(filepath.Join(st.dir(parseTestCommunity), indexFileName))
	require.NoError(t, err)
	var file indexFile
	require.NoError(t, json.Unmarshal(data, &file))
	require.Len(t, file.Rows, 2)
}
