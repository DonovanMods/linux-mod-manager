// kind_purge.go registers the "purge" plan kind - `lmm purge` as a Plan ->
// confirm -> job flow (#326; the epic live review's C-3 found it the one
// destructive CLI command with no web path at all).
//
// It is a Plan/Apply pair in core already: PlanPurge does the
// installed-mods read the CLI used to do before prompting, and its Mods
// slice IS the set ApplyPurge purges - "one object, so the number shown
// and the number purged cannot disagree" (internal/core/purge.go). Nothing
// is rebuilt here; this file only decodes the two option halves and hands
// the plan back.
//
// The profile is the SELECTED one (?profile=), not a body member: purge
// undeploys what you are looking at, the same relationship deploy and
// uninstall have with the selection. The two sibling profile kinds
// (switch, profile_apply) name a profile in the body because they act on
// one you are NOT currently in.
//
// Options follow #226's rule for uninstall: they are passed at plan time
// so the preview tells the truth (PurgePlan echoes Uninstall, and its hook
// list is empty under SkipHooks) AND at apply time, because ApplyPurge
// reads its own PurgeOptions rather than the plan's - so a confirm modal
// that lets the user toggle "also remove the records" applies what the
// user finally chose.
package serve

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "purge",
		PlanOptions:  decodeKindOptions[purgePlanRequest],
		ApplyOptions: decodeKindOptions[purgeApplyRequest],
		Plan:         planPurgeKind,
		Apply:        applyPurgeKind,
	})
}

// purgePlanRequest is POST /api/v1/plans/purge's request body: the two
// options that change what the PLAN itself says. Both are optional - a
// caller with nothing to say sends no body at all.
type purgePlanRequest struct {
	// Uninstall mirrors `lmm purge --uninstall`: also delete each purged
	// mod's DB record and profile entry. Echoed onto the plan.
	Uninstall bool `json:"uninstall,omitzero"`
	// SkipHooks mirrors the global `--no-hooks`: it empties the plan's hook
	// list, which is what the confirm modal renders.
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// purgeApplyRequest is the "options" member POST /api/v1/jobs accepts for a
// purge plan - core.PurgeOptions' three fields, `lmm purge`'s own flags.
type purgeApplyRequest struct {
	Uninstall bool `json:"uninstall,omitzero"`
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// purgeOptions renders the request as the core options struct.
func (r purgeApplyRequest) purgeOptions() core.PurgeOptions {
	return core.PurgeOptions{Uninstall: r.Uninstall, Force: r.Force, SkipHooks: r.SkipHooks}
}

// pendingPurge is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyPurge's staleness check) and the game
// it was computed for.
type pendingPurge struct {
	Game *domain.Game
	Plan *core.PurgePlan
}

// planPurgeKind implements planKind.Plan for "purge".
func planPurgeKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(purgePlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("purge plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanPurge(ctx, sel.Game, sel.Profile, core.PurgeOptions{
		Uninstall: req.Uninstall,
		SkipHooks: req.SkipHooks,
	})
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingPurge{Game: sel.Game, Plan: plan}, nil
}

// applyPurgeKind implements planKind.Apply for "purge".
func applyPurgeKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingPurge)
	if !ok {
		return nil, fmt.Errorf("purge apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(purgeApplyRequest)
	if !ok {
		return nil, fmt.Errorf("purge apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyPurge(ctx, p.Game, p.Plan, req.purgeOptions(), sink)
}
