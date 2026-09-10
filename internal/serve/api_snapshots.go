// api_snapshots.go answers the Snapshots card's three single-step routes
// (#350): the listing, a create, and a delete.
//
// They are NOT jobs, for the reason api_mod_settings.go's doc comment
// gives: a job exists to give a mutation somewhere to report progress and
// to let its control morph while it runs, and both of these have nothing to
// show in flight - a create writes one metadata file (its only cost is
// hashing the deployed tree, which reports nothing), and a delete removes
// one. RESTORE is the destructive, multi-stage half, and that one IS a job
// with a plan behind it (kind_snapshot_restore.go).
//
// Each answers with the goldened core document its `lmm snapshot ...` twin
// emits, so the two frontends cannot disagree about what a snapshot is, and
// so a caller never needs a follow-up read to see what changed.
package serve

import (
	"errors"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// snapshotCreateRequest is POST /api/v1/snapshots' request body.
//
// Name is optional: empty gets the same date-and-time default `lmm snapshot
// create` uses with no --name, so the card's "Snapshot now" button needs no
// text input. The PROFILE is the selected one (?profile=), not a body
// member - a snapshot records what you are looking at, the relationship
// deploy, purge and uninstall all have with the selection.
type snapshotCreateRequest struct {
	Name string `json:"name,omitzero"`
}

// handleAPISnapshots answers GET /api/v1/snapshots with the
// core.SnapshotListing document `lmm snapshot list --json` emits.
//
// Game-scoped but NOT profile-scoped: snapshots belong to a game, and a
// card that hid the ones taken under another profile would hide exactly
// the snapshot a user switched profiles away from.
func (s *Server) handleAPISnapshots(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}
	listing, err := s.svc.ListSnapshots(r.Context(), sel.Game.ID)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, listing)
}

// handleAPISnapshotCreate answers POST /api/v1/snapshots with the
// core.SnapshotResult document `lmm snapshot create --json` emits.
//
// 409 for a name already taken (the CLI's own refusal - a snapshot is the
// only copy of an arrangement the user asked lmm to remember, so it is
// never silently overwritten), 400 for a name that is not usable as a file
// name.
func (s *Server) handleAPISnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req snapshotCreateRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}
	name := req.Name
	if name == "" {
		name = core.DefaultSnapshotName(time.Now())
	}

	result, err := s.svc.CreateSnapshot(r.Context(), sel.Game, sel.Profile, name)
	if err != nil {
		s.writeAPIError(w, snapshotErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPISnapshotDelete answers DELETE /api/v1/snapshots/{name} with the
// core.SnapshotDeleteResult document `lmm snapshot delete --json` emits.
func (s *Server) handleAPISnapshotDelete(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}
	result, err := s.svc.DeleteSnapshot(r.Context(), sel.Game.ID, r.PathValue("name"))
	if err != nil {
		s.writeAPIError(w, snapshotErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// snapshotErrorStatus classifies core's snapshot sentinels: a name that
// does not exist is 404, one already taken is 409 (the request was
// well-formed and refused on state, which is what 409 means), one that is
// not a legal file name is 400. Anything else is the server's problem.
func snapshotErrorStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrSnapshotNotFound):
		return http.StatusNotFound
	case errors.Is(err, core.ErrSnapshotExists):
		return http.StatusConflict
	case errors.Is(err, core.ErrInvalidSnapshotName):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
