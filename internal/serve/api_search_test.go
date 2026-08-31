package serve_test

import (
	"context"
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
