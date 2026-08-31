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

// TestServer_Search_RendersHits is /search's headline RED test
// (docs/plans/2026-08-30-serve-impl.md Task 4): a query must render a
// matching hit's name/version, its installed state, the CSRF token, and
// the install form's target route Task 8 (#322) will wire.
func TestServer_Search_RendersHits(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Worse Hats", Version: "2.0"}})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/search?q=boots", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Better Boots")
	assert.NotContains(t, body, "Worse Hats")
	assert.Contains(t, body, `action="/mods/fake/1/install"`)
	// Task 8 (#322) wired the route: the button submits rather than
	// rendering as Task 4's disabled shell.
	assert.NotContains(t, body, "disabled")
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_Search_MarksInstalledHits proves a hit already installed in the
// current profile is flagged, not just listed alongside uninstalled ones.
func TestServer_Search_MarksInstalledHits(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID}, true, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/search?q=boots", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	rows := rec.Body.String()
	assert.Regexp(t, `Better Boots</a></td>\s*<td[^>]*>1\.0</td>\s*<td[^>]*>yes</td>`, rows)
}

// TestServer_Search_NoQuery_RendersFormOnly covers the CSS/JS-absent, no-
// query landing state: the bare search form, no results section, no core
// Search call (a query never reaches the source).
func TestServer_Search_NoQuery_RendersFormOnly(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/search", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `action="/search"`)
	assert.Contains(t, body, "Enter a query above")
}
