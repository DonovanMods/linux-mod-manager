// kind_mod_fomod.go registers the "mod_fomod" plan kind - `lmm mod fomod`
// as a Plan -> job flow (#615): the full mod page's "Reconfigure
// installer…".
//
// The flow is core's PlanFomodReconfigure/ApplyFomodReconfigure. Its first
// job always fails with the installer's question (fomod_choices_required,
// preselected from the recorded choices) and writes nothing; the SPA's
// installer wizard asks it and re-posts the plan and the job with the
// answers in "fomod", which installs the new choices as a new cache
// variant and redeploys the mod.
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
		Name:         "mod_fomod",
		PlanOptions:  decodeKindOptions[modFomodPlanRequest],
		ApplyOptions: decodeKindOptions[modFomodApplyRequest],
		Plan:         planModFomodKind,
		Apply:        applyModFomodKind,
	})
}

// modFomodPlanRequest is POST /api/v1/plans/mod_fomod's request body: the
// installed mod whose installer is asked again.
type modFomodPlanRequest struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
}

// validate implements validatingOptions.
func (r *modFomodPlanRequest) validate() error {
	if r.SourceID == "" || r.ModID == "" {
		return errors.New(`"source_id" and "mod_id" are both required`)
	}
	return nil
}

// modFomodApplyRequest is the "options" member POST /api/v1/jobs accepts
// for a mod_fomod plan: the answers, and install's conflict and hook
// switches.
type modFomodApplyRequest struct {
	// Fomod answers the question, keyed by "<source-id>:<mod-id>" - or, for
	// a mod whose files each carry an installer, "<source-id>:<mod-id>#
	// <file-id>" (#634). Without an answer for the mod the job fails with
	// the question.
	Fomod map[string]domain.FomodChoices `json:"fomod,omitzero"`
	// FileID picks which installer of a mod with several is asked again
	// (#634): one of the plan's installers. Empty asks every one.
	FileID          string `json:"file_id,omitzero"`
	AcceptConflicts bool   `json:"accept_conflicts,omitzero"`
	Force           bool   `json:"force,omitzero"`
	SkipHooks       bool   `json:"skip_hooks,omitzero"`
	SkipVerify      bool   `json:"skip_verify,omitzero"`
}

// pendingModFomod is what the plan store holds between Plan and Apply.
type pendingModFomod struct {
	Game *domain.Game
	Plan *core.FomodReconfigurePlan
}

// planModFomodKind implements planKind.Plan for "mod_fomod".
func planModFomodKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(modFomodPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("mod fomod plan: unexpected options type %T", opts)
	}
	plan, err := s.svc.PlanFomodReconfigure(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingModFomod{Game: sel.Game, Plan: plan}, nil
}

// applyModFomodKind implements planKind.Apply for "mod_fomod".
func applyModFomodKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingModFomod)
	if !ok {
		return nil, fmt.Errorf("mod fomod apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(modFomodApplyRequest)
	if !ok {
		return nil, fmt.Errorf("mod fomod apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyFomodReconfigure(ctx, p.Game, p.Plan, core.FomodReconfigureOptions{
		Fomod:           req.Fomod,
		FileID:          req.FileID,
		AcceptConflicts: req.AcceptConflicts,
		Force:           req.Force,
		SkipHooks:       req.SkipHooks,
		SkipVerify:      req.SkipVerify,
	}, sink)
}
