package serve

// Internal (package serve) tests for the security middleware chain -
// docs/plans/2026-08-30-serve-design.md §Security: Host allow-list, Origin
// check on non-GET, and the CSRF token on every state-changing request.
// These register a throwaway echo route directly on the unexported mux
// (rather than adding a test-only exported method to Server) so the
// production API surface stays exactly what the design calls for.

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const middlewareTestAddr = "127.0.0.1:7420"

// newMiddlewareTestServer builds a Server over a throwaway Service (no
// fixture game needed - these tests never reach a page handler) with one
// extra echo route wired through the same security middleware every real
// route uses.
func newMiddlewareTestServer(t *testing.T) *Server {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	srv := New(svc, slog.New(slog.DiscardHandler), Options{Addr: middlewareTestAddr})
	srv.mux.Handle("/__test/echo", srv.wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return srv
}

func TestMiddleware_CrossOriginPOST_403(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "http://"+middlewareTestAddr+"/__test/echo", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMiddleware_SameOriginPOSTWithoutCSRFToken_403(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "http://"+middlewareTestAddr+"/__test/echo", nil)
	req.Header.Set("Origin", "http://"+middlewareTestAddr)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMiddleware_POSTWithValidCSRFHeader_Passes(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "http://"+middlewareTestAddr+"/__test/echo", nil)
	req.Header.Set("X-CSRF-Token", srv.csrf.token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_POSTWithValidCSRFFormField_Passes(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	form := "csrf_token=" + srv.csrf.token
	req := httptest.NewRequest(http.MethodPost, "http://"+middlewareTestAddr+"/__test/echo", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_POSTWithInvalidCSRFToken_403(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "http://"+middlewareTestAddr+"/__test/echo", nil)
	req.Header.Set("X-CSRF-Token", "not-the-right-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMiddleware_GETNeedsNoCSRFToken(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/echo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestAllowedHostsFor covers allowedHostsFor's concrete-vs-wildcard split
// (task-3 review Important 1): a concrete bind (any specific IP or name)
// yields a single-entry allow-list; a wildcard bind - including the exact
// "[::]:44957"-shaped string a real ephemeral wildcard Listen resolves to
// - yields nil, hostCheck's "accept any Host" signal.
func TestAllowedHostsFor(t *testing.T) {
	concrete := []struct {
		name     string
		hostPort string
	}{
		{"loopback IPv4", "127.0.0.1:7420"},
		{"loopback IPv6", "[::1]:7420"},
		{"private LAN IP", "192.168.1.5:7420"},
		{"hostname", "example.com:7420"},
	}
	for _, tt := range concrete {
		t.Run(tt.name, func(t *testing.T) {
			got := allowedHostsFor(tt.hostPort)
			_, ok := got[tt.hostPort]
			assert.True(t, ok, "expected %q in allow-list, got %v", tt.hostPort, got)
			assert.Len(t, got, 1)
		})
	}

	wildcards := []string{"0.0.0.0:7420", "[::]:7420", ":7420", "0.0.0.0:0", "[::]:44957"}
	for _, addr := range wildcards {
		t.Run(addr, func(t *testing.T) {
			assert.Nil(t, allowedHostsFor(addr))
		})
	}
}

// TestMiddleware_HostCheck_ExactMatchOnly covers the DNS-rebinding guard at
// the unit level (server_test.go covers it end to end through the status
// page): only the exact configured host passes.
func TestMiddleware_HostCheck_ExactMatchOnly(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	ok := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/echo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, ok)
	require.Equal(t, http.StatusOK, rec.Code)

	bad := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/echo", nil)
	bad.Host = "attacker.example"
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, bad)
	require.Equal(t, http.StatusForbidden, rec2.Code)
}
