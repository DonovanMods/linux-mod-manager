// api_mod_settings.go answers the slide-over's and the full mod page's
// EDITABLE lock, update-policy and pak-conversion controls (docs/plans/2026-08-31-serve-spa
// -design.md §Slide-over: "lock + policy controls (editable)") over four
// thin POST routes - Service.SetModLock/ClearModLock/SetModUpdatePolicy/
// SetModConvertPaks verbatim, each already a single beginOp-gated call with nothing to
// preview (mirroring EnableMod/DisableMod's own shape, mod_settings.go's own
// doc comment).
//
// These are deliberately NOT jobs, unlike kind_toggle.go's enable/disable.
// A job exists to give a mutation a place to report progress and to let its
// control morph while it runs; a lock/unlock/policy write is a single DB
// row update with no core.EventSink of its own (mod_settings.go: "each a
// single beginOp-gated mutation") and nothing meaningful to show in
// flight - running it as a job would put an empty progress bar on screen
// for a call that has already returned by the time a frame could arrive.
// They still go through s.wrap (the same CSRF/Origin/logging every other
// state-changing route gets) and answer synchronously with the goldened
// core.ModSettingResult document every one of the three core calls already
// returns, so a caller never needs a follow-up read to see what changed.
package serve

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// modLockRequest is POST /api/v1/mods/{source}/{id}/lock's request body.
// Version empty locks at whatever is currently installed - SetModLock's own
// convention (internal/core/mod_settings.go) - a caller sends a specific
// version only when locking from the full mod page's versions table.
type modLockRequest struct {
	Version string `json:"version,omitzero"`
}

// modConvertRequest is POST /api/v1/mods/{source}/{id}/convert's request
// body: whether this mod's prebuilt .pak should be converted into the
// profile's merged artifact. Enabled is a *bool, not bool, because
// json/v2 has no "required" struct tag option to enforce presence on
// decode - `lmm mod convert <mod-id> <on|off>` makes the caller say which
// way, and a nil pointer (empty body, "{}", or an explicit null) must be
// refused by handleAPIModConvert rather than silently landing on false.
type modConvertRequest struct {
	Enabled *bool `json:"enabled"`
}

// modUpdatePolicyRequest is POST /api/v1/mods/{source}/{id}/update-policy's
// request body: domain.UpdatePolicy's own text encoding ("notify", "auto",
// "pinned"), so a malformed value fails to decode rather than silently
// landing on the zero value.
type modUpdatePolicyRequest struct {
	Policy domain.UpdatePolicy `json:"policy"`
}

// decodeAPIBody reads and strictly decodes r's body into out, the same
// unknown-member-rejecting contract decodeKindOptions gives every plan
// kind's options - this package's one JSON request decoder, used here and
// there for the same reason: a misspelled member must fail loudly rather
// than silently doing nothing. An empty body decodes to out's zero value.
func decodeAPIBody(w http.ResponseWriter, r *http.Request, out any) error {
	body, err := readAPIBody(w, r)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out, json.RejectUnknownMembers(true))
}

// handleAPIModLock answers POST /api/v1/mods/{source}/{id}/lock.
func (s *Server) handleAPIModLock(w http.ResponseWriter, r *http.Request) {
	var req modLockRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	sourceID, modID := r.PathValue("source"), r.PathValue("id")
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	result, err := s.svc.SetModLock(r.Context(), sourceID, modID, sel.Game.ID, sel.Profile, req.Version)
	if err != nil {
		s.writeAPIError(w, s.modSettingErrorStatus(r.Context(), sourceID, modID, sel, err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIModUnlock answers POST /api/v1/mods/{source}/{id}/unlock. It
// takes no request body - there is nothing to say beyond which mod.
func (s *Server) handleAPIModUnlock(w http.ResponseWriter, r *http.Request) {
	sourceID, modID := r.PathValue("source"), r.PathValue("id")
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	result, err := s.svc.ClearModLock(r.Context(), sourceID, modID, sel.Game.ID, sel.Profile)
	if err != nil {
		s.writeAPIError(w, s.modSettingErrorStatus(r.Context(), sourceID, modID, sel, err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIModUpdatePolicy answers
// POST /api/v1/mods/{source}/{id}/update-policy.
func (s *Server) handleAPIModUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	var req modUpdatePolicyRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	sourceID, modID := r.PathValue("source"), r.PathValue("id")
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	result, err := s.svc.SetModUpdatePolicy(r.Context(), sourceID, modID, sel.Game.ID, sel.Profile, req.Policy)
	if err != nil {
		s.writeAPIError(w, s.modSettingErrorStatus(r.Context(), sourceID, modID, sel, err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIModConvert answers POST /api/v1/mods/{source}/{id}/convert with
// the same core.ModSettingResult document `lmm mod convert --json` emits.
//
// #326 (epic live review C-3): this is the Icarus pak-conversion toggle -
// the one missing web path the review said it would not defer, since it is
// a first-class supported game's per-mod setting. Single-step and
// job-free like its lock/policy siblings, for the reason this file's doc
// comment gives.
//
// It reproduces `lmm mod convert`'s own guard rather than inventing one: a
// mod with no pak-kind merge source has nothing to convert or leave raw,
// so the request is 400 (bad input - this mod cannot take this setting)
// with the CLI's exact wording, not a stored flag nothing will ever read.
// The check runs against a live GetInstalledMod, which also gives the
// not-found path its 404 before any write is attempted.
func (s *Server) handleAPIModConvert(w http.ResponseWriter, r *http.Request) {
	var req modConvertRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`"enabled" is required`))
		return
	}

	sourceID, modID := r.PathValue("source"), r.PathValue("id")
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	mod, err := s.svc.GetInstalledMod(ctx, sourceID, modID, sel.Game.ID, sel.Profile)
	if err != nil {
		s.writeAPIError(w, s.modSettingErrorStatus(ctx, sourceID, modID, sel, err), err)
		return
	}
	if !s.svc.ModHasPakMergeSource(sel.Game, mod) {
		s.writeAPIError(w, http.StatusBadRequest,
			fmt.Errorf("mod %s has no pak merge source: pak conversion does not apply", modID))
		return
	}

	result, err := s.svc.SetModConvertPaks(ctx, sourceID, modID, sel.Game.ID, sel.Profile, *req.Enabled)
	if err != nil {
		s.writeAPIError(w, s.modSettingErrorStatus(ctx, sourceID, modID, sel, err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// modSettingErrorStatus classifies a SetModLock/ClearModLock/
// SetModUpdatePolicy failure (M2): not-found - sourceID/modID names a mod
// that is not installed in sel's profile, so a lock/unlock/policy write has
// no existing install to target - answers 404, the same not-found
// treatment the sibling read route already gets (api_mod_files.go); any
// other failure (a profile config I/O error, say) answers 500.
// SetModUpdatePolicy's own not-found path already wraps
// domain.ErrModNotFound (db.UpdateModPolicy); ProfileManager.SetModLock/
// ClearModLock's does not (a plain "not found in profile" error, pinned
// verbatim by other tests, so not reworded here) - a live existence check
// is the one not-found signal both shapes share.
func (s *Server) modSettingErrorStatus(ctx context.Context, sourceID, modID string, sel selection, err error) int {
	if errors.Is(err, domain.ErrModNotFound) {
		return http.StatusNotFound
	}
	if _, gerr := s.svc.GetInstalledMod(ctx, sourceID, modID, sel.Game.ID, sel.Profile); errors.Is(gerr, domain.ErrModNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
