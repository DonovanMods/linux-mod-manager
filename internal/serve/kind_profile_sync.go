// kind_profile_sync.go registers the "profile_sync" plan kind - `lmm
// profile sync` as a Plan -> confirm -> job flow (#326; the epic live
// review's C-3 named it one of four commands with no web path).
//
// It is the repair flow: the DB says a mod is installed and enabled, the
// profile's own list disagrees, and the sync makes the list match the DB.
// The plan is the whole disclosure - PlanProfileSync computes the three
// buckets (to_add, to_remove, to_update) with zero side effects, so the
// confirm modal shows the exact diff the job will write.
//
// Like switch and profile_apply it takes its target profile in the BODY
// rather than from the ?profile= selection: all three act on a profile as
// an object, and a repair is routinely run against one you are not
// currently looking at. ApplyProfileSync takes no options at all (`lmm
// profile sync` has no flags beyond --yes/--dry-run, both of which are the
// confirm modal itself here), so the apply half declares the empty struct
// for the same reason switch does: passing options must be a 400, not a
// silent no-op.
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
		Name:         "profile_sync",
		PlanOptions:  decodeKindOptions[profileSyncPlanRequest],
		ApplyOptions: decodeKindOptions[profileSyncApplyRequest],
		Plan:         planProfileSyncKind,
		Apply:        applyProfileSyncKind,
	})
}

// profileSyncPlanRequest is POST /api/v1/plans/profile_sync's request
// body: which profile to reconcile against the database.
type profileSyncPlanRequest struct {
	Profile string `json:"profile"`
}

// validate implements validatingOptions.
func (r *profileSyncPlanRequest) validate() error {
	if r.Profile == "" {
		return errors.New(`"profile" is required`)
	}
	return nil
}

// profileSyncApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a profile-sync plan: nothing, mirroring ApplyProfileSync's
// own signature.
type profileSyncApplyRequest struct{}

// pendingProfileSync is what the plan store holds between Plan and Apply:
// the plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyProfileSync's staleness check) and
// the game it was computed for.
type pendingProfileSync struct {
	Game *domain.Game
	Plan *core.ProfileSyncPlan
}

// planProfileSyncKind implements planKind.Plan for "profile_sync".
func planProfileSyncKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(profileSyncPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("profile sync plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanProfileSync(ctx, sel.Game, req.Profile)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingProfileSync{Game: sel.Game, Plan: plan}, nil
}

// applyProfileSyncKind implements planKind.Apply for "profile_sync".
func applyProfileSyncKind(ctx context.Context, s *Server, pending, _ any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingProfileSync)
	if !ok {
		return nil, fmt.Errorf("profile sync apply: unexpected pending type %T", pending)
	}
	return s.svc.ApplyProfileSync(ctx, p.Game, p.Plan, sink)
}
