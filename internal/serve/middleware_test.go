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

	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: middlewareTestAddr})
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

// TestMiddleware_SameOriginPOSTWithOriginHeader_Passes covers the
// originCheck accept branch (task-3 review Minor 6): the other passing-POST
// tests above omit the Origin header entirely, so a same-origin request
// that actually carries one was previously unexercised.
func TestMiddleware_SameOriginPOSTWithOriginHeader_Passes(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "http://"+middlewareTestAddr+"/__test/echo", nil)
	req.Header.Set("Origin", "http://"+middlewareTestAddr)
	req.Header.Set("X-CSRF-Token", srv.csrf.token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_GETNeedsNoCSRFToken(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/echo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestMiddleware_ResponseController_FlushWorksThroughWrap proves task-3
// review Important 2's fix: http.NewResponseController(w).Flush() must
// succeed for a handler registered through wrap, since Unit 4's SSE
// endpoint depends on this exact path.
func TestMiddleware_ResponseController_FlushWorksThroughWrap(t *testing.T) {
	srv := newMiddlewareTestServer(t)
	var flushErr error
	srv.mux.Handle("/__test/flush", srv.wrap(func(w http.ResponseWriter, _ *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	}))

	req := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/flush", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.NoError(t, flushErr)
}

// TestAllowedHostsFor covers allowedHostsFor's concrete-vs-wildcard split
// (task-3 review Important 1): a concrete bind (any specific IP or name)
// yields a single-entry allow-list; a wildcard bind - including the exact
// "[::]:44957"-shaped string a real ephemeral wildcard Listen resolves to
// - yields nil, hostCheck's "accept any Host" signal.
func TestAllowedHostsFor(t *testing.T) {
	concrete := []struct {
		name        string
		hostPort    string
		wantAliases []string // beyond hostPort itself
	}{
		{"loopback IPv4", "127.0.0.1:7420", []string{"localhost:7420"}},
		{"loopback IPv6", "[::1]:7420", []string{"localhost:7420"}},
		{"private LAN IP", "192.168.1.5:7420", nil},
		{"hostname", "example.com:7420", nil},
	}
	for _, tt := range concrete {
		t.Run(tt.name, func(t *testing.T) {
			got, wildcardPort := allowedHostsFor(tt.hostPort)
			_, ok := got[tt.hostPort]
			assert.True(t, ok, "expected %q in allow-list, got %v", tt.hostPort, got)
			for _, alias := range tt.wantAliases {
				_, ok := got[alias]
				assert.True(t, ok, "expected alias %q in allow-list, got %v", alias, got)
			}
			assert.Len(t, got, 1+len(tt.wantAliases))
			assert.Empty(t, wildcardPort, "a concrete bind's allow-list already pins the port")
		})
	}

	wildcards := []struct {
		addr     string
		wantPort string
	}{
		{"0.0.0.0:7420", "7420"},
		{"[::]:7420", "7420"},
		{":7420", "7420"},
		// A literal "0" isn't a port any real request could arrive on - it
		// means "the OS will pick one", normally overwritten by Listen's
		// own allowedHostsFor call once that happens - so it normalises to
		// "" (no port check) rather than the number 0.
		{"0.0.0.0:0", ""},
		{"[::]:44957", "44957"},
	}
	for _, tt := range wildcards {
		t.Run(tt.addr, func(t *testing.T) {
			hosts, wildcardPort := allowedHostsFor(tt.addr)
			assert.Nil(t, hosts)
			assert.Equal(t, tt.wantPort, wildcardPort)
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

// TestMiddleware_HostCheck_RejectsIPLiteralMismatch covers the same guard
// with IP-literal Host values (task-3 review Minor 6): the existing
// wrong-Host tests only ever use DNS names.
func TestMiddleware_HostCheck_RejectsIPLiteralMismatch(t *testing.T) {
	srv := newMiddlewareTestServer(t)

	for _, host := range []string{"192.168.1.5:7420", "127.0.0.1:9999"} {
		t.Run(host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/echo", nil)
			req.Host = host
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			require.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

// TestMiddleware_HostCheck_AcceptsLocalhostAliasOnLoopbackBind proves
// task-3 review Minor 8's fix: users habitually type localhost, and on a
// loopback bind (the default) that alias is now accepted alongside the
// exact bound address.
func TestMiddleware_HostCheck_AcceptsLocalhostAliasOnLoopbackBind(t *testing.T) {
	srv := newMiddlewareTestServer(t) // bound to middlewareTestAddr = "127.0.0.1:7420"

	req := httptest.NewRequest(http.MethodGet, "http://"+middlewareTestAddr+"/__test/echo", nil)
	req.Host = "localhost:7420"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestHostIsSafeForWildcardBind covers the IP-literal-or-localhost split a
// wildcard bind falls back to (a security-review finding on the Important
// 1 fix: comparing Origin to an unpinned r.Host is only safe once r.Host
// itself can't be a DNS-rebound name), plus the boundPort check (task-3
// re-review New finding 4): a Host that DOES name a port must name the
// bound one.
func TestHostIsSafeForWildcardBind(t *testing.T) {
	safe := []string{"127.0.0.1:7420", "[::1]:7420", "192.168.1.9:7420", "localhost:7420", "localhost"}
	for _, host := range safe {
		t.Run(host, func(t *testing.T) {
			assert.True(t, hostIsSafeForWildcardBind(host, "7420"))
		})
	}

	unsafeHosts := []string{"attacker.example:7420", "attacker.example", "lmm.local:7420"}
	for _, host := range unsafeHosts {
		t.Run(host, func(t *testing.T) {
			assert.False(t, hostIsSafeForWildcardBind(host, "7420"))
		})
	}
}

// TestHostIsSafeForWildcardBind_WrongPortRejected is the New finding 4
// fix's own test: an IP literal or "localhost" naming the WRONG port on a
// wildcard bind must still be rejected - only the DNS-name-vs-IP-literal
// split was checked before, so "10.10.10.110:9999" was wrongly admitted on
// a ":17421" bind. A Host with no port at all is unaffected (it never
// claimed to name the bound port).
func TestHostIsSafeForWildcardBind_WrongPortRejected(t *testing.T) {
	assert.False(t, hostIsSafeForWildcardBind("10.10.10.110:9999", "17421"))
	assert.False(t, hostIsSafeForWildcardBind("localhost:9999", "17421"))
	assert.True(t, hostIsSafeForWildcardBind("10.10.10.110:17421", "17421"))
	assert.True(t, hostIsSafeForWildcardBind("localhost", "17421"), "a portless Host never claimed to name the bound port")
}

// TestMiddleware_WildcardBind_RejectsDNSRebindingHost proves the fix for
// that finding directly: on a wildcard bind, a DNS-name Host (the shape a
// DNS-rebinding attack arrives with) is rejected even though hostCheck
// can't pin a single value, while a real client's IP-literal or localhost
// Host still gets through - and originCheck, which compares Origin against
// this same r.Host, can no longer be fooled into treating a rebound
// request as same-origin.
func TestMiddleware_WildcardBind_RejectsDNSRebindingHost(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: "0.0.0.0:7420"})
	srv.mux.Handle("/__test/echo", srv.wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rebind := httptest.NewRequest(http.MethodGet, "http://attacker.example:7420/__test/echo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, rebind)
	require.Equal(t, http.StatusForbidden, rec.Code)

	ipLiteral := httptest.NewRequest(http.MethodGet, "http://192.168.1.9:7420/__test/echo", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, ipLiteral)
	require.Equal(t, http.StatusOK, rec2.Code)
}

// TestMiddleware_WildcardBind_RejectsWrongPort is
// TestMiddleware_WildcardBind_RejectsDNSRebindingHost's sibling for task-3
// re-review New finding 4: an IP-literal Host naming a port other than the
// one this wildcard bind is listening on must still be rejected, not
// admitted just because the host half looks safe.
func TestMiddleware_WildcardBind_RejectsWrongPort(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: "0.0.0.0:7420"})
	srv.mux.Handle("/__test/echo", srv.wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	wrongPort := httptest.NewRequest(http.MethodGet, "http://192.168.1.9:9999/__test/echo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, wrongPort)
	require.Equal(t, http.StatusForbidden, rec.Code)

	rightPort := httptest.NewRequest(http.MethodGet, "http://192.168.1.9:7420/__test/echo", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, rightPort)
	require.Equal(t, http.StatusOK, rec2.Code)
}
