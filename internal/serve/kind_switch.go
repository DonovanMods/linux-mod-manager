// kind_switch.go registers the "switch" plan kind - `lmm profile switch` as
// a Plan -> confirm -> job flow (docs/plans/2026-08-30-serve-impl.md Task
// 9).
//
// The target profile is the ONE thing a switch decides, and it lives in the
// route's path (/profiles/{name}/switch), so this kind offers no options at
// all: ApplyProfileSwitch takes none either ("profile switch takes no CLI
// flags beyond the target profile name", internal/core/switch.go).
//
// What the confirm page has to earn its keep with is the diff and the
// warning class #294 exists for. The diff is what PlanProfileSwitch already
// computed - which mods get undeployed, which get re-enabled under the
// target, which have to be downloaded. The warning is the locked ref: a
// profile entry locked at one version whose install records another is
// exactly the case ProfileManager.UpsertMod refuses, and the switch reports
// that refusal as a SwitchResult Warning rather than a --verbose note,
// because it leaves the DB row and the profile record disagreeing. Naming
// those refs on the confirm page says so BEFORE the install happens; the
// job's own result says so after.
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
		Name:         "switch",
		Title:        "Switch profile",
		PlanOptions:  decodeKindOptions[switchPlanRequest],
		ApplyOptions: decodeKindOptions[switchApplyRequest],
		Plan:         planSwitchKind,
		Apply:        applySwitchKind,
		Summarize:    summarizeSwitchResult,
		Form: &kindForm{
			PlanOptions:  switchPlanForm,
			ApplyOptions: switchApplyForm,
			Confirm:      confirmSwitchPlan,
		},
	})
}

// switchPlanRequest is POST /api/v1/plans/switch's request body: which
// profile to switch TO. The profile to switch FROM is never a parameter -
// it is whichever one is currently active, which is what makes a switch a
// switch.
type switchPlanRequest struct {
	Profile string `json:"profile"`
}

// validate implements validatingOptions.
func (r *switchPlanRequest) validate() error {
	if r.Profile == "" {
		return errors.New(`"profile" is required`)
	}
	return nil
}

// switchApplyRequest is the "options" member POST /api/v1/jobs accepts for
// a switch plan: nothing. Declaring the empty struct rather than skipping
// the decoder is what makes an attempt to pass options a 400 rather than a
// silent no-op - the same reason deployApplyRequest exists.
type switchApplyRequest struct{}

// pendingSwitch is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyProfileSwitch's staleness check) and
// the game it was computed for.
type pendingSwitch struct {
	Game *domain.Game
	Plan *core.SwitchPlan
}

// planSwitchKind implements planKind.Plan for "switch".
func planSwitchKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(switchPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("switch plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanProfileSwitch(ctx, sel.Game, req.Profile)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingSwitch{Game: sel.Game, Plan: plan}, nil
}

// applySwitchKind implements planKind.Apply for "switch".
func applySwitchKind(ctx context.Context, s *Server, pending, _ any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingSwitch)
	if !ok {
		return nil, fmt.Errorf("switch apply: unexpected pending type %T", pending)
	}
	return s.svc.ApplyProfileSwitch(ctx, p.Game, p.Plan, sink)
}

// summarizeSwitchResult implements planKind.Summarize for "switch". The
// Warnings come last and are never omitted: #294's whole point is that they
// reach the user without a verbosity flag standing in the way.
func summarizeSwitchResult(result any) []resultFact {
	res, ok := result.(*core.SwitchResult)
	if !ok {
		return nil
	}

	facts := []resultFact{
		{Label: "Disabled", Value: strconv.Itoa(res.Disabled)},
		{Label: "Enabled", Value: strconv.Itoa(res.Enabled)},
		{Label: "Installed", Value: strconv.Itoa(res.Installed)},
	}
	for _, n := range res.Notes {
		facts = append(facts, resultFact{Label: "Note", Value: n})
	}
	for _, w := range res.Warnings {
		facts = append(facts, resultFact{Label: "Warning", Value: w})
	}
	return facts
}

// switchPlanForm implements kindForm.PlanOptions: the target profile comes
// from the path, never the body, so a submission can never switch to a
// different profile than the button the user pressed.
func switchPlanForm(r *http.Request) (any, error) {
	return switchPlanRequest{Profile: r.PathValue("name")}, nil
}

// switchApplyForm implements kindForm.ApplyOptions: there is nothing to
// read back.
func switchApplyForm(*http.Request) (any, error) {
	return switchApplyRequest{}, nil
}

// confirmSwitchPlan implements kindForm.Confirm: the diff, and the locked
// refs whose profile record the switch will not be able to update.
func confirmSwitchPlan(pending, _ any) confirmView {
	p, ok := pending.(*pendingSwitch)
	if !ok {
		return confirmView{Submit: "Switch"}
	}

	plan := p.Plan
	view := confirmView{
		Heading: fmt.Sprintf("%s -> %s", plan.From, plan.To),
		Submit:  "Switch",
		Facts: []resultFact{
			{Label: "From", Value: plan.From},
			{Label: "To", Value: plan.To},
		},
	}
	switch {
	case plan.AlreadyActive:
		view.Facts = append(view.Facts, resultFact{Label: "Changes", Value: plan.To + " is already the active profile"})
	case plan.NoChanges:
		view.Facts = append(view.Facts, resultFact{Label: "Changes", Value: "the same mods are in both profiles; only the active profile would move"})
	}

	if names := installedModNames(plan.ToDisable); len(names) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be disabled and undeployed", Items: names})
	}
	if names := installedModNames(plan.ToEnable); len(names) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be enabled and deployed", Items: names})
	}
	if refs := modRefTexts(plan.ToInstall); len(refs) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be downloaded and installed", Items: refs})
	}
	if locked := lockedRefWarnings(plan.ToInstall); len(locked) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Locked profile entries", Items: locked})
	}
	return view
}

// installedModNames lists installed mods by name and version, for the two
// sides of a diff that carry whole rows.
func installedModNames(mods []domain.InstalledMod) []string {
	if len(mods) == 0 {
		return nil
	}
	names := make([]string, 0, len(mods))
	for _, m := range mods {
		names = append(names, m.Name+" "+m.Version)
	}
	return names
}

// modRefTexts names references a plan carries with no resolved mod behind
// them yet - a profile's own entries, which have an id and a version but no
// name until the source is asked.
func modRefTexts(refs []domain.ModReference) []string {
	if len(refs) == 0 {
		return nil
	}
	items := make([]string, 0, len(refs))
	for _, ref := range refs {
		text := domain.ModKey(ref.SourceID, ref.ModID)
		if ref.Version != "" {
			text += " " + ref.Version
		}
		items = append(items, text)
	}
	return items
}

// lockedRefWarnings names the refs whose lock will refuse the switch's own
// profile write (#294). It is warning-class rather than a plain list entry:
// the install still happens, the DB row still moves, and the profile record
// silently does not - which is precisely why core reports it
// unconditionally instead of as a --verbose note.
func lockedRefWarnings(refs []domain.ModReference) []string {
	var locked []string
	for _, ref := range refs {
		if !ref.Locked {
			continue
		}
		locked = append(locked, fmt.Sprintf(
			"%s is locked at %s - if the install records a different version, its profile entry will not be updated and the switch will report a warning",
			domain.ModKey(ref.SourceID, ref.ModID), ref.Version))
	}
	return locked
}
