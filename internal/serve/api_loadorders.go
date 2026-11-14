// api_loadorders.go answers the reorder modal's load-order tabs (#620): a
// game's file load orders (Cyberpunk 2077's archives) and every change
// `lmm load-order` makes to them - move and reset.
//
// The read is core.FileLoadOrdersReport verbatim, `lmm load-order list
// --json`'s document. The write is core's PlanFileLoadOrderEdit/
// ApplyFileLoadOrderEdit pair driven in one request, as the Plugins tab's
// is (api_plugins.go): an edit is one profile write and a list-file write,
// quick and with nothing to wait for, so it answers synchronously rather
// than as a job. dry_run stops after the plan and answers it - `lmm
// load-order ... --dry-run`'s document.
//
// The profile is named in the PATH, as every profiles-modal route names it,
// and only the GAME half of the selection is resolved.
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// loadOrderEditRequest is POST /api/v1/profiles/{name}/load-orders's body:
// the edit (core.FileLoadOrderEdit, the document `lmm load-order` builds
// from its arguments) and whether to stop at the plan.
type loadOrderEditRequest struct {
	Edit   core.FileLoadOrderEdit `json:"edit"`
	DryRun bool                   `json:"dry_run"`
}

// handleAPIProfileLoadOrders answers GET /api/v1/profiles/{name}/load-orders
// with the core.FileLoadOrdersReport `lmm load-order list --json` emits -
// or, for a game without a file load order, 204 No Content: the reorder
// modal reads this on every open to know which tabs to offer, and most
// games have none, which is no error.
func (s *Server) handleAPIProfileLoadOrders(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	report, err := s.svc.FileLoadOrders(r.Context(), sel.Game, r.PathValue("name"))
	if errors.Is(err, core.ErrNoFileLoadOrder) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.writeAPIError(w, loadOrderErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// handleAPIProfileLoadOrderEdit answers POST
// /api/v1/profiles/{name}/load-orders: the edit's
// core.FileLoadOrderEditPlan when dry_run is set, else what applying it
// did, core.FileLoadOrderEditResult.
func (s *Server) handleAPIProfileLoadOrderEdit(w http.ResponseWriter, r *http.Request) {
	var req loadOrderEditRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.Edit.Op == "" {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`"edit.op" is required`))
		return
	}
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	plan, err := s.svc.PlanFileLoadOrderEdit(ctx, sel.Game, r.PathValue("name"), req.Edit)
	if err != nil {
		s.writeAPIError(w, loadOrderErrorStatus(err), err)
		return
	}
	if req.DryRun {
		s.writeJSON(w, http.StatusOK, plan)
		return
	}
	res, err := s.svc.ApplyFileLoadOrderEdit(ctx, sel.Game, plan)
	if err != nil {
		s.writeAPIError(w, loadOrderErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

// loadOrderErrorStatus classifies a load-order read or edit failure: an
// edit naming what is not there, or that cannot mean anything, is the
// caller's (400); a game without that load order, or a profile that does
// not exist, is not found (404); a profile changed under the plan
// conflicts with the state (409); anything else is the server's (500).
func loadOrderErrorStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrLoadOrderFileNotFound), errors.Is(err, core.ErrLoadOrderFileNotOwned),
		errors.Is(err, core.ErrInvalidLoadOrderEdit), errors.Is(err, domain.ErrInvalidProfileName):
		return http.StatusBadRequest
	case errors.Is(err, core.ErrNoFileLoadOrder), errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	case errors.Is(err, core.ErrStalePlan):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
