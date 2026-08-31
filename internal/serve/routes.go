package serve

import "net/http"

// routes registers every page, /api/v1, and asset route. Security headers
// and the Host allow-list apply to both routes and the mux's own 404/405
// responses via the root Handler (see New); pages and /api/v1 additionally
// go through wrap for the Origin/CSRF checks and request logging that
// state-changing methods need - every /api/v1 route here is GET-only, so
// wrap's Origin/CSRF checks are no-ops for them today, but registering
// through it keeps every route's request logging consistent and costs
// nothing. Static assets are GET-only and carry no user data, so they skip
// wrap entirely (task-3 review Minor 4).
func (s *Server) routes() {
	s.mux.Handle("GET /{$}", s.wrap(s.handleStatus))
	s.mux.Handle("GET /mods", s.wrap(s.handleMods))
	s.mux.Handle("GET /mods/{source}/{id}", s.wrap(s.handleModDetail))
	s.mux.Handle("GET /search", s.wrap(s.handleSearch))
	s.mux.Handle("GET /updates", s.wrap(s.handleUpdates))
	s.mux.Handle("GET /profiles", s.wrap(s.handleProfiles))
	s.mux.Handle("GET /health", s.wrap(s.handleHealth))
	s.mux.Handle("GET /jobs/{id}", s.wrap(s.handleJobPage))
	// Task 8's single-mod mutations (mutations.go). Each is POST-only: the
	// route IS the action, and the confirm page a plan renders posts back to
	// the very same URL rather than to a second "confirm" endpoint.
	s.mux.Handle("POST /mods/{source}/{id}/enable", s.wrap(s.handleModEnable))
	s.mux.Handle("POST /mods/{source}/{id}/disable", s.wrap(s.handleModDisable))
	s.mux.Handle("POST /mods/{source}/{id}/install", s.wrap(s.handleModInstall))
	s.mux.Handle("POST /mods/{source}/{id}/uninstall", s.wrap(s.handleModUninstall))
	// Task 9's batch, profile and health mutations. Same shape, different
	// targets: the selection (updates), the path's {name} (profiles), or the
	// resolved game+profile itself (health).
	s.mux.Handle("POST /updates/apply", s.wrap(s.handleUpdatesApply))
	s.mux.Handle("POST /profiles/{name}/switch", s.wrap(s.handleProfileSwitch))
	s.mux.Handle("POST /profiles/{name}/apply", s.wrap(s.handleProfileApply))
	s.mux.Handle("GET /api/v1/status", s.wrap(s.handleAPIStatus))
	s.mux.Handle("GET /api/v1/mods", s.wrap(s.handleAPIMods))
	s.mux.Handle("GET /api/v1/mods/{source}/{id}", s.wrap(s.handleAPIModDetail))
	s.mux.Handle("GET /api/v1/search", s.wrap(s.handleAPISearch))
	s.mux.Handle("GET /api/v1/updates", s.wrap(s.handleAPIUpdates))
	s.mux.Handle("GET /api/v1/profiles", s.wrap(s.handleAPIProfiles))
	s.mux.Handle("GET /api/v1/health", s.wrap(s.handleAPIHealth))
	s.mux.Handle("GET /api/v1/conflicts", s.wrap(s.handleAPIConflicts))
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
	s.mux.Handle("/static/", http.StripPrefix("/static/", staticHandler()))
}
