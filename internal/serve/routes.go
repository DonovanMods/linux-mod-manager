package serve

import "net/http"

// routes registers every route on the mux: the SPA's own (spa.go - the
// shell, its assets, the legacy redirects) and /api/v1. Security headers
// and the Host allow-list apply to every response, routed or not, via the
// root Handler (see New). Most routes below additionally go through wrap
// for the Origin/CSRF checks and request logging that state-changing
// methods need - the two exceptions are /static/ and /vendor/ (spa.go's
// spaRoutes), which carry no user data and accept no state-changing
// method, so they skip wrap entirely (middleware.go's wrap doc comment).
func (s *Server) routes() {
	s.spaRoutes()

	s.mux.Handle("GET /api/v1/status", s.wrap(s.handleAPIStatus))
	s.mux.Handle("GET /api/v1/mods", s.wrap(s.handleAPIMods))
	s.mux.Handle("GET /api/v1/mods/{source}/{id}", s.wrap(s.handleAPIModDetail))
	s.mux.Handle("GET /api/v1/search", s.wrap(s.handleAPISearch))
	s.mux.Handle("GET /api/v1/updates", s.wrap(s.handleAPIUpdates))
	s.mux.Handle("GET /api/v1/profiles", s.wrap(s.handleAPIProfiles))
	s.mux.Handle("GET /api/v1/health", s.wrap(s.handleAPIHealth))
	s.mux.Handle("GET /api/v1/conflicts", s.wrap(s.handleAPIConflicts))
	// The two plan-free toggles (kind_toggle.go). They are registered as
	// two literal routes rather than one {action} wildcard so the table
	// stays closed: an unknown action reaches the /api/v1/ fallback's JSON
	// 404 instead of a handler that would have to refuse it itself.
	s.mux.Handle("POST /api/v1/mods/{source}/{id}/enable", s.wrap(s.handleAPIModEnable))
	s.mux.Handle("POST /api/v1/mods/{source}/{id}/disable", s.wrap(s.handleAPIModDisable))
	s.mux.Handle("POST /api/v1/plans/{kind}", s.wrap(s.handleAPIPlan))
	s.mux.Handle("POST /api/v1/jobs", s.wrap(s.handleAPIStartJob))
	s.mux.Handle("GET /api/v1/jobs/{id}", s.wrap(s.handleAPIJobStatus))
	s.mux.Handle("GET /api/v1/jobs/{id}/events", s.wrap(s.handleAPIJobEvents))
	// The /api/v1/ subtree fallback must be registered LAST in spirit
	// (net/http's most-specific-pattern-wins makes the order irrelevant in
	// fact): it claims every /api/v1 path no route above took, so no API
	// request can fall through to net/http's text/plain 404. It goes
	// through requestLogging only, not wrap - a request for a path that
	// does not exist has nothing to protect with an Origin or CSRF check,
	// and running them first would answer an unknown state-changing path
	// with a text/plain 403, reintroducing exactly the non-JSON response
	// this route exists to remove.
	s.mux.Handle("/api/v1/", s.requestLogging(http.HandlerFunc(s.handleAPINotFound)))
}
