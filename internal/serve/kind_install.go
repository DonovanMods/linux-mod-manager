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
	"net/http"
	"strconv"

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
		Summarize:    summarizeInstallResult,
		Form: &kindForm{
			PlanOptions:  installPlanForm,
			ApplyOptions: installApplyForm,
			Confirm:      confirmInstallPlan,
		},
	})
}

// installPlanRequest is POST /api/v1/plans/install's request body.
type installPlanRequest struct {
	// SourceID and ModID name the mod to install; both are required, since
	// unlike uninstall there is no installed set to disambiguate against.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Version, when set, is the version the confirm page should preview and
	// preselect (#225). It does NOT change what PlanInstall computes - that
	// method takes no version - it selects which files the candidate pool is
	// drawn from, and it is applied for real through
	// installApplyRequest.Version.
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
// freshness snapshot survives to ApplyInstall's staleness check), the game
// it was computed for, and the #225 material the wire plan does not carry -
// the versions the source offers and the candidate files the picker
// renders.
type pendingInstall struct {
	Game *domain.Game
	Plan *core.InstallPlan
	// Versions is AvailableModVersions' answer, or nil when the source
	// reports no per-file version information at all (which is a supported
	// state, not a failure: no select renders).
	Versions []string
	// Version is the version the request asked to preview, if any.
	Version string
	// Candidates is the file pool the selection is drawn from - the
	// requested version's files when one was named, else the same
	// filtered/sorted list PlanInstall itself chose from.
	Candidates []domain.DownloadableFile
}

// planInstallKind implements planKind.Plan for "install". The #225
// enrichment is best-effort on purpose: a source that cannot list versions
// or files still gets a usable confirm page showing the plan's own default
// selection, exactly as the mod-detail page degrades (pages_mods.go).
func planInstallKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(installPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("install plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanInstall(ctx, sel.Game, sel.Profile, req.SourceID, req.ModID, req.ShowArchived)
	if err != nil {
		return nil, nil, err
	}

	pending := &pendingInstall{Game: sel.Game, Plan: plan, Version: req.Version}
	if versions, verr := s.svc.AvailableModVersions(ctx, req.SourceID, &plan.Mod); verr == nil {
		pending.Versions = versions
	} else {
		s.log.Debug("install plan: AvailableModVersions failed", "source", req.SourceID, "mod", req.ModID, "err", verr)
	}
	pending.Candidates = s.installCandidates(ctx, req, plan)

	return plan, pending, nil
}

// installCandidates is the file pool the confirm page's picker offers. With
// a version named it is that version's files (ResolveVersionFiles, which
// deliberately includes archived ones - a version pin usually targets one);
// without, it is the same FilterAndSortFiles list PlanInstall drew its own
// default from. Any failure degrades to the plan's own selection rather
// than failing the plan: the picker is an affordance, not a precondition.
func (s *Server) installCandidates(ctx context.Context, req installPlanRequest, plan *core.InstallPlan) []domain.DownloadableFile {
	if req.Version != "" {
		files, err := s.svc.ResolveModVersion(ctx, req.SourceID, &plan.Mod, req.Version)
		if err == nil {
			return files
		}
		s.log.Debug("install plan: version resolution failed", "version", req.Version, "err", err)
		return plan.Files
	}

	files, err := s.svc.GetModFiles(ctx, req.SourceID, &plan.Mod)
	if err != nil {
		s.log.Debug("install plan: listing files failed", "source", req.SourceID, "mod", req.ModID, "err", err)
		return plan.Files
	}
	return core.FilterAndSortFiles(files, plan.ShowArchived)
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

// summarizeInstallResult implements planKind.Summarize for "install".
func summarizeInstallResult(result any) []resultFact {
	res, ok := result.(*core.InstallResult)
	if !ok {
		return nil
	}

	facts := make([]resultFact, 0, len(res.Installed)+len(res.Warnings)+len(res.Notes)+2)
	for _, ref := range res.Installed {
		facts = append(facts, resultFact{Label: "Installed", Value: installedRefText(ref)})
	}
	facts = append(facts, resultFact{Label: "Files deployed", Value: strconv.Itoa(res.FilesDeployed)})
	for _, ref := range res.Failed {
		facts = append(facts, resultFact{Label: "Failed", Value: installedRefText(ref)})
	}
	if res.MergedPakSyncFailed {
		facts = append(facts, resultFact{Label: "Merged pak", Value: "the end-of-install sync failed"})
	}
	for _, w := range res.Warnings {
		facts = append(facts, resultFact{Label: "Warning", Value: w})
	}
	for _, n := range res.Notes {
		facts = append(facts, resultFact{Label: "Note", Value: n})
	}
	return facts
}

// installedRefText renders one InstalledRef as a line - name, version, and
// the reason when the entry is a skip or a failure.
func installedRefText(ref core.InstalledRef) string {
	text := ref.Name
	if ref.Version != "" {
		text += " " + ref.Version
	}
	if ref.Reason != "" {
		text += " - " + ref.Reason
	}
	return text
}

// installPlanForm implements kindForm.PlanOptions.
func installPlanForm(r *http.Request) (any, error) {
	return installPlanRequest{
		SourceID:     r.PathValue("source"),
		ModID:        r.PathValue("id"),
		Version:      r.FormValue(versionField),
		ShowArchived: formFlag(r, showArchivedField),
	}, nil
}

// installApplyForm implements kindForm.ApplyOptions: #225's two picks plus
// the conflict answer, read back from the confirm form.
func installApplyForm(r *http.Request) (any, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("reading form: %w", err)
	}
	return installApplyRequest{
		Version:         r.FormValue(versionField),
		FileIDs:         r.Form[fileField],
		AcceptConflicts: formFlag(r, acceptConflictsField),
		Force:           formFlag(r, forceField),
		SkipHooks:       formFlag(r, skipHooksField),
	}, nil
}

// confirmInstallPlan implements kindForm.Confirm: what the install would
// fetch and deploy, plus #225's version select and file picker.
func confirmInstallPlan(pending, opts any) confirmView {
	p, ok := pending.(*pendingInstall)
	if !ok {
		return confirmView{Submit: "Install"}
	}
	req, _ := opts.(installApplyRequest)

	plan := p.Plan
	chosenVersion := firstNonEmpty(req.Version, p.Version, planSelectedVersion(plan))
	view := confirmView{
		Heading:  fmt.Sprintf("%s %s", plan.Mod.Name, plan.Mod.Version),
		Submit:   "Install",
		Versions: offeredVersions(p.Versions, chosenVersion),
		Version:  chosenVersion,
		// Sticky across a re-plan: once the user has answered a conflict,
		// pressing "Update plan" must not silently drop that answer.
		AcceptConflicts: req.AcceptConflicts,
		Facts: []resultFact{
			{Label: "Mod", Value: domain.ModKey(plan.SourceID, plan.Mod.ID)},
			{Label: "Profile", Value: plan.Profile},
			{Label: "Download", Value: downloadSizeText(plan.TotalDownloadBytes)},
		},
		Toggles: []confirmToggle{
			{
				Name:    skipHooksField,
				Label:   "Skip hooks",
				Help:    "run no install.* hooks at all",
				Checked: req.SkipHooks,
			},
			{
				Name:    forceField,
				Label:   "Force",
				Help:    "continue past a failing before-hook, and overwrite conflicting files",
				Checked: req.Force,
			},
		},
	}

	if plan.Replaces != nil {
		view.Facts = append(view.Facts, resultFact{
			Label: "Replaces",
			Value: "the installed " + plan.Replaces.Version,
		})
	}
	if plan.CycleDetected {
		view.Facts = append(view.Facts, resultFact{
			Label: "Dependencies",
			Value: "a circular reference was found; install order is best-effort",
		})
	}

	// #225: a picker only where there is a choice. One candidate is shown as
	// a fact instead, so the page still says what would be downloaded.
	view.Files = installFileChoices(p, req)
	if len(view.Files) == 0 {
		view.Facts = append(view.Facts, resultFact{Label: "File", Value: selectedFileText(p)})
	}

	if names := modNames(plan.Dependencies); len(names) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Dependencies that would be installed first", Items: names})
	}
	if len(plan.MissingDependencies) > 0 {
		items := make([]string, 0, len(plan.MissingDependencies))
		for _, ref := range plan.MissingDependencies {
			items = append(items, domain.ModKey(ref.SourceID, ref.ModID))
		}
		view.Lists = append(view.Lists, confirmList{Label: "Dependencies that could not be resolved", Items: items})
	}
	if len(plan.Conflicts) > 0 {
		items := make([]string, 0, len(plan.Conflicts))
		for _, c := range plan.Conflicts {
			items = append(items, fmt.Sprintf("%s (owned by %s)", c.RelativePath, domain.ModKey(c.CurrentSourceID, c.CurrentModID)))
		}
		view.Lists = append(view.Lists, confirmList{Label: "Files this would overwrite", Items: items})
	}
	return view
}

// planSelectedVersion is the version the plan's OWN file selection would
// record - domain.EffectiveInstalledVersion, the same rule the install
// itself uses for the DB row, the profile ref and the cache key. It is the
// version select's default, because a select whose visible value differed
// from the plan shown right above it would pin a version the user never
// chose the moment they pressed the button.
func planSelectedVersion(plan *core.InstallPlan) string {
	selected := make([]*domain.DownloadableFile, len(plan.Files))
	for i := range plan.Files {
		selected[i] = &plan.Files[i]
	}
	return domain.EffectiveInstalledVersion(plan.Mod.Version, selected)
}

// offeredVersions is the version select's option list: what the source
// reports, with the plan's own version prepended when it is somehow absent
// from that list. Without the guard, an unlisted default would leave the
// browser selecting the FIRST option instead - silently pinning a version
// the plan never previewed. An empty list renders no select at all.
func offeredVersions(versions []string, chosen string) []string {
	if len(versions) == 0 || chosen == "" {
		return versions
	}
	for _, v := range versions {
		if v == chosen {
			return versions
		}
	}
	return append([]string{chosen}, versions...)
}

// installFileChoices renders the candidate pool as checkboxes, or nil when
// the pool holds fewer than two files - "file selection where the plan
// offers it". A pick already made (req.FileIDs) wins; otherwise the plan's
// own default selection is what shows as ticked.
func installFileChoices(p *pendingInstall, req installApplyRequest) []confirmFile {
	if len(p.Candidates) < 2 {
		return nil
	}

	// A pick the CURRENT pool no longer contains is not a pick: changing
	// the version narrows the pool, and carrying the old version's file IDs
	// forward would tick nothing and then fail the Apply's pin resolution.
	// Falling back to the plan's own selection is what makes "Update plan"
	// after a version change land somewhere sensible.
	available := make(map[string]bool, len(p.Candidates))
	for _, f := range p.Candidates {
		available[f.ID] = true
	}
	chosen := make(map[string]bool, len(req.FileIDs))
	for _, id := range req.FileIDs {
		if available[id] {
			chosen[id] = true
		}
	}
	if len(chosen) == 0 {
		for _, f := range p.Plan.Files {
			chosen[f.ID] = true
		}
	}

	files := make([]confirmFile, 0, len(p.Candidates))
	for _, f := range p.Candidates {
		files = append(files, confirmFile{ID: f.ID, Label: downloadableFileText(f), Selected: chosen[f.ID]})
	}
	return files
}

// downloadableFileText names one file the way a picker should: its display
// name, its filename, and its version and size when the source reports them.
func downloadableFileText(f domain.DownloadableFile) string {
	text := f.Name
	if text == "" {
		text = f.FileName
	} else if f.FileName != "" && f.FileName != f.Name {
		text += " (" + f.FileName + ")"
	}
	if f.Version != "" {
		text += " - version " + f.Version
	}
	if f.Size > 0 {
		text += ", " + downloadSizeText(f.Size)
	}
	return text
}

// selectedFileText names the single file a no-choice plan would download.
func selectedFileText(p *pendingInstall) string {
	if len(p.Plan.Files) == 0 {
		return "none"
	}
	return downloadableFileText(p.Plan.Files[0])
}

// downloadSizeText renders a byte count the way core reports it: -1 means
// at least one selected file's size is unreported, which is a real answer
// ("unknown"), not a zero.
func downloadSizeText(bytes int64) string {
	if bytes < 0 {
		return "size unknown"
	}
	if bytes < 1024 {
		return strconv.FormatInt(bytes, 10) + " bytes"
	}
	return strconv.FormatInt(bytes/1024, 10) + " KiB"
}

// modNames lists mods by name for a confirm-page list.
func modNames(mods []domain.Mod) []string {
	if len(mods) == 0 {
		return nil
	}
	names := make([]string, 0, len(mods))
	for _, m := range mods {
		names = append(names, m.Name+" "+m.Version)
	}
	return names
}

// firstNonEmpty returns the first non-empty of its arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
