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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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

// TestServer_APIMods_InternalFailure_Renders500 proves a genuine internal
// failure (the DB closed out from under the request - the same
// "closing the DB early forces the read to fail" pattern internal/core's
// own tests use) answers the {"error","details"} envelope at 500, never a
// 200-with-envelope.
func TestServer_APIMods_InternalFailure_Renders500(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.Close())

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/bogus", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}

// TestServer_APIModDetail_AnImportedMod (#447): GET
// /api/v1/mods/local/{id} describes an imported mod from its installed row
// instead of answering 404 for want of a `local` source.
func TestServer_APIModDetail_AnImportedMod(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "imp", SourceID: domain.SourceLocal, Name: "Imported", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/local/imp", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var detail core.ModDetail
	decodeStrict(t, rec.Body.Bytes(), &detail)
	assert.Equal(t, "Imported", detail.Mod.Name)
	require.NotNil(t, detail.Installed)
}
