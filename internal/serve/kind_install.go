// kind_install.go registers the "install" plan kind - Task 8's second
// Plan -> confirm -> Apply flow, and the one whose shape the whole unit is
// built around (docs/plans/2026-08-30-serve-impl.md Task 8).
//
// Two things make install different from every other mutation here.
//
// #225, the selection: what a user most wants to decide before installing
// is WHICH version and WHICH file. PlanInstall answers neither on its own -
// it takes no version and its Files is the non-interactive default pick
// ("INTERACTIVE selection is the CALLER's job", internal/core/install.go).
// So the confirm page asks, and the answer travels as
// InstallOptions.TargetVersion / TargetFileIDs, which ApplyInstall resolves
// up front - the sanctioned core path (#96/#140), not a plan.Files
// overwrite of our own. The candidate pool the picker renders is computed
// here at plan time, and a pool of one renders no picker at all: "file
// selection where the plan offers it" means offering a choice only where
// there is one.
//
// The conflict gate: installing can only discover its conflicts AFTER the
// download, because installer.GetConflicts reads the cache
// (core.InstallOptions.AcceptConflicts). An unaccepted conflict is
// therefore not a plan-time warning but a *core.ConflictError from the
// Apply - a failed job, whose page renders the stored conflict list and
// offers Overwrite. That re-run finds the cache warm and downloads nothing,
// which is exactly why the refusal is cheap enough to be the default.
package serve

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The install confirm form's own field names (#225 plus the conflict
// answer). fileField is repeated once per ticked checkbox, which is how a
// browser submits a multi-select.
const (
	versionField         = "version"
	fileField            = "file"
	acceptConflictsField = "accept_conflicts"
	showArchivedField    = "show_archived"
)

func init() {
	registerPlanKind(planKind{
		Name:         "install",
		Title:        "Install",
		PlanOptions:  decodeKindOptions[installPlanRequest],
		ApplyOptions: decodeKindOptions[installApplyRequest],
		Plan:         planInstallKind,
		Apply:        applyInstallKind,
	})
}

// installPlanRequest is POST /api/v1/plans/install's request body.
type installPlanRequest struct {
	// SourceID and ModID name the mod to install; both are required, since
	// unlike uninstall there is no installed set to disambiguate against.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Version, when set, is the version a frontend intends to preview and
	// preselect (#225). It has never changed what PlanInstall computes -
	// that method takes no version - and with the server-rendered confirm
	// page gone (docs/plans/2026-08-31-serve-spa-design.md) nothing reads it
	// at plan time today: it is a frozen part of this request's wire shape
	// (testdata/json/install_plan_request.golden), and the pick that
	// actually applies travels as installApplyRequest.Version. The SPA's
	// install modal re-surfaces the version/file pool in Unit 5.
	Version string `json:"version,omitzero"`
	// ShowArchived mirrors `lmm install --show-archived`: it widens both the
	// plan's own file filter and the candidate pool.
	ShowArchived bool `json:"show_archived,omitzero"`
}

// validate implements validatingOptions.
func (r *installPlanRequest) validate() error {
	if r.SourceID == "" || r.ModID == "" {
		return errors.New(`"source_id" and "mod_id" are both required`)
	}
	return nil
}

// installApplyRequest is the "options" member POST /api/v1/jobs accepts for
// an install plan - every decision a confirm page can still change.
type installApplyRequest struct {
	// Version and FileIDs are #225's picks, carried into the core options
	// ApplyInstall resolves up front (#96/#140).
	Version string   `json:"version,omitzero"`
	FileIDs []string `json:"file_ids,omitzero"`
	// AcceptConflicts is the answer to a refused conflict - the mid-flight
	// decision v2 Phase 3 Ruling 1 says a caller answers by re-running
	// Apply, never by a callback.
	AcceptConflicts bool `json:"accept_conflicts,omitzero"`
	// Force and SkipHooks mirror `lmm install --force/--no-hooks`.
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// installOptions renders the request as the core options struct.
func (r installApplyRequest) installOptions() core.InstallOptions {
	return core.InstallOptions{
		TargetVersion:   r.Version,
		TargetFileIDs:   r.FileIDs,
		AcceptConflicts: r.AcceptConflicts,
		Force:           r.Force,
		SkipHooks:       r.SkipHooks,
	}
}

// pendingInstall is what the plan store holds between Plan and Apply: the
// plan object itself (pointer identity preserved, so its unexported
// freshness snapshot survives to ApplyInstall's staleness check) and the
// game it was computed for.
type pendingInstall struct {
	Game *domain.Game
	Plan *core.InstallPlan
}

// planInstallKind implements planKind.Plan for "install".
func planInstallKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(installPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("install plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanInstall(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID, req.ShowArchived)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingInstall{Game: sel.Game, Plan: plan}, nil
}

// applyInstallKind implements planKind.Apply for "install".
func applyInstallKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingInstall)
	if !ok {
		return nil, fmt.Errorf("install apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(installApplyRequest)
	if !ok {
		return nil, fmt.Errorf("install apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyInstall(ctx, p.Game, p.Plan, req.installOptions(), sink)
}
