// kind_profile_apply.go registers the "profile_apply" plan kind - `lmm
// profile apply` as a Plan -> confirm -> job flow
// (docs/plans/2026-08-30-serve-impl.md Task 9): make what is installed under
// a profile match what the profile lists.
//
// Like switch it takes no options - core.ProfileApplyOptions is
// deliberately empty ("a field nothing reads would be exactly the flag-lies
// trap", internal/core/profile_apply.go) - and its target profile lives in
// the route's path.
//
// Its warning class is on the PLAN rather than only on the result:
// PlanProfileApply resolves every to-install entry against its source and
// records a failure as data (ProfileApplyInstall.Error), keeping the entry
// in place so the apply reports it at that position and carries on. A
// confirm page that showed only the successful entries would let a user
// commit to a convergence that silently cannot converge, so the failures
// are named up front, and the result's own Failed list names them again
// after.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "profile_apply",
		Title:        "Apply profile",
		PlanOptions:  decodeKindOptions[profileApplyPlanRequest],
		ApplyOptions: decodeKindOptions[profileApplyRequest],
		Plan:         planProfileApplyKind,
		Apply:        applyProfileApplyKind,
		Summarize:    summarizeProfileApplyResult,
		Form: &kindForm{
			PlanOptions:  profileApplyPlanForm,
			ApplyOptions: profileApplyForm,
			Confirm:      confirmProfileApplyPlan,
		},
	})
}

// profileApplyPlanRequest is POST /api/v1/plans/profile_apply's request
// body: which profile to converge.
type profileApplyPlanRequest struct {
	Profile string `json:"profile"`
}

// validate implements validatingOptions.
func (r *profileApplyPlanRequest) validate() error {
	if r.Profile == "" {
		return errors.New(`"profile" is required`)
	}
	return nil
}

// profileApplyRequest is the "options" member POST /api/v1/jobs accepts for
// a profile-apply plan: nothing, mirroring core.ProfileApplyOptions.
type profileApplyRequest struct{}

// pendingProfileApply is what the plan store holds between Plan and Apply:
// the plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyProfileApply's staleness check) and
// the game it was computed for.
type pendingProfileApply struct {
	Game *domain.Game
	Plan *core.ProfileApplyPlan
}

// planProfileApplyKind implements planKind.Plan for "profile_apply".
func planProfileApplyKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(profileApplyPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("profile apply plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanProfileApply(ctx, sel.Game, req.Profile)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingProfileApply{Game: sel.Game, Plan: plan}, nil
}

// applyProfileApplyKind implements planKind.Apply for "profile_apply".
func applyProfileApplyKind(ctx context.Context, s *Server, pending, _ any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingProfileApply)
	if !ok {
		return nil, fmt.Errorf("profile apply: unexpected pending type %T", pending)
	}
	return s.svc.ApplyProfileApply(ctx, p.Game, p.Plan, core.ProfileApplyOptions{}, sink)
}

// summarizeProfileApplyResult implements planKind.Summarize for
// "profile_apply".
func summarizeProfileApplyResult(result any) []resultFact {
	res, ok := result.(*core.ProfileApplyResult)
	if !ok {
		return nil
	}

	facts := []resultFact{
		{Label: "Disabled", Value: strconv.Itoa(res.Disabled)},
		{Label: "Enabled", Value: strconv.Itoa(res.Enabled)},
		{Label: "Installed", Value: strconv.Itoa(res.Installed)},
	}
	if res.Replaced > 0 {
		facts = append(facts, resultFact{Label: "Replaced", Value: strconv.Itoa(res.Replaced)})
	}
	for _, ref := range res.Failed {
		facts = append(facts, resultFact{Label: "Failed", Value: installedRefText(ref), Failure: true})
	}
	for _, n := range res.Notes {
		facts = append(facts, resultFact{Label: "Note", Value: n})
	}
	for _, w := range res.Warnings {
		facts = append(facts, resultFact{Label: "Warning", Value: w})
	}
	return facts
}

// profileApplyPlanForm implements kindForm.PlanOptions: the target profile
// comes from the path.
func profileApplyPlanForm(r *http.Request) (any, error) {
	return profileApplyPlanRequest{Profile: r.PathValue("name")}, nil
}

// profileApplyForm implements kindForm.ApplyOptions: nothing to read back.
func profileApplyForm(*http.Request) (any, error) {
	return profileApplyRequest{}, nil
}

// confirmProfileApplyPlan implements kindForm.Confirm: what would be
// installed, what would be removed, and what could not be resolved.
func confirmProfileApplyPlan(pending, _ any) confirmView {
	p, ok := pending.(*pendingProfileApply)
	if !ok {
		return confirmView{Submit: "Apply"}
	}

	plan := p.Plan
	installs, unresolved := profileApplyInstallTexts(plan.ToInstall)
	view := confirmView{
		Heading: plan.Profile,
		Submit:  "Apply",
		Facts: []resultFact{
			{Label: "Profile", Value: plan.Profile},
			// len(installs), not len(plan.ToInstall) (epic live review M6):
			// ToInstall also holds the entries profileApplyInstallTexts
			// splits off as unresolved, which the apply will skip, not
			// install - counting them here overstated "To install" by
			// exactly the number listed separately below as skipped.
			{Label: "To install", Value: strconv.Itoa(len(installs))},
			{Label: "To remove", Value: strconv.Itoa(len(plan.ToDisable))},
		},
	}
	if plan.NoChanges {
		view.Facts = append(view.Facts, resultFact{Label: "Changes", Value: "what is installed already matches this profile"})
	}

	if len(installs) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be installed", Items: installs})
	}
	if names := installedModNames(plan.ToDisable); len(names) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be removed from this profile", Items: names})
	}
	if names := installedModNames(plan.ToEnable); len(names) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be enabled and deployed", Items: names})
	}
	if len(unresolved) > 0 {
		view.Lists = append(view.Lists, confirmList{
			Label: "Entries that could not be resolved, and would be reported and skipped",
			Items: unresolved,
		})
	}
	return view
}

// profileApplyInstallTexts splits the install list into the entries that
// resolved and the ones that did not. The two are separated on the page
// because they are separate decisions: the first is what the apply would
// do, the second is what it already knows it cannot.
func profileApplyInstallTexts(entries []core.ProfileApplyInstall) (installs, unresolved []string) {
	for _, entry := range entries {
		key := domain.ModKey(entry.Ref.SourceID, entry.Ref.ModID)
		if entry.Error != "" {
			unresolved = append(unresolved, key+" - "+entry.Error)
			continue
		}
		text := key
		if entry.Mod != nil && entry.Mod.Name != "" {
			text = entry.Mod.Name
		}
		if entry.Version != "" {
			text += " " + entry.Version
		}
		if entry.Cached {
			text += " (already cached)"
		}
		if entry.Replaces != nil {
			text += " - replacing the installed " + entry.Replaces.Version
		}
		installs = append(installs, text)
	}
	return installs, unresolved
}
