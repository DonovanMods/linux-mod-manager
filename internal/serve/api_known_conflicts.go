package serve

// The Conflicts card's "Mark as intended" and "Unmark" (#668): POST
// /api/v1/conflicts/mark and POST /api/v1/conflicts/unmark, scoped by
// ?game=/?profile= like GET /api/v1/conflicts. Each is one beginOp-gated
// profile write in core (MarkConflictsKnown, MarkCurrentConflictsKnown,
// UnmarkConflictsKnown) - nothing to preview, so no plan and no job, the
// mod settings routes' shape - answered with the core.KnownConflictsResult
// document `lmm conflicts ack/unack --json` emits. Two literal routes
// rather than one {action} wildcard, as enable/disable are, so the table
// stays closed.

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// knownConflictsRequest is both routes' body: the pairs to mark or unmark
// (each mod as a reorder names it - "source:modid", or a bare mod id), or,
// for mark only, all_current: every present conflict as it stands - `lmm
// conflicts ack --all-current`.
type knownConflictsRequest struct {
	Pairs      []core.ConflictPair `json:"pairs,omitempty"`
	AllCurrent bool                `json:"all_current,omitzero"`
}

// handleAPIConflictsMark answers POST /api/v1/conflicts/mark.
func (s *Server) handleAPIConflictsMark(w http.ResponseWriter, r *http.Request) {
	var req knownConflictsRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.AllCurrent && len(req.Pairs) > 0 {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`"all_current" marks every present conflict: send no "pairs" with it`))
		return
	}
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}
	var (
		result *core.KnownConflictsResult
		err    error
	)
	if req.AllCurrent {
		result, err = s.svc.MarkCurrentConflictsKnown(r.Context(), sel.Game, sel.Profile)
	} else {
		result, err = s.svc.MarkConflictsKnown(r.Context(), sel.Game, sel.Profile, req.Pairs)
	}
	if err != nil {
		s.writeAPIError(w, knownConflictsErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleAPIConflictsUnmark answers POST /api/v1/conflicts/unmark.
func (s *Server) handleAPIConflictsUnmark(w http.ResponseWriter, r *http.Request) {
	var req knownConflictsRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.AllCurrent {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`"all_current" only marks: name the pairs to unmark`))
		return
	}
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}
	result, err := s.svc.UnmarkConflictsKnown(r.Context(), sel.Game, sel.Profile, req.Pairs)
	if err != nil {
		s.writeAPIError(w, knownConflictsErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// knownConflictsErrorStatus classifies a mark/unmark failure: a pair that
// names no mod of the profile, an ambiguous one, one mod twice, or none at
// all is bad input; a refused adapter is the known state GET
// /api/v1/conflicts answers 409 for; a vanished profile is 404.
// writeAPIError turns a running operation into 409 itself.
func knownConflictsErrorStatus(err error) int {
	var refused *core.AdapterRefusedError
	switch {
	case errors.Is(err, core.ErrModNotInProfile), errors.Is(err, core.ErrAmbiguousModID),
		errors.Is(err, core.ErrSameModPair), errors.Is(err, core.ErrNoConflictPairs):
		return http.StatusBadRequest
	case errors.As(err, &refused):
		return http.StatusConflict
	case errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
