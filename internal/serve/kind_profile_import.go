// kind_profile_import.go registers the "profile_import" plan kind - `lmm
// profile import` as a Plan -> confirm -> job flow, the profiles modal's
// Import half (docs/plans/2026-08-31-serve-spa-design.md §Modals:
// "profiles (list/create/rename/delete/export/import/set-default)").
//
// Import is the ONE profile mutation that is a real Plan/Apply pair rather
// than one of the sanctioned single-step writes in api_profiles.go, and for
// the usual reason: it has a genuine preview. PlanImport parses the
// document and sorts every mod it names into three buckets - already
// installed and cached, needs re-downloading, missing entirely - without
// writing anything or touching the network. That is precisely the question
// the CLI asks before it acts ("Download and install mods? [Y/n]"), and it
// is a question the user can only answer once they have seen the answer.
//
// The document itself arrives as TEXT in the request rather than as an
// upload: the SPA reads the file the user picked and posts its contents, so
// this endpoint stays plain JSON like every other one and core keeps
// receiving the same []byte config.ImportProfile has always parsed (YAML or
// JSON - ImportProfile accepts both, since JSON is valid YAML).
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
		Name:         "profile_import",
		PlanOptions:  decodeKindOptions[profileImportPlanRequest],
		ApplyOptions: decodeKindOptions[profileImportApplyRequest],
		Plan:         planProfileImportKind,
		Apply:        applyProfileImportKind,
	})
}

// profileImportPlanRequest is POST /api/v1/plans/profile_import's request
// body: the exported profile document, verbatim, as text. The profile's
// NAME is not a parameter - it is inside the document, which is what makes
// an export portable.
type profileImportPlanRequest struct {
	Data string `json:"data"`
}

// validate implements validatingOptions.
func (r *profileImportPlanRequest) validate() error {
	if r.Data == "" {
		return errors.New(`"data" is required`)
	}
	return nil
}

// profileImportApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a profile_import plan: core.ProfileImportOptions' three
// fields, which are exactly `lmm profile import`'s own flags.
//
// Install is v2 Phase 3 Ruling 1 in its purest form - the decision is fully
// derivable from the plan (its NeedsRedownload/Missing buckets) BEFORE
// Apply runs, so it is an option the confirm modal sets, never a callback
// core reaches back through. NoInstall is the hard override the CLI's
// --no-install is: it wins over Install and counts every pending mod as
// Skipped.
type profileImportApplyRequest struct {
	Install   bool `json:"install,omitzero"`
	Force     bool `json:"force,omitzero"`
	NoInstall bool `json:"no_install,omitzero"`
}

// importOptions renders the request as the core options struct.
func (r profileImportApplyRequest) importOptions() core.ProfileImportOptions {
	return core.ProfileImportOptions{Install: r.Install, Force: r.Force, NoInstall: r.NoInstall}
}

// pendingProfileImport is what the plan store holds between Plan and Apply:
// the plan object itself (pointer identity preserved, so both its
// unexported raw import bytes and its freshness snapshot survive to
// ApplyImport) and the game it was computed for.
type pendingProfileImport struct {
	Game *domain.Game
	Plan *core.ImportPlan
}

// planProfileImportKind implements planKind.Plan for "profile_import".
func planProfileImportKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(profileImportPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("profile import plan: unexpected options type %T", opts)
	}

	// Parse first, so a document that is not a profile export at all is
	// the caller's 400 rather than the server's 500 (errBadPlanRequest).
	// PlanImport parses again for itself; a second parse of a small YAML
	// document is cheaper than a plan API that cannot tell a bad upload
	// from a broken server.
	//
	// ParseProfile (config.ImportProfile) accepts any YAML mapping,
	// nameless ones included - it never checks Name itself. A document
	// with no name parses "successfully" into an empty-named profile that
	// PlanImport would then plan and the job would necessarily fail at
	// SaveProfile, so the empty name is refused here too: it is
	// parseable YAML/JSON, but not a profile export (#332 M2).
	parsed, err := s.svc.NewProfileManager().ParseProfile([]byte(req.Data))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
	}
	if parsed.Name == "" {
		return nil, nil, fmt.Errorf("%w: the document has no profile name - it is not a profile export", errBadPlanRequest)
	}

	plan, err := s.svc.PlanImport(ctx, sel.Game, []byte(req.Data))
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingProfileImport{Game: sel.Game, Plan: plan}, nil
}

// applyProfileImportKind implements planKind.Apply for "profile_import".
func applyProfileImportKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingProfileImport)
	if !ok {
		return nil, fmt.Errorf("profile import apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(profileImportApplyRequest)
	if !ok {
		return nil, fmt.Errorf("profile import apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyImport(ctx, p.Game, p.Plan, req.importOptions(), sink)
}
