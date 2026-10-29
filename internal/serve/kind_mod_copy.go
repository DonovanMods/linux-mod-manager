// kind_mod_copy.go registers the "mod_copy" plan kind - `lmm mod copy` as a
// Plan -> confirm -> job flow (#437): one mod of the selected profile put
// into another profile of the same game, from the cache, without asking any
// source anything.
//
// The split follows core's own (internal/core/mod_copy.go). WHERE to copy
// travels at PLAN time - the destination, the mod (or all of them) -
// because it is what the plan describes: what is added, at which version,
// whether the destination already lists it, whether it is the active
// profile (deploys) or not (records only). The copy's FROM is the selected
// profile (?profile=), exactly as every other mod-scoped kind reads its
// profile, so the library row a user clicked is always the one copied.
//
// Replace travels at APPLY time, because core.CopyModOptions is where
// ApplyCopyMod reads it. Unlike install's conflict gate (#225), which can
// only discover a conflict after the download and so surfaces it as a
// failed job the user answers with Overwrite, a copy's collision is known
// at PLAN time: the plan's AlreadyListed says so, and the confirm modal's
// Confirm IS the answer to it - plan_mod_copy.js labels the click
// "Replace" and main.js#confirmPlan sends replace for exactly that plan,
// the accept_mismatch pattern update_from_archive established (#530). A
// destination that came to list the mod between Plan and Apply still gets
// core's *core.ModAlreadyListedError as the job's failure, whose sentence
// names the remedy.
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
		Name:         "mod_copy",
		PlanOptions:  decodeKindOptions[modCopyPlanRequest],
		ApplyOptions: decodeKindOptions[modCopyApplyRequest],
		Plan:         planModCopyKind,
		Apply:        applyModCopyKind,
	})
}

// modCopyPlanRequest is POST /api/v1/plans/mod_copy's request body: where
// to copy, and what. The profile copied FROM is the selection's.
type modCopyPlanRequest struct {
	// To is the destination profile, another profile of the same game.
	To string `json:"to"`
	// SourceID and ModID name the one mod to copy, as the selected profile
	// has it installed. Both are required unless All is set.
	SourceID string `json:"source_id,omitzero"`
	ModID    string `json:"mod_id,omitzero"`
	// All is `lmm mod copy --all`: every mod the selected profile lists,
	// SourceID/ModID then left empty. Mods the plan cannot copy are
	// reported in its Skipped rather than refusing the whole copy.
	All bool `json:"all,omitzero"`
}

// validate implements validatingOptions: a destination is required, and
// exactly one of "a mod" and "all".
func (r *modCopyPlanRequest) validate() error {
	if r.To == "" {
		return errors.New(`"to" is required`)
	}
	switch {
	case r.All && (r.SourceID != "" || r.ModID != ""):
		return errors.New(`"all" copies every mod - leave "source_id" and "mod_id" empty`)
	case !r.All && (r.SourceID == "" || r.ModID == ""):
		return errors.New(`"source_id" and "mod_id" are both required unless "all" is set`)
	}
	return nil
}

// modCopyApplyRequest is the "options" member POST /api/v1/jobs accepts for
// a mod_copy plan - core.CopyModOptions verbatim.
type modCopyApplyRequest struct {
	// Replace overwrites a destination that already lists the mod (keeping
	// its load-order position). Without it a single-mod copy of a listed
	// mod fails with *core.ModAlreadyListedError, and an all-copy skips it.
	Replace bool `json:"replace,omitzero"`
}

// pendingModCopy is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshots survive to ApplyCopyMod's staleness check) and the
// game it was computed for.
type pendingModCopy struct {
	Game *domain.Game
	Plan *core.CopyModPlan
}

// planModCopyKind implements planKind.Plan for "mod_copy". A destination
// that IS the selected profile is the caller's mistake (400), checked here
// because core answers it with an untyped error.
func planModCopyKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(modCopyPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("mod copy plan: unexpected options type %T", opts)
	}
	if req.To == sel.Profile {
		return nil, nil, fmt.Errorf("%w: cannot copy into profile %q - it is the profile being copied from", errBadPlanRequest, req.To)
	}

	plan, err := s.svc.PlanCopyMod(ctx, sel.Game, core.CopyModRequest{
		From:     sel.Profile,
		To:       req.To,
		SourceID: req.SourceID,
		ModID:    req.ModID,
		All:      req.All,
	})
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingModCopy{Game: sel.Game, Plan: plan}, nil
}

// applyModCopyKind implements planKind.Apply for "mod_copy".
func applyModCopyKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingModCopy)
	if !ok {
		return nil, fmt.Errorf("mod copy apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(modCopyApplyRequest)
	if !ok {
		return nil, fmt.Errorf("mod copy apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyCopyMod(ctx, p.Game, p.Plan, core.CopyModOptions{Replace: req.Replace}, sink)
}
