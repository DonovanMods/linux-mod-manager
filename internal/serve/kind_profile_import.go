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
//
// An lmm collection (#621) - a version-1 YAML document, or a `.lmmc`
// container, which is binary and so arrives base64-encoded in DataBase64 -
// takes core's collection import instead: PlanCollectionImport into the
// selected game, ApplyCollectionImport with the apply request's WithHooks.
// The document is a file the user picked, so its hooks are importable.
package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"

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
// body: EITHER the exported profile document, verbatim as text, or a Steam
// Workshop collection reference (#269 W2).
//
// For a document the profile's NAME is not a parameter - it is inside the
// document, which is what makes an export portable. A collection carries no
// such name, so ProfileName may supply one; empty derives it from the
// collection's own title, exactly as `lmm profile import --as` does.
type profileImportPlanRequest struct {
	Data string `json:"data,omitzero"`
	// DataBase64 is a document as base64 (#621): a `.lmmc` container is not
	// text. Kept for small files and older clients. Mutually exclusive with
	// Data and UploadID.
	DataBase64 string `json:"data_base64,omitzero"`
	// UploadID names a document staged by POST /api/v1/uploads (#621): how
	// the SPA posts a file, so the web UI imports what the CLI does, past
	// the plan body's 1 MiB cap. Read once at plan time and removed.
	UploadID uploadID `json:"upload_id,omitzero"`
	// WorkshopCollection is a collection id or the URL of its page,
	// forwarded to core verbatim: the SOURCE owns what it recognises, and a
	// second parser here could only disagree with it.
	WorkshopCollection string `json:"workshop_collection,omitzero"`
	ProfileName        string `json:"profile_name,omitzero"`
}

// validate implements validatingOptions. The two inputs are mutually
// exclusive: with both, the server would be guessing which one the caller
// meant, and the CLI refuses the same combination.
func (r *profileImportPlanRequest) validate() error {
	if r.UploadID != "" {
		// The document is read from the upload at plan time; until then
		// only the inputs can be checked.
		switch {
		case r.Data != "" || r.DataBase64 != "":
			return errors.New(`"upload_id" and "data"/"data_base64" are mutually exclusive`)
		case r.WorkshopCollection != "":
			return errors.New(`"upload_id" and "workshop_collection" are mutually exclusive`)
		}
		return nil
	}
	if r.DataBase64 != "" {
		if r.Data != "" {
			return errors.New(`"data" and "data_base64" are mutually exclusive`)
		}
		decoded, err := base64.StdEncoding.DecodeString(r.DataBase64)
		if err != nil {
			return fmt.Errorf(`"data_base64" is not base64: %w`, err)
		}
		r.Data, r.DataBase64 = string(decoded), ""
	}
	switch {
	case r.Data == "" && r.WorkshopCollection == "":
		return errors.New(`"data" or "workshop_collection" is required`)
	case r.Data != "" && r.WorkshopCollection != "":
		return errors.New(`"data" and "workshop_collection" are mutually exclusive`)
	case r.Data == "" && r.ProfileName == "":
		return nil
	case r.Data != "" && r.ProfileName != "" && !core.IsCollectionDocument([]byte(r.Data)):
		return errors.New(`"profile_name" applies to "workshop_collection" or an lmm collection only - an older profile document names its own profile`)
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
	// Fomod answers FOMOD installer questions (#615): the choices per mod,
	// keyed by "<source-id>:<mod-id>", that a job refused with a
	// fomod_choices_required question for - the web UI's installer wizard
	// re-posts the plan and the job with the answers accumulated so far.
	Fomod map[string]domain.FomodChoices `json:"fomod,omitzero"`
	// WithHooks is `--with-hooks` (#621): save the hooks the document
	// carries, which its plan lists (ImportPlan.Hooks). AllowAnyOverrides
	// is `--allow-any-overrides`: save its overrides that are not text
	// config files too (ImportPlan.RestrictedOverrides). Both apply to
	// every document import.
	WithHooks         bool `json:"with_hooks,omitzero"`
	AllowAnyOverrides bool `json:"allow_any_overrides,omitzero"`
}

// importOptions renders the request as the core options struct.
func (r profileImportApplyRequest) importOptions() core.ProfileImportOptions {
	return core.ProfileImportOptions{Install: r.Install, Force: r.Force, NoInstall: r.NoInstall, WithHooks: r.WithHooks, AllowAnyOverrides: r.AllowAnyOverrides, Fomod: r.Fomod}
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

	if req.WorkshopCollection != "" {
		return planWorkshopCollectionImport(ctx, s, sel, req)
	}
	if req.UploadID != "" {
		data, err := s.readProfileUpload(req.UploadID)
		if err != nil {
			return nil, nil, err
		}
		req.Data, req.UploadID = string(data), ""
		if err := req.validate(); err != nil {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
	}
	if core.IsCollectionDocument([]byte(req.Data)) {
		return planLMMCollectionImport(ctx, s, sel, req)
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
	// #332 M2 sibling (unit 6 re-review): ParseProfile never checks GameID
	// either. A document naming no game ID plans 200 and only fails once
	// the job reaches SaveProfile ("importing profile: invalid game ID:
	// value is empty" - validateProfilePath, config/profiles.go). Worse, a
	// document naming a DIFFERENT game's ID doesn't fail at all: ApplyImport
	// (via its import save) saves profile.GameID from the
	// document verbatim, never sel.Game.ID, so the import would silently
	// land under the wrong game's directory - never even touching the game
	// the caller selected. Both are refused here, the same way the nameless
	// case above is.
	if parsed.GameID == "" {
		return nil, nil, fmt.Errorf("%w: the document has no game ID - it is not a profile export", errBadPlanRequest)
	}
	if parsed.GameID != sel.Game.ID {
		return nil, nil, fmt.Errorf("%w: the document is for game %q, not the selected game %q", errBadPlanRequest, parsed.GameID, sel.Game.ID)
	}

	plan, err := s.svc.PlanImport(ctx, sel.Game, []byte(req.Data))
	if err != nil {
		// #621: a document whose hooks or overrides are refused outright
		// is the caller's input, not a failure.
		if core.IsUnsafeImport(err) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
		return nil, nil, err
	}
	return plan, &pendingProfileImport{Game: sel.Game, Plan: plan}, nil
}

// planWorkshopCollectionImport is the collection half of the same plan
// kind. It produces the SAME core.ImportPlan - with WorkshopCollection
// attached - so the confirm modal, the job, and the apply path below are
// the ones profile import already had.
//
// A reference the source cannot parse, and a game with no Steam Workshop
// mapping, are both the CALLER's input being wrong rather than the server
// failing, so both answer 400 with the message core produced.
func planWorkshopCollectionImport(ctx context.Context, s *Server, sel selection, req profileImportPlanRequest) (any, any, error) {
	plan, err := s.svc.PlanWorkshopCollectionImport(ctx, sel.Game, req.ProfileName, req.WorkshopCollection)
	if err != nil {
		if core.IsBadCollectionRef(err) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
		return nil, nil, err
	}
	return plan, &pendingProfileImport{Game: sel.Game, Plan: plan}, nil
}

// readProfileUpload reads the profile document a POST /api/v1/uploads
// staged, up to core.MaxCollectionFileBytes, and removes the upload: the
// plan holds the document from here on, so nothing else needs the file.
// A missing upload, or one over the cap, is the caller's 400.
func (s *Server) readProfileUpload(id uploadID) ([]byte, error) {
	staged, ok := s.uploads.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: no staged upload %q (it expired or was cancelled)", errBadPlanRequest, id)
	}
	defer s.uploads.Remove(id)
	f, err := os.Open(staged.Path)
	if err != nil {
		return nil, fmt.Errorf("reading the upload: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, core.MaxCollectionFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the upload: %w", err)
	}
	if len(data) > core.MaxCollectionFileBytes {
		return nil, fmt.Errorf("%w: %s is over the %d-byte limit for a profile document", errBadPlanRequest, staged.Filename, core.MaxCollectionFileBytes)
	}
	return data, nil
}

// planLMMCollectionImport is the lmm collection half of the plan kind
// (#621): the document imports into the selected game (a document for
// another game plans with a warning saying so), as a file the user picked.
// A document the reader refuses is the caller's 400.
func planLMMCollectionImport(ctx context.Context, s *Server, sel selection, req profileImportPlanRequest) (any, any, error) {
	plan, err := s.svc.PlanCollectionImport(ctx, core.CollectionImportRequest{
		Data:        []byte(req.Data),
		Input:       core.CollectionFromFile,
		GameID:      sel.Game.ID,
		ProfileName: req.ProfileName,
	})
	if err != nil {
		if core.IsInvalidCollection(err) || core.IsUnsafeImport(err) || core.IsCollectionGameUnresolved(err) || errors.Is(err, domain.ErrInvalidProfileName) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
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
	// A collection plan takes core's collection apply, which forces
	// NoInstall: lmm cannot fetch a Workshop item the user is not
	// subscribed to until Tier 3 (#347), and that rule belongs in core so
	// both frontends inherit it rather than in whichever one remembered.
	if p.Plan.WorkshopCollection != nil {
		return s.svc.ApplyWorkshopCollectionImport(ctx, p.Game, p.Plan, req.importOptions(), sink)
	}
	if p.Plan.Collection != nil {
		return s.svc.ApplyCollectionImport(ctx, p.Game, p.Plan, req.importOptions(), sink)
	}
	return s.svc.ApplyImport(ctx, p.Game, p.Plan, req.importOptions(), sink)
}
