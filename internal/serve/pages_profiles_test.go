package serve_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_Profiles_RendersProfileList is /profiles's headline RED test
// (docs/plans/2026-08-30-serve-impl.md Task 4): the page must render every
// profile the game has, the default marker, the CSRF token, and the
// switch/apply/deploy form's target routes Task 9 (#322) will wire.
func TestServer_Profiles_RendersProfileList(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "hardcore")
	require.NoError(t, err)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "hardcore")
	assert.Contains(t, body, `action="/profiles/default/switch"`)
	assert.Contains(t, body, `action="/profiles/default/apply"`)
	assert.Contains(t, body, `action="/profiles/default/deploy"`)
	assert.Contains(t, body, "coming in this release")
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_Profiles_NoGames_RendersEmptyState covers the CSS/JS-absent,
// no-data case.
func TestServer_Profiles_NoGames_RendersEmptyState(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No games configured")
}
