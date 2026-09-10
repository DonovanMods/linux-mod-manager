// kind_workshop_adopt.go registers the "workshop_adopt" plan kind: `lmm
// import --workshop` (#269) driven from the browser - bringing the Steam
// Workshop items the Steam client has already downloaded under lmm's
// tracking.
//
// The plan document is core.WorkshopAdoptPlan verbatim: the scan (which
// libraries were read, every item, how many are already tracked) plus one
// entry per untracked item with whatever Steam's keyless API said about it.
// That is exactly what the CLI prints before its "Track these items?"
// prompt, so the SPA renders the same facts from the plan alone.
//
// NOTHING THIS JOB DOES TOUCHES A FILE. Apply writes a DB row and a profile
// ref per item and stops there - the Steam client owns the content, and the
// adopted mods are EXTERNAL, which every downstream flow already refuses to
// deploy, purge, enable or update. The end-state test
// (api_flow_workshop_adopt_internal_test.go) asserts that nothing lands
// under mod_path or in the cache.
//
// DRY RUN IS PLAN-ONLY, exactly as it is for "adopt": a frontend that
// previews and does not confirm has already performed the dry run, so this
// kind exposes no dry-run option.
package serve

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "workshop_adopt",
		PlanOptions:  decodeKindOptions[workshopAdoptPlanRequest],
		ApplyOptions: decodeKindOptions[workshopAdoptApplyRequest],
		Plan:         planWorkshopAdoptKind,
		Apply:        applyWorkshopAdoptKind,
	})
}

// workshopAdoptPlanRequest is POST /api/v1/plans/workshop_adopt's request
// body.
type workshopAdoptPlanRequest struct {
	// Refresh mirrors the CLI's --refresh: bypass the source's own metadata
	// cache for this plan, so an item subscribed moments ago shows its real
	// title rather than waiting out the cache's TTL.
	Refresh bool `json:"refresh,omitzero"`
}

// workshopAdoptApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a workshop-adopt plan: nothing. The one choice this flow
// offers is made at plan time (refresh); the decision itself is expressed
// by starting the job or not (Ruling 6). No json tags, so there is no wire
// shape to pin.
type workshopAdoptApplyRequest struct{}

// pendingWorkshopAdopt is what the plan store holds between Plan and Apply:
// the plan object (pointer identity preserved, so its unexported freshness
// snapshot survives to Apply's checkPlanFresh) and the game it was computed
// for.
type pendingWorkshopAdopt struct {
	Game *domain.Game
	Plan *core.WorkshopAdoptPlan
}

// planWorkshopAdoptKind implements planKind.Plan for "workshop_adopt".
//
// A game with no `steamworkshop` mapping has nothing to scan, and that is
// the caller's input being wrong rather than the server failing - the
// remedy is to map the source, which the sources editor already does - so
// it answers 400 rather than 500.
func planWorkshopAdoptKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(workshopAdoptPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("workshop adopt plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanWorkshopAdopt(ctx, sel.Game, sel.Profile,
		core.WorkshopAdoptOptions{Refresh: req.Refresh})
	if err != nil {
		if core.IsNoWorkshopSource(err) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
		return nil, nil, err
	}
	return plan, &pendingWorkshopAdopt{Game: sel.Game, Plan: plan}, nil
}

// applyWorkshopAdoptKind implements planKind.Apply for "workshop_adopt".
func applyWorkshopAdoptKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingWorkshopAdopt)
	if !ok {
		return nil, fmt.Errorf("workshop adopt apply: unexpected pending type %T", pending)
	}
	if _, ok := opts.(workshopAdoptApplyRequest); !ok {
		return nil, fmt.Errorf("workshop adopt apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyWorkshopAdopt(ctx, p.Game, p.Plan, sink)
}
