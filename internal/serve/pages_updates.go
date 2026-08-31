package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// updatesPageData is "/updates"'s template data. Updates is empty both when
// the selection isn't ready (docs/plans/2026-08-30-serve-impl.md Task 4
// ruling on game/profile selection) and when a ready selection simply has
// nothing to update - the template renders the same "nothing here" message
// either way, so the two cases don't need to be told apart.
type updatesPageData struct {
	pageChrome
	Updates []domain.Update
}

// handleUpdates renders "/updates": the profile's available updates, one
// checkbox per row (#74), via CheckGameUpdates
// (docs/plans/2026-08-30-serve-design.md §HTTP surface). The single "Apply
// selected" form targets POST /updates/apply, the batch route Task 9
// (#322) wires; it renders disabled until then.
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := updatesPageData{pageChrome: s.chrome(r, "Updates", &sel)}
	if sel.ready() {
		ctx := r.Context()
		installed, err := s.svc.GetInstalledMods(ctx, sel.Game.ID, sel.Profile)
		if err != nil {
			s.renderError(w, err)
			return
		}
		updates, err := s.svc.CheckGameUpdates(ctx, sel.Game, sel.Profile, installed, nil)
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.Updates = updates
	}

	s.render(w, updatesTemplate, data)
}
