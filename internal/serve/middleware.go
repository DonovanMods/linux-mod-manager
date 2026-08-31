package serve

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"time"
)

// csrfFormField and csrfHeaderName are the two places a caller may attach
// the CSRF token (docs/plans/2026-08-30-serve-design.md §Security): a
// hidden field on every rendered form, or a header for the enhancement JS
// and API scripts.
const (
	csrfFormField  = "csrf_token"
	csrfHeaderName = "X-CSRF-Token"
)

// csrfGuard issues and verifies the server's single per-process CSRF token
// (a synchronizer token, not a session cookie: a page rendered by this
// process is the only way to ever see the value, so a cross-origin form
// submission - blocked already by the Origin check - could never have
// learned it either).
type csrfGuard struct {
	token string
}

// newCSRFGuard generates a fresh random token via crypto/rand (GO.md:
// security-sensitive randomness must not use math/rand), unique to this
// server process's lifetime.
func newCSRFGuard() *csrfGuard {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read failing means the OS entropy source is broken -
		// not a recoverable request-time condition, so this fails the same
		// way template.Must does for a broken embedded template.
		panic(fmt.Errorf("serve: generating CSRF token: %w", err))
	}
	return &csrfGuard{token: hex.EncodeToString(b)}
}

// valid reports whether got matches the guard's token, in constant time so
// timing can't leak the token byte-by-byte.
func (g *csrfGuard) valid(got string) bool {
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(g.token), []byte(got)) == 1
}

// tokenFrom extracts a submitted CSRF token from r: the header first, then
// the parsed form's csrf_token field. r.ParseForm is safe to call
// repeatedly and safe on a GET (it just parses the URL query in that case).
func tokenFrom(r *http.Request) string {
	if h := r.Header.Get(csrfHeaderName); h != "" {
		return h
	}
	_ = r.ParseForm()
	return r.PostForm.Get(csrfFormField)
}

// unsafeMethod reports whether m is a state-changing HTTP method - the
// Origin and CSRF checks apply only to these (docs/plans/2026-08-30-serve-design.md
// §Security: "Origin check on non-GET").
func unsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// wrap applies request logging and - for state-changing methods - the
// Origin and CSRF checks to fn. Security headers and the Host allow-list
// (DNS-rebinding guard) are installed once at the mux root (see New) so
// they cover every response including the mux's own 404/405 and
// /static/, which don't go through wrap (task-3 review Minor 4: neither
// carries user data or accepts state-changing methods, so they don't need
// request logging or the Origin/CSRF checks).
func (s *Server) wrap(fn http.HandlerFunc) http.Handler {
	var h http.Handler = fn
	h = s.csrfCheck(h)
	h = s.originCheck(h)
	h = s.requestLogging(h)
	return h
}

// securityHeaders sets conservative response headers
// (docs/plans/2026-08-30-serve-design.md §Security: "assets served with
// conservative headers") on every response, page or asset alike.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// requestLogging logs each request at Debug once it completes: method,
// path, status, and duration.
func (s *Server) requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

// statusRecorder captures the status code a handler wrote, since
// http.ResponseWriter has no getter of its own.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records code before delegating, so a handler that never calls
// it explicitly (relying on the implicit 200 from the first Write) still
// reports the default set in the recorder's zero value.
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the underlying ResponseWriter to http.ResponseController,
// so handlers behind this middleware can still Flush (SSE) and set
// deadlines - without it, http.NewResponseController(w).Flush() fails with
// "feature not supported" for every handler wrap wraps, which would look
// like an sse.go bug rather than a middleware one once Unit 4 adds SSE
// (task-3 review Important 2).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// allowedHostsFor derives the Host allow-list hostCheck enforces from a
// "host:port" string - either Options.Addr as given, or the address Listen
// actually bound. A concrete bind (a specific IP or name) is pinned to
// that exact value. A wildcard bind (0.0.0.0, [::], or an empty host as in
// ":7420") returns nil: a wildcard has no single correct Host by
// definition (measured directly - a wildcard Listen resolves to an
// address like "[::]:44957" that no real client ever sends as its Host),
// so hostCheck must accept any Host it sees there instead of rejecting
// every request (task-3 review Important 1).
func allowedHostsFor(hostPort string) map[string]struct{} {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return map[string]struct{}{hostPort: {}}
	}
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return nil
	}
	return map[string]struct{}{hostPort: {}}
}

// hostIsSafeForWildcardBind reports whether host (a Host header, with or
// without a port) is safe to admit when allowedHosts can't pin a single
// value: an IP literal, or the special name "localhost". DNS rebinding
// depends on a DNS name whose resolution changes after the browser has
// already loaded a page under that name - an IP literal can't be "rebound"
// that way, and "localhost" is resolved by the OS/browser stack itself,
// not by attacker-controlled DNS, so both remain safe to compare Origin
// against even without a pinned Host (see hostCheck and originCheck).
func hostIsSafeForWildcardBind(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	return host == "localhost" || net.ParseIP(host) != nil
}

// hostCheck rejects any request whose Host header is not in the server's
// allow-list - the DNS-rebinding guard
// (docs/plans/2026-08-30-serve-design.md §Security). allowedHosts is nil
// only for a wildcard bind (0.0.0.0/[::]), which has no single correct
// Host to pin (task-3 review Important 1); rather than accept any Host
// unconditionally, it still rejects a DNS name that isn't "localhost" -
// otherwise a DNS-rebinding attack (the victim's browser loads a page from
// an attacker-controlled name, whose DNS record is then repointed at this
// server) would sail straight through, and since originCheck compares
// Origin against this same r.Host, it would appear same-origin too,
// defeating both checks at once. An IP literal or "localhost" can't be
// rebound this way (see hostIsSafeForWildcardBind), which is what real LAN
// traffic to a wildcard bind normally uses anyway.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.allowedHosts != nil {
			if _, ok := s.allowedHosts[r.Host]; !ok {
				http.Error(w, "host not allowed", http.StatusForbidden)
				return
			}
		} else if !hostIsSafeForWildcardBind(r.Host) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originCheck rejects a state-changing request whose Origin header names a
// different origin than the Host it arrived on. Comparing against r.Host
// rather than a value fixed at construction keeps this correct for a
// wildcard bind too, where hostCheck (which always runs first - see wrap)
// admits more than one Host - though only an IP literal or "localhost"
// there, which closes the DNS-rebinding gap a same-origin-by-header-
// comparison would otherwise reopen (see hostCheck). A request with no
// Origin header at all (curl, the API used from a script) is not rejected
// here - the CSRF check downstream still guards it.
func (s *Server) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unsafeMethod(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" {
				expected := "http://" + r.Host
				if origin != expected {
					http.Error(w, "cross-origin request rejected", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrfCheck rejects a state-changing request that does not carry a valid
// CSRF token (docs/plans/2026-08-30-serve-design.md §Security: "CSRF token
// on every form and state-changing API call").
func (s *Server) csrfCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unsafeMethod(r.Method) && !s.csrf.valid(tokenFrom(r)) {
			http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
