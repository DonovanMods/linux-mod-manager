package serve

import (
	"html/template"
	"net/http"
)

// render executes tmpl's "layout" template with data and writes it as the
// response body with a 200 status. Every page that has no reason to answer
// with a different status (almost all of them) uses this entry point.
func (s *Server) render(w http.ResponseWriter, tmpl *template.Template, data any) {
	s.renderStatus(w, http.StatusOK, tmpl, data)
}

// renderStatus is render with an explicit status code, for the pages that
// need one - e.g. a mod-detail lookup that found nothing answers 404 while
// still rendering a normal HTML page (WEBUI.md: no bare error text where a
// real page belongs).
func (s *Server) renderStatus(w http.ResponseWriter, status int, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
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
