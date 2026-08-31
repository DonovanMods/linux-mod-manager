package serve_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAddr = "127.0.0.1:7420"

// freeAddr returns a "127.0.0.1:<port>" address that was free at the moment
// of the call, for a test that needs a real, known port up front (an
// ephemeral ":0" bind only reveals its port after Listen, which the
// ListenAndServe composition under test does not expose).
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func newTestServer(t *testing.T) (*serve.Server, http.Handler) {
	t.Helper()
	svc := newFixtureService(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

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
// reaches any route handler - still carries them. It asserts
// Content-Security-Policy specifically (not X-Content-Type-Options,
// which net/http's own http.Error already sets on every response
// regardless of any middleware): a build that dropped securityHeaders
// from the root handler entirely would still pass the old assertion
// (task-3 re-review New finding 1).
func TestServer_NotFound_HasSecurityHeaders(t *testing.T) {
	_, handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/nope", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
}

// TestServer_ListenAndServe_BindsAndDrainsOnCancel proves task-3 review
// Minor 5's fix: nothing else calls ListenAndServe (cmd/lmm needs the
// resolved address to print its startup URL, so it calls Listen and Serve
// separately), so this is the only place its Listen+Serve composition is
// exercised. It drives a real request over the bound port before cancelling
// and, after ListenAndServe returns, re-binds the same address itself: a
// stub that returned nil without ever listening or serving anything (task-3
// re-review New finding 1) would fail both the request and the re-bind.
func TestServer_ListenAndServe_BindsAndDrainsOnCancel(t *testing.T) {
	svc := newFixtureService(t)
	addr := freeAddr(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: addr})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()

	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 10*time.Millisecond, "server never answered a real request on the bound address")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after ctx cancellation")
	}

	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err, "port still bound after ListenAndServe returned")
	require.NoError(t, ln.Close())
}

// TestServer_Close_ClosesListener proves task-3 review Minor 7's fix: the
// listener bound by Listen is released even if the caller never reaches
// Serve (e.g. doServe's startup-print failure path). It re-binds the same
// address afterward rather than just asserting Close returned nil: a
// stubbed Close that did nothing (task-3 re-review New finding 1) would
// leave the port held and fail the re-bind with "address already in use".
func TestServer_Close_ClosesListener(t *testing.T) {
	svc := newFixtureService(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: "127.0.0.1:0"})

	addr, err := srv.Listen()
	require.NoError(t, err)
	require.NoError(t, srv.Close())

	ln, err := net.Listen("tcp", addr.String())
	require.NoError(t, err, "port still held after Close")
	require.NoError(t, ln.Close())
}

// TestServer_Close_NoopWhenNeverListened proves Close is safe to call
// unconditionally, as doServe's deferred call does, even when Listen was
// never invoked.
func TestServer_Close_NoopWhenNeverListened(t *testing.T) {
	svc := newFixtureService(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: "127.0.0.1:0"})

	require.NoError(t, srv.Close())
}

// TestServer_RejectedAndUnroutedRequests_AreLogged proves task-3 re-review
// New finding 2's fix: a Host rejected by hostCheck and a path the mux
// itself answers with 404 must still produce a Debug log line - a
// regression the Minor-4 hoist introduced by moving hostCheck above
// requestLogging without anything replacing the coverage that lost.
func TestServer_RejectedAndUnroutedRequests_AreLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := newFixtureService(t)
	srv := serve.New(t.Context(), svc, logger, serve.Options{Addr: testAddr})
	handler := srv.Handler()

	wrongHost := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	wrongHost.Host = "evil.example:7420"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, wrongHost)
	require.Equal(t, http.StatusForbidden, rec.Code)

	unrouted := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/nope", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, unrouted)
	require.Equal(t, http.StatusNotFound, rec2.Code)

	logged := buf.String()
	assert.Contains(t, logged, "status=403")
	assert.Contains(t, logged, "status=404")
	assert.Contains(t, logged, "path=/nope")
}

// TestServer_OrdinaryRoute_LoggedOnce proves rootLogging's marker skip: a
// normal request, which requestLogging inside wrap already logs, must not
// also be logged by rootLogging - otherwise every ordinary page view would
// produce two log lines instead of one.
func TestServer_OrdinaryRoute_LoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := newFixtureService(t)
	srv := serve.New(t.Context(), svc, logger, serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, 1, strings.Count(buf.String(), "http request"))
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
