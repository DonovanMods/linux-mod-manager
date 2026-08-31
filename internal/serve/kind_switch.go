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
