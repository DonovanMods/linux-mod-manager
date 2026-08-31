package serve

import (
	"html/template"
	"net/http"
)

// render executes tmpl's "layout" template with data and writes it as the
// response body. Every page shares this one entry point so the
// Content-Type header and error handling stay in one place.
func (s *Server) render(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		s.log.Error("rendering template", "err", err)
	}
}

// renderError reports err as a plain-text 500. Task 3's routes have no
// mutation paths yet; the JSON {"error","details"} envelope
// (docs/plans/2026-08-30-serve-design.md §HTTP surface) lands with api.go.
func (s *Server) renderError(w http.ResponseWriter, err error) {
	s.log.Error("handling request", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
