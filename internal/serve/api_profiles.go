// api_profiles.go answers the profiles modal and the reorder modal
// (docs/plans/2026-08-31-serve-spa-design.md §Modals: "reorder
// (drag-and-drop load order with live conflict preview) ... profiles
// (list/create/rename/delete/export/import/set-default)") - the write half
// of a surface whose read half is api.go's GET /api/v1/profiles.
//
// Every route here is one of the SANCTIONED single-step mutations: a
// reorder, a create, a delete, a set-default, a rename and a duplicate
// (#438) each write once
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
	s.writeJSON(w, http.StatusOK, core.NewProfileResult(*profile))
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

// profileNameRequest is the one-member body three routes share: POST
// /api/v1/profiles (the name of the profile to create), POST
// /api/v1/profiles/{name}/rename (the name to rename it TO) and POST
// /api/v1/profiles/{name}/duplicate (the name of the copy, #438). They are
// one type rather than three identical ones because they are one wire
// shape - a profile name and nothing else - and a second copy would pin
// the same bytes twice.
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
// profile as created, which is empty apart from its identity, via the
// gated core.Service.CreateProfile so a create cannot interleave with a
// deploy or install racing to write the same game's state (#332 I1).
//
// A name another profile already uses answers 409: it is neither bad input
// (the request was well formed and the name is legal) nor a missing thing,
// but a collision with state the caller could not have known about. The
// collision is detected INSIDE the gate as the typed core.ErrProfileExists
// (#332 M6), so the status never depends on an untyped error's wording or
// on an un-gated pre-check that could itself race the write.
func (s *Server) handleAPIProfileCreate(w http.ResponseWriter, r *http.Request) {
	name, ok := s.decodeProfileName(w, r)
	if !ok {
		return
	}
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}

	result, err := s.svc.CreateProfile(r.Context(), sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIProfileDelete answers DELETE /api/v1/profiles/{name} with the
// core.ProfileResult document `lmm profile delete --json` emits: the
// profile as it stood immediately BEFORE the delete, since there is nothing
// left to report afterwards (core.ProfileResult's own doc comment). Like
// the CLI, an unreadable-but-present profile still deletes and the document
// then names it and nothing else - but unlike the CLI, a profile that is
// not there at all is a 404 rather than a plain error, because a
// name-in-the-path route has a status for exactly that.
//
// Goes through the gated core.Service.DeleteProfile (#332 I1): unlike the
// CLI, `lmm serve` runs Applies in background goroutines, so an un-gated
// delete could remove a profile's file while a deploy job for that very
// profile is mid-flight, leaving deployed_files rows for a profile that no
// longer exists.
func (s *Server) handleAPIProfileDelete(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	result, err := s.svc.DeleteProfile(r.Context(), sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIProfileSetDefault answers POST /api/v1/profiles/{name}/set-default
// with the ProfileResult of the profile that is now the default - re-read
// after the write, so the document carries the is_default flag as
// persisted rather than as requested. SetDefault clears the flag on every
// other profile itself. Goes through the gated core.Service.
// SetDefaultProfile so it cannot interleave with a deploy or install
// racing to write the same game's profiles (#332 I1).
func (s *Server) handleAPIProfileSetDefault(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	result, err := s.svc.SetDefaultProfile(r.Context(), sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIProfileRename answers POST /api/v1/profiles/{name}/rename with
// the ProfileResult of the profile under its NEW name, via the gated
// core.Service.RenameProfile - which moves the profile file and every DB
// row keyed by profile as one completion chain (core/profile.go's Rename).
//
// An occupied target name is a 409, the same classification the create
// route gives the same collision - detected INSIDE the gate as the typed
// core.ErrProfileExists (#332 M6), never by an un-gated pre-check that
// could itself race the write.
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

	result, err := s.svc.RenameProfile(r.Context(), sel.Game.ID, name, newName)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIProfileDuplicate answers POST /api/v1/profiles/{name}/duplicate
// with the ProfileResult of the NEW profile, via the gated
// core.Service.DuplicateProfile (#438) - which writes the copy's document
// and a twin of every installed row as one completion chain, none of them
// deployed (the game directory stays the active profile's).
//
// It is rename's twin in every way the wire can see: the same body, the
// same refusals - a name already taken is a 409, detected inside the gate
// as core.ErrProfileExists; an unknown source profile a 404; an unusable
// name a 400 - and the same document.
func (s *Server) handleAPIProfileDuplicate(w http.ResponseWriter, r *http.Request) {
	newName, ok := s.decodeProfileName(w, r)
	if !ok {
		return
	}
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	result, err := s.svc.DuplicateProfile(r.Context(), sel.Game.ID, name, newName)
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
//
// ?format=lmmc (#621) answers the profile as an lmm collection instead: the
// `.lmmc` container `lmm profile export -o` writes, as an attachment named
// "<profile>.lmmc" - what the SPA's Export downloads. A profile's first
// such export records its collection id in the profile (core.
// ExportCollection). The default answer is unchanged. Each bundle=<mod>
// (#623, repeatable: "source:id", a mod id or a name) carries that mod's
// files in the container as the author's own - `lmm profile export
// --bundle`, with its limits; GET .../bundle-candidates lists the mods it
// accepts. A mod from a source, or a name matching no mod, answers 400.
func (s *Server) handleAPIProfileExport(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	switch format := r.URL.Query().Get("format"); format {
	case "", "json":
	case "lmmc":
		s.writeCollectionExport(w, r, sel.Game.ID, name, r.URL.Query()["bundle"])
		return
	default:
		s.writeAPIError(w, http.StatusBadRequest, fmt.Errorf("unknown export format %q (json, lmmc)", format))
		return
	}

	exported, err := s.svc.ExportProfile(r.Context(), sel.Game.ID, name)
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".json"))
	s.writeJSON(w, http.StatusOK, exported)
}

// writeCollectionExport answers the profile as a `.lmmc` attachment. It
// never writes (#621 review S8): a GET is not CSRF-guarded, so a
// cross-origin page or a prefetcher could otherwise make it record an id,
// and a GET must not queue behind a running job. A profile whose collection
// identity is not recorded yet answers 409 naming the POST that records
// it (handleAPIProfileCollection), which the SPA's Export calls first.
func (s *Server) writeCollectionExport(w http.ResponseWriter, r *http.Request, gameID, name string, bundle []string) {
	export, err := s.svc.ExportCollection(r.Context(), gameID, name, core.CollectionExportOptions{NoRecord: true, Bundle: bundle})
	if errors.Is(err, core.ErrCollectionIdentityUnrecorded) {
		s.writeAPIError(w, http.StatusConflict, fmt.Errorf("%w: POST /api/v1/profiles/%s/collection records it, then export again", err, name))
		return
	}
	if core.IsBundleRefused(err) {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	data, err := export.Container()
	if core.IsBundleRefused(err) {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+core.CollectionContainerExt))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleAPIProfileBundleCandidates answers GET
// /api/v1/profiles/{name}/bundle-candidates (#623) with the
// []core.BundleCandidate an export can bundle: the profile's mods imported
// from a file, and those a collection already bundled (always carried).
// Read-only.
func (s *Server) handleAPIProfileBundleCandidates(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	candidates, err := s.svc.BundleCandidates(r.Context(), sel.Game.ID, r.PathValue("name"))
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, candidates)
}

// handleAPIProfileCollection answers POST
// /api/v1/profiles/{name}/collection (#621): it records the profile's
// collection identity - minting its collection id on the first call - and
// answers the domain.CollectionInfo it holds. It is the CSRF-guarded write
// the SPA's Export makes before its side-effect-free GET of the `.lmmc`.
//
// Its optional body names the mods an export will bundle (#623, the GET's
// bundle= values): the export is then checked as well - the choice and the
// files' size, without building the container - and a refusal answers 400,
// so the SPA shows it before the download instead of navigating to it.
func (s *Server) handleAPIProfileCollection(w http.ResponseWriter, r *http.Request) {
	var req profileCollectionRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	info, err := s.svc.RecordCollectionIdentity(r.Context(), sel.Game.ID, name, core.CollectionExportOptions{})
	if err != nil {
		s.writeAPIError(w, s.profileErrorStatus(err), err)
		return
	}
	if len(req.Bundle) > 0 {
		err := s.svc.CheckCollectionExport(r.Context(), sel.Game.ID, name, core.CollectionExportOptions{Bundle: req.Bundle})
		if core.IsBundleRefused(err) {
			s.writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		if err != nil {
			s.writeAPIError(w, s.profileErrorStatus(err), err)
			return
		}
	}
	s.writeJSON(w, http.StatusOK, info)
}

// profileCollectionRequest is POST /api/v1/profiles/{name}/collection's
// optional body (#623): the mods the export about to be downloaded bundles,
// as its bundle= values ("source:id", a mod id or a name).
type profileCollectionRequest struct {
	Bundle []string `json:"bundle,omitzero"`
}

// profileErrorStatus classifies a profile CRUD failure: a profile that is
// not there answers 404, a name already taken (core.ErrProfileExists,
// detected inside the gated Create/Rename seams - #332 M6), a delete of
// the active profile (core.ErrProfileActive, #446) or a delete in a game
// whose files do not say which profile is active (core.
// ErrActiveProfileUnknown, #445 review F2) answers 409, a name no profile
// file could carry answers 400, and anything else is a genuine I/O failure
// (500).
func (s *Server) profileErrorStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	case errors.Is(err, core.ErrProfileExists), errors.Is(err, core.ErrProfileActive), errors.Is(err, core.ErrActiveProfileUnknown):
		return http.StatusConflict
	case errors.Is(err, domain.ErrInvalidProfileName), errors.Is(err, domain.ErrInvalidGameID):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
