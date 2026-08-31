// Package serve implements `lmm serve`: a local HTTP server rendering
// server-side HTML pages and a small /api/v1 JSON layer over a long-lived
// *core.Service, per docs/plans/2026-08-30-serve-design.md. It imports only
// internal/app, internal/core, internal/domain, and the standard library
// (enforced by boundary_test.go).
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// defaultShutdownGrace bounds how long Serve waits for in-flight requests to
// finish once its ctx is cancelled, when Options.ShutdownGrace is zero.
const defaultShutdownGrace = 10 * time.Second

// Options configures a Server.
type Options struct {
	// Addr is the "host:port" ListenAndServe binds to, e.g.
	// "127.0.0.1:7420". Until Listen resolves the actually-bound address, it
	// also seeds the Host allow-list hostCheck enforces (the DNS-rebinding
	// guard in docs/plans/2026-08-30-serve-design.md §Security): a concrete
	// bind pins that exact value, while a wildcard bind (0.0.0.0, [::], or a
	// bare ":port") has no single correct Host by definition, so hostCheck
	// accepts any Host it sees and leaves the Origin and CSRF checks as the
	// defense on an exposed bind (task-3 review Important 1; see
	// allowedHostsFor in middleware.go).
	Addr string

	// ShutdownGrace bounds how long a cancelled Serve waits for in-flight
	// requests to finish before returning. Zero uses defaultShutdownGrace.
	ShutdownGrace time.Duration
}

// Server is the lmm serve HTTP server: an *http.Server wired to a
// *core.Service, with the security middleware
// (docs/plans/2026-08-30-serve-design.md §Security) and every page/asset
// route already registered.
type Server struct {
	httpServer *http.Server
	svc        *core.Service
	log        *slog.Logger
	mux        *http.ServeMux
	// allowedHosts is the Host allow-list hostCheck enforces (see
	// allowedHostsFor in middleware.go). nil means a wildcard bind: any
	// Host is accepted.
	allowedHosts  map[string]struct{}
	shutdownGrace time.Duration
	csrf          *csrfGuard
	ln            net.Listener
}

// New builds a Server over svc. log receives request-level diagnostics at
// slog.LevelDebug; a nil log is treated as (*core.Service).Logger(), which
// is itself never nil (it defaults to a discard handler).
func New(svc *core.Service, log *slog.Logger, opts Options) *Server {
	if log == nil {
		log = svc.Logger()
	}
	grace := opts.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}

	s := &Server{
		svc:           svc,
		log:           log,
		mux:           http.NewServeMux(),
		allowedHosts:  allowedHostsFor(opts.Addr),
		shutdownGrace: grace,
		csrf:          newCSRFGuard(),
	}
	s.httpServer = &http.Server{
		Addr: opts.Addr,
		// Security headers and the Host allow-list apply to every request
		// the mux ever sees - including a 404/405 the mux generates itself
		// and /static/, neither of which goes through wrap (task-3 review
		// Minor 4). wrap adds the checks specific to the routes that need
		// them.
		Handler: securityHeaders(s.hostCheck(s.mux)),
	}
	s.routes()
	return s
}

// Handler returns the Server's root http.Handler, for tests that want to
// drive it with httptest without opening a real network listener.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Listen resolves Server's configured address into a real net.Listener and
// refines the Host allow-list to the address actually bound (needed for
// Options.Addr's "host:0" ephemeral-port form, used by tests). It must be
// called at most once.
func (s *Server) Listen() (net.Addr, error) {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", s.httpServer.Addr, err)
	}
	s.ln = ln
	s.allowedHosts = allowedHostsFor(ln.Addr().String())
	return ln.Addr(), nil
}

// Close closes the listener bound by Listen, if any, without starting a
// shutdown. It's for a caller that fails between Listen and Serve (e.g. a
// startup-print error) and needs to release the socket instead of leaking
// it (task-3 review Minor 7); it is a no-op if Listen was never called or
// Serve is already draining the listener itself.
func (s *Server) Close() error {
	if s.ln == nil {
		return nil
	}
	return s.ln.Close()
}

// Serve runs the server until ctx is cancelled, then drains in-flight
// requests within a bounded grace period before returning (see
// serveGraceful). Listen must have been called first; ListenAndServe does
// both in the usual order.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("serve: Listen must be called before Serve")
	}
	return serveGraceful(ctx, s.httpServer, s.ln, s.shutdownGrace)
}

// ListenAndServe binds Server's configured address and serves until ctx is
// cancelled, then gracefully drains in-flight requests (running jobs get a
// bounded grace - docs/plans/2026-08-30-serve-impl.md Task 3).
func (s *Server) ListenAndServe(ctx context.Context) error {
	if _, err := s.Listen(); err != nil {
		return err
	}
	return s.Serve(ctx)
}

// serveGraceful runs srv.Serve(ln) until ctx is cancelled, then calls
// Shutdown with a fresh, bounded deadline so an in-flight request finishes
// instead of being cut off by ctx's own cancellation. It uses
// context.WithoutCancel rather than context.Background so the shutdown
// timeout still carries any ctx values, while the sanctioned
// context.Background() call count (see CLAUDE.md's v2 boundary rules) stays
// unchanged - the caller's ctx is the only root this package ever derives
// from.
func serveGraceful(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// IsLoopbackAddr reports whether addr's host is loopback-only
// (127.0.0.1, ::1, or the literal "localhost"). A wildcard host ("",
// "0.0.0.0", "::") or any other host/IP is NOT loopback and is the signal
// cmd/lmm's serve command uses to print its non-loopback warning
// (docs/plans/2026-08-30-serve-design.md §Security).
func IsLoopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("parsing addr %q: %w", addr, err)
	}
	if host == "" {
		return false, nil
	}
	if host == "localhost" {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, nil
	}
	return ip.IsLoopback(), nil
}
