// api.go implements Task 5's read-only /api/v1 JSON surface
// (docs/plans/2026-08-30-serve-design.md §HTTP surface): the same core
// documents the CLI's --json output emits for the identical call, framed
// identically via core.EncodeJSON - no other encoder appears anywhere in
// this file, so the bytes match --json byte for byte. Every failure path
// answers the {"error","details"} envelope (apiErrorEnvelope below); a
// successful response is never wrapped, and no cache headers are set (a
// dashboard must never be served a stale response).
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// apiContentType is every /api/v1 response's Content-Type, success or
// failure alike.
const apiContentType = "application/json"

// apiErrorEnvelope is the {"error","details"} document every /api/v1
// failure emits (docs/plans/2026-08-30-serve-design.md §HTTP surface:
// "Failures use the CLI's {"error","details"} envelope, same typed
// details"). Details is declared after Error - not alphabetical, but so
// "error" is always the first wire key (core.EncodeJSON's Deterministic
// option governs map/key ordering, not struct field order) - matching
// cmd/lmm's jsonErrorEnvelope byte-for-byte. Unit 4's job-failure envelope
// reuses this same type.
type apiErrorEnvelope struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

// errorDetails returns the data err carries for the /api/v1 error
// envelope's "details" field via the shared `Details() any` convention
// (*core.ConflictError, *core.ProfileWarningsError, ...) - the same
// extension point cmd/lmm's own errorDetails (jsonout.go) uses, guard
// included: core.ErrStalePlan is a plain sentinel with no data of its own,
// so it must never surface a Details() payload even from a hypothetical
// wrapper that carries one. It is its own copy here rather than shared
// code: cmd/lmm imports internal/serve, never the reverse (boundary rule),
// so serve cannot reuse cmd/lmm's unexported definition. No Details()
// implementer wraps ErrStalePlan today (task-5 gate review Minor 4) - this
// guard is prophylactic for Unit 4, which routes job failures (including a
// stale plan) through this same writer.
func errorDetails(err error) any {
	if errors.Is(err, core.ErrStalePlan) {
		return nil
	}
	var withDetails interface{ Details() any }
	if errors.As(err, &withDetails) {
		return withDetails.Details()
	}
	return nil
}

// writeJSON encodes v as status's response body via core.EncodeJSON only -
// the exact wire framing (2-space indent, deterministic key ordering, one
// trailing newline) the CLI's --json output uses for the identical value
// (docs/plans/2026-08-30-serve-impl.md Task 5 ruling: no second encoder
// anywhere in this file). A mid-stream encode/write failure is logged, not
// silently discarded (task-5 gate review Minor 5): the status line and
// headers are already on the wire by the time EncodeJSON runs, so nothing
// about the response itself can still change - logging is all that's left,
// unlike cmd/lmm's emitJSON, which can still return the error to its caller.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", apiContentType)
	w.WriteHeader(status)
	if err := core.EncodeJSON(w, v); err != nil {
		s.log.Error("api response encode failed", "status", status, "err", err)
	}
}

// writeAPIError writes err as the {"error","details"} envelope at status.
func (s *Server) writeAPIError(w http.ResponseWriter, status int, err error) {
	s.log.Debug("api error", "status", status, "err", err)
	s.writeJSON(w, status, apiErrorEnvelope{Error: err.Error(), Details: errorDetails(err)})
}

// selectionErrorDetails is the "details" payload for a game/profile
// selection that did not resolve (docs/plans/2026-08-30-serve-impl.md Task
// 5 ruling: "unknown game/profile -> the envelope, status 404, details
// listing the valid choices") - the same choices the page's nav switcher
// would offer. Profiles is empty whenever the game itself didn't resolve,
// since resolveSelection never populates it in that case.
type selectionErrorDetails struct {
	Games    []core.GameListEntry `json:"games"`
	Profiles []string             `json:"profiles,omitempty"`
}

// writeSelectionError answers a game/profile-scoped /api/v1 endpoint whose
// resolveSelection did not produce what the endpoint needs: the 404
// envelope Task 5 rules for "unknown game/profile". sel.Warning already
// names an explicit unresolvable ?game=/?profile= value or a missing
// default; it is empty only for the one case resolveSelection itself never
// messages - zero games configured at all - so that gets a generic
// fallback instead.
func (s *Server) writeSelectionError(w http.ResponseWriter, sel selection) {
	msg := sel.Warning
	if msg == "" {
		msg = "no games configured"
	}
	s.log.Debug("api selection unresolved", "msg", msg)
	s.writeJSON(w, http.StatusNotFound, apiErrorEnvelope{
		Error:   msg,
		Details: selectionErrorDetails{Games: sel.Games, Profiles: sel.Profiles},
	})
}

// resolveReadyAPISelection resolves r's game/profile selection for an
// endpoint that needs both a game AND a profile - every scoped endpoint
// except /api/v1/profiles (resolveGameAPISelection). On failure it writes
// the response itself (500 for a genuine resolveSelection error, 404 via
// writeSelectionError when the selection simply didn't resolve) and
// returns ok=false, so the caller only has to check ok before proceeding.
func (s *Server) resolveReadyAPISelection(w http.ResponseWriter, r *http.Request) (selection, bool) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return sel, false
	}
	if !sel.ready() {
		s.writeSelectionError(w, sel)
		return sel, false
	}
	return sel, true
}

// handleAPIMods answers GET /api/v1/mods with exactly the core.ModList
// document `lmm list --json` emits for the resolved game/profile
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "/mods" -> ListMods).
func (s *Server) handleAPIMods(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	list, err := s.svc.ListMods(r.Context(), sel.Game, sel.Profile)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

// handleAPIModDetail answers GET /api/v1/mods/{source}/{id} with exactly
// the core.ModDetail document `lmm mod show --json` emits
// (docs/plans/2026-08-30-serve-design.md §HTTP surface). Any ModDetail
// failure renders as 404: it mirrors pages_mods.go's handleModDetail,
// which has no way to tell a genuinely unknown mod ID apart from a
// transient source failure either, and treats both the same way.
func (s *Server) handleAPIModDetail(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source")
	modID := r.PathValue("id")

	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	detail, err := s.svc.ModDetail(r.Context(), sel.Game, sel.Profile, sourceID, modID)
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, err)
		return
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// handleAPISearch answers GET /api/v1/search?q= with exactly the
// core.SearchReport document `lmm search --json` emits
// (docs/plans/2026-08-30-serve-design.md §HTTP surface). Unlike the /search
// PAGE, which renders the bare form when q is absent, the API has no form
// to fall back to: a missing/empty q is bad input (400), matching the
// CLI's own cobra.MinimumNArgs(1) requirement for `lmm search`. That check
// runs before selection resolution, since a malformed request is wrong
// regardless of what game/profile might have resolved.
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`missing required query parameter "q"`))
		return
	}

	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	report, err := s.svc.Search(r.Context(), sel.Game, sel.Profile, query, core.SearchOptions{})
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// handleAPIUpdates answers GET /api/v1/updates with exactly the
// core.UpdateCheckReport document a bulk `lmm update --json` (no mod ID)
// emits (docs/plans/2026-08-30-serve-design.md §HTTP surface: "/updates" ->
// CheckGameUpdates), built the same way cmd/lmm/update.go's own
// bulkCheckReport does. A partial CheckGameUpdates failure (e.g. one
// source down) still answers 200 with the one document ErrorMessage names -
// the CLI's own --json contract for this call keeps the partial results on
// stdout rather than discarding them - never the generic error envelope,
// which is reserved for a failure that leaves no usable document at all.
func (s *Server) handleAPIUpdates(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	installed, err := s.svc.GetInstalledMods(ctx, sel.Game.ID, sel.Profile)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}

	updates, checkErr := s.svc.CheckGameUpdates(ctx, sel.Game, sel.Profile, installed, nil)
	report := &core.UpdateCheckReport{
		GameID:  sel.Game.ID,
		Profile: sel.Profile,
		Updates: updates,
		Skipped: core.CountUpdateSkips(installed),
	}
	if checkErr != nil {
		report.ErrorMessage = checkErr.Error()
	}
	s.writeJSON(w, http.StatusOK, report)
}

// resolveGameAPISelection is resolveReadyAPISelection for /api/v1/profiles,
// which - like its page (pages_profiles.go) - only needs the game half of
// the selection to resolve: it lists every profile a game has, active one
// or not, so an unresolvable ?profile= is irrelevant to it.
func (s *Server) resolveGameAPISelection(w http.ResponseWriter, r *http.Request) (selection, bool) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return sel, false
	}
	if sel.Game == nil {
		s.writeSelectionError(w, sel)
		return sel, false
	}
	return sel, true
}

// handleAPIProfiles answers GET /api/v1/profiles with exactly the
// core.ProfileListing document `lmm profile list --json` emits
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "/profiles" ->
// ListProfiles). Only the game half of the selection needs to resolve,
// like its page.
func (s *Server) handleAPIProfiles(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}

	listing, err := s.svc.ListProfiles(r.Context(), sel.Game.ID)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, listing)
}

// handleAPIHealth answers GET /api/v1/health with exactly the
// core.VerifyReport document `lmm verify --json` emits (docs/plans/2026-08-30
// -serve-design.md §HTTP surface: "/health" -> Verify). Ruled deviation from
// the design doc's original api-route list (coordinator ruling, Task 5):
// this endpoint answers with the bare VerifyReport document - exact
// CLI-document parity, no serve-local composite type - and the conflicts
// half of the page's pairing gets its own additive GET /api/v1/conflicts
// route (handleAPIConflicts) instead of being folded into this one's shape.
// The design doc's HTTP-surface table gets a one-line amendment for this in
// Unit 6's docs task.
//
// Tier is VerifyFull, matching the CLI's own hardcoded tier (cmd/lmm/verify.go
// has no tier flag) - task-5 gate review Important 1: an earlier version
// pinned VerifyLocal to keep the endpoint cheap and offline like the /health
// PAGE (pages_health.go, which stays VerifyLocal on purpose - a page render
// must not hit the network), but core.VerifyReport carries no tier field, so
// the API and the CLI's --json document were silently disagreeing on the
// same state with no way for a consumer to tell. An additive `?tier=` query
// param (default full) to let an API caller opt back into the cheap offline
// check is a possible later addition - not added here.
func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	report, err := s.svc.VerifyReport(r.Context(), sel.Game, sel.Profile, core.VerifyOptions{Tier: core.VerifyFull}, nil)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// handleAPIConflicts answers GET /api/v1/conflicts with exactly the
// core.ConflictReport document `lmm conflicts --json` emits. Additive route
// beyond the design doc's original api-route list - see handleAPIHealth's
// doc comment for the ruling that put conflicts here instead of folded into
// /api/v1/health.
func (s *Server) handleAPIConflicts(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	conflicts, err := s.svc.GetProfileConflicts(r.Context(), sel.Game, sel.Profile)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.ConflictReport{GameID: sel.Game.ID, Profile: sel.Profile, Conflicts: conflicts})
}

// handleAPIStatus answers GET /api/v1/status with exactly the
// core.StatusReport document `lmm status --json` emits with no --game flag
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "/" -> Status).
// Unlike every other /api/v1 endpoint, status is not game/profile-scoped -
// it summarizes every configured game at once, mirroring "/"'s own
// handleStatus, which never calls resolveSelection either.
func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	report, err := s.svc.Status(r.Context())
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}
