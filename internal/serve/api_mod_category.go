// api_mod_category.go answers the web UI's mod-category controls (#592):
// POST /api/v1/mods/{source}/{id}/category sets or clears a mod's user
// category (Service.SetModCategory), and GET /api/v1/categories lists the
// effective categories the mod panel's picker suggests
// (Service.ListCategories). The write is a single beginOp-gated profile
// write with nothing to preview, so it follows api_mod_settings.go's
// lock/unlock/update-policy routes exactly - no job, CSRF-guarded through
// s.wrap, answering with the core.ModSettingResult document its siblings
// answer with.
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// modCategoryRequest is POST /api/v1/mods/{source}/{id}/category's request
// body. An empty Category (or one of only whitespace) clears the user
// category, so the mod falls back to its source's category name, if any.
type modCategoryRequest struct {
	Category string `json:"category"`
}

// categoriesDocument is GET /api/v1/categories's response: the distinct
// effective categories of the resolved profile's installed mods, in the
// order core.GroupModListByCategory sorts its groups. Never null - an empty
// profile answers [].
type categoriesDocument struct {
	Categories []string `json:"categories"`
}

// handleAPIModCategory answers POST /api/v1/mods/{source}/{id}/category.
// A name core refuses (core.ErrInvalidCategory: too long, control
// characters, invalid UTF-8) is the caller's bad input - 400 - and a mod
// not installed in the profile is 404, as for its lock/policy siblings.
func (s *Server) handleAPIModCategory(w http.ResponseWriter, r *http.Request) {
	var req modCategoryRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	sourceID, modID := r.PathValue("source"), r.PathValue("id")
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	result, err := s.svc.SetModCategory(r.Context(), sourceID, modID, sel.Game.ID, sel.Profile, req.Category)
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, core.ErrInvalidCategory) {
			status = s.modSettingErrorStatus(r.Context(), sourceID, modID, sel, err)
		}
		s.writeAPIError(w, status, err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPICategories answers GET /api/v1/categories.
func (s *Server) handleAPICategories(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	names, err := s.svc.ListCategories(r.Context(), sel.Game, sel.Profile)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	s.writeJSON(w, http.StatusOK, categoriesDocument{Categories: names})
}
