package serve_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_APISearch_ReturnsExactSearchReport is /api/v1/search's
// headline RED test (docs/plans/2026-08-30-serve-impl.md Task 5): the body
// must decode into core.SearchReport with unknown members rejected AND
// byte-match core.EncodeJSON of the same live Search call.
func TestServer_APISearch_ReturnsExactSearchReport(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Worse Hats", Version: "2.0"}})
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.SearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)

	want, err := svc.Search(context.Background(), game, "default", "boots", core.SearchOptions{})
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APISearch_LimitParam_CapsResults is the task-5 gate review's
// Minor 7 fix: /api/v1/search passed a zero core.SearchOptions, so it
// always returned every hit, while `lmm search --json` defaults to
// --limit 10 - the one endpoint whose bytes could diverge from its CLI
// twin on the same state for a reason unrelated to Important 1. An
// explicit ?limit= now caps core.SearchReport.Mods the same way
// core.SearchOptions.Limit always has; the default (no ?limit=) stays
// unset/uncapped, matching every other test in this file.
func TestServer_APISearch_LimitParam_CapsResults(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Boots Alpha", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Boots Beta", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "3", SourceID: "fake", Name: "Boots Gamma", Version: "1.0"}})
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&limit=2", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var report core.SearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Len(t, report.Mods, 2, "?limit=2 must cap the returned hits")
	assert.Equal(t, 3, report.TotalResults, "TotalResults stays the untruncated count")

	want, err := svc.Search(context.Background(), game, "default", "boots", core.SearchOptions{Limit: 2})
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APISearch_InvalidLimitParam_Renders400 proves a non-numeric
// ?limit= is bad input (400), the same class of error as a missing q.
func TestServer_APISearch_InvalidLimitParam_Renders400(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&limit=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "limit")
}

// TestServer_APISearch_MissingQuery_Renders400 proves a missing/empty q is
// bad input (400): unlike the /search PAGE, which has a bare form to fall
// back to, the API has nothing to render without a query, matching the
// CLI's own cobra.MinimumNArgs(1) requirement for `lmm search`.
func TestServer_APISearch_MissingQuery_Renders400(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "q")
}

// TestServer_APISearch_PageParams_ThreadThroughAndCapResults is #331's RED
// test for the search PAGE's pagination: ?page=/?page_size= forward into
// SearchOptions, echo back on the report, and (with no ?limit=) page_size
// doubles as the merged-results cap so a client asking for one page of N
// gets at most N rows rather than every source's own page concatenated.
func TestServer_APISearch_PageParams_ThreadThroughAndCapResults(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Boots Alpha", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Boots Beta", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "3", SourceID: "fake", Name: "Boots Gamma", Version: "1.0"}})
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&page=0&page_size=2", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var report core.SearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	assert.Equal(t, 0, report.Page)
	assert.Equal(t, 2, report.PageSize)
	require.Len(t, report.Mods, 2, "page_size doubles as the merged-results cap with no explicit limit")

	want, err := svc.Search(context.Background(), game, "default", "boots", core.SearchOptions{Page: 0, PageSize: 2, Limit: 2})
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APISearch_InvalidPageParam_Renders400 and its page_size
// sibling prove both new params are refused the same way ?limit= already
// is: bad input, not a 500 from a core call that never runs.
func TestServer_APISearch_InvalidPageParam_Renders400(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&page=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "page")
}

func TestServer_APISearch_InvalidPageSizeParam_Renders400(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&page_size=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "page_size")
}

// TestServer_APISearch_ExplicitLimitOverridesPageSizeCap proves an explicit
// ?limit= wins over page_size's implicit cap, the more specific ask.
func TestServer_APISearch_ExplicitLimitOverridesPageSizeCap(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Boots Alpha", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Boots Beta", Version: "1.0"}})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&page_size=2&limit=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var report core.SearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Len(t, report.Mods, 1, "the explicit limit must win over page_size's implicit cap")
}

// TestServer_APISearch_AggregateMultiSourcePagination_NoResultsDropped is
// Important 6 (unit 5 fix wave): with TWO sources configured, page_size
// previously doubled as an implicit Limit on the merged results, capping a
// 20-candidate page at its own page_size even though searchAllSources had
// fetched a page from EACH source. Two sources, 15 mods apiece (fs1's IDs/
// names sort strictly before fs2's, so rankAggregate's name-ascending
// tiebreak - both sources' Downloads are 0 - orders the merge fs1-then-fs2
// deterministically): page 0 at page_size=10 fetches page 0 from BOTH (10 +
// 10 = 20 candidates); the old bug capped that at 10, permanently dropping
// fs2's entire page-0 contribution (fs2 page 1 only ever re-fetches items
// 11-15, never 1-10). This proves every one of the 30 catalog mods is
// reachable across the two pages that exist, with no explicit ?limit=.
func TestServer_APISearch_AggregateMultiSourcePagination_NoResultsDropped(t *testing.T) {
	fs1 := newFakeSource("fs1")
	fs2 := newFakeSource("fs2")
	for i := 1; i <= 15; i++ {
		fs1.addMod(fakeSourceMod{Mod: domain.Mod{
			ID: fmt.Sprintf("%02d", i), SourceID: "fs1",
			Name: fmt.Sprintf("Item S1-%02d", i), Version: "1.0",
		}})
		fs2.addMod(fakeSourceMod{Mod: domain.Mod{
			ID: fmt.Sprintf("%02d", i), SourceID: "fs2",
			Name: fmt.Sprintf("Item S2-%02d", i), Version: "1.0",
		}})
	}

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(fs1)
	svc.RegisterSource(fs2)

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{fs1.ID(): "", fs2.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(context.Background(), game.ID))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	fetchPage := func(page int) core.SearchReport {
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("http://%s/api/v1/search?q=item&page=%d&page_size=10", testAddr, page), nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var report core.SearchReport
		decodeStrict(t, rec.Body.Bytes(), &report)
		return report
	}

	page0 := fetchPage(0)
	require.Len(t, page0.Mods, 20, "page 0 must hold BOTH sources' own page-0 contributions (10+10), uncapped")

	page1 := fetchPage(1)
	require.Len(t, page1.Mods, 10, "page 1 holds both sources' remaining 5 mods apiece")

	seen := map[string]bool{}
	for _, hit := range append(page0.Mods, page1.Mods...) {
		seen[hit.SourceID+"/"+hit.ID] = true
	}
	assert.Len(t, seen, 30, "every mod from both 15-mod catalogs must be reachable across the two pages")
}

// TestServer_APISearch_CategoryParam_FiltersServerSide is Important 1b
// (unit 5 fix wave): the search page's category filter moved server-side -
// ?category= forwards into SearchOptions.Category (already forwarded to
// every source's own SearchQuery), narrowing TotalResults to the CATALOG-
// wide count for that category, not a client-side slice of one page.
func TestServer_APISearch_CategoryParam_FiltersServerSide(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Boots Alpha", Version: "1.0", Category: "Armor"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Boots Beta", Version: "1.0", Category: "Weapons"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "3", SourceID: "fake", Name: "Boots Gamma", Version: "1.0", Category: "Armor"}})
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&category=Armor", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var report core.SearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Len(t, report.Mods, 2, "only the two Armor-category mods must match, server-side")
	assert.Equal(t, 2, report.TotalResults)
	for _, hit := range report.Mods {
		assert.Equal(t, "Armor", hit.Category)
	}

	want, err := svc.Search(context.Background(), game, "default", "boots", core.SearchOptions{Category: "Armor"})
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APISearch_SourceParam_NarrowsToNamedSource is Important 1b's
// other half: ?source= forwards into SearchOptions.SourceID, the existing
// named-source path (`lmm search --source`), narrowing an aggregate of
// several sources down to exactly the one named.
func TestServer_APISearch_SourceParam_NarrowsToNamedSource(t *testing.T) {
	fs1 := newFakeSource("fs1")
	fs1.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fs1", Name: "Boots From One", Version: "1.0"}})
	fs2 := newFakeSource("fs2")
	fs2.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fs2", Name: "Boots From Two", Version: "1.0"}})

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(fs1)
	svc.RegisterSource(fs2)
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{fs1.ID(): "", fs2.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(context.Background(), game.ID))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&source=fs2", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var report core.SearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Len(t, report.Mods, 1, "only fs2's own mod must appear")
	assert.Equal(t, "fs2", report.Mods[0].SourceID)
	assert.Equal(t, "Boots From Two", report.Mods[0].Name)
}

// TestServer_APISearch_UnresolvedSelection_Renders404 proves the missing-q
// check only gates a genuinely absent/empty q: once q is present
// (?q=boots), an unresolvable ?game= still surfaces as the ordinary
// selection 404 (details naming the valid game), the same as every other
// scoped endpoint - the 400 short-circuit above does not swallow it.
func TestServer_APISearch_UnresolvedSelection_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&game=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "nope")
}
