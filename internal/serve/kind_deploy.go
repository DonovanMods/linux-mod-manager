// kind_deploy.go registers the "deploy" plan kind - Task 7's one wired
// mutation, and the reference implementation Tasks 8 and 9 follow for the
// rest (docs/plans/2026-08-30-serve-impl.md Tasks 8-9). Deploy is the kind
// Task 7 wires because it needs no source and no network (its files are
// already in the cache), and because it is the flow whose live progress the
// SSE stream exists for (#257).
//
// Task 9 adds its browser half, and deploy is the one kind where the
// confirm page IS the plan page in a load-bearing sense: its options are
// PLAN-time (see deployApplyRequest), so ticking --purge and pressing
// Deploy cannot silently apply the plan computed without it. kindForm's
// PlanIsCurrent catches exactly that and re-plans instead - the same answer
// every other "this is not the plan you were looking at" case gets.
//
// Two of the option struct's members are deliberately NOT on the page.
// ModID/SourceID would make the button a different action ("deploy this one
// mod"), not an option of "deploy this profile", and no read page offers
// that button; LinkMethod is a per-deploy override of how files land, which
// the confirm form has no honest one-line way to explain and which stays
// available through /api/v1. Both remain fully wired for JSON callers.
package serve

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The deploy confirm form's own field names. purgeField and allField are
// deploy's alone; the hook and force switches share the names every other
// flow's confirm form uses, so one checkbox means one thing everywhere.
const (
	purgeField = "purge"
	allField   = "all"
)

func init() {
	registerPlanKind(planKind{
		Name:         "deploy",
		Title:        "Deploy",
		PlanOptions:  decodeKindOptions[deployPlanRequest],
		ApplyOptions: decodeKindOptions[deployApplyRequest],
		Plan:         planDeployKind,
		Apply:        applyDeployKind,
		Summarize:    summarizeDeployResult,
		Form: &kindForm{
			PlanOptions:   deployPlanForm,
			ApplyOptions:  deployApplyForm,
			Confirm:       confirmDeployPlan,
			PlanIsCurrent: deployPlanIsCurrent,
		},
	})
}

// deployPlanRequest is POST /api/v1/plans/deploy's request body: the
// serve-owned wire form of core.DeployOptions, which carries no json tags
// of its own because it is an internal call shape, not a wire contract.
// Every member mirrors a `lmm deploy` flag, and every one is optional - an
// empty body plans a full-profile deploy, exactly like a bare `lmm deploy`.
type deployPlanRequest struct {
	// Purge undeploys everything currently deployed before the deploy loop
	// runs (`lmm deploy --purge`).
	Purge bool `json:"purge,omitzero"`
	// All includes disabled mods in a full-profile deploy, or allows
	// deploying a disabled ModID (`--all`).
	All bool `json:"all,omitzero"`
	// ModID restricts the deploy to one mod (`lmm deploy <mod-id>`);
	// SourceID picks which source's copy of it (`--source`).
	ModID    string `json:"mod_id,omitzero"`
	SourceID string `json:"source_id,omitzero"`
	// LinkMethod overrides the profile's effective link method for this
	// deploy (`--method`): "symlink", "hardlink", or "copy". Empty means
	// "use the profile's own".
	LinkMethod string `json:"link_method,omitzero"`
	// Force continues past a failing before_* hook; SkipHooks runs none at
	// all (`--force`, `--no-hooks`).
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// validate implements validatingOptions: the only member that can be
// syntactically valid JSON yet unusable is link_method, and rejecting it
// here means an unknown method is a 400 at the request boundary rather
// than a silently ignored option.
func (r *deployPlanRequest) validate() error {
	if r.LinkMethod == "" {
		return nil
	}
	if _, ok := domain.ParseLinkMethod(r.LinkMethod); !ok {
		return fmt.Errorf("unknown link_method %q (want symlink, hardlink, or copy)", r.LinkMethod)
	}
	return nil
}

// deployOptions renders the request as the core options struct. It assumes
// validate has already run, which decodeKindOptions guarantees.
func (r deployPlanRequest) deployOptions() core.DeployOptions {
	opts := core.DeployOptions{
		Purge:     r.Purge,
		All:       r.All,
		ModID:     r.ModID,
		SourceID:  r.SourceID,
		Force:     r.Force,
		SkipHooks: r.SkipHooks,
	}
	if r.LinkMethod != "" {
		method, _ := domain.ParseLinkMethod(r.LinkMethod)
		opts.LinkMethod = &method
	}
	return opts
}

// deployApplyRequest is the "options" member POST /api/v1/jobs accepts for
// a deploy plan: nothing. ApplyDeploy must receive the SAME DeployOptions
// its plan was computed from - a plan computed with --purge and applied
// without it would do something the user never previewed - so the options
// are fixed at plan time and stored with the pending value. Declaring an
// empty struct (rather than skipping the decoder) is what makes an attempt
// to pass deploy options at apply time a 400 rather than a silent no-op.
type deployApplyRequest struct{}

// pendingDeploy is what the deploy kind keeps in the plan store between
// Plan and Apply: the plan object itself (pointer identity preserved, so
// its unexported freshness snapshot survives to ApplyDeploy's staleness
// check), the game it was computed for, and the options that computed it.
type pendingDeploy struct {
	Game *domain.Game
	Plan *core.DeployPlan
	Opts core.DeployOptions
	// Req is the request Opts was built from - kept because it is
	// comparable (core.DeployOptions holds a *domain.LinkMethod and is not),
	// which is what lets deployPlanIsCurrent tell "the same options" from
	// "the user changed something".
	Req deployPlanRequest
}

// planDeployKind implements planKind.Plan for "deploy".
func planDeployKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(deployPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("deploy plan: unexpected options type %T", opts)
	}

	dopts := req.deployOptions()
	plan, err := s.svc.PlanDeploy(ctx, sel.Game, sel.Profile, dopts)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingDeploy{Game: sel.Game, Plan: plan, Opts: dopts, Req: req}, nil
}

// applyDeployKind implements planKind.Apply for "deploy". ctx is the job's
// own context, never the request's (jobs.go), so closing the tab that
// started a deploy cannot tear it up mid-write.
func applyDeployKind(ctx context.Context, s *Server, pending, _ any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingDeploy)
	if !ok {
		return nil, fmt.Errorf("deploy apply: unexpected pending type %T", pending)
	}
	return s.svc.ApplyDeploy(ctx, p.Game, p.Plan, p.Opts, sink)
}

// summarizeDeployResult implements planKind.Summarize for "deploy": the
// handful of numbers a user actually reads off a finished deploy. Rows
// with nothing to say (no warnings, no merged artifact) are omitted rather
// than rendered as zeroes.
func summarizeDeployResult(result any) []resultFact {
	res, ok := result.(*core.DeployResult)
	if !ok {
		return nil
	}

	facts := []resultFact{{Label: "Deployed", Value: strconv.Itoa(res.Deployed)}}
	if len(res.Skipped) > 0 {
		facts = append(facts, resultFact{Label: "Skipped", Value: strconv.Itoa(len(res.Skipped))})
	}
	if res.MergedArtifact != "" {
		facts = append(facts, resultFact{
			Label: "Merged artifact",
			Value: fmt.Sprintf("%s (%d mods, %d raw fallbacks)", res.MergedArtifact, res.MergedMods, res.RawFallbacks),
		})
	}
	for _, note := range res.Notes {
		facts = append(facts, resultFact{Label: "Note", Value: note})
	}
	for _, warning := range res.Warnings {
		facts = append(facts, resultFact{Label: "Warning", Value: warning})
	}
	if len(res.Skipped) > 0 {
		names := make([]string, 0, len(res.Skipped))
		for _, ref := range res.Skipped {
			names = append(names, ref.Name)
		}
		facts = append(facts, resultFact{Label: "Skipped mods", Value: strings.Join(names, ", ")})
	}
	return facts
}

// deployPlanForm implements kindForm.PlanOptions: the confirm page's four
// switches. ModID/SourceID and LinkMethod are not read from a form at all -
// see this file's doc comment.
func deployPlanForm(r *http.Request) (any, error) {
	return deployPlanRequest{
		Purge:     formFlag(r, purgeField),
		All:       formFlag(r, allField),
		Force:     formFlag(r, forceField),
		SkipHooks: formFlag(r, skipHooksField),
	}, nil
}

// deployApplyForm implements kindForm.ApplyOptions: there is nothing to
// read back, because everything a deploy decides was decided at plan time.
func deployApplyForm(*http.Request) (any, error) {
	return deployApplyRequest{}, nil
}

// deployPlanIsCurrent implements kindForm.PlanIsCurrent: the stored plan is
// applicable only if the submission still asks for the options it was
// computed from.
func deployPlanIsCurrent(pending any, r *http.Request) bool {
	p, ok := pending.(*pendingDeploy)
	if !ok {
		return false
	}
	opts, err := deployPlanForm(r)
	if err != nil {
		return false
	}
	req, ok := opts.(deployPlanRequest)
	return ok && req == p.Req
}

// confirmDeployPlan implements kindForm.Confirm: what would be linked,
// what a purge pass would remove first, which hooks would run, and the
// merged-artifact readout on a compile game (#255).
func confirmDeployPlan(pending, _ any) confirmView {
	p, ok := pending.(*pendingDeploy)
	if !ok {
		return confirmView{Submit: "Deploy"}
	}

	plan := p.Plan
	view := confirmView{
		Heading: plan.Profile,
		Submit:  "Deploy",
		Facts: []resultFact{
			{Label: "Profile", Value: plan.Profile},
			{Label: "Mods", Value: strconv.Itoa(len(plan.Mods))},
		},
		Toggles: []confirmToggle{
			{
				Name:    purgeField,
				Label:   "Purge first",
				Help:    "undeploy everything currently in the game directory before deploying",
				Checked: p.Req.Purge,
			},
			{
				Name:    allField,
				Label:   "Include disabled mods",
				Help:    "deploy the profile's disabled mods too",
				Checked: p.Req.All,
			},
			{
				Name:    skipHooksField,
				Label:   "Skip hooks",
				Help:    "run no install.* or uninstall.* hooks at all",
				Checked: p.Req.SkipHooks,
			},
			{
				Name:    forceField,
				Label:   "Continue past a failing before-hook",
				Help:    "the failure is recorded as a warning instead of stopping",
				Checked: p.Req.Force,
			},
		},
	}
	if plan.NoChanges {
		view.Facts = append(view.Facts, resultFact{Label: "Changes", Value: "no mod is selected and no purge pass would run"})
	}
	if plan.Merged != nil {
		view.Facts = append(view.Facts, resultFact{Label: "Merged artifact", Value: mergePlanSummary(plan.Merged)})
	}

	if mods := deployPlanModTexts(plan.Mods); len(mods) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be deployed", Items: mods})
	}
	if len(plan.Purge) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Paths the purge pass would remove first", Items: plan.Purge})
	}
	if len(plan.Hooks) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Hooks that would run", Items: plan.Hooks})
	}
	return view
}

// deployPlanModTexts names each planned mod and the files it would link -
// the deploy plan's whole answer to "what is about to happen to my game
// directory".
func deployPlanModTexts(mods []core.DeployPlanMod) []string {
	if len(mods) == 0 {
		return nil
	}
	items := make([]string, 0, len(mods))
	for _, mod := range mods {
		text := mod.Name
		if mod.Ref.Version != "" {
			text += " " + mod.Ref.Version
		}
		if len(mod.Link) > 0 {
			text += " - " + strings.Join(mod.Link, ", ")
		}
		items = append(items, text)
	}
	return items
}

// mergePlanSummary renders the #255 compile readout as one line.
func mergePlanSummary(merge *core.MergePlan) string {
	return fmt.Sprintf("%s (%d merge source(s), %d raw fallback(s))",
		merge.Artifact, len(merge.Sources), len(merge.RawFallbacks))
}
