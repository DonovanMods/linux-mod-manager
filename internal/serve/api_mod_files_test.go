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

// TestServer_APIModFiles_ReturnsExactModFilesReport is
// /api/v1/mods/{source}/{id}/files' headline test: the body must decode
// into core.ModFilesReport with unknown members rejected AND byte-match
// core.EncodeJSON of the same live ModFiles call - matching `lmm mod files
// --json` exactly, the un-merged half of what the deleted page layer's mod
// page rendered inline (api_mods_test.go's own precedent for ModDetail).
func TestServer_APIModFiles_ReturnsExactModFilesReport(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, map[string][]byte{"boots.esp": []byte("data")})

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/1/files", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.ModFilesReport
	decodeStrict(t, rec.Body.Bytes(), &report)

	want, err := svc.ModFiles(context.Background(), game, "default", "fake", "1")
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIModFiles_NotInstalled_Renders404 proves a mod that exists at
// the source but is not installed in the resolved profile answers 404 - the
// ModFiles query is install-scoped, unlike ModDetail.
func TestServer_APIModFiles_NotInstalled_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/1/files", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}

// TestServer_APIModVersions_ReturnsVersionsFromTheSource proves
// /api/v1/mods/{source}/{id}/versions decodes into the wrapper document
// carrying AvailableModVersions' own list, Supported true - the endpoint
// that un-orphans AvailableModVersions (#97's "lmm serve's intended
// consumer").
func TestServer_APIModVersions_ReturnsVersionsFromTheSource(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		Files: []domain.DownloadableFile{
			{ID: "f1", Version: "1.0"},
			{ID: "f2", Version: "2.0"},
		},
	})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/1/versions", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var doc struct {
		Versions  []string `json:"versions"`
		Supported bool     `json:"supported"`
	}
	decodeStrict(t, rec.Body.Bytes(), &doc)
	assert.True(t, doc.Supported)
	assert.Equal(t, []string{"1.0", "2.0"}, doc.Versions)
}

// TestServer_APIModVersions_UnsupportedSource_Renders200WithSupportedFalse
// proves a source with no per-file version metadata is not an error - the
// same "nothing to report, not a failure" treatment ModDetail's own
// changelog gives a source with no source.ChangelogProvider.
func TestServer_APIModVersions_UnsupportedSource_Renders200WithSupportedFalse(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"},
		Files: []domain.DownloadableFile{{ID: "f1"}},
	})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/1/versions", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var doc struct {
		Versions  []string `json:"versions"`
		Supported bool     `json:"supported"`
	}
	decodeStrict(t, rec.Body.Bytes(), &doc)
	assert.False(t, doc.Supported)
	assert.Empty(t, doc.Versions)
}

// TestServer_APIModVersions_UnknownMod_Renders404 mirrors ModDetail's own
// unknown-mod treatment (api_mods_test.go).
func TestServer_APIModVersions_UnknownMod_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods/fake/bogus/versions", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}
