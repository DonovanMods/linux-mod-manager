// kind_update_from_archive.go registers the "update_from_archive" plan kind
// (#530): `lmm update <mod> --from-file <archive>` driven from the browser,
// over an archive the user uploaded (api_uploads.go). It is the SPA's
// "Update from file...", offered on a mod and beside a failed download whose
// source refused it.
//
// It shares import_archive's upload lifecycle, for the same reasons (see
// kind_import_archive.go): the staged archive is marked in use from the plan
// through the apply, the plan's archive fingerprint is re-checked by core
// rather than bypassed, and the upload is removed only after a SUCCESSFUL
// apply - a refused mismatch or a failed hook is something the user answers
// and retries, and re-uploading the archive to do it would be the wrong
// answer.
//
// The one decision this kind carries is the filename gate. The plan says
// whether the archive is the file the update check advertised
// (UpdateFromArchivePlan.Match); a mismatch is answered with
// accept_mismatch, the SPA's "Update anyway", and Apply refuses it with
// *core.ArchiveMismatchError otherwise (Ruling 1).
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
		Name:         "update_from_archive",
		PlanOptions:  decodeKindOptions[updateFromArchivePlanRequest],
		ApplyOptions: decodeKindOptions[updateFromArchiveApplyRequest],
		Plan:         planUpdateFromArchiveKind,
		Apply:        applyUpdateFromArchiveKind,
	})
}

// updateFromArchivePlanRequest is POST /api/v1/plans/update_from_archive's
// request body.
type updateFromArchivePlanRequest struct {
	// UploadID names the staged archive (POST /api/v1/uploads); there is
	// deliberately no path member.
	UploadID uploadID `json:"upload_id"`
	// SourceID and ModID name the installed mod to update.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Version is `--version`: required only when the plan answers
	// version_required.
	Version string `json:"version,omitzero"`
}

// validate implements validatingOptions.
func (r *updateFromArchivePlanRequest) validate() error {
	switch {
	case r.UploadID == "":
		return errors.New(`"upload_id" is required`)
	case r.SourceID == "" || r.ModID == "":
		return errors.New(`"source_id" and "mod_id" are required`)
	}
	return nil
}

// updateFromArchiveApplyRequest is the "options" member POST /api/v1/jobs
// accepts for an update_from_archive plan.
type updateFromArchiveApplyRequest struct {
	// AcceptMismatch is "Update anyway": the archive is not the file the
	// update check advertised, and the user updates from it regardless.
	AcceptMismatch bool `json:"accept_mismatch,omitzero"`
	// Force and SkipHooks mirror `lmm update --force/--no-hooks`.
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// pendingUpdateFromArchive is what the plan store holds between Plan and
// Apply: the plan object (its unexported fingerprint and freshness snapshot
// survive by pointer identity), the game, the upload it was computed over,
// and the plan-time version, which the apply is given again.
type pendingUpdateFromArchive struct {
	Game     *domain.Game
	Plan     *core.UpdateFromArchivePlan
	UploadID uploadID
	Version  string
}

// planUpdateFromArchiveKind implements planKind.Plan for
// "update_from_archive".
func planUpdateFromArchiveKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(updateFromArchivePlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("update_from_archive plan: unexpected options type %T", opts)
	}
	staged, ok := s.uploads.Get(req.UploadID)
	if !ok {
		return nil, nil, fmt.Errorf("%w: no staged upload %q (it expired or was cancelled)", errBadPlanRequest, req.UploadID)
	}
	// In use from here through the apply (cleared by the apply either way),
	// as import_archive's: a sweep must not reclaim a file being read.
	s.uploads.MarkInUse(req.UploadID)

	plan, err := s.svc.PlanUpdateFromArchive(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID, staged.Path,
		core.UpdateFromArchiveOptions{Version: req.Version})
	if err != nil {
		s.uploads.ClearInUse(req.UploadID)
		return nil, nil, err
	}
	return plan, &pendingUpdateFromArchive{Game: sel.Game, Plan: plan, UploadID: req.UploadID, Version: req.Version}, nil
}

// applyUpdateFromArchiveKind implements planKind.Apply for
// "update_from_archive". It removes the staged archive once - and only once -
// the update has succeeded.
func applyUpdateFromArchiveKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingUpdateFromArchive)
	if !ok {
		return nil, fmt.Errorf("update_from_archive apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(updateFromArchiveApplyRequest)
	if !ok {
		return nil, fmt.Errorf("update_from_archive apply: unexpected options type %T", opts)
	}
	defer s.uploads.ClearInUse(p.UploadID)

	result, err := s.svc.ApplyUpdateFromArchive(ctx, p.Game, p.Plan, core.UpdateFromArchiveOptions{
		Version:        p.Version,
		AcceptMismatch: req.AcceptMismatch,
		Force:          req.Force,
		SkipHooks:      req.SkipHooks,
	}, sink)
	if err != nil {
		return result, err
	}
	s.uploads.Remove(p.UploadID)
	return result, nil
}
