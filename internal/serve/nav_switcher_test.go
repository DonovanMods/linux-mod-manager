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

// TestServer_NavSwitcher_HiddenWithOneGameAndProfile is the Task 4 review's
// Minor 1 fix, carried into Task 5 (docs/plans/2026-08-30-serve-impl.md):
// with exactly one game and one profile configured, the nav switcher's
// <select> elements must not render at all - there is nothing to switch
// to, so a functioning-but-pointless single-option control is a
// regression, not a harmless no-op.
func TestServer_NavSwitcher_HiddenWithOneGameAndProfile(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, `id="nav-game"`)
	assert.NotContains(t, body, `id="nav-profile"`)
	assert.NotContains(t, body, `aria-label="Switch game or profile"`)
}

// TestServer_NavSwitcher_ShownWithMultipleGames proves the positive case:
// once a second game exists, the game <select> renders (and the switcher
// form itself appears), even though there is still only one profile.
func TestServer_NavSwitcher_ShownWithMultipleGames(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "g2",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}))

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `id="nav-game"`)
	assert.NotContains(t, body, `id="nav-profile"`)
}

// TestServer_NavSwitcher_ShownWithMultipleProfiles is the profile-side twin:
// a second profile on the same game shows the profile <select> even with
// only one game configured.
func TestServer_NavSwitcher_ShownWithMultipleProfiles(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "survival")
	require.NoError(t, err)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, `id="nav-game"`)
	assert.Contains(t, body, `id="nav-profile"`)
}

// TestServer_NavSwitcher_PreservesSearchQuery is the Task 4 review's Minor
// 2 fix: switching game/profile from /search?q=<term> must not drop the
// typed query - the switcher form needs a hidden "q" field carrying it
// through, since a GET form submission replaces the whole query string
// with only the fields the form itself declares.
func TestServer_NavSwitcher_PreservesSearchQuery(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "g2",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}))

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/search?q=boots", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `<input type="hidden" name="q" value="boots">`)
}

// TestServer_NavSwitcher_NoExtraParamsOnMods proves the hidden-field
// round-trip is generic (any current query param other than game/profile),
// not special-cased to "q": /mods has no such param, so the switcher form
// carries no hidden fields at all.
func TestServer_NavSwitcher_NoExtraParamsOnMods(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "g2",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}))

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `type="hidden"`)
}
