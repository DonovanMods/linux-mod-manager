package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// searchPageData is "/search"'s template data. Report is nil until a
// selection is ready and a non-empty query has actually been run
// (docs/plans/2026-08-30-serve-impl.md Task 4 ruling on game/profile
// selection) - an empty query renders the bare search form rather than
// calling Search with nothing to look for.
type searchPageData struct {
	pageChrome
	Query  string
	Report *core.SearchReport
}

// handleSearch renders "/search?q=": the search form plus, once a query and
// a ready selection exist, results via Search with a disabled install form
// shell per hit targeting the route Task 8 (#322) wires
// (docs/plans/2026-08-30-serve-design.md §HTTP surface).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	query := r.URL.Query().Get("q")
	data := searchPageData{pageChrome: s.chrome(r, "Search", &sel), Query: query}
	if sel.ready() && query != "" {
		report, err := s.svc.Search(r.Context(), sel.Game, sel.Profile, query, core.SearchOptions{})
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.Report = report
	}

	s.render(w, searchTemplate, data)
}
