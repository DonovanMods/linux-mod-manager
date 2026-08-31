package serve_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAddr = "127.0.0.1:7420"

func newTestServer(t *testing.T) (*serve.Server, http.Handler) {
	t.Helper()
	svc := newFixtureService(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	return srv, srv.Handler()
}

// TestServer_StatusPage_RendersGameName is the skeleton's headline RED
// test (docs/plans/2026-08-30-serve-impl.md Task 3): `/` must render the
// seeded fixture game's name, using the Status/GameStatus core calls
// (docs/plans/2026-08-30-serve-design.md §HTTP surface).
func TestServer_StatusPage_RendersGameName(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Fixture Game")
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
}

// TestServer_StatusPage_WorksWithNoGames covers the CSS/JS-absent, no-data
// case: an empty StatusReport must still render a normal 200 page (WEBUI.md
// "semantic HTML first" - the dashboard is never blank/broken just because
// nothing is configured yet).
func TestServer_StatusPage_WorksWithNoGames(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No games configured")
}

// TestServer_WrongHost_403 covers the DNS-rebinding guard
// (docs/plans/2026-08-30-serve-design.md §Security): a request whose Host
// header does not match the server's bound address is rejected before it
// reaches any handler.
func TestServer_WrongHost_403(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	req.Host = "evil.example:7420"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestServer_RightHost_NotForbidden is TestServer_WrongHost_403's inverse
// sanity check: the exact configured host must still be let through.
func TestServer_RightHost_NotForbidden(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusForbidden, rec.Code)
}

// TestServer_StaticAsset_Served proves static/app.css is reachable over
// /static/, embedded via go:embed rather than read from disk at runtime.
func TestServer_StaticAsset_Served(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/static/app.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "css")
	assert.NotEmpty(t, rec.Body.String())
}

// TestServer_NotFound_Is404 is a baseline sanity check for the mux itself.
func TestServer_NotFound_Is404(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestServer_NotFound_HasSecurityHeaders proves task-3 review Minor 4's
// fix: security headers apply at the mux root, so a 404 - which never
// reaches any route handler - still carries them.
func TestServer_NotFound_HasSecurityHeaders(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

// TestServer_ListenAndServe_BindsAndDrainsOnCancel proves task-3 review
// Minor 5's fix: nothing else calls ListenAndServe (cmd/lmm needs the
// resolved address to print its startup URL, so it calls Listen and Serve
// separately), so this is the only place its Listen+Serve composition is
// exercised.
func TestServer_ListenAndServe_BindsAndDrainsOnCancel(t *testing.T) {
	svc := newFixtureService(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: "127.0.0.1:0"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after ctx cancellation")
	}
}

// TestServer_StaticAsset_WrongHost_403 proves the same fix for static
// assets: /static/ no longer bypasses the Host allow-list the way it did
// when only securityHeaders (not hostCheck) wrapped it.
func TestServer_StaticAsset_WrongHost_403(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/static/app.css", nil)
	req.Host = "evil.example:7420"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}
