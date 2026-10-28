// kind_rollback.go registers the "rollback" plan kind - #330's new
// extension of the confirm-plan framework Unit 3 landed (docs/plans/2026-08
// -31-webui-impl.md §Pre-flight: "U3 confirm framework -> U4-U7 mutations
// ... later units consume, never fork it"). It is the framework's designed
// extension path applied to core.PlanRollback/ApplyRollback: a plan kind
// entry here, a renderer registered in planrenderers.js, nothing else.
//
// Both options halves are the plan-time/apply-time split every other flow
// in this package uses: PlanRollback takes no options of its own besides
// naming the mod (RollbackOptions only exists for ApplyRollback), so the
// plan-time request carries only the selection and the apply-time request
// carries RollbackOptions' own Force/SkipHooks - mirroring uninstall's
// KeepCache/SkipHooks split (kind_uninstall.go) for the same reason: a
// frontend that showed the plan and then let the user toggle --force still
// applies what the user finally chose.
package serve

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "rollback",
		PlanOptions:  decodeKindOptions[rollbackPlanRequest],
		ApplyOptions: decodeKindOptions[rollbackApplyRequest],
		Plan:         planRollbackKind,
		Apply:        applyRollbackKind,
	})
}

// rollbackPlanRequest is POST /api/v1/plans/rollback's request body: which
// installed mod to roll back to its previous version.
type rollbackPlanRequest struct {
	ModID    string `json:"mod_id"`
	SourceID string `json:"source_id,omitzero"`
}

// validate implements validatingOptions.
func (r *rollbackPlanRequest) validate() error {
	if r.ModID == "" {
		return errors.New(`"mod_id" is required`)
	}
	return nil
}

// rollbackApplyRequest is the "options" member POST /api/v1/jobs accepts for
// a rollback plan - core.RollbackOptions' own two fields, mirroring
// `lmm update rollback --force/--no-hooks`.
type rollbackApplyRequest struct {
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// rollbackOptions renders the request as the core options struct.
func (r rollbackApplyRequest) rollbackOptions() core.RollbackOptions {
	return core.RollbackOptions{Force: r.Force, SkipHooks: r.SkipHooks}
}

// pendingRollback is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyRollback's staleness check) and the
// game it was computed for - the same shape pendingUninstall keeps.
type pendingRollback struct {
	Game *domain.Game
	Plan *core.RollbackPlan
}

// planRollbackKind implements planKind.Plan for "rollback".
func planRollbackKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(rollbackPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("rollback plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanRollback(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingRollback{Game: sel.Game, Plan: plan}, nil
}

// applyRollbackKind implements planKind.Apply for "rollback".
func applyRollbackKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingRollback)
	if !ok {
		return nil, fmt.Errorf("rollback apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(rollbackApplyRequest)
	if !ok {
		return nil, fmt.Errorf("rollback apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyRollback(ctx, p.Game, p.Plan, req.rollbackOptions(), sink)
}
