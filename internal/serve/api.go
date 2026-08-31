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
// extension point cmd/lmm's own errorDetails (jsonout.go) uses. It is its
// own copy here rather than shared code: cmd/lmm imports internal/serve,
// never the reverse (boundary rule), so serve cannot reuse cmd/lmm's
// unexported definition.
func errorDetails(err error) any {
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
// anywhere in this file).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", apiContentType)
	w.WriteHeader(status)
	_ = core.EncodeJSON(w, v)
}

// writeAPIError writes err as the {"error","details"} envelope at status.
func (s *Server) writeAPIError(w http.ResponseWriter, status int, err error) {
	s.log.Debug("api error", "status", status, "err", err)
	writeJSON(w, status, apiErrorEnvelope{Error: err.Error(), Details: errorDetails(err)})
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
	writeJSON(w, http.StatusNotFound, apiErrorEnvelope{
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
	writeJSON(w, http.StatusOK, list)
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
	writeJSON(w, http.StatusOK, detail)
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
	writeJSON(w, http.StatusOK, report)
}
