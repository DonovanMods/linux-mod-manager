package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// modsPageData is "/mods"'s template data. List is nil whenever
// resolveSelection didn't resolve a ready game+profile (docs/plans/2026-08-30-serve-impl.md
// Task 4 ruling on game/profile selection): the template renders the
// friendly empty state in that case instead of an empty table.
type modsPageData struct {
	pageChrome
	List *core.ModList
}

// handleMods renders "/mods": the profile's installed mods
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "installed list +
// enable/disable/uninstall forms" via ListMods). The per-row mutation forms
// target the routes Task 8 (#322) wires (POST /mods/{source}/{id}/enable,
// /disable, /uninstall) but render disabled until then.
func (s *Server) handleMods(w http.ResponseWriter, r *http.Request) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := modsPageData{pageChrome: s.chrome(r, "Mods", &sel)}
	if sel.ready() {
		list, err := s.svc.ListMods(r.Context(), sel.Game, sel.Profile)
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.List = list
	}

	s.render(w, modsTemplate, data)
}
