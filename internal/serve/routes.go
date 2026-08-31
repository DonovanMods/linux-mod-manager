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
	s.mux.Handle("GET /api/v1/status", s.wrap(s.handleAPIStatus))
	s.mux.Handle("GET /api/v1/mods", s.wrap(s.handleAPIMods))
	s.mux.Handle("GET /api/v1/mods/{source}/{id}", s.wrap(s.handleAPIModDetail))
	s.mux.Handle("GET /api/v1/search", s.wrap(s.handleAPISearch))
	s.mux.Handle("GET /api/v1/updates", s.wrap(s.handleAPIUpdates))
	s.mux.Handle("GET /api/v1/profiles", s.wrap(s.handleAPIProfiles))
	s.mux.Handle("GET /api/v1/health", s.wrap(s.handleAPIHealth))
	s.mux.Handle("GET /api/v1/conflicts", s.wrap(s.handleAPIConflicts))
	s.mux.Handle("/static/", http.StripPrefix("/static/", staticHandler()))
}
