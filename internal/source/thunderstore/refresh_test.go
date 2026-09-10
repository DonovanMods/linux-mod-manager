package thunderstore_test

import (
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

// TestWarmIndexWithinTheTTLMakesNoRequest pins the cheapest case: an index
// fetched an hour ago answers a search with no network at all.
func TestWarmIndexWithinTheTTLMakesNoRequest(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, clock := newSource(t, srv)

	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	clock.advance(time.Hour)

	_, err = src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "skinwalkers"})
	require.NoError(t, err)
	_, err = src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)

	requests, _, _, _ := srv.counts()
	assert.Equal(t, 1, requests, "only the cold build should have asked upstream")
}

// TestPastTheTTLA304CostsNothing pins the conditional refresh: past the
// TTL, lmm asks - and an unchanged document answers in zero bytes, leaving
// both data files untouched while the clock restarts.
func TestPastTheTTLA304CostsNothing(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, clock := newSource(t, srv)

	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	dir := indexDir(cacheDir, testCommunity)
	before := readFiles(t, dir)
	beforeStamps := modTimes(t, dir)
	firstFetchedAt := readWatermark(t, cacheDir, testCommunity)["fetched_at"]

	clock.advance(7 * time.Hour)
	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	assert.True(t, status.Present)
	assert.False(t, status.Stale, "the copy on disk was just confirmed current")
	assert.Equal(t, 12, status.Packages, "a 304 keeps the package count it already had")

	_, conditionals, served200, served304 := srv.counts()
	assert.Equal(t, 1, conditionals, "the refresh must carry If-Modified-Since")
	assert.Equal(t, 1, served200, "only the cold build transferred a document")
	assert.Equal(t, 1, served304)

	after := readFiles(t, dir)
	afterStamps := modTimes(t, dir)
	assert.Equal(t, before["index.json"], after["index.json"])
	assert.Equal(t, before["packages.jsonl"], after["packages.jsonl"])
	assert.Equal(t, beforeStamps["index.json"], afterStamps["index.json"], "a 304 rewrites nothing")
	assert.Equal(t, beforeStamps["packages.jsonl"], afterStamps["packages.jsonl"])
	assert.NotEqual(t, firstFetchedAt, readWatermark(t, cacheDir, testCommunity)["fetched_at"],
		"the watermark's fetched_at is what a 304 buys")
}

// TestForceStillSendsIfModifiedSince pins that `--refresh` skips the TTL
// but not the conditional GET: a 304 is the correct answer to "is this
// current?" and it costs nothing.
func TestForceStillSendsIfModifiedSince(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	_, err = src.RefreshIndex(t.Context(), testCommunity, true, nil)
	require.NoError(t, err)

	requests, conditionals, served200, served304 := srv.counts()
	assert.Equal(t, 2, requests, "force asks even inside the TTL")
	assert.Equal(t, 1, conditionals)
	assert.Equal(t, 1, served200)
	assert.Equal(t, 1, served304)
}

// TestAChangedDocumentRewritesBothFiles pins the other half: when upstream
// really has moved, a 200 replaces the index.
func TestAChangedDocumentRewritesBothFiles(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, clock := newSource(t, srv)

	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	before := readFiles(t, indexDir(cacheDir, testCommunity))

	srv.publish(twoPackageDocument(), "Thu, 11 Sep 2026 12:00:00 GMT")
	clock.advance(7 * time.Hour)

	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, status.Packages)

	after := readFiles(t, indexDir(cacheDir, testCommunity))
	assert.NotEqual(t, before["index.json"], after["index.json"])
	assert.NotEqual(t, before["packages.jsonl"], after["packages.jsonl"])
	assert.Equal(t, "Thu, 11 Sep 2026 12:00:00 GMT", readWatermark(t, cacheDir, testCommunity)["last_modified"])

	// The search that follows must see the NEW index, not the copy the
	// process loaded before the refresh.
	result, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalCount)
}

// TestATornRefreshReadsAsCold is the write-then-rename, watermark-last
// guarantee: a process that dies mid-commit leaves a directory that reads
// as cold - one wasted rebuild - rather than as an index whose offsets
// address the previous document.
func TestATornRefreshReadsAsCold(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, _ := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)

	// Exactly the state commit passes through between removing the
	// watermark and writing the new one: both data files present, no
	// watermark.
	dir := indexDir(cacheDir, testCommunity)
	require.NoError(t, os.Remove(filepath.Join(dir, "watermark.json")))

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present, "no watermark means no index")

	// And the next search rebuilds rather than serving what is lying there.
	result, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "skinwalkers"})
	require.NoError(t, err)
	assert.NotZero(t, result.TotalCount)
	requests, _, _, _ := srv.counts()
	assert.Equal(t, 2, requests)
}

// TestCorruptOrStaleOnDiskStatesReadAsCold covers the rest of the "either
// file missing or short" rule in one table.
func TestCorruptOrStaleOnDiskStatesReadAsCold(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, dir string)
	}{
		{"no watermark", func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, "watermark.json")))
		}},
		{"a watermark from another schema", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "watermark.json"),
				[]byte(`{"last_modified":"x","fetched_at":1,"packages":12,"schema":99}`), 0o644))
		}},
		{"an unreadable watermark", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "watermark.json"), []byte("{"), 0o644))
		}},
		{"no index.json", func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, "index.json")))
		}},
		{"an empty index.json", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), nil, 0o644))
		}},
		{"a corrupt index.json", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), []byte(`[["only","two"]]`), 0o644))
		}},
		{"no packages.jsonl", func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, "packages.jsonl")))
		}},
		{"a truncated packages.jsonl", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "packages.jsonl")
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.NoError(t, os.Truncate(path, info.Size()/2))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newIndexServer(t, fixtureDocument(t))
			src, cacheDir, _ := newSource(t, srv)
			_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
			require.NoError(t, err)

			tt.damage(t, indexDir(cacheDir, testCommunity))

			status, err := src.IndexStatus(t.Context(), testCommunity)
			require.NoError(t, err)
			assert.False(t, status.Present, "a damaged index must read as absent, never as an index")
		})
	}
}

// TestAFailedRefreshServesTheStaleIndex pins source.LocalIndexSource's one
// unguessable rule at the implementation: upstream being down does not take
// a working index away from the user.
func TestAFailedRefreshServesTheStaleIndex(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, clock := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	before := readFiles(t, indexDir(cacheDir, testCommunity))

	srv.Close()
	clock.advance(7 * time.Hour)

	status, err := src.RefreshIndex(t.Context(), testCommunity, true, nil)
	require.Error(t, err, "an explicit refresh reports what went wrong")
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
	assert.True(t, status.Present, "...alongside the index it could not replace")
	assert.True(t, status.Stale)
	assert.Equal(t, 12, status.Packages)

	// A search over the same stale index still answers.
	result, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "skinwalkers"})
	require.NoError(t, err, "a search must never fail over a usable index")
	assert.NotZero(t, result.TotalCount)

	after := readFiles(t, indexDir(cacheDir, testCommunity))
	assert.Equal(t, before["index.json"], after["index.json"], "a failed refresh changes nothing on disk")
	assert.Equal(t, before["packages.jsonl"], after["packages.jsonl"])
}

// TestAFailedRefreshMidStreamKeepsTheOldIndex is the torn-refresh case that
// really happens: the transfer dies halfway through. The staging files go,
// the published ones do not move.
func TestAFailedRefreshMidStreamKeepsTheOldIndex(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, clock := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	dir := indexDir(cacheDir, testCommunity)
	before := readFiles(t, dir)
	beforeStamps := modTimes(t, dir)

	// Half a document, then the connection ends.
	truncated := fixtureDocument(t)[:len(fixtureDocument(t))/2]
	srv.publish(truncated, "Thu, 11 Sep 2026 12:00:00 GMT")
	clock.advance(7 * time.Hour)

	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
	assert.True(t, status.Present, "the old index is still there")

	after := readFiles(t, dir)
	assert.Equal(t, before["index.json"], after["index.json"])
	assert.Equal(t, before["packages.jsonl"], after["packages.jsonl"])
	assert.Equal(t, beforeStamps["packages.jsonl"], modTimes(t, dir)["packages.jsonl"])
	assert.Equal(t, 12, status.Packages)
	assertNoStagingLeftovers(t, dir)
}

// TestNoCacheDirectoryFailsLoudly pins that a source with nowhere to keep
// an index says so, rather than silently re-downloading 34 MB per query.
func TestNoCacheDirectoryFailsLoudly(t *testing.T) {
	sandboxEnv(t)
	srv := newIndexServer(t, fixtureDocument(t))
	src := thunderstore.New(thunderstore.Options{BaseURL: srv.URL})

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present)

	_, err = src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
}

// TestRetriesAThrottledFetch pins that the house backoff transport is in
// front of the index fetch: a 429 is retried, not surfaced.
func TestRetriesAThrottledFetch(t *testing.T) {
	sandboxEnv(t)
	body := fixtureDocument(t)
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	src := thunderstore.New(thunderstore.Options{CacheDir: t.TempDir(), BaseURL: srv.URL})
	status, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	assert.Equal(t, 12, status.Packages)
	assert.Equal(t, 2, attempts, "the throttled attempt is retried, not reported")
}

// modTimes reads every file's modification time in dir.
func modTimes(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		require.NoError(t, err)
		out[e.Name()] = info.ModTime()
	}
	return out
}

// twoPackageDocument is a hand-written index of two packages, for the
// "upstream changed" case - deliberately a different SHAPE from the
// fixture, so a stale read cannot pass by accident.
func twoPackageDocument() []byte {
	return []byte(`[
	  {"name":"Alpha","full_name":"Owner-Alpha","owner":"Owner","date_created":"2026-01-01T00:00:00Z","date_updated":"2026-09-11T00:00:00Z","is_deprecated":false,"categories":["Mods"],
	   "versions":[{"version_number":"1.0.0","description":"The first.","dependencies":[],"date_created":"2026-09-11T00:00:00Z","website_url":"","file_size":100}]},
	  {"name":"Beta","full_name":"Owner-Beta","owner":"Owner","date_created":"2026-01-01T00:00:00Z","date_updated":"2026-09-10T00:00:00Z","is_deprecated":false,"categories":["Tools"],
	   "versions":[{"version_number":"2.0.0","description":"The second.","dependencies":[],"date_created":"2026-09-10T00:00:00Z","website_url":"","file_size":200}]}
	]`)
}
