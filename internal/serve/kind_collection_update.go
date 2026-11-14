// kind_collection_update.go registers the "collection_update" plan kind
// (#613): `lmm profile sync --collection` as a Plan -> confirm -> job flow,
// the profiles modal's "Update collection…" on a profile whose collection
// has an origin to update from (core.ProfileSummary.CollectionOrigin).
//
// The plan is core's CollectionUpdatePlan verbatim: the entries the new
// version adds, changes, drops and conflicts with, and the new document's
// gate. Every choice the CLI takes as a flag is an apply-time option here,
// so the confirm modal's checkboxes - which obsolete members to remove,
// which conflicts to resolve the collection's way - never re-fetch the
// collection: --remove/--remove-obsolete are remove/remove_obsolete,
// --override/--override-conflicts override/override_conflicts, and
// --allow-any-overrides allow_any_overrides. --with-optional is a plan-time
// option, and so is --optional, as on the import (they change what the
// plan converts). Installer
// questions come back as the job's failure, answered by re-posting with
// "fomod", as every FOMOD round trip is.
//
// Like profile_sync it names its target profile in the body: a collection
// is routinely updated from the profiles modal for a profile you are not
// in.
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
		Name:         "collection_update",
		PlanOptions:  decodeKindOptions[collectionUpdatePlanRequest],
		ApplyOptions: decodeKindOptions[collectionUpdateApplyRequest],
		Plan:         planCollectionUpdateKind,
		Apply:        applyCollectionUpdateKind,
	})
}

// collectionUpdatePlanRequest is POST /api/v1/plans/collection_update's
// request body: the profile to update, and which of a source collection's
// optional items to opt in - every one (`--with-optional`) or those named
// by core.ConvertedCollectionItem.ID (`--optional`), as the plan view's
// per-item toggles re-plan with.
type collectionUpdatePlanRequest struct {
	Profile      string   `json:"profile"`
	WithOptional bool     `json:"with_optional,omitzero"`
	Optional     []string `json:"optional,omitzero"`
}

// validate implements validatingOptions.
func (r *collectionUpdatePlanRequest) validate() error {
	if r.Profile == "" {
		return errors.New(`"profile" is required`)
	}
	return nil
}

// collectionUpdateApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a collection_update plan: core.CollectionUpdateOptions, which
// are exactly `lmm profile sync --collection`'s own flags. Each key names
// an entry as "<source-id>:<mod-id>" (core.CollectionUpdateItem.Key).
type collectionUpdateApplyRequest struct {
	Remove            []string                       `json:"remove,omitzero"`
	RemoveObsolete    bool                           `json:"remove_obsolete,omitzero"`
	Override          []string                       `json:"override,omitzero"`
	OverrideConflicts bool                           `json:"override_conflicts,omitzero"`
	AllowAnyOverrides bool                           `json:"allow_any_overrides,omitzero"`
	Fomod             map[string]domain.FomodChoices `json:"fomod,omitzero"`
}

// updateOptions renders the request as the core options struct.
func (r collectionUpdateApplyRequest) updateOptions() core.CollectionUpdateOptions {
	return core.CollectionUpdateOptions{
		Remove: r.Remove, RemoveObsolete: r.RemoveObsolete,
		Override: r.Override, OverrideConflicts: r.OverrideConflicts,
		AllowAnyOverrides: r.AllowAnyOverrides, Fomod: r.Fomod,
	}
}

// pendingCollectionUpdate is what the plan store holds between Plan and
// Apply: the plan object itself (its unexported base profile, document and
// snapshot survive to ApplyCollectionUpdate) and its game.
type pendingCollectionUpdate struct {
	Game *domain.Game
	Plan *core.CollectionUpdatePlan
}

// planCollectionUpdateKind implements planKind.Plan for
// "collection_update".
func planCollectionUpdateKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(collectionUpdatePlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("collection update plan: unexpected options type %T", opts)
	}
	plan, err := s.svc.PlanCollectionUpdate(ctx, sel.Game, core.CollectionUpdateRequest{
		Profile: req.Profile, WithOptional: req.WithOptional, Optional: req.Optional,
	})
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingCollectionUpdate{Game: sel.Game, Plan: plan}, nil
}

// applyCollectionUpdateKind implements planKind.Apply for
// "collection_update".
func applyCollectionUpdateKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingCollectionUpdate)
	if !ok {
		return nil, fmt.Errorf("collection update apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(collectionUpdateApplyRequest)
	if !ok {
		return nil, fmt.Errorf("collection update apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyCollectionUpdate(ctx, p.Game, p.Plan, req.updateOptions(), sink)
}
