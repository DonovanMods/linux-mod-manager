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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "hardcore")
	assert.Contains(t, body, `action="/profiles/default/switch"`)
	assert.Contains(t, body, `action="/profiles/default/apply"`)
	// Deploy carries no decorative {name} - the row scopes it with a hidden
	// "profile" field instead (gate review Minor 1).
	assert.Contains(t, body, `action="/deploy"`)
	assert.Contains(t, body, `name="profile" value="default"`)
	assert.NotContains(t, body, "coming in this release", "switch and apply went live with Task 9")
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_Profiles_MarksTheActiveProfile is M6 (epic live review):
// /profiles had a "Default" column but marked no ACTIVE profile - with no
// default explicitly set, every row read "no" even though one profile was
// genuinely the one in use (resolveSelection's own fallback, mirrored by
// the nav bar, which was the only place that said which). Requests the
// non-default "hardcore" profile explicitly and asserts ITS row - not
// "default"'s - is the one marked active.
func TestServer_Profiles_MarksTheActiveProfile(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "hardcore")
	require.NoError(t, err)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/profiles?profile=hardcore", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Regexp(t, `<tr[^>]*>\s*<td[^>]*>hardcore</td>\s*<td[^>]*>\d+</td>\s*<td[^>]*>(yes|no)</td>\s*<td[^>]*>(yes|no)</td>`, body)
	assert.Regexp(t, `hardcore</td>\s*<td[^>]*>\d+</td>\s*<td[^>]*>(yes|no)</td>\s*<td[^>]*>yes</td>`, body,
		"the requested (active) profile's row must be marked active")
	assert.Regexp(t, `>default</td>\s*<td[^>]*>\d+</td>\s*<td[^>]*>(yes|no)</td>\s*<td[^>]*>no</td>`, body,
		"a non-active profile's row must not be marked active")
}

// TestServer_Profiles_NoGames_RendersEmptyState covers the CSS/JS-absent,
// no-data case.
func TestServer_Profiles_NoGames_RendersEmptyState(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No games configured")
}
