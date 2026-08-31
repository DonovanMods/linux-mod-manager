package serve

import "net/http"

// routes registers every page and asset route. Security headers and the
// Host allow-list apply to both routes and the mux's own 404/405 responses
// via the root Handler (see New); pages additionally go through wrap for
// the Origin/CSRF checks and request logging that state-changing methods
// need - static assets are GET-only and carry no user data, so they skip
// wrap entirely (task-3 review Minor 4).
func (s *Server) routes() {
	s.mux.Handle("GET /{$}", s.wrap(s.handleStatus))
	s.mux.Handle("GET /mods", s.wrap(s.handleMods))
	s.mux.Handle("GET /mods/{source}/{id}", s.wrap(s.handleModDetail))
	s.mux.Handle("GET /search", s.wrap(s.handleSearch))
	s.mux.Handle("GET /updates", s.wrap(s.handleUpdates))
	s.mux.Handle("/static/", http.StripPrefix("/static/", staticHandler()))
}
