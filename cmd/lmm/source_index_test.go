package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `lmm source index` and `lmm source index prune` (#410).

var updateSourceIndexGoldens = flag.Bool("update-source-index", false,
	"re-record cmd/lmm/testdata/json_golden/source_index*.golden from current output")

// sourceIndexFetchedAt is the fixed moment every fake index was fetched.
var sourceIndexFetchedAt = time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)

// inventoryIndexSource is coldIndexSource with directories on "disk".
type inventoryIndexSource struct {
	*coldIndexSource
	cached    map[string]source.CachedIndex
	removed   []string
	removeErr map[string]error
}

func (s *inventoryIndexSource) IndexStatus(_ context.Context, id string) (source.IndexStatus, error) {
	ci, ok := s.cached[id]
	if !ok {
		return source.IndexStatus{GameID: id}, nil
	}
	return source.IndexStatus{GameID: id, Present: ci.Present, Packages: ci.Packages, FetchedAt: ci.FetchedAt, Bytes: ci.Bytes}, nil
}

func (s *inventoryIndexSource) CachedIndexes(context.Context) ([]source.CachedIndex, error) {
	out := make([]source.CachedIndex, 0, len(s.cached))
	for _, ci := range s.cached {
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

func (s *inventoryIndexSource) RemoveIndex(_ context.Context, id string, _ time.Time) (int64, error) {
	if err := s.removeErr[id]; err != nil {
		return 0, err
	}
	ci, ok := s.cached[id]
	if !ok {
		return 0, nil
	}
	delete(s.cached, id)
	s.removed = append(s.removed, id)
	return ci.Bytes, nil
}

// newSourceIndexService: a thunderstore-shaped source, the game mapping it
// to lethal-company, and two indexes on disk - the game's, and one nobody
// uses.
func newSourceIndexService(t *testing.T) (*core.Service, *domain.Game, *inventoryIndexSource) {
	t.Helper()
	src := &inventoryIndexSource{
		coldIndexSource: &coldIndexSource{id: "thunderstore"},
		cached: map[string]source.CachedIndex{
			"lethal-company":  {GameID: "lethal-company", Present: true, Packages: 50707, Bytes: 239075328, FetchedAt: sourceIndexFetchedAt, Removable: true},
			"content-warning": {GameID: "content-warning", Present: true, Packages: 900, Bytes: 4096000, FetchedAt: sourceIndexFetchedAt, Removable: true},
		},
	}
	svc, game := newColdIndexService(t, src)
	return svc, game, src
}

func withJSON(t *testing.T) {
	t.Helper()
	orig := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = orig })
}

func checkSourceIndexGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "json_golden", name+".golden")
	if *updateSourceIndexGoldens {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden %s: run go test ./cmd/lmm -run TestSourceIndex -update-source-index", path)
	assert.Equal(t, string(want), got)
}

func TestSourceIndex_ShowsTheGamesIndex(t *testing.T) {
	svc, game, _ := newSourceIndexService(t)
	out := captureStdout(t, func() error { return doSourceIndex(t.Context(), svc, game, "", false) })
	assert.Contains(t, out, "Thunderstore index for lethal-company")
	assert.Contains(t, out, "Packages: 50707")
	assert.Contains(t, out, "Size:     228.0 MB")
	assert.Contains(t, out, "Updated:  ")
}

func TestSourceIndex_SaysWhenThereIsNoIndexYet(t *testing.T) {
	svc, game, src := newSourceIndexService(t)
	delete(src.cached, "lethal-company")
	out := captureStdout(t, func() error { return doSourceIndex(t.Context(), svc, game, "", false) })
	assert.Contains(t, out, "No Thunderstore index for lethal-company yet")
	assert.Contains(t, out, "lmm source index --refresh")
}

// TestSourceIndex_JSON is the read half's --json document: exactly
// core.IndexStatus.
func TestSourceIndex_JSON(t *testing.T) {
	svc, game, _ := newSourceIndexService(t)
	withJSON(t)
	out := captureStdout(t, func() error { return doSourceIndex(t.Context(), svc, game, "thunderstore", false) })
	checkSourceIndexGolden(t, "source_index", out)
}

func TestSourceIndex_RefreshRebuildsAndSaysWhatHappened(t *testing.T) {
	svc, game, src := newSourceIndexService(t)
	delete(src.cached, "lethal-company")

	stdout, stderr, err := captureStdoutAndStderr(t, func() error {
		return doSourceIndex(t.Context(), svc, game, "", true)
	})
	require.NoError(t, err)
	assert.True(t, src.forced, "--refresh skips the TTL")
	assert.Contains(t, stderr, "Fetching the Thunderstore index for lethal-company...", "progress goes where every index notice goes")
	assert.Contains(t, stdout, "Thunderstore index for lethal-company updated: 50707 packages, 228.0 MB")
}

// TestSourceIndex_RefreshJSONIsTheReport: the write half's document is
// core.IndexReport, and nothing but it is on stdout.
func TestSourceIndex_RefreshJSONIsTheReport(t *testing.T) {
	svc, game, _ := newSourceIndexService(t)
	withJSON(t)
	out := captureStdout(t, func() error { return doSourceIndex(t.Context(), svc, game, "", true) })
	assert.True(t, strings.HasPrefix(out, "{\n  \"source\": \"thunderstore\",\n  \"game\": \"lethal-company\",\n  \"status\": "), out)
	assert.NotContains(t, out, "Fetching")
}

func TestSourceIndex_AGameWithNoIndexedSourceSaysHowToAddOne(t *testing.T) {
	spy := &pageSizeSpySource{id: "spy-index"}
	svc, game := newPageSizeSpyService(t, spy)
	err := doSourceIndex(t.Context(), svc, game, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source thunderstore=<community>")

	err = doSourceIndex(t.Context(), svc, game, "spy-index", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keeps no local index")
}

func TestSourceIndex_AllCannotBeCombinedWithRefresh(t *testing.T) {
	origAll, origRefresh := sourceIndexAll, sourceIndexRefresh
	t.Cleanup(func() { sourceIndexAll, sourceIndexRefresh = origAll, origRefresh })
	sourceIndexAll, sourceIndexRefresh = true, true
	err := sourceIndexCmd.RunE(sourceIndexCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with --all")
}

func TestSourceIndex_AllListsEveryIndex(t *testing.T) {
	svc, _, _ := newSourceIndexService(t)
	out := captureStdout(t, func() error { return doSourceIndexList(t.Context(), svc, "") })
	assert.Contains(t, out, "SOURCE")
	assert.Contains(t, out, "content-warning")
	assert.Contains(t, out, "lethal-company")
	assert.Contains(t, out, "lethal") // used by
	assert.Contains(t, out, "Total on disk: 231.9 MB")
}

func TestSourceIndex_AllJSON(t *testing.T) {
	svc, _, _ := newSourceIndexService(t)
	withJSON(t)
	out := captureStdout(t, func() error { return doSourceIndexList(t.Context(), svc, "") })
	checkSourceIndexGolden(t, "source_index_all", out)
}

// TestSourceIndexPrune_RemovesWhatNoGameUses: a plain prune removes the
// unused index without asking, and keeps the game's.
func TestSourceIndexPrune_RemovesWhatNoGameUses(t *testing.T) {
	svc, _, src := newSourceIndexService(t)
	out := captureStdout(t, func() error {
		return doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{}, false)
	})
	assert.Equal(t, []string{"content-warning"}, src.removed)
	assert.Contains(t, out, "removed")
	assert.Contains(t, out, "no game uses it")
	assert.Contains(t, out, "used by lethal")
	assert.Contains(t, out, "Removed 1 index(es), freeing 3.9 MB.")
}

func TestSourceIndexPrune_DryRunRemovesNothing(t *testing.T) {
	svc, _, src := newSourceIndexService(t)
	out := captureStdout(t, func() error {
		return doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{All: true, DryRun: true}, false)
	})
	assert.Empty(t, src.removed)
	assert.Contains(t, out, "would remove")
	assert.Contains(t, out, "Would remove 2 index(es)")
	assert.Contains(t, out, "Nothing was removed (dry run).")
}

func TestSourceIndexPrune_DryRunJSON(t *testing.T) {
	svc, _, src := newSourceIndexService(t)
	withJSON(t)
	out := captureStdout(t, func() error {
		return doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{DryRun: true}, false)
	})
	assert.Empty(t, src.removed)
	checkSourceIndexGolden(t, "source_index_prune_dry_run", out)
}

// TestSourceIndexPrune_AllAsksFirst: --all removes indexes a game uses, so
// it asks - and a no is a cancellation with nothing removed.
func TestSourceIndexPrune_AllAsksFirst(t *testing.T) {
	t.Run("no", func(t *testing.T) {
		svc, _, src := newSourceIndexService(t)
		var err error
		withStdin(t, "n\n", func() {
			_ = captureStdout(t, func() error {
				err = doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{All: true}, false)
				return nil
			})
		})
		assert.ErrorIs(t, err, ErrCancelled)
		assert.Empty(t, src.removed)
	})
	t.Run("yes", func(t *testing.T) {
		svc, _, src := newSourceIndexService(t)
		var out string
		withStdin(t, "y\n", func() {
			out = captureStdout(t, func() error {
				return doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{All: true}, false)
			})
		})
		sort.Strings(src.removed)
		assert.Equal(t, []string{"content-warning", "lethal-company"}, src.removed)
		assert.Contains(t, out, "Remove 2 index(es), 231.9 MB?")
	})
	t.Run("-y", func(t *testing.T) {
		svc, _, src := newSourceIndexService(t)
		_ = captureStdout(t, func() error {
			return doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{All: true}, true)
		})
		assert.Len(t, src.removed, 2)
	})
	t.Run("--json without -y", func(t *testing.T) {
		svc, _, src := newSourceIndexService(t)
		withJSON(t)
		err := doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{All: true}, false)
		assert.ErrorIs(t, err, core.ErrConfirmationRequired)
		assert.Empty(t, src.removed)
	})
}

// TestReportError_JSON_IndexUnavailableError pins the envelope a host lmm
// is holding off produces, built the way production builds it (T3 review
// P4): the real Thunderstore source against a local server that answers
// 429 with a ten-minute Retry-After, the real core classification, and the
// real formatter. The sentence names the hold once, in local time, and
// retry_at is the same moment to the second.
func TestReportError_JSON_IndexUnavailableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	cacheDir := t.TempDir()
	svc.RegisterSource(thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: srv.URL}))
	game := &domain.Game{
		ID: "lethal", Name: "Lethal Company", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"thunderstore": "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	withSearchFlags(t, "thunderstore", 10)

	searchErr := doSearch(t.Context(), svc, game, []string{"ship"})
	var typed *core.IndexUnavailableError
	require.ErrorAs(t, searchErr, &typed)

	withJSONOutput(t)
	out := captureStdout(t, func() error { reportError(searchErr); return nil })
	var doc struct {
		Error   string `json:"error"`
		Details struct {
			Source  string `json:"source"`
			Game    string `json:"game"`
			Reason  string `json:"reason"`
			RetryAt string `json:"retry_at"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	retryAt, err := time.Parse(time.RFC3339, doc.Details.RetryAt)
	require.NoError(t, err, "retry_at is RFC 3339: %q", doc.Details.RetryAt)
	assert.Equal(t, time.UTC, retryAt.Location(), "retry_at is UTC")
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), retryAt, 5*time.Second)

	const reason = "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s"
	assert.Equal(t, "thunderstore", doc.Details.Source)
	assert.Equal(t, "lethal-company", doc.Details.Game)
	assert.Equal(t, reason, doc.Details.Reason)
	assert.Equal(t,
		`search failed: source "thunderstore": the lethal-company index could not be built: not asking Thunderstore again until `+
			retryAt.Local().Format("15:04:05")+": "+reason,
		doc.Error, "the hold is named once, at the local clock time retry_at names")
	assert.NotContains(t, doc.Error, "unavailable", "the sentinel's own words are not repeated into the sentence")
}

// TestReportError_JSON_GameIdentifierError pins the envelope a missing or
// malformed games.yaml identifier produces.
func TestReportError_JSON_GameIdentifierError(t *testing.T) {
	withJSONOutput(t)
	err := &core.GameIdentifierError{
		GameID: "lethal", Source: "thunderstore", Value: "",
		Err: errors.New("game \"lethal\" maps source \"thunderstore\" to an empty identifier; set it with 'lmm game edit lethal --source thunderstore=<identifier>': the game's identifier for this source is missing or malformed"),
	}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"game \\\"lethal\\\" maps source \\\"thunderstore\\\" to an empty identifier; set it with 'lmm game edit lethal --source thunderstore=<identifier>': the game's identifier for this source is missing or malformed\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"lethal\",\n"+
		"    \"source\": \"thunderstore\",\n"+
		"    \"value\": \"\"\n"+
		"  }\n"+
		"}\n", out)
}

// heldInventorySource is inventoryIndexSource while the host is holding
// requests off.
type heldInventorySource struct {
	*inventoryIndexSource
	holds []source.Hold
}

func (s *heldInventorySource) Holds(context.Context) []source.Hold { return s.holds }

// TestSourceIndex_SaysWhenLmmWillAskAgain: a hold another lmm process
// recorded is shown before anyone searches into it (T3 review F3/F10).
func TestSourceIndex_SaysWhenLmmWillAskAgain(t *testing.T) {
	at := time.Now().Add(9*time.Minute + 30*time.Second).Truncate(time.Second)
	src := &heldInventorySource{
		inventoryIndexSource: &inventoryIndexSource{coldIndexSource: &coldIndexSource{id: "thunderstore"}, cached: map[string]source.CachedIndex{
			"lethal-company":  {GameID: "lethal-company", Present: true, Packages: 50707, Bytes: 239075328, FetchedAt: sourceIndexFetchedAt, Removable: true},
			"content-warning": {GameID: "content-warning", Bytes: 5120, Reason: "the content-warning index directory holds notes.txt, which is not part of an index"},
		}},
		holds: []source.Hold{{Source: "Thunderstore", Until: at, Reason: "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s"}},
	}
	svc, game := newColdIndexService(t, src)

	out := captureStdout(t, func() error { return doSourceIndex(t.Context(), svc, game, "", false) })
	assert.Contains(t, out, "Not asking Thunderstore again until "+at.Local().Format("15:04:05"))
	assert.Contains(t, out, "rate limited by Thunderstore (HTTP 429)")

	out = captureStdout(t, func() error { return doSourceIndexList(t.Context(), svc, "") })
	assert.Contains(t, out, "Not asking Thunderstore again until "+at.Local().Format("15:04:05"))
	assert.Contains(t, out, "prune keeps it: the content-warning index directory holds notes.txt", "a row a prune would keep says why")
	assert.Contains(t, out, "unusable", "a directory with no usable index is not listed like one")
}

// TestSourceIndex_AnUnusableIndexOnDiskIsNotCalledMissing is review F12: an
// index an older lmm wrote was "No ... index yet" here and a sized row in
// --all.
func TestSourceIndex_AnUnusableIndexOnDiskIsNotCalledMissing(t *testing.T) {
	svc, game, src := newSourceIndexService(t)
	src.cached["lethal-company"] = source.CachedIndex{GameID: "lethal-company", Packages: 50000, Bytes: 200000000, FetchedAt: sourceIndexFetchedAt, Removable: true}
	out := captureStdout(t, func() error { return doSourceIndex(t.Context(), svc, game, "", false) })
	assert.NotContains(t, out, "yet")
	assert.Contains(t, out, "cannot use")
	assert.Contains(t, out, "190.7 MB")
	assert.Contains(t, out, "lmm source index --refresh")
}

// TestSourceIndexPrune_AFailedRemovalExitsNonZero is review F11: a run in
// which an index could not be removed exited 0.
func TestSourceIndexPrune_AFailedRemovalExitsNonZero(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		svc, _, src := newSourceIndexService(t)
		src.removeErr = map[string]error{"content-warning": errors.New("permission denied")}
		if asJSON {
			withJSON(t)
		}
		var err error
		out := captureStdout(t, func() error {
			err = doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{}, false)
			return nil
		})
		require.Error(t, err, "json=%v", asJSON)
		assert.ErrorIs(t, err, ErrReported, "the report already said it: nothing is printed twice")
		assert.Equal(t, exitError, exitCodeFor(err))
		assert.Contains(t, out, "permission denied")
		if asJSON {
			assert.Equal(t, 1, strings.Count(out, "\"entries\""), "one document on stdout")
		}
	}
}

// TestSourceIndexPrune_TheAllPromptDoesNotSayNothingWasRemoved: the preview
// --all shows before asking is not a finished dry run (review F11).
func TestSourceIndexPrune_TheAllPromptDoesNotSayNothingWasRemoved(t *testing.T) {
	svc, _, _ := newSourceIndexService(t)
	var out string
	withStdin(t, "n\n", func() {
		out = captureStdout(t, func() error {
			_ = doSourceIndexPrune(t.Context(), svc, core.IndexPruneOptions{All: true}, false)
			return nil
		})
	})
	assert.NotContains(t, out, "dry run")
	assert.NotContains(t, out, "Nothing was removed")
	assert.Contains(t, out, "Remove 2 index(es), 231.9 MB?")
}
