package serve

import "net/http"

// routes registers every page and asset route. Pages go through wrap (the
// full security/observability middleware chain); static assets only need
// securityHeaders since they are GET-only and carry no user data.
func (s *Server) routes() {
	s.mux.Handle("GET /{$}", s.wrap(s.handleStatus))
	s.mux.Handle("/static/", http.StripPrefix("/static/", securityHeaders(staticHandler())))
}
