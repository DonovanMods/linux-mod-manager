// Package serve implements `lmm serve`: a local HTTP server hosting a
// single-page application and the /api/v1 JSON + SSE layer it drives, over
// a long-lived *core.Service. The API, jobs and SSE surfaces are
// docs/plans/2026-08-30-serve-design.md's; the SPA that replaced that
// design's server-rendered page layer is
// docs/plans/2026-08-31-serve-spa-design.md. It imports only internal/app,
// internal/core, internal/domain, and the standard library (enforced by
// boundary_test.go).
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
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
	// bind pins that exact value (plus "localhost:<port>" when the bind is
	// loopback, since users habitually type localhost - task-3 review Minor
	// 8), while a wildcard bind (0.0.0.0, [::], or a bare ":port") has no
	// single correct Host by definition, so hostCheck accepts any IP-literal
	// or localhost Host it sees there and leaves the Origin and CSRF checks
	// as the rest of the defense on an exposed bind (task-3 review Important
	// 1; see allowedHostsFor in middleware.go).
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
	// IP-literal or "localhost" Host naming wildcardPort is accepted.
	allowedHosts map[string]struct{}
	// wildcardPort is the port a wildcard bind is listening on, used to
	// close the port-ignoring gap in hostIsSafeForWildcardBind (task-3
	// re-review New finding 4). Empty when allowedHosts is non-nil - a
	// concrete bind's allow-list already pins an exact "host:port".
	wildcardPort  string
	shutdownGrace time.Duration
	csrf          *csrfGuard
	ln            net.Listener

	// plans holds the server-side Plan objects a confirm page round-trips
	// a plan_id through (see plans.go); jobs runs each confirmed Apply in
	// its own goroutine, rooted at New's ctx rather than any request's
	// (see jobs.go). Both are process-lifetime state, forgotten on
	// restart - the database remains the truth about what actually
	// happened.
	plans *planStore
	jobs  *jobRegistry

	// heartbeat is the clock seam every SSE stream's comment heartbeat
	// runs on (see sse.go). Production is realHeartbeatTicker; an internal
	// test swaps in a channel it sends on by hand so a heartbeat assertion
	// never waits a real sseHeartbeatInterval.
	heartbeat heartbeatTicker

	// draining is closed once the http.Server begins shutting down. It
	// exists for the SSE streams: an open stream is an ACTIVE request, and
	// http.Server.Shutdown waits for active requests rather than
	// cancelling their contexts, so a stream that only ended when its job
	// ended would hold the entire shutdown grace open. Watching this makes
	// a draining server hang up on its streams immediately while the jobs
	// behind them keep running to their own bounded grace (jobs.go).
	draining     chan struct{}
	drainingOnce sync.Once
}

// New builds a Server over svc. log receives request-level diagnostics at
// slog.LevelDebug; a nil log is treated as (*core.Service).Logger(), which
// is itself never nil (it defaults to a discard handler).
//
// ctx is the SERVER's lifetime context - the serve command's own root - and
// is what every job's Apply ultimately derives from (see jobRegistry.
// rootCtx for why the registry, not this context's cancellation, decides
// when a running job is cut off). It is deliberately not the same
// parameter Serve takes: Serve's ctx says when to START shutting down,
// while this one is the root the work hangs off, and passing it here is
// what keeps this package's context.Background() call count at zero.
func New(ctx context.Context, svc *core.Service, log *slog.Logger, opts Options) *Server {
	if log == nil {
		log = svc.Logger()
	}
	grace := opts.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}

	allowedHosts, wildcardPort := allowedHostsFor(opts.Addr)
	s := &Server{
		svc:           svc,
		log:           log,
		mux:           http.NewServeMux(),
		allowedHosts:  allowedHosts,
		wildcardPort:  wildcardPort,
		shutdownGrace: grace,
		csrf:          newCSRFGuard(),
		plans:         newPlanStore(defaultPlanTTL, defaultPlanStoreCap, time.Now),
		jobs:          newJobRegistry(ctx, log, defaultJobRingSize, defaultJobRetention),
		heartbeat:     realHeartbeatTicker,
		draining:      make(chan struct{}),
	}
	s.httpServer = &http.Server{
		Addr: opts.Addr,
		// Security headers, request logging, and the Host allow-list apply
		// to every request the mux ever sees - including a 404/405 the mux
		// generates itself, which goes through no route at all (task-3
		// review Minor 4; the logging half is task-3 re-review New finding
		// 2). wrap adds the checks specific to the routes that need them.
		Handler: securityHeaders(s.rootLogging(s.hostCheck(s.mux))),
	}
	// RegisterOnShutdown fires when Shutdown is called - the only hook
	// net/http offers for "we are going away", since it never cancels an
	// active request's context. sync.Once because Shutdown may be called
	// more than once and each call runs the callbacks again.
	s.httpServer.RegisterOnShutdown(func() { s.drainingOnce.Do(func() { close(s.draining) }) })
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
	s.allowedHosts, s.wildcardPort = allowedHostsFor(ln.Addr().String())
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
// requests AND running jobs within a bounded grace period before returning
// (see serveGraceful). Listen must have been called first; ListenAndServe
// does both in the usual order.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("serve: Listen must be called before Serve")
	}
	return serveGraceful(ctx, s.httpServer, s.ln, s.shutdownGrace, s.jobs.shutdown)
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
//
// drain (nil in the direct-call tests, jobRegistry.shutdown in production)
// is the SAME bounded window applied to running jobs, and runs concurrently
// with Shutdown rather than after it: an Apply and the requests watching it
// are draining the same event, so serialising the two would double the
// worst-case exit time for no benefit. serveGraceful does not return until
// both have finished, so a cancelled `lmm serve` never exits out from under
// a mutation still in flight (docs/plans/2026-08-30-serve-impl.md Task 3:
// "running jobs get a bounded grace"). A listener that dies on its own gets
// the same drain before the failure is reported.
func serveGraceful(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration, drain func(context.Context)) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	var serveFailure error
	select {
	case err := <-serveErr:
		serveFailure = err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		if drain != nil {
			drain(shutdownCtx)
		}
	}()

	if serveFailure != nil {
		<-drained
		return serveFailure
	}

	shutdownErr := srv.Shutdown(shutdownCtx)
	<-drained
	if shutdownErr != nil {
		return fmt.Errorf("shutting down: %w", shutdownErr)
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
