// api_profiles.go answers the profiles modal and the reorder modal
// (docs/plans/2026-08-31-serve-spa-design.md §Modals: "reorder
// (drag-and-drop load order with live conflict preview) ... profiles
// (list/create/rename/delete/export/import/set-default)") - the write half
// of a surface whose read half is api.go's GET /api/v1/profiles.
//
// Every route here is one of the SANCTIONED single-step mutations: a
// reorder, a create, a delete, a set-default and a rename each write once
// and have nothing to preview, exactly like EnableMod/DisableMod and the
// lock/policy writes (api_mod_settings.go's own doc comment). They are
// therefore not jobs and not plans - they answer synchronously with the
// core.ProfileResult document the equivalent `lmm profile ...  --json`
// command emits, so a caller never needs a follow-up read to see what
// changed. Profile IMPORT is the one profile mutation that is a real
// Plan/Apply pair, because it has a genuine preview (what would be
// installed, what is missing) - it lives in kind_profile_import.go with
// every other plan kind.
//
// The profile these routes act on is named in the PATH, not the ?profile=
// query param: "rename the profile I clicked" must not silently retarget
// the active one, and a modal that manages profiles routinely acts on a
// profile that is not the selected one. Only the GAME half of the
// selection is resolved (resolveGameAPISelection), for the same reason
// GET /api/v1/profiles resolves only that half.
package serve

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// profileReorderRequest is POST /api/v1/profiles/{name}/reorder's request
// body: the new load order as mod identifiers, lowest priority first - the
// same identifiers `lmm profile reorder` takes ("source:modid", or a bare
// mod id when it is unambiguous within the profile), resolved by the same
// core.Service.ResolveReorder. Ids need not name every mod in the profile:
// whatever is left unmentioned keeps its existing relative order after the
// mentioned ones, which is ResolveReorder's own documented rule.
type profileReorderRequest struct {
	IDs []string `json:"ids"`
}

// validate implements validatingOptions. It refuses an empty list (a
// reorder that says nothing is a request that cannot mean anything) and,
// unlike ResolveReorder itself - which silently keeps a repeated id's first
// occurrence, the tolerant behaviour `lmm profile reorder`'s argv has always
// had - it refuses a DUPLICATE. A drag-and-drop list that hands the same row
// to the server twice is a frontend bug, and silently collapsing it would
// persist an order the user never saw.
func (r *profileReorderRequest) validate() error {
	if len(r.IDs) == 0 {
		return errors.New(`"ids" is required`)
	}
	seen := make(map[string]bool, len(r.IDs))
	for _, id := range r.IDs {
		if id == "" {
			return errors.New(`"ids" must not contain an empty id`)
		}
		if seen[id] {
			return fmt.Errorf("duplicate mod id %q", id)
		}
		seen[id] = true
	}
	return nil
}

// handleAPIProfileReorder answers POST /api/v1/profiles/{name}/reorder with
// the core.ProfileResult document `lmm profile reorder --json` emits: the
// profile RE-READ after the write, so the document reports what was
// persisted rather than what was requested (cmd/lmm/profile.go's own rule).
func (s *Server) handleAPIProfileReorder(w http.ResponseWriter, r *http.Request) {
	var req profileReorderRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	ctx := r.Context()
	refs, err := s.svc.ResolveReorder(ctx, sel.Game, name, req.IDs)
	if err != nil {
		s.writeAPIError(w, s.reorderErrorStatus(err), err)
		return
	}
	if err := s.svc.ReorderProfileMods(ctx, sel.Game.ID, name, refs); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}

	profile, err := s.svc.NewProfileManager().Get(ctx, sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.ProfileResult{Profile: *profile})
}

// reorderErrorStatus classifies a ResolveReorder failure. An id naming a
// mod the profile does not hold, or one ambiguous across sources, is bad
// input from the caller (400), as is a name no profile file could ever
// carry; a profile that does not exist answers 404, the same not-found
// treatment every other name-in-the-path route gives; anything else is a
// genuine read failure (500).
func (s *Server) reorderErrorStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrModNotInProfile), errors.Is(err, core.ErrAmbiguousModID),
		errors.Is(err, domain.ErrInvalidProfileName):
		return http.StatusBadRequest
	case errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
