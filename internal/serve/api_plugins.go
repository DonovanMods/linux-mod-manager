// api_plugins.go answers the reorder modal's Plugins tab (#617, design
// docs/plans/2026-11-10-collections-design.md §5 "Frontends"): a Bethesda
// game's effective plugin order, and every change `lmm plugins` makes to
// it - enable, disable, move, sort, and the profile's rules and groups.
//
// The read is core.PluginOrderReport verbatim, `lmm plugins list --json`'s
// document. The write is core's PlanPluginEdit/ApplyPluginEdit pair driven
// in one request, like the reorder beside it (api_profiles.go): a plugin
// edit is one profile write and a Plugins.txt write, quick and with nothing
// to wait for, so it answers synchronously rather than as a job. dry_run
// stops after the plan and answers it - `lmm plugins ... --dry-run`'s
// document - so the tab can preview a sort exactly as the CLI does.
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

// pluginEditRequest is POST /api/v1/profiles/{name}/plugins's body: the
// edit (core.PluginEdit, the document `lmm plugins` builds from its
// arguments) and whether to stop at the plan.
type pluginEditRequest struct {
	Edit   core.PluginEdit `json:"edit"`
	DryRun bool            `json:"dry_run"`
}

// handleAPIProfilePlugins answers GET /api/v1/profiles/{name}/plugins with
// the core.PluginOrderReport `lmm plugins list --json` emits.
func (s *Server) handleAPIProfilePlugins(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	report, err := s.svc.PluginOrder(r.Context(), sel.Game, r.PathValue("name"))
	if err != nil {
		s.writeAPIError(w, pluginErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// handleAPIProfilePluginEdit answers POST /api/v1/profiles/{name}/plugins:
// the edit's core.PluginEditPlan when dry_run is set, else what applying
// it did, core.PluginEditResult.
func (s *Server) handleAPIProfilePluginEdit(w http.ResponseWriter, r *http.Request) {
	var req pluginEditRequest
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
	plan, err := s.svc.PlanPluginEdit(ctx, sel.Game, r.PathValue("name"), req.Edit)
	if err != nil {
		s.writeAPIError(w, pluginErrorStatus(err), err)
		return
	}
	if req.DryRun {
		s.writeJSON(w, http.StatusOK, plan)
		return
	}
	res, err := s.svc.ApplyPluginEdit(ctx, sel.Game, plan)
	if err != nil {
		s.writeAPIError(w, pluginErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

// pluginErrorStatus classifies a plugin read or edit failure: an edit
// naming what is not there, or that cannot mean anything, is the caller's
// (400); a game with no plugin list, or a profile that does not exist, is
// not found (404); a rule set the edit would make cyclic, or a profile
// changed under the plan, conflicts with the state (409, the cycle in the
// envelope's details); anything else is the server's (500).
func pluginErrorStatus(err error) int {
	var cycle *core.PluginOrderCycleError
	switch {
	case errors.Is(err, core.ErrPluginNotFound), errors.Is(err, core.ErrPluginNotOwned),
		errors.Is(err, core.ErrPluginRuleNotFound), errors.Is(err, core.ErrInvalidPluginEdit),
		errors.Is(err, domain.ErrInvalidProfileName):
		return http.StatusBadRequest
	case errors.Is(err, core.ErrNoPluginList), errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	case errors.As(err, &cycle), errors.Is(err, core.ErrStalePlan):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
