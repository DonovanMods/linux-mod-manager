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

// TestServer_APIMods_ReturnsExactModList is /api/v1/mods's headline RED
// test (docs/plans/2026-08-30-serve-impl.md Task 5): the body must decode
// into core.ModList with unknown members rejected AND byte-match
// core.EncodeJSON of the same live ListMods call.
func TestServer_APIMods_ReturnsExactModList(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "42", SourceID: "fake", Name: "Better Boots", Version: "1.2.0", GameID: game.ID,
	}, true, map[string][]byte{"boots.esp": []byte("data")})

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var list core.ModList
	decodeStrict(t, rec.Body.Bytes(), &list)

	want, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIMods_NoGames_Renders404 proves the Task 5 ruling: unlike
// the /mods PAGE (which degrades to a 200 friendly empty state), the API
// has no page to degrade to, so an unresolved selection - here, zero games
// configured at all - answers the {"error","details"} envelope at 404,
// with details listing the (empty) valid choices.
func TestServer_APIMods_NoGames_Renders404(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var envelope struct {
		Error   string `json:"error"`
		Details struct {
			Games    []core.GameListEntry `json:"games"`
			Profiles []string             `json:"profiles"`
		} `json:"details"`
	}
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
	assert.Empty(t, envelope.Details.Games)
}

// TestServer_APIMods_UnknownGameParam_Renders404 proves an explicit
// ?game= naming an unconfigured game answers 404 with details listing the
// real, configured game(s) as the valid choices.
func TestServer_APIMods_UnknownGameParam_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods?game=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)

	var envelope struct {
		Error   string `json:"error"`
		Details struct {
			Games []core.GameListEntry `json:"games"`
		} `json:"details"`
	}
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "nope")
	require.Len(t, envelope.Details.Games, 1)
	assert.Equal(t, "g1", envelope.Details.Games[0].ID)
}

// TestServer_APIMods_InternalFailure_Renders500 proves a genuine internal
// failure (the DB closed out from under the request - the same
// "closing the DB early forces the read to fail" pattern internal/core's
// own tests use) answers the {"error","details"} envelope at 500, never a
// 200-with-envelope.
func TestServer_APIMods_InternalFailure_Renders500(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.Close())

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}

// TestServer_APIModDetail_ReturnsExactModDetail is
// /api/v1/mods/{source}/{id}'s headline test: the body must decode into
// core.ModDetail with unknown members rejected AND byte-match
// core.EncodeJSON of the same live ModDetail call - matching `lmm mod show
// --json`'s own document exactly (no ModFiles/AvailableModVersions merged
// in, unlike the page).
func TestServer_APIModDetail_ReturnsExactModDetail(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var detail core.ModDetail
	decodeStrict(t, rec.Body.Bytes(), &detail)

	want, err := svc.ModDetail(context.Background(), game, "default", "fake", "1")
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIModDetail_UnknownMod_Renders404 proves an unknown mod ID
// answers the envelope at 404 - mirroring the page's own NotFound
// treatment of any ModDetail failure.
func TestServer_APIModDetail_UnknownMod_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/bogus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}
