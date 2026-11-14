// kind_profile_fill.go registers the "profile_fill" plan kind: `lmm profile
// fill` driven from the browser (#608). A collection profile a free account
// imported records every mod pending; the user downloads the files by hand
// and drops them on the web UI, which uploads them (api_uploads.go) and
// plans the fill over the staged uploads - the same matching, by size and
// md5 against the collection's recipe, that the CLI runs over a directory.
//
// Or the user picks a folder on this machine with the folder chooser
// (folderpicker.js, GET /api/v1/fs/dirs) and the plan names it: the server
// scans it exactly as `lmm profile fill <dir>` does - its top level, no
// link followed, a file read only when its size is a pending file's - and
// nothing is uploaded, so neither the 2 GiB upload cap nor the four
// uploads a plan can name apply. The folder must be an absolute path to a
// real directory (core.ErrFillFolderInvalid, a 400).
//
// A plan with no uploads at all is allowed, and is how the SPA learns the
// pending files' sizes before uploading anything: a dropped file whose size
// is no pending file's is never uploaded, as the CLI never reads one.
//
// The staged uploads are removed after a SUCCESSFUL apply and kept after a
// failed one (a FOMOD question re-plans over them), as import_archive's
// are; the uploads' own TTL reclaims what nobody comes back for.
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
		Name:         "profile_fill",
		PlanOptions:  decodeKindOptions[profileFillPlanRequest],
		ApplyOptions: decodeKindOptions[profileFillApplyRequest],
		Plan:         planProfileFillKind,
		Apply:        applyProfileFillKind,
	})
}

// maxFillUploads bounds one plan's uploads: the upload store holds
// defaultUploadStoreCap archives at once, so a plan naming more names
// uploads the store has already evicted.
const maxFillUploads = defaultUploadStoreCap

// profileFillPlanRequest is POST /api/v1/plans/profile_fill's request body.
// The profile is the request's ?profile= selection.
type profileFillPlanRequest struct {
	// UploadIDs name the staged archives (POST /api/v1/uploads) to match
	// against the profile's pending pins. Empty plans the pending pins
	// alone.
	UploadIDs []uploadID `json:"upload_ids,omitzero"`
	// Folder is a folder on this machine to scan instead: an absolute path
	// the folder chooser gave. It cannot be combined with UploadIDs.
	Folder string `json:"folder,omitzero"`
}

// validate implements validatingOptions.
func (r *profileFillPlanRequest) validate() error {
	if len(r.UploadIDs) > maxFillUploads {
		return fmt.Errorf(`"upload_ids" names %d uploads; one plan takes at most %d`, len(r.UploadIDs), maxFillUploads)
	}
	if r.Folder != "" && len(r.UploadIDs) > 0 {
		return errors.New(`"folder" and "upload_ids" are two ways to name the files; send one`)
	}
	for _, id := range r.UploadIDs {
		if id == "" {
			return errors.New(`"upload_ids" holds an empty id`)
		}
	}
	return nil
}

// profileFillApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a profile_fill plan.
type profileFillApplyRequest struct {
	// Fomod answers FOMOD installer questions, keyed by
	// "<source-id>:<mod-id>", that a job refused with a
	// fomod_choices_required question for (#615's wizard).
	Fomod map[string]domain.FomodChoices `json:"fomod,omitzero"`
}

// pendingProfileFill is what the plan store holds between Plan and Apply.
type pendingProfileFill struct {
	Game      *domain.Game
	Plan      *core.ProfileFillPlan
	UploadIDs []uploadID
}

// planProfileFillKind implements planKind.Plan for "profile_fill".
func planProfileFillKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(profileFillPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("profile_fill plan: unexpected options type %T", opts)
	}
	archives := make([]core.FillArchive, 0, len(req.UploadIDs))
	for _, id := range req.UploadIDs {
		staged, ok := s.uploads.Get(id)
		if !ok {
			s.clearInUse(req.UploadIDs)
			return nil, nil, fmt.Errorf("%w: no staged upload %q (it expired or was cancelled)", errBadPlanRequest, id)
		}
		// In use from here through the apply (see planImportArchiveKind).
		s.uploads.MarkInUse(id)
		archives = append(archives, core.FillArchive{Path: staged.Path, Name: staged.Filename})
	}
	plan, err := s.svc.PlanProfileFill(ctx, sel.Game, core.ProfileFillRequest{Profile: sel.Profile, Archives: archives, Folder: req.Folder})
	if err != nil {
		s.clearInUse(req.UploadIDs)
		if errors.Is(err, core.ErrProfileNotFillable) || errors.Is(err, core.ErrFillFolderInvalid) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
		return nil, nil, err
	}
	return plan, &pendingProfileFill{Game: sel.Game, Plan: plan, UploadIDs: req.UploadIDs}, nil
}

// clearInUse ends ids' in-use windows.
func (s *Server) clearInUse(ids []uploadID) {
	for _, id := range ids {
		s.uploads.ClearInUse(id)
	}
}

// applyProfileFillKind implements planKind.Apply for "profile_fill". It
// removes the staged uploads once - and only once - the fill succeeded.
func applyProfileFillKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingProfileFill)
	if !ok {
		return nil, fmt.Errorf("profile_fill apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(profileFillApplyRequest)
	if !ok {
		return nil, fmt.Errorf("profile_fill apply: unexpected options type %T", opts)
	}
	defer s.clearInUse(p.UploadIDs)
	result, err := s.svc.ApplyProfileFill(ctx, p.Game, p.Plan, core.ProfileFillOptions{Fomod: req.Fomod}, sink)
	if err != nil {
		return result, err
	}
	for _, id := range p.UploadIDs {
		s.uploads.Remove(id)
	}
	return result, nil
}
