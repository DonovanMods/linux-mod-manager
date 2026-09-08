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

// profileNameRequest is the one-member body two routes share: POST
// /api/v1/profiles (the name of the profile to create) and POST
// /api/v1/profiles/{name}/rename (the name to rename it TO). They are one
// type rather than two identical ones because they are one wire shape - a
// profile name and nothing else - and a second copy would pin the same
// bytes twice.
type profileNameRequest struct {
	Name string `json:"name"`
}

// validate implements validatingOptions. Only emptiness is checked here;
// what makes a name USABLE (no path separators, no traversal) is
// config.SaveProfile's own validation, and letting it answer keeps one
// definition of a legal profile name instead of a second, drifting copy in
// the HTTP layer.
func (r *profileNameRequest) validate() error {
	if r.Name == "" {
		return errors.New(`"name" is required`)
	}
	return nil
}

// decodeProfileName decodes and validates a profileNameRequest, writing the
// 400 envelope itself on failure.
func (s *Server) decodeProfileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req profileNameRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return "", false
	}
	if err := req.validate(); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return "", false
	}
	return req.Name, true
}

// handleAPIProfileCreate answers POST /api/v1/profiles with the
// core.ProfileResult document `lmm profile create --json` emits - the
// profile as created, which is empty apart from its identity.
//
// A name another profile already uses answers 409: it is neither bad input
// (the request was well formed and the name is legal) nor a missing thing,
// but a collision with state the caller could not have known about. The
// check is explicit rather than inferred from Create's untyped "profile
// already exists" error, so the status never depends on error wording.
func (s *Server) handleAPIProfileCreate(w http.ResponseWriter, r *http.Request) {
	name, ok := s.decodeProfileName(w, r)
	if !ok {
		return
	}
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	pm := s.svc.NewProfileManager()
	if _, err := pm.Get(ctx, sel.Game.ID, name); err == nil {
		s.writeAPIError(w, http.StatusConflict, fmt.Errorf("profile already exists: %s", name))
		return
	}

	profile, err := pm.Create(ctx, sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.ProfileResult{Profile: *profile})
}

// handleAPIProfileDelete answers DELETE /api/v1/profiles/{name} with the
// core.ProfileResult document `lmm profile delete --json` emits: the
// profile as it stood immediately BEFORE the delete, since there is nothing
// left to report afterwards (core.ProfileResult's own doc comment). Like
// the CLI, an unreadable-but-present profile still deletes and the document
// then names it and nothing else - but unlike the CLI, a profile that is
// not there at all is a 404 rather than a plain error, because a
// name-in-the-path route has a status for exactly that.
func (s *Server) handleAPIProfileDelete(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	ctx := r.Context()
	pm := s.svc.NewProfileManager()
	deleted := domain.Profile{Name: name, GameID: sel.Game.ID}
	if p, err := pm.Get(ctx, sel.Game.ID, name); err == nil {
		deleted = *p
	}

	if err := pm.Delete(ctx, sel.Game.ID, name); err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.ProfileResult{Profile: deleted})
}

// handleAPIProfileSetDefault answers POST /api/v1/profiles/{name}/set-default
// with the ProfileResult of the profile that is now the default - re-read
// after the write, so the document carries the is_default flag as
// persisted rather than as requested. SetDefault clears the flag on every
// other profile itself.
func (s *Server) handleAPIProfileSetDefault(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	ctx := r.Context()
	pm := s.svc.NewProfileManager()
	if err := pm.SetDefault(ctx, sel.Game.ID, name); err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}

	profile, err := pm.Get(ctx, sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.ProfileResult{Profile: *profile})
}

// handleAPIProfileRename answers POST /api/v1/profiles/{name}/rename with
// the ProfileResult of the profile under its NEW name, via the gated
// core.Service.RenameProfile - which moves the profile file and every DB
// row keyed by profile as one completion chain (core/profile.go's Rename).
//
// An occupied target name is a 409, the same classification the create
// route gives the same collision.
func (s *Server) handleAPIProfileRename(w http.ResponseWriter, r *http.Request) {
	newName, ok := s.decodeProfileName(w, r)
	if !ok {
		return
	}
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	ctx := r.Context()
	if _, err := s.svc.NewProfileManager().Get(ctx, sel.Game.ID, newName); err == nil {
		s.writeAPIError(w, http.StatusConflict, fmt.Errorf("profile already exists: %s", newName))
		return
	}

	result, err := s.svc.RenameProfile(ctx, sel.Game.ID, name, newName)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIProfileExport answers GET /api/v1/profiles/{name}/export with
// the domain.ExportedProfile document `lmm profile export --json` emits -
// the portable form, with each mod ref's file ids backfilled from its
// install row, so an export can be re-imported anywhere.
//
// It is the one route in this file that sets Content-Disposition: the
// export exists to be SAVED, and naming the attachment "<profile>.json"
// here means the browser's own download does the naming rather than the
// SPA having to synthesize a blob and a filename for it. The name is
// quoted and the profile name itself is safe to interpolate: it came
// through config's own validation (no separators, no traversal) on the way
// in, and a profile that failed that validation cannot be read here at all.
func (s *Server) handleAPIProfileExport(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	exported, err := s.svc.ExportProfile(r.Context(), sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".json"))
	s.writeJSON(w, http.StatusOK, exported)
}

// profileErrorStatus classifies a profile CRUD failure: a profile that is
// not there answers 404, a name no profile file could carry answers 400,
// and anything else is a genuine I/O failure (500). "Already exists" never
// reaches here - both routes that can hit it check for it explicitly, so
// the 409 never depends on an untyped error's wording.
func (s *Server) profileErrorStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrInvalidProfileName), errors.Is(err, domain.ErrInvalidGameID):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
