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

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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

// TestServer_APISearch_MissingQuery_Renders400 proves a missing/empty q is
// bad input (400): unlike the /search PAGE, which has a bare form to fall
// back to, the API has nothing to render without a query, matching the
// CLI's own cobra.MinimumNArgs(1) requirement for `lmm search`.
func TestServer_APISearch_MissingQuery_Renders400(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

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
// check runs before selection resolution would even matter, but an empty
// query on a game-less deployment still surfaces as bad input, not a
// selection 404 - 400 takes priority since the request itself is malformed
// independent of what game/profile might have resolved.
func TestServer_APISearch_UnresolvedSelection_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search?q=boots&game=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "nope")
}
