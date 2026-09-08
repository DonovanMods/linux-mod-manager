// kind_adopt.go registers the "adopt" plan kind: `lmm import` in SCAN mode
// - bringing mods already sitting in the game's mod_path under lmm's
// management - driven from the browser.
//
// The plan document is core.AdoptPlan verbatim, which is what makes this
// kind worth having: it carries the whole LocalScan (tracked, untracked,
// the backfill candidates, the extract-mode caveat) plus one AdoptMatch per
// untracked entry and the duplicate preview, so the SPA renders the same
// facts the CLI prints before its "Import these mods? [y/N]" prompt, from
// the plan alone.
//
// DRY RUN IS PLAN-ONLY. `lmm import --dry-run` is, in core's terms, a
// caller that plans and never applies (AdoptOptions.DryRun's own doc
// comment: "It does not make the flow dry by itself"). A frontend that
// previews and does not confirm has already performed the dry run, so this
// kind exposes no dry-run option - there is nothing for one to mean here.
//
// ONE JOB, TWO APPLIES. The adopt flow has two Apply entry points, and the
// split is behavioural, not cosmetic: the pre-lift engine ran the metadata
// backfill - a mutation - BEFORE the confirmation that gates the adoption
// itself, so declining kept the backfill (internal/core/adopt.go's shape
// note). The CLI reproduces that by running ApplyAdoptBackfill while it
// renders the scan and ApplyAdopt after the decision. A browser cannot:
// the decision is the confirm dialog, which happens between the plan
// request and the job request, and mutating during a PLAN would make a
// preview a write.
//
// So both applies run inside the job, backfill first, and the counts come
// back as one document (core.AdoptResult.Backfilled, #333's additive
// member). A composed core method running both would be the convenience
// wrapper v2 Phase 3 forbids; composing them HERE, where the two-step is
// visible, is the sanctioned shape - the same thing kind_updates.go does
// with the per-mod update plans.
package serve

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "adopt",
		PlanOptions:  decodeKindOptions[adoptPlanRequest],
		ApplyOptions: decodeKindOptions[adoptApplyRequest],
		Plan:         planAdoptKind,
		Apply:        applyAdoptKind,
	})
}

// adoptPlanRequest is POST /api/v1/plans/adopt's request body.
type adoptPlanRequest struct {
	// SkipMatch mirrors `lmm import --skip-match`: it suppresses BOTH the
	// per-entry source matching and the metadata backfill, so a plan
	// computed with it has nothing for ApplyAdoptBackfill to do. The plan
	// echoes it (AdoptPlan.SkipMatch), which is how a reader tells "no
	// source matched" from "no lookup was attempted".
	SkipMatch bool `json:"skip_match,omitzero"`
}

// adoptApplyRequest is the "options" member POST /api/v1/jobs accepts for
// an adopt plan: nothing. Every choice this flow offers is made at plan
// time (--skip-match) or is the decision itself, which a frontend expresses
// by starting the job or not (Ruling 6). It carries no json tags, so there
// is no wire shape to pin.
type adoptApplyRequest struct{}

// pendingAdopt is what the plan store holds between Plan and Apply: the
// plan object (pointer identity preserved, so its unexported freshness
// snapshot survives to BOTH applies' checkPlanFresh) and the game it was
// computed for.
type pendingAdopt struct {
	Game *domain.Game
	Plan *core.AdoptPlan
}

// planAdoptKind implements planKind.Plan for "adopt".
func planAdoptKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(adoptPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("adopt plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanAdopt(ctx, sel.Game, sel.Profile, core.AdoptOptions{SkipMatch: req.SkipMatch})
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingAdopt{Game: sel.Game, Plan: plan}, nil
}

// applyAdoptKind implements planKind.Apply for "adopt": the metadata
// backfill, then the adoption, then one result document carrying both
// counts.
//
// A backfill FAILURE is fatal to the job. It runs first and it is a
// mutation, so continuing past one would adopt against a set whose state
// nobody can describe - and its own per-row failures are not errors at all
// (they are notes on the event stream), so an error here means the plan was
// stale or the context is gone, both of which the adoption would hit next
// anyway.
func applyAdoptKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingAdopt)
	if !ok {
		return nil, fmt.Errorf("adopt apply: unexpected pending type %T", pending)
	}
	if _, ok := opts.(adoptApplyRequest); !ok {
		return nil, fmt.Errorf("adopt apply: unexpected options type %T", opts)
	}

	backfill, err := s.svc.ApplyAdoptBackfill(ctx, p.Game, p.Plan, sink)
	if err != nil {
		return &core.AdoptResult{Backfilled: backfill.Backfilled}, err
	}

	result, err := s.svc.ApplyAdopt(ctx, p.Game, p.Plan, sink)
	if result != nil {
		result.Backfilled = backfill.Backfilled
	}
	return result, err
}
