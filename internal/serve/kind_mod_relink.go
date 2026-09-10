// kind_mod_relink.go registers the "mod_relink" plan kind - `lmm mod edit`
// as a Plan -> confirm -> job flow (#326; the epic live review's C-3 named
// it one of four commands with no web path).
//
// The kind is called "mod_relink" rather than "mod_edit" because that is
// what core calls the flow (PlanRelinkMod/ApplyRelinkMod) and what it
// actually does: move one installed mod to a different source_id/mod_id,
// optionally overriding its recorded name, version or author. The CLI's
// command name is the odd one out, not this.
//
// The split follows the flow's own shape (internal/core/mod_edit.go): the
// RE-LINK travels at PLAN time, because it is what the plan describes -
// From, To, whether the target is already installed, and the lock Refusal
// that makes the whole edit impossible - while the METADATA overrides
// travel at APPLY time, because RelinkOptions is what ApplyRelinkMod reads
// them from, and a re-link's metadata refresh from the target source fills
// in whichever of them was left empty.
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
		Name:         "mod_relink",
		PlanOptions:  decodeKindOptions[modRelinkPlanRequest],
		ApplyOptions: decodeKindOptions[modRelinkApplyRequest],
		Plan:         planModRelinkKind,
		Apply:        applyModRelinkKind,
	})
}

// modRelinkPlanRequest is POST /api/v1/plans/mod_relink's request body:
// which installed mod to edit, and where it should point.
type modRelinkPlanRequest struct {
	// ModID and SourceID name the mod as it is installed TODAY. ModID is
	// required; SourceID picks which source's copy when the same id is
	// installed from more than one, exactly as `lmm mod edit`'s --source
	// resolution does.
	ModID    string `json:"mod_id"`
	SourceID string `json:"source_id,omitzero"`
	// NewSourceID and NewModID are `lmm mod edit --to-source/--to-source-id`:
	// leave both empty for a metadata-only edit, or set either to re-link
	// (the one left empty keeps the mod's current value).
	NewSourceID string `json:"new_source_id,omitzero"`
	NewModID    string `json:"new_mod_id,omitzero"`
}

// validate implements validatingOptions: without a mod there is nothing to
// plan.
func (r *modRelinkPlanRequest) validate() error {
	if r.ModID == "" {
		return errors.New(`"mod_id" is required`)
	}
	return nil
}

// modRelinkApplyRequest is the "options" member POST /api/v1/jobs accepts
// for a mod_relink plan - core.RelinkOptions verbatim, `lmm mod edit
// --name/--version/--author`. Each is applied when non-empty; a re-link
// fills the rest from the target source's own metadata.
type modRelinkApplyRequest struct {
	Name    string `json:"name,omitzero"`
	Version string `json:"version,omitzero"`
	Author  string `json:"author,omitzero"`
}

// relinkOptions renders the request as the core options struct.
func (r modRelinkApplyRequest) relinkOptions() core.RelinkOptions {
	return core.RelinkOptions{Name: r.Name, Version: r.Version, Author: r.Author}
}

// pendingModRelink is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyRelinkMod's staleness check) and the
// game it was computed for.
type pendingModRelink struct {
	Game *domain.Game
	Plan *core.RelinkPlan
}

// planModRelinkKind implements planKind.Plan for "mod_relink".
func planModRelinkKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(modRelinkPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("mod relink plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanRelinkMod(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID, req.NewSourceID, req.NewModID)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingModRelink{Game: sel.Game, Plan: plan}, nil
}

// applyModRelinkKind implements planKind.Apply for "mod_relink".
func applyModRelinkKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingModRelink)
	if !ok {
		return nil, fmt.Errorf("mod relink apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(modRelinkApplyRequest)
	if !ok {
		return nil, fmt.Errorf("mod relink apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyRelinkMod(ctx, p.Game, p.Plan, req.relinkOptions(), sink)
}
