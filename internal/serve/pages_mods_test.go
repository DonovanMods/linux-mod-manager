package serve_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_Mods_RendersInstalledMod is /mods's headline RED test
// (docs/plans/2026-08-30-serve-impl.md Task 4): the page must render a
// seeded installed mod's name/version/enabled state, the CSRF token in its
// per-row forms, and the exact form targets Task 8 (#322) will wire.
func TestServer_Mods_RendersInstalledMod(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID:       "42",
		SourceID: "fake",
		Name:     "Better Boots",
		Version:  "1.2.0",
		GameID:   game.ID,
	}, true, map[string][]byte{"boots.esp": []byte("data")})

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Better Boots")
	assert.Contains(t, body, "1.2.0")
	// The mod seeded above is enabled, so only its disable/uninstall forms
	// render (mirroring the real toggle: an enabled mod has no "enable"
	// action) - Task 8 (#322) wires these same two routes, plus the
	// symmetric /enable route a disabled mod's row targets instead (see
	// TestServer_Mods_ToggleForms_AreLive).
	assert.Contains(t, body, `action="/mods/fake/42/disable"`)
	assert.Contains(t, body, `action="/mods/fake/42/uninstall"`)
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_Mods_ToggleForms_AreLive proves Task 8 (#322) wired the two
// toggle routes: a disabled mod's row renders a submittable Enable button,
// not the Task 4 shell that carried the disabled attribute.
func TestServer_Mods_ToggleForms_AreLive(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "1", SourceID: "fake", Name: "Mod", Version: "1.0", GameID: game.ID}, false, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `action="/mods/fake/1/enable"`)
	assert.Regexp(t, `<button type="submit"(?:\s+class="[^"]*")?>Enable</button>`, body)
	assert.NotRegexp(t, `<button type="submit" disabled[^>]*>Enable</button>`, body)
}

// TestServer_Mods_NoModsInstalled_RendersEmptyState covers the
// CSS/JS-absent, "profile exists but has nothing installed" case.
func TestServer_Mods_NoModsInstalled_RendersEmptyState(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No mods installed")
}
