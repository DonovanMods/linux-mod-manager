package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// profilesPageData is "/profiles"'s template data. Listing is nil whenever
// resolveSelection didn't resolve a game (docs/plans/2026-08-30-serve-impl.md
// Task 4 ruling on game/profile selection) - unlike the other game/profile-
// scoped pages, this one only needs the GAME half to resolve (it lists every
// profile a game has, active one or not), so it checks sel.Game rather than
// sel.ready().
type profilesPageData struct {
	pageChrome
	Listing *core.ProfileListing
}

// handleProfiles renders "/profiles": the game's profiles via ListProfiles,
// with disabled switch/apply/deploy form shells per row targeting the
// routes Task 9 (#322) wires (docs/plans/2026-08-30-serve-design.md §HTTP
// surface).
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := profilesPageData{pageChrome: s.chrome(r, "Profiles", &sel)}
	if sel.Game != nil {
		listing, err := s.svc.ListProfiles(r.Context(), sel.Game.ID)
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.Listing = listing
	}

	s.render(w, profilesTemplate, data)
}
