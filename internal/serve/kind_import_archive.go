// kind_import_archive.go registers the "import_archive" plan kind: `lmm
// import <archive>` driven from the browser, over an archive the user
// uploaded (api_uploads.go) rather than a path they typed.
//
// WHAT MAKES THIS KIND DIFFERENT from every other one here.
//
// Its plan carries a SECOND freshness precondition. Every core plan is
// refused when the profile's installed set has moved (checkPlanFresh);
// ImportArchivePlan additionally fingerprints the archive FILE - size and
// mtime at plan time - and ApplyImportArchive refuses a plan whose archive
// changed underneath it (#314's Apply-only-with-fingerprint rule). Over an
// uploaded archive that check can never fire: the staged file is written
// once and afterwards only ever deleted, so nothing can edit it between
// plan and apply. It is kept honest rather than bypassed - the apply runs
// the same check the CLI does, and if the staging directory were ever
// tampered with, the import would refuse exactly as it should.
//
// Its conflict answer is a PLAN-time decision, not a mid-flight one.
// Because #314 made the conflict set computable without ingesting anything,
// ImportArchivePlan.Conflicts is already on the plan document the SPA
// renders, and the Overwrite affordance answers it by re-running with
// AcceptConflicts - one ingest, one identity, one import readout (Ruling
// 18). Apply still recomputes the set from what it actually ingested: one
// that no longer matches the plan's is ErrStalePlan, and a non-empty one
// this option has not answered is *core.ConflictError - the same typed
// refusal install's job surfaces, so the SPA reuses that renderer.
//
// It is also the SPA's "Install from file…" (#535): beside a failed install
// whose source refused the download, install_from_file plus the failure's
// source_id, mod_id and expected_file_id make the import core's install from
// a file - the archive matched against the file the install tried, and a
// mismatch answered by accept_mismatch ("Install anyway"), as
// update_from_archive's is.
//
// The staged archive is removed after a SUCCESSFUL apply and kept after a
// failed one. A failed import is the case where the user most wants to fix
// something (accept the conflicts, pass --force) and try again, and
// re-uploading hundreds of megabytes to do it would be the wrong answer;
// the upload's own TTL is what eventually reclaims it.
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
		Name:         "import_archive",
		PlanOptions:  decodeKindOptions[importArchivePlanRequest],
		ApplyOptions: decodeKindOptions[importArchiveApplyRequest],
		Plan:         planImportArchiveKind,
		Apply:        applyImportArchiveKind,
	})
}

// importArchivePlanRequest is POST /api/v1/plans/import_archive's request
// body.
type importArchivePlanRequest struct {
	// UploadID names the staged archive (POST /api/v1/uploads). It is
	// required, and it is the only way this endpoint can be pointed at a
	// file - there is deliberately no path member.
	UploadID uploadID `json:"upload_id"`
	// SourceID and ModID are `lmm import --source/--id`: setting BOTH turns
	// the import into a source-linked one, whose metadata is fetched and
	// whose file is resolved at PLAN time (that enrichment is what
	// finalises the identity the plan prints). Either alone imports the
	// archive under its own detected identity.
	SourceID string `json:"source_id,omitzero"`
	ModID    string `json:"mod_id,omitzero"`
	// InstallFromFile is "Install from file…" (#535): install the mod
	// SourceID/ModID name (both then required) from the archive, as `lmm
	// install --from-file`. ExpectedFileID is the file the failed install
	// tried (its error's file_id), and Version is --version - required only
	// when the plan answers version_required.
	InstallFromFile bool   `json:"install_from_file,omitzero"`
	ExpectedFileID  string `json:"expected_file_id,omitzero"`
	Version         string `json:"version,omitzero"`
}

// validate implements validatingOptions.
func (r *importArchivePlanRequest) validate() error {
	switch {
	case r.UploadID == "":
		return errors.New(`"upload_id" is required`)
	case r.InstallFromFile && (r.SourceID == "" || r.ModID == ""):
		return errors.New(`"install_from_file" needs "source_id" and "mod_id"`)
	}
	return nil
}

// options is the core options this request plans with - and, with the
// apply's own answers added, applies with.
func (r importArchivePlanRequest) options() core.ImportArchiveOptions {
	return core.ImportArchiveOptions{
		SourceID:        r.SourceID,
		ModID:           r.ModID,
		InstallFromFile: r.InstallFromFile,
		ExpectedFileID:  r.ExpectedFileID,
		Version:         r.Version,
	}
}

// importArchiveApplyRequest is the "options" member POST /api/v1/jobs
// accepts for an import_archive plan.
type importArchiveApplyRequest struct {
	// AcceptConflicts is the Overwrite affordance's answer, and it maps to
	// core.ImportArchiveOptions.AcceptConflicts - NOT to Force. The two are
	// different questions: this one says "overwriting those files is fine",
	// while Force skips the conflict CHECK entirely (GetConflicts is never
	// called) and additionally downgrades a failed install.before_* hook
	// from fatal to a warning. A confirm dialog that answered a conflict
	// list with Force would be silently turning off a hook gate the user
	// never saw.
	AcceptConflicts bool `json:"accept_conflicts,omitzero"`
	// Force and SkipHooks mirror `lmm import --force/--no-hooks`.
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
	// AcceptMismatch is "Install anyway" (#535): the archive is not the
	// file the install would download, and the user installs it regardless.
	AcceptMismatch bool `json:"accept_mismatch,omitzero"`
}

// pendingImportArchive is what the plan store holds between Plan and Apply:
// the plan object (pointer identity preserved, so its unexported
// fingerprint and freshness snapshot survive), the game and profile it was
// computed for, the upload it was computed over, and the plan-time options
// the apply must be given again.
//
// The last one is not redundancy: ApplyImportArchive reads SourceID/ModID
// (and, for an install from a file, the rest of the request) from its own
// opts (it rebuilds ImportOptions from them), so a plan
// computed with --source/--id and applied without them would ingest under a
// different identity than the one the user was shown.
type pendingImportArchive struct {
	Game     *domain.Game
	Profile  string
	Plan     *core.ImportArchivePlan
	UploadID uploadID
	Request  importArchivePlanRequest
}

// planImportArchiveKind implements planKind.Plan for "import_archive".
func planImportArchiveKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(importArchivePlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("import_archive plan: unexpected options type %T", opts)
	}
	staged, ok := s.uploads.Get(req.UploadID)
	if !ok {
		// The caller's own handle is what is wrong, and re-uploading is the
		// fix - a 400, not a 500.
		return nil, nil, fmt.Errorf("%w: no staged upload %q (it expired or was cancelled)", errBadPlanRequest, req.UploadID)
	}
	// Marked in-use from here through the apply that follows (cleared by
	// applyImportArchiveKind either way): an import can run past the
	// upload's 30-minute TTL while still reading the staged file, and
	// without this a sweep triggered by unrelated traffic could reclaim it
	// - and the directory it lives in - mid-read (#333 Minor #2).
	s.uploads.MarkInUse(req.UploadID)

	plan, err := s.svc.PlanImportArchive(ctx, sel.Game, sel.Profile, staged.Path, req.options())
	if err != nil {
		s.uploads.ClearInUse(req.UploadID)
		return nil, nil, err
	}
	return plan, &pendingImportArchive{
		Game:     sel.Game,
		Profile:  sel.Profile,
		Plan:     plan,
		UploadID: req.UploadID,
		Request:  req,
	}, nil
}

// applyImportArchiveKind implements planKind.Apply for "import_archive". It
// removes the staged archive once - and only once - the import has
// succeeded.
func applyImportArchiveKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingImportArchive)
	if !ok {
		return nil, fmt.Errorf("import_archive apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(importArchiveApplyRequest)
	if !ok {
		return nil, fmt.Errorf("import_archive apply: unexpected options type %T", opts)
	}
	// Whatever happens below, this upload's in-use window (opened by
	// planImportArchiveKind) ends here - a successful apply removes the
	// entry outright, and a failed one is just an ordinary TTL'd upload
	// again, eligible for the next sweep like any other.
	defer s.uploads.ClearInUse(p.UploadID)

	applyOpts := p.Request.options()
	applyOpts.AcceptConflicts = req.AcceptConflicts
	applyOpts.Force = req.Force
	applyOpts.SkipHooks = req.SkipHooks
	applyOpts.AcceptMismatch = req.AcceptMismatch
	result, err := s.svc.ApplyImportArchive(ctx, p.Game, p.Profile, p.Plan, applyOpts, sink)
	if err != nil {
		// Keep the staged archive: a refused conflict, a hook failure or a
		// stale plan are all things the user fixes and retries, and the
		// upload's TTL reclaims it if they do not.
		return result, err
	}
	s.uploads.Remove(p.UploadID)
	return result, nil
}
