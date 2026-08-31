package serve_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_Mods_NoGames_RendersEmptyState proves resolveSelection's
// "nothing configured yet" path reaches a page as a normal 200, not a
// 404/500 - exercised through /mods since resolveSelection has no exported
// surface of its own (docs/plans/2026-08-30-serve-impl.md Task 4 ruling on
// game/profile selection).
func TestServer_Mods_NoGames_RendersEmptyState(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No games configured")
}

// TestServer_Mods_UnknownGameParam_RendersWarning proves an explicit
// ?game= naming an unconfigured game degrades to a friendly warning rather
// than a 404/500, and still lists the valid game(s) to switch to.
func TestServer_Mods_UnknownGameParam_RendersWarning(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods?game=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "unknown game")
	assert.Contains(t, body, "nope")
	assert.Contains(t, body, "Fixture Game")
}

// TestServer_Mods_UnknownProfileParam_RendersWarning proves an explicit
// ?profile= naming a profile the resolved game doesn't have degrades the
// same way, while keeping the resolved game (so the switcher still shows
// its valid profiles).
func TestServer_Mods_UnknownProfileParam_RendersWarning(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods?profile=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "unknown profile")
	assert.Contains(t, body, "nope")
}

// TestServer_Mods_NoDefaultGame_RendersWarning proves a game configured
// but with no default game SET (`lmm game set-default` never run) degrades
// to the same friendly warning rather than picking one arbitrarily.
func TestServer_Mods_NoDefaultGame_RendersWarning(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "g1", Name: "Undefaulted Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
	}))
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "no default game is configured")
}

// TestServer_Mods_DefaultSelection_RendersGameAndProfile proves the
// zero-param case resolves the configured default game and its default
// profile, and that the resolved pair is visible on the page (Global
// Constraints / ruling: "every page VISIBLY shows the active
// game+profile").
func TestServer_Mods_DefaultSelection_RendersGameAndProfile(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Fixture Game")
	assert.Contains(t, rec.Body.String(), "default")
}
