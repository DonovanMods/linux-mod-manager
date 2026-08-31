// kind_uninstall.go registers the "uninstall" plan kind - Task 8's first
// Plan -> confirm -> Apply flow (docs/plans/2026-08-30-serve-impl.md Task
// 8). It is the flow whose plan already carries everything the user needs
// to decide: PlanUninstall names the exact game-directory paths that would
// disappear, the hooks that would run, and what the profile's merged
// artifact would become - so the confirm page asks its question with the
// real answer already on the screen.
//
// The options half is #226: keep-cache and the two hook switches are
// APPLY-time, not plan-time, because ApplyUninstall reads them from opts
// rather than from the plan ("a frontend that showed the plan and then let
// the user toggle --keep-cache still applies what the user finally chose",
// internal/core/uninstall.go). They are also passed at plan time so the
// preview tells the truth: UninstallPlan.KeepCache echoes the option, and
// the hook list is empty under SkipHooks.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The uninstall confirm form's own field names (#226). They are the names
// uninstallApplyForm reads back, and the names confirmUninstallPlan puts on
// its toggles - one constant each, so a rename cannot half-apply.
const (
	keepCacheField = "keep_cache"
	forceField     = "force"
	skipHooksField = "skip_hooks"
)

func init() {
	registerPlanKind(planKind{
		Name:         "uninstall",
		Title:        "Uninstall",
		PlanOptions:  decodeKindOptions[uninstallPlanRequest],
		ApplyOptions: decodeKindOptions[uninstallApplyRequest],
		Plan:         planUninstallKind,
		Apply:        applyUninstallKind,
		Summarize:    summarizeUninstallResult,
		Form: &kindForm{
			PlanOptions:  uninstallPlanForm,
			ApplyOptions: uninstallApplyForm,
			Confirm:      confirmUninstallPlan,
		},
	})
}

// uninstallPlanRequest is POST /api/v1/plans/uninstall's request body: which
// installed mod to remove, and the two options that change what the PLAN
// itself says (KeepCache is echoed on the plan; SkipHooks empties its hook
// list).
type uninstallPlanRequest struct {
	// ModID is the installed mod's own ID, required. SourceID picks which
	// source's copy when the same ID is installed from more than one; empty
	// takes the first, exactly as `lmm uninstall <id>` always has.
	ModID    string `json:"mod_id"`
	SourceID string `json:"source_id,omitzero"`
	// KeepCache and SkipHooks mirror `lmm uninstall --keep-cache/--no-hooks`.
	KeepCache bool `json:"keep_cache,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// validate implements validatingOptions: without a mod there is nothing to
// plan, and saying so at the request boundary beats a core lookup failure.
func (r *uninstallPlanRequest) validate() error {
	if r.ModID == "" {
		return errors.New(`"mod_id" is required`)
	}
	return nil
}

// uninstallApplyRequest is the "options" member POST /api/v1/jobs accepts
// for an uninstall plan - the choices ApplyUninstall reads from opts rather
// than from the plan, so the confirm page's checkboxes are what actually
// applies (#226).
type uninstallApplyRequest struct {
	KeepCache bool `json:"keep_cache,omitzero"`
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// uninstallOptions renders the request as the core options struct.
func (r uninstallApplyRequest) uninstallOptions() core.UninstallOptions {
	return core.UninstallOptions{KeepCache: r.KeepCache, Force: r.Force, SkipHooks: r.SkipHooks}
}

// pendingUninstall is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyUninstall's staleness check) and the
// game it was computed for.
type pendingUninstall struct {
	Game *domain.Game
	Plan *core.UninstallPlan
}

// planUninstallKind implements planKind.Plan for "uninstall".
func planUninstallKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(uninstallPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("uninstall plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanUninstall(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID, core.UninstallOptions{
		KeepCache: req.KeepCache,
		SkipHooks: req.SkipHooks,
	})
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingUninstall{Game: sel.Game, Plan: plan}, nil
}

// applyUninstallKind implements planKind.Apply for "uninstall". The sink is
// unused because ApplyUninstall takes none - the flow reports through its
// result's Notes and Warnings instead, which Summarize surfaces.
func applyUninstallKind(ctx context.Context, s *Server, pending, opts any, _ core.EventSink) (any, error) {
	p, ok := pending.(*pendingUninstall)
	if !ok {
		return nil, fmt.Errorf("uninstall apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(uninstallApplyRequest)
	if !ok {
		return nil, fmt.Errorf("uninstall apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyUninstall(ctx, p.Game, p.Plan, req.uninstallOptions())
}

// summarizeUninstallResult implements planKind.Summarize for "uninstall".
// UninstallResult carries only diagnostics - every step that could report a
// number is unconditional - so a clean run says so in words rather than
// rendering an empty readout.
func summarizeUninstallResult(result any) []resultFact {
	res, ok := result.(*core.UninstallResult)
	if !ok {
		return nil
	}
	facts := make([]resultFact, 0, len(res.Warnings)+len(res.Notes)+1)
	for _, w := range res.Warnings {
		facts = append(facts, resultFact{Label: "Warning", Value: w})
	}
	for _, n := range res.Notes {
		facts = append(facts, resultFact{Label: "Note", Value: n})
	}
	if len(facts) == 0 {
		facts = append(facts, resultFact{Label: "Mod", Value: "removed from the profile"})
	}
	return facts
}

// uninstallPlanForm implements kindForm.PlanOptions: the target comes from
// the path, the two plan-visible options from the form.
func uninstallPlanForm(r *http.Request) (any, error) {
	return uninstallPlanRequest{
		ModID:     r.PathValue("id"),
		SourceID:  r.PathValue("source"),
		KeepCache: formFlag(r, keepCacheField),
		SkipHooks: formFlag(r, skipHooksField),
	}, nil
}

// uninstallApplyForm implements kindForm.ApplyOptions: the confirm form's
// three checkboxes (#226), read back into the same type the JSON decoder
// produces.
func uninstallApplyForm(r *http.Request) (any, error) {
	return uninstallApplyRequest{
		KeepCache: formFlag(r, keepCacheField),
		Force:     formFlag(r, forceField),
		SkipHooks: formFlag(r, skipHooksField),
	}, nil
}

// confirmUninstallPlan implements kindForm.Confirm: what the uninstall
// would remove, and the options the user may still change.
func confirmUninstallPlan(pending, opts any) confirmView {
	p, ok := pending.(*pendingUninstall)
	if !ok {
		return confirmView{Submit: "Uninstall"}
	}
	req, _ := opts.(uninstallApplyRequest)

	plan := p.Plan
	view := confirmView{
		Heading: fmt.Sprintf("%s %s", plan.Mod.Name, plan.Mod.Version),
		Submit:  "Uninstall",
		Facts: []resultFact{
			{Label: "Mod", Value: domain.ModKey(plan.Mod.SourceID, plan.Mod.ID)},
			{Label: "Profile", Value: plan.Mod.ProfileName},
			{Label: "Files to remove", Value: fmt.Sprintf("%d", len(plan.Files))},
		},
		Toggles: []confirmToggle{
			{
				Name:    keepCacheField,
				Label:   "Keep the cached download",
				Help:    "reinstalling later needs no download",
				Checked: req.KeepCache,
			},
			{
				Name:    skipHooksField,
				Label:   "Skip hooks",
				Help:    "run no uninstall.* hooks at all",
				Checked: req.SkipHooks,
			},
			{
				Name:    forceField,
				Label:   "Continue past a failing before-hook",
				Help:    "the failure is recorded as a warning instead of stopping",
				Checked: req.Force,
			},
		},
	}

	if plan.MergedArtifact != nil {
		view.Facts = append(view.Facts, resultFact{
			Label: "Merged artifact",
			Value: mergedArtifactSummary(plan.MergedArtifact),
		})
	}
	if len(plan.Files) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Files that would be removed", Items: plan.Files})
	}
	if len(plan.Hooks) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Hooks that would run", Items: plan.Hooks})
	}
	return view
}

// mergedArtifactSummary renders a plan's merged-artifact effect as one line
// - an effect the file list cannot express, since the artifact belongs to
// the profile rather than to the mod being removed.
func mergedArtifactSummary(effect *core.MergedArtifactEffect) string {
	if effect.Action == core.MergedArtifactRemove {
		return effect.Path + " would be removed"
	}
	return effect.Path + " would be rebuilt from the merge sources that remain"
}
