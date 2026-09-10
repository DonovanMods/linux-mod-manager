package thunderstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The index-row field positions, written down once. index.json's "rows"
// is an array of fixed-shape arrays; these are what the shape MEANS.
const (
	fieldFullName = iota
	fieldDescription
	fieldCategories
	fieldDateUpdated
	fieldLatestVersion
	fieldDeprecated
	fieldOffset
	fieldLength
)

// TestRefreshIndexBuildsTheSplitIndex is the unit's central claim: one
// request produces a searchable projection and a detail store, and the
// offsets in the first actually address the right lines of the second.
func TestRefreshIndexBuildsTheSplitIndex(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, _ := newSource(t, srv)

	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	assert.True(t, status.Present)
	assert.Equal(t, 12, status.Packages, "every package in the fixture is indexed")
	assert.Equal(t, testCommunity, status.GameID)
	assert.False(t, status.Stale)
	assert.Positive(t, status.Bytes)

	dir := indexDir(cacheDir, testCommunity)
	for _, name := range []string{"index.json", "packages.jsonl", "watermark.json"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err, "%s must exist", name)
	}

	wm := readWatermark(t, cacheDir, testCommunity)
	assert.Equal(t, "Wed, 10 Sep 2026 12:00:00 GMT", wm["last_modified"])
	assert.EqualValues(t, 12, wm["packages"])
	assert.EqualValues(t, 2, wm["schema"])
	assert.NotEmpty(t, wm["generation"], "the watermark names the build its data files carry")
	assert.NotZero(t, wm["fetched_at"])

	packages, err := os.ReadFile(filepath.Join(dir, "packages.jsonl"))
	require.NoError(t, err)

	rows := readIndexRows(t, cacheDir, testCommunity)
	require.Len(t, rows, 12)
	for _, row := range rows {
		require.Len(t, row, 8, "an index row is a fixed 8-element array")
		offset, length := rowInt(t, row, fieldOffset), rowInt(t, row, fieldLength)
		require.LessOrEqual(t, offset+length, int64(len(packages)))

		var record struct {
			FullName string `json:"full_name"`
			Versions []json.RawMessage
		}
		require.NoError(t, json.Unmarshal(packages[offset:offset+length], &record),
			"the byte range must be exactly one record")
		assert.Equal(t, rowString(t, row, fieldFullName), record.FullName,
			"the row must address ITS OWN record, not a neighbour's")
		assert.Equal(t, byte('\n'), packages[offset+length], "a record's range excludes its newline")
	}

	// The rows carry what a search needs and nothing more.
	first := rows[0]
	assert.Equal(t, "RugbugRedfern-Skinwalkers", rowString(t, first, fieldFullName))
	assert.Equal(t, "3.0.2", rowString(t, first, fieldLatestVersion), "Thunderstore orders versions newest-first")
	assert.Equal(t, "2026-09-08T10:00:00.000000Z", rowString(t, first, fieldDateUpdated))
	assert.Contains(t, rowString(t, first, fieldDescription), "mimic the voices")
}

// TestIndexRecordsEveryVersion pins the design's deliberate refusal to cap
// versions: a rollback to an old release is only possible if the old
// release is still in the index.
func TestIndexRecordsEveryVersion(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, _ := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)

	packages, err := os.ReadFile(filepath.Join(indexDir(cacheDir, testCommunity), "packages.jsonl"))
	require.NoError(t, err)
	rows := readIndexRows(t, cacheDir, testCommunity)

	var record struct {
		FullName string              `json:"full_name"`
		Versions [][]json.RawMessage `json:"versions"`
	}
	offset, length := rowInt(t, rows[0], fieldOffset), rowInt(t, rows[0], fieldLength)
	require.NoError(t, json.Unmarshal(packages[offset:offset+length], &record))
	require.Equal(t, "RugbugRedfern-Skinwalkers", record.FullName)
	require.Len(t, record.Versions, 5, "all five versions, not a capped window")

	// A version row is the fixed array [version, file_size, date, deps].
	require.Len(t, record.Versions[0], 4)
	var version string
	var size int64
	var deps []string
	require.NoError(t, json.Unmarshal(record.Versions[0][0], &version))
	require.NoError(t, json.Unmarshal(record.Versions[0][1], &size))
	require.NoError(t, json.Unmarshal(record.Versions[0][3], &deps))
	assert.Equal(t, "3.0.2", version)
	assert.EqualValues(t, 240128, size)
	assert.Equal(t, []string{"BepInEx-BepInExPack-5.4.2100"}, deps)
}

// TestRebuildIsByteForByteIdentical pins that a rebuild of an unchanged
// document produces the same two files - the property that makes a forced
// refresh safe to run at any time.
func TestRebuildIsByteForByteIdentical(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, _ := newSource(t, srv)

	_, err := src.RefreshIndex(t.Context(), testCommunity, true, nil)
	require.NoError(t, err)
	first := readFiles(t, indexDir(cacheDir, testCommunity))

	// A new Last-Modified so the conditional GET answers 200 and the whole
	// document is streamed again.
	srv.publish(fixtureDocument(t), "Thu, 11 Sep 2026 12:00:00 GMT")
	_, err = src.RefreshIndex(t.Context(), testCommunity, true, nil)
	require.NoError(t, err)
	second := readFiles(t, indexDir(cacheDir, testCommunity))

	assert.Equal(t, first["index.json"], second["index.json"])
	assert.Equal(t, first["packages.jsonl"], second["packages.jsonl"])
}

// TestGzippedIndexDecodesTransparently pins that the wire form the design
// is sized on - 34.6 MB gzipped for the largest community - is what the
// streaming decode actually reads through.
func TestGzippedIndexDecodesTransparently(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	srv.serveGzip()
	src, _, _ := newSource(t, srv)

	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	assert.Equal(t, 12, status.Packages)
}

// TestIndexStatusReportsAnAbsentIndex pins the read a frontend makes to
// decide whether to warn the user about a one-time build.
func TestIndexStatusReportsAnAbsentIndex(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present)
	assert.Zero(t, status.Packages)
	assert.Equal(t, testCommunity, status.GameID)

	requests, _, _, _ := srv.counts()
	assert.Zero(t, requests, "asking what is cached must never fetch anything")
}

// TestIndexStatusReportsStalenessWithoutFetching pins that a status read is
// a pure disk read: it says the copy is old, it does not go and fix it.
func TestIndexStatusReportsStalenessWithoutFetching(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, clock := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)

	before, _, _, _ := srv.counts()
	clock.advance(7 * time.Hour)

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.True(t, status.Present)
	assert.True(t, status.Stale, "past the six-hour TTL")

	after, _, _, _ := srv.counts()
	assert.Equal(t, before, after)
}

// TestRefreshProgressTicks pins the progress contract a frontend renders:
// a start, then a done naming what was indexed.
func TestRefreshProgressTicks(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	var phases []string
	var details []string
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, func(phase, detail string, bytes int64) {
		phases = append(phases, phase)
		details = append(details, detail)
	})
	require.NoError(t, err)

	require.NotEmpty(t, phases)
	assert.Equal(t, source.FetchPhaseStarted, phases[0])
	assert.Equal(t, source.FetchPhaseDone, phases[len(phases)-1])
	assert.Contains(t, details[len(details)-1], "12 packages")
}

// TestBuildIsCancellable pins that a closed tab or a Ctrl-C stops a build
// rather than riding it out, and leaves nothing half-written behind.
func TestBuildIsCancellable(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, _ := newSource(t, srv)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := src.RefreshIndex(ctx, testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present, "a cancelled build publishes nothing")
	assertNoStagingLeftovers(t, indexDir(cacheDir, testCommunity))
}

// TestFailedColdBuildIsUnavailable pins that a source with no index and no
// upstream is an ERROR, not an empty result: lmm could not ask.
func TestFailedColdBuildIsUnavailable(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)
	srv.Close()

	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
	assert.ErrorIs(t, err, thunderstore.ErrIndexUnavailable)
	assert.False(t, status.Present)
}

// TestMalformedDocumentIsUnavailable pins that a body that is not the
// expected array fails loudly rather than publishing an empty index.
func TestMalformedDocumentIsUnavailable(t *testing.T) {
	srv := newIndexServer(t, []byte(`{"detail":"not an array"}`))
	src, cacheDir, _ := newSource(t, srv)

	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present)
	assertNoStagingLeftovers(t, indexDir(cacheDir, testCommunity))
}

// TestCommunitySlugIsRefusedBeforeAnyPathIsJoined is the path-safety gate.
// The slug comes from games.yaml, which the user edits by hand, and it is
// joined onto both a filesystem path and a URL.
func TestCommunitySlugIsRefusedBeforeAnyPathIsJoined(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{"empty", ""},
		{"parent traversal", "../../etc"},
		{"a path separator", "a/b"},
		{"a backslash", `a\b`},
		{"absolute", "/etc/passwd"},
		{"a leading dot", ".hidden"},
		{"a leading hyphen", "-nope"},
		{"uppercase", "Lethal-Company"},
		{"a space", "lethal company"},
		{"a NUL", "lethal\x00company"},
		{"over 64 characters", string(make([]byte, 0, 300)) + repeat("a", 300)},
		{"an underscore", "lethal_company"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newIndexServer(t, fixtureDocument(t))
			src, cacheDir, _ := newSource(t, srv)

			_, err := src.IndexStatus(t.Context(), tt.slug)
			require.Error(t, err)
			assert.ErrorIs(t, err, thunderstore.ErrCommunityNotConfigured)

			_, err = src.RefreshIndex(t.Context(), tt.slug, false, nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)

			_, err = src.Search(t.Context(), source.SearchQuery{GameID: tt.slug, Query: "x"})
			require.Error(t, err)
			assert.ErrorIs(t, err, thunderstore.ErrCommunityNotConfigured)

			requests, _, _, _ := srv.counts()
			assert.Zero(t, requests, "a refused slug never reaches the network")
			entries, err := os.ReadDir(filepath.Join(cacheDir, "_thunderstore"))
			if err == nil {
				assert.Empty(t, entries, "a refused slug never creates a directory")
			}
		})
	}
}

// TestEmptyAndMalformedSlugsAreDistinguishable pins that the two failure
// wordings differ: one game has no Thunderstore mapping at all, the other
// has one the user got wrong.
func TestEmptyAndMalformedSlugsAreDistinguishable(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	_, empty := src.IndexStatus(t.Context(), "")
	_, malformed := src.IndexStatus(t.Context(), "Nope!")
	require.Error(t, empty)
	require.Error(t, malformed)
	assert.Contains(t, empty.Error(), "no Thunderstore community configured")
	assert.Contains(t, malformed.Error(), "not a valid Thunderstore community slug")
	assert.True(t, errors.Is(empty, thunderstore.ErrCommunityNotConfigured))
	assert.True(t, errors.Is(malformed, thunderstore.ErrCommunityNotConfigured))
}

// readFiles reads every regular file in dir, keyed by name.
func readFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := make(map[string][]byte, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		out[e.Name()] = data
	}
	return out
}

// assertNoStagingLeftovers pins that a failed or cancelled build removes
// its own staging files rather than filling the cache with them.
func assertNoStagingLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".packages-", "a staging file was left behind")
		assert.NotContains(t, e.Name(), ".index-", "a staging file was left behind")
		assert.NotContains(t, e.Name(), ".stage-", "a staging file was left behind")
	}
}

// repeat is strings.Repeat, spelled out so the table above reads as data.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

// TestACancelledBuildIsClassifiableAsACancellation is T1 review #5.
// indexUnavailable wrapped its cause with %v, so the cause was flattened
// into text and only the sentinel joined the chain. Cancellation lands
// inside dec.Decode as often as it lands on the per-package ctx.Err()
// check - Decode wins on any package larger than one read - and a caller
// that cannot tell a closed browser tab from a dead upstream logs a 502
// for a user who simply navigated away.
func TestACancelledBuildIsClassifiableAsACancellation(t *testing.T) {
	sandboxEnv(t)

	// The server stalls in the MIDDLE of a package, so the decoder is
	// blocked inside Decode - not sitting on the loop's own ctx.Err()
	// guard between two packages - when the cancellation arrives.
	stalled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Mod0","full_name":"Owner-Mod0","owner":"Owner",`))
		w.(http.Flusher).Flush()
		close(stalled)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	src := thunderstore.New(thunderstore.Options{CacheDir: t.TempDir(), BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-stalled
		cancel()
	}()

	_, err := src.RefreshIndex(ctx, testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled,
		"a cancelled build must be classifiable as a cancellation, not only as an unavailable index")
}

// TestAFailedBuildKeepsItsCauseInTheChain is the other half of the same
// wrap: a build that fails for a reason that is NOT a cancellation is
// still ErrIndexUnavailable, and its cause is still reachable with
// errors.Is rather than only readable in the sentence.
func TestAFailedBuildKeepsItsCauseInTheChain(t *testing.T) {
	sandboxEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Mod0","versions":"not an array"}]`))
	}))
	t.Cleanup(srv.Close)

	src := thunderstore.New(thunderstore.Options{CacheDir: t.TempDir(), BaseURL: srv.URL})
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, thunderstore.ErrIndexUnavailable)
	assert.NotErrorIs(t, err, context.Canceled)

	var typeErr *json.UnmarshalTypeError
	assert.ErrorAs(t, err, &typeErr, "the decode failure must stay in the chain, not be flattened to text")
}

// TestAnOversizedDocumentFailsRatherThanFillingTheDisk is T1 review #6 at
// this source's own surface: the index build is the one place in lmm that
// writes a response body straight to disk as it arrives, so the ceiling
// has to stop it, and the failure has to be the ordinary "no index" one -
// leaving nothing half-written behind.
func TestAnOversizedDocumentFailsRatherThanFillingTheDisk(t *testing.T) {
	sandboxEnv(t)
	cacheDir := t.TempDir()

	// A document that never ends. The cap is what stops it; nothing else
	// here would, short of the ten-minute timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("["))
		// The document never closes: one package list's worth of records,
		// bracket-stripped and comma-terminated, written over and over.
		doc := syntheticDocument(200, 8)
		filler := append(doc[1:len(doc)-1:len(doc)-1], ',')
		for r.Context().Err() == nil {
			if _, err := w.Write(filler); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	src := thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: srv.URL, MaxIndexBytes: 1 << 20})
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, thunderstore.ErrIndexUnavailable)

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present, "an over-long document leaves no index behind")

	entries, err := os.ReadDir(indexDir(cacheDir, testCommunity))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".packages-", "no staging file may be left behind")
		assert.NotContains(t, e.Name(), ".index-", "no staging file may be left behind")
	}
}
