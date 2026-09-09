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

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "profile_apply",
		PlanOptions:  decodeKindOptions[profileApplyPlanRequest],
		ApplyOptions: decodeKindOptions[profileApplyRequest],
		Plan:         planProfileApplyKind,
		Apply:        applyProfileApplyKind,
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
