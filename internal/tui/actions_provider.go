package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// ActionProvider is the write-side seam over the core mutation flows
// (Service.EnableMod/DisableMod/UninstallMod/DeployProfile/
// PlanProfileSwitch/ApplyProfileSwitch). It is deliberately separate from
// DataProvider: DataProvider stays provably read-only (nothing in its
// interface can mutate app state), and implementations of one need not
// implement the other - though coreProvider and prototypeProvider both do,
// each on its own single struct (see NewCoreActions and NewPrototypeProvider
// for how a caller obtains each role).
//
// Error semantics: an error does NOT imply nothing changed. The underlying
// flows are multi-step (deploy files then flip DB state; undeploy, delete
// cache, then delete the DB row; whole per-mod loops before SetDefault); a
// failure partway leaves earlier steps applied. Callers must treat any error
// as "state may have partially changed": refresh data after every action,
// success or failure, and never offer undo/retry affordances that assume a
// failed action was a no-op. PlanProfileSwitch and PlanInstall are the
// exceptions: planning is pure and never mutates either way.
//
// Progress-callback lifetime: the progress func(ActionProgress) argument
// ApplyProfileSwitch/ApplyInstall/ApplyUpdate accept must never be called
// after the method itself has returned. buildAction (actions.go) closes the
// channel progress writes into immediately once the method returns, so an
// implementation that reports progress from a detached goroutine outliving
// the call risks a send-on-closed-channel panic - progress must only be
// invoked synchronously within the method's own call stack (or from a
// goroutine fully joined before returning).
// errCannotDeleteActiveProfile is the shared wording for refusing to delete
// the currently active profile - the TUI's own status-line refusal
// (mutations.go's deleteSelectedProfile) and both ActionProvider
// implementations' defense-in-depth guards (coreProvider.DeleteProfile,
// prototypeProvider.DeleteProfile below) all use this SAME string, so the
// three independent checks (self-review finding, Task 6) can never drift out
// of sync with each other.
const errCannotDeleteActiveProfile = "cannot delete the active profile"

type ActionProvider interface {
	EnableMod(ctx context.Context, item ModItem) (ActionOutcome, error)
	DisableMod(ctx context.Context, item ModItem) (ActionOutcome, error)
	UninstallMod(ctx context.Context, item ModItem) (ActionOutcome, error)
	DeployProfile(ctx context.Context) (ActionOutcome, error)
	PlanProfileSwitch(ctx context.Context, profile string) (SwitchPlanView, error)
	// ApplyProfileSwitch's progress may be nil (see ActionProgress); it
	// streams download/install ticks when the plan being applied needs
	// them (Phase 5b Task 4 - see coreProvider/prototypeProvider's own doc
	// comments on this method).
	ApplyProfileSwitch(ctx context.Context, profile string, progress func(ActionProgress)) (ActionOutcome, error)

	// PlanInstall computes what installing item would do (files, resolved
	// dependencies, conflicts, size), without mutating anything - the
	// install-modal analog of PlanProfileSwitch.
	PlanInstall(ctx context.Context, item ModItem) (InstallPlanView, error)
	// ApplyInstall executes the plan PlanInstall would currently compute for
	// item (coreProvider re-plans at apply time, mirroring
	// ApplyProfileSwitch's own precedent). progress may be nil.
	ApplyInstall(ctx context.Context, item ModItem, progress func(ActionProgress)) (ActionOutcome, error)
	// CheckUpdates reports available updates for every checkable installed
	// mod (pinned/local mods are never checkable - filtered by core, not
	// re-filtered here).
	CheckUpdates(ctx context.Context) (UpdatesView, error)
	// ApplyUpdate applies one update reported by CheckUpdates. progress may
	// be nil.
	ApplyUpdate(ctx context.Context, u UpdateItem, progress func(ActionProgress)) (ActionOutcome, error)
	// SetUpdatePolicy sets item's update-check policy to policy, one of
	// "notify" (default: show available updates, require approval), "auto"
	// (apply automatically), or "pin" (never update) - mapping to
	// domain.UpdateNotify/UpdateAuto/UpdatePinned respectively for
	// coreProvider. Unlike CheckUpdates/ApplyUpdate this never touches the
	// network - a local DB write - so it carries no progress callback.
	SetUpdatePolicy(ctx context.Context, item ModItem, policy string) (ActionOutcome, error)

	// CreateProfile creates a new, empty profile named name (Task 6's
	// Profiles-screen 'c' binding - see mutations.go's createProfilePrompt).
	// A name colliding with an existing profile is rejected - coreProvider
	// via ProfileManager.Create's own duplicate check, prototypeProvider
	// mirroring it defensively even though the TUI's own input-modal
	// validate closure already refuses a colliding name before this is ever
	// called.
	CreateProfile(ctx context.Context, name string) (ActionOutcome, error)
	// DeleteProfile removes profile name (Task 6's Profiles-screen 'd'
	// binding - see mutations.go's deleteSelectedProfile). Deleting the
	// currently active profile is refused - the TUI's own handler already
	// checks this synchronously before ever reaching here (a status-line
	// refusal, no modal), but every implementation repeats the guard
	// defense-in-depth, since a stale active-profile row (a refresh landed
	// between the keypress and confirm) could otherwise let it through.
	DeleteProfile(ctx context.Context, name string) (ActionOutcome, error)

	// PurgeProfile undeploys every mod currently installed in the active
	// profile (Task 7's Dashboard/Installed-Mods 'X' binding - see
	// mutations.go's purgeProfilePrompt): the TUI equivalent of `lmm purge`
	// with neither --uninstall nor --force. Mod records are preserved, only
	// marked not-deployed - matching the CLI default (coreProvider never
	// exposes --uninstall's record-deleting variant; see its own doc
	// comment). progress may be nil, like every other streaming
	// ActionProvider method.
	PurgeProfile(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error)
}

// ActionOutcome is what the TUI status line renders after a successful
// ActionProvider call. Message and each Warnings entry are TUI-facing
// English composed by the provider (not core, which never prints or
// formats for a specific caller) and are expected to be truncated to panel
// width by the renderer - keep them short and specific.
type ActionOutcome struct {
	Message  string   // one-line success summary, e.g. `Enabled "SkyUI"` / "Deployed 5 mod(s)"
	Warnings []string // non-fatal diagnostics: the underlying flow result's Warnings then Notes, in that order
}

// SwitchPlanView is the render model for the profile-switch confirmation
// modal, mapped from core.SwitchPlan (see coreProvider's switchPlanView) or
// computed directly from prototype demo data.
type SwitchPlanView struct {
	From, To       string
	Enable         []string // mod names to enable
	Disable        []string // mod names to disable
	NeedsDownloads []string // mod refs requiring download - ApplyProfileSwitch downloads and installs these itself (Phase 5b Task 4; see its doc comment), streaming progress the same way ApplyInstall/ApplyUpdate do
	NoChanges      bool
	AlreadyActive  bool
}

// InstallPlanView is the render model for the install confirmation modal,
// mapped from core.InstallPlan (see coreProvider's installPlanView) or
// computed directly from prototype demo data - the install-modal analog of
// SwitchPlanView.
type InstallPlanView struct {
	Name, Version, Source string
	Files                 []string // display labels of the file(s) that would be downloaded
	Dependencies          []string // display names of resolved, not-yet-installed dependencies that would also install
	Conflicts             []string // "path (owned by <mod-id>)", one per conflicting file
	MissingDependencies   []string // "sourceID:modID" refs that couldn't be resolved - warn, don't block
	CycleWarning          bool     // a circular dependency was found among Dependencies; install order is best-effort
	Reinstall             bool     // item is already installed - applying replaces it rather than installing fresh
	SizeLabel             string   // "12.3 MiB", or "size unknown" when no selected file declares a size
}

// UpdateItem is one available update, as reported by CheckUpdates and
// consumed by ApplyUpdate.
type UpdateItem struct {
	Source, ID, Name       string
	FromVersion, ToVersion string
}

// UpdatesView is CheckUpdates' result: the available updates plus any
// non-fatal per-source diagnostics (partial results still populate Updates
// - see coreProvider.CheckUpdates' doc comment).
type UpdatesView struct {
	Updates  []UpdateItem
	Warnings []string
}

// installSizeLabel renders bytes as InstallPlanView.SizeLabel's documented
// "12.3 MiB" format, or "size unknown" when bytes isn't a known positive
// size - shared by coreProvider (from InstallPlan.TotalDownloadBytes, which
// is -1 when any selected file's size is unreported) and prototypeProvider
// (from a canned Mod.SizeBytes, which is 0 for every mod that doesn't
// declare one). Only a positive value is ever treated as known, matching
// the same convention DeployProgress's own Downloaded/TotalBytes fields
// document.
func installSizeLabel(bytes int64) string {
	if bytes <= 0 {
		return "size unknown"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// mergeDiagnostics composes an ActionOutcome.Warnings slice from a flow
// result's own Warnings and Notes fields, in that order - the contract
// documented on ActionOutcome.Warnings. Returns nil (not an empty slice)
// when both inputs are empty, matching every other TUI DataProvider
// method's "no diagnostics" convention.
func mergeDiagnostics(warnings, notes []string) []string {
	if len(warnings) == 0 && len(notes) == 0 {
		return nil
	}
	merged := make([]string, 0, len(warnings)+len(notes))
	merged = append(merged, warnings...)
	merged = append(merged, notes...)
	return merged
}

// --- prototypeProvider: ActionProvider ---
//
// Simulated and side-effect-free outside prototypeProvider's own in-memory
// data field (see service.go's DataProvider methods on the same type):
// EnableMod/DisableMod/UninstallMod flip or remove InstalledMods entries and
// adjust Stats.Enabled/Installed accordingly; DeployProfile and the profile
// switch pair report plausible, data-derived Outcomes. PlanProfileSwitch's
// diff is fake but consistent - it never invents a phantom ToInstall entry
// out of thin air; the ONE exception is prototype.NeedsDownloadProfileName,
// a canned profile whose Mods list deliberately references an ID absent
// from InstalledMods (see prototype/data.go) so --prototype mode can demo
// ApplyProfileSwitch's downloading-switch path end to end (Phase 5b Task 4 -
// see that method's own doc comment below). Every other canned profile
// leaves Mods unset and is unaffected.

// findInstalledIndex returns the index of the ACTIVE game's installed-mods
// entry matching (sourceID, id), or -1 if none matches - routed through
// activeMods (service.go, Copilot PR #69) so every lookup-based operation
// automatically addresses whichever game the session is bound to.
func (p *prototypeProvider) findInstalledIndex(sourceID, id string) int {
	for i, mod := range p.activeMods() {
		if mod.Source == sourceID && mod.ID == id {
			return i
		}
	}
	return -1
}

func (p *prototypeProvider) EnableMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	idx := p.findInstalledIndex(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	mods := p.activeMods()
	if mods[idx].Status != "disabled" {
		return ActionOutcome{Message: fmt.Sprintf("%q is already enabled", item.Name)}, nil
	}
	mods[idx].Status = "installed"
	p.adjustStats(0, 1)
	return ActionOutcome{Message: fmt.Sprintf("Enabled %q", item.Name)}, nil
}

func (p *prototypeProvider) DisableMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	idx := p.findInstalledIndex(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	mods := p.activeMods()
	if mods[idx].Status == "disabled" {
		return ActionOutcome{Message: fmt.Sprintf("%q is already disabled", item.Name)}, nil
	}
	mods[idx].Status = "disabled"
	p.adjustStats(0, -1)
	return ActionOutcome{Message: fmt.Sprintf("Disabled %q", item.Name)}, nil
}

func (p *prototypeProvider) UninstallMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	idx := p.findInstalledIndex(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	mods := p.activeMods()
	wasEnabled := mods[idx].Status != "disabled"
	p.setActiveMods(append(mods[:idx], mods[idx+1:]...))
	enabledDelta := 0
	if wasEnabled {
		enabledDelta = -1
	}
	p.adjustStats(-1, enabledDelta)
	return ActionOutcome{Message: fmt.Sprintf("Uninstalled %q", item.Name)}, nil
}

func (p *prototypeProvider) DeployProfile(_ context.Context) (ActionOutcome, error) {
	deployed := 0
	for _, mod := range p.activeMods() {
		if mod.Status != "disabled" {
			deployed++
		}
	}
	return ActionOutcome{Message: fmt.Sprintf("Deployed %d mod(s)", deployed)}, nil
}

// activeProfileName returns the canned Profiles entry currently marked
// Active, falling back to data.Profile.Name (kept in sync by
// ApplyProfileSwitch) if none is marked.
func (p *prototypeProvider) activeProfileName() string {
	for _, pr := range p.data.Profiles {
		if pr.Active {
			return pr.Name
		}
	}
	return p.data.Profile.Name
}

// findProfile returns the canned Profiles entry named name, or false if
// none matches.
func (p *prototypeProvider) findProfile(name string) (prototype.Profile, bool) {
	for _, pr := range p.data.Profiles {
		if pr.Name == name {
			return pr, true
		}
	}
	return prototype.Profile{}, false
}

// needsDownloads reports, for each ID in profile.Mods, whether it names an
// InstalledMods entry - and returns a NeedsDownloads-shaped entry for every
// one that doesn't. All canned mods use source "nexusmods" (see
// prototype/data.go), so that source is hardcoded here; this is demo-only
// plumbing, not a general (source, id) reference. Every profile except
// prototype.NeedsDownloadProfileName leaves Mods unset, so this returns nil
// for them - the alternating Enable/Disable plan below is unaffected.
func (p *prototypeProvider) needsDownloads(profile prototype.Profile) []string {
	var needs []string
	for _, id := range profile.Mods {
		if p.findInstalledIndex("nexusmods", id) < 0 {
			needs = append(needs, fmt.Sprintf("nexusmods:%s v1.0", id))
		}
	}
	return needs
}

// PlanProfileSwitch computes a fake-but-consistent plan from prototype
// data: alternating InstalledMods entries (by index) toggle between the two
// buckets, giving a plausible, deterministic, non-empty mix of Enable/
// Disable for any target profile other than the active one. The one
// exception is prototype.NeedsDownloadProfileName (see needsDownloads and
// this file's package doc comment), which short-circuits straight to a
// NeedsDownloads-only plan instead - the missing mod(s) don't exist in
// InstalledMods yet, so there's nothing yet to bucket into Enable/Disable;
// ApplyProfileSwitch materializes them and RE-PLANS (see its own doc
// comment) before this alternating logic ever runs for them.
func (p *prototypeProvider) PlanProfileSwitch(_ context.Context, profileName string) (SwitchPlanView, error) {
	target, ok := p.findProfile(profileName)
	if !ok {
		return SwitchPlanView{}, fmt.Errorf("profile not found: %s", profileName)
	}

	current := p.activeProfileName()
	if profileName == current {
		return SwitchPlanView{From: current, To: profileName, AlreadyActive: true}, nil
	}

	if needs := p.needsDownloads(target); len(needs) > 0 {
		return SwitchPlanView{From: current, To: profileName, NeedsDownloads: needs}, nil
	}

	var enable, disable []string
	for i, mod := range p.activeMods() {
		if i%2 == 0 {
			if mod.Status == "disabled" {
				enable = append(enable, mod.Name)
			}
		} else if mod.Status != "disabled" {
			disable = append(disable, mod.Name)
		}
	}

	return SwitchPlanView{
		From:      current,
		To:        profileName,
		Enable:    enable,
		Disable:   disable,
		NoChanges: len(enable) == 0 && len(disable) == 0,
	}, nil
}

// fakeProgressTicks emits the classic "0/50/100%" fake progress sequence
// (the brief's own wording for both ApplyInstall and ApplyUpdate) prefixed
// with label, if progress is non-nil - shared by every prototypeProvider
// mutation that streams progress (ApplyProfileSwitch's download loop,
// ApplyInstall, ApplyUpdate).
func fakeProgressTicks(progress func(ActionProgress), label string) {
	if progress == nil {
		return
	}
	for _, pct := range []float64{0, 50, 100} {
		progress(ActionProgress{Line: fmt.Sprintf("%s: %.0f%%", label, pct), Percent: pct})
	}
}

// materializeNeedsDownloads adds a freshly-"installed" (disabled, so the
// alternating Enable/Disable pass below decides its fate) InstalledMods
// entry for every one of profileName's referenced mod IDs not already
// present - simulating the download ApplyProfileSwitch just fake-streamed
// progress for. All canned mods use source "nexusmods" (see needsDownloads'
// own doc comment on why that's hardcoded here).
func (p *prototypeProvider) materializeNeedsDownloads(profileName string) {
	target, ok := p.findProfile(profileName)
	if !ok {
		return
	}
	for _, id := range target.Mods {
		if p.findInstalledIndex("nexusmods", id) >= 0 {
			continue
		}
		p.setActiveMods(append(p.activeMods(), prototype.Mod{
			ID: id, Name: id, Source: "nexusmods", Version: "1.0", Status: "disabled",
		}))
		p.adjustStats(1, 0)
	}
}

// ApplyProfileSwitch re-plans and applies. A plan with NeedsDownloads
// entries - reachable via the prototype's own planner for
// prototype.NeedsDownloadProfileName (see this file's package doc comment)
// - USED to refuse outright (5a's TUI had no install/download path yet);
// Phase 5b Task 4 lifts that refusal into a WORKING demo instead: every
// missing mod streams a fake 0/50/100% download tick (fakeProgressTicks),
// is materialized into InstalledMods (materializeNeedsDownloads), and the
// plan is then RECOMPUTED - mirroring coreProvider's own re-plan-at-apply
// precedent - so the now-satisfied plan's Enable/Disable buckets (if any)
// apply exactly like any other switch, below.
func (p *prototypeProvider) ApplyProfileSwitch(ctx context.Context, profileName string, progress func(ActionProgress)) (ActionOutcome, error) {
	view, err := p.PlanProfileSwitch(ctx, profileName)
	if err != nil {
		return ActionOutcome{}, err
	}
	if view.AlreadyActive {
		return ActionOutcome{Message: fmt.Sprintf("Already on profile %q", profileName)}, nil
	}

	if len(view.NeedsDownloads) > 0 {
		for _, ref := range view.NeedsDownloads {
			fakeProgressTicks(progress, "Switching: downloading "+ref)
		}
		p.materializeNeedsDownloads(profileName)

		view, err = p.PlanProfileSwitch(ctx, profileName)
		if err != nil {
			return ActionOutcome{}, err
		}
	}

	enable := make(map[string]bool, len(view.Enable))
	for _, name := range view.Enable {
		enable[name] = true
	}
	disable := make(map[string]bool, len(view.Disable))
	for _, name := range view.Disable {
		disable[name] = true
	}
	mods := p.activeMods()
	for i := range mods {
		switch name := mods[i].Name; {
		case enable[name]:
			mods[i].Status = "installed"
		case disable[name]:
			mods[i].Status = "disabled"
		}
	}

	for i := range p.data.Profiles {
		p.data.Profiles[i].Active = p.data.Profiles[i].Name == profileName
	}
	p.data.Profile.Name = profileName

	return ActionOutcome{Message: fmt.Sprintf("Switched to %q", profileName)}, nil
}

// CreateProfile appends a new canned Profile entry to data.Profiles, visible
// in a repeated Profiles call - mirrors EnableMod/DisableMod's own "same
// instance, same session" contract. A name colliding with an existing
// profile is refused, mirroring coreProvider's own ProfileManager.Create
// precedent (service_core.go) even though this is defense-in-depth here too
// (see ActionProvider.CreateProfile's doc comment).
func (p *prototypeProvider) CreateProfile(_ context.Context, name string) (ActionOutcome, error) {
	if _, ok := p.findProfile(name); ok {
		return ActionOutcome{}, fmt.Errorf("profile already exists: %s", name)
	}
	p.data.Profiles = append(p.data.Profiles, prototype.Profile{Name: name})
	return ActionOutcome{Message: fmt.Sprintf("Created profile: %s", name)}, nil
}

// DeleteProfile removes the canned Profiles entry named name, visible in a
// repeated Profiles call. Refuses to delete the active profile - defense-in-
// depth mirroring coreProvider's own guard (see ActionProvider.DeleteProfile's
// doc comment).
func (p *prototypeProvider) DeleteProfile(_ context.Context, name string) (ActionOutcome, error) {
	if name == p.activeProfileName() {
		return ActionOutcome{}, errors.New(errCannotDeleteActiveProfile)
	}
	for i, pr := range p.data.Profiles {
		if pr.Name == name {
			p.data.Profiles = append(p.data.Profiles[:i], p.data.Profiles[i+1:]...)
			return ActionOutcome{Message: fmt.Sprintf("Deleted profile: %s", name)}, nil
		}
	}
	return ActionOutcome{}, fmt.Errorf("profile not found: %s", name)
}

// findSearchResult returns the index of the SearchResults entry matching
// (sourceID, id), or -1 if none matches - the PlanInstall/ApplyInstall
// analog of findInstalledIndex.
func (p *prototypeProvider) findSearchResult(sourceID, id string) int {
	for i, mod := range p.data.SearchResults {
		if mod.Source == sourceID && mod.ID == id {
			return i
		}
	}
	return -1
}

// PlanInstall computes a deterministic fake plan from item's canned
// SearchResults entry: its own Dependencies/Conflicts/SizeBytes (see
// prototype.Mod's doc comment - most canned mods leave these unset; a
// handful deliberately carry one of each so --prototype mode can demo every
// InstallPlanView field) plus a synthesized single file. Reinstall reports
// true if item is already installed - PlanInstall never invents a phantom
// dependency/conflict beyond what's canned, mirroring
// PlanProfileSwitch's own "never invent a phantom X" convention.
func (p *prototypeProvider) PlanInstall(_ context.Context, item ModItem) (InstallPlanView, error) {
	idx := p.findSearchResult(item.Source, item.ID)
	if idx < 0 {
		return InstallPlanView{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	mod := p.data.SearchResults[idx]

	return InstallPlanView{
		Name:         mod.Name,
		Version:      mod.Version,
		Source:       mod.Source,
		Files:        []string{mod.Name + ".7z"},
		Dependencies: mod.Dependencies,
		Conflicts:    mod.Conflicts,
		SizeLabel:    installSizeLabel(mod.SizeBytes),
		Reinstall:    p.findInstalledIndex(item.Source, item.ID) >= 0,
	}, nil
}

// ApplyInstall emits the brief's own "0/50/100%" fake progress sequence,
// then mutates in-memory data so item shows installed in both Overview
// (InstalledMods) and Search (SearchResults, which ModItem.Status also
// derives from - see service.go's Search): a fresh InstalledMods row is
// appended if item wasn't already installed (Reinstall case: just flips its
// existing row's Status/Version), and the matching SearchResults entry's
// own Status flips too so a repeated Search reflects it - both through the
// SAME prototypeProvider instance, mirroring EnableMod/UninstallMod's own
// "visible in a repeated read" contract.
func (p *prototypeProvider) ApplyInstall(_ context.Context, item ModItem, progress func(ActionProgress)) (ActionOutcome, error) {
	idx := p.findSearchResult(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	mod := p.data.SearchResults[idx]

	fakeProgressTicks(progress, fmt.Sprintf("Installing %s", mod.Name))

	if installedIdx := p.findInstalledIndex(item.Source, item.ID); installedIdx >= 0 {
		installedMods := p.activeMods()
		installedMods[installedIdx].Status = "installed"
		installedMods[installedIdx].Version = mod.Version
	} else {
		installed := mod
		installed.Status = "installed"
		p.setActiveMods(append(p.activeMods(), installed))
		p.adjustStats(1, 1)
	}
	p.data.SearchResults[idx].Status = "installed"

	return ActionOutcome{Message: fmt.Sprintf("Installed %q", mod.Name)}, nil
}

// CheckUpdates returns the canned update set: every InstalledMods entry
// with a non-empty AvailableVersion (see prototype.Mod's doc comment -
// skyui is canned "auto", ussep "notify", giving at least one of each
// policy for a future keybinding layer to consult, though UpdateItem itself
// carries no policy field - see its doc comment).
func (p *prototypeProvider) CheckUpdates(_ context.Context) (UpdatesView, error) {
	var view UpdatesView
	for _, mod := range p.activeMods() {
		if mod.AvailableVersion == "" {
			continue
		}
		view.Updates = append(view.Updates, UpdateItem{
			Source: mod.Source, ID: mod.ID, Name: mod.Name,
			FromVersion: mod.Version, ToVersion: mod.AvailableVersion,
		})
	}
	return view, nil
}

// ApplyUpdate emits the brief's own fake progress sequence, then bumps the
// matching InstalledMods entry's Version to u.ToVersion and clears its
// AvailableVersion - so a repeated CheckUpdates no longer reports it,
// mirroring a real update's "already up to date" outcome.
func (p *prototypeProvider) ApplyUpdate(_ context.Context, u UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	idx := p.findInstalledIndex(u.Source, u.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", u.ID)
	}

	fakeProgressTicks(progress, fmt.Sprintf("Updating %s", u.Name))

	mods := p.activeMods()
	mods[idx].Version = u.ToVersion
	mods[idx].AvailableVersion = ""
	mods[idx].Status = "installed"

	return ActionOutcome{Message: fmt.Sprintf("Updated %q to %s", u.Name, u.ToVersion)}, nil
}

// isValidUpdatePolicy reports whether policy is one of the three strings
// SetUpdatePolicy accepts - shared by both providers' validation (see
// coreProvider's own parseUpdatePolicy in service_core.go, which additionally
// maps a valid string to its domain.UpdatePolicy constant; the prototype has
// no such enum to map to, since it stores the policy as a plain string
// directly on the canned Mod).
func isValidUpdatePolicy(policy string) bool {
	switch policy {
	case "notify", "auto", "pin":
		return true
	default:
		return false
	}
}

// SetUpdatePolicy mutates the canned InstalledMods entry's UpdatePolicy
// field in place - visible in a repeated Overview, mirroring
// EnableMod/DisableMod/UninstallMod's own "same instance, same session"
// contract.
func (p *prototypeProvider) SetUpdatePolicy(_ context.Context, item ModItem, policy string) (ActionOutcome, error) {
	if !isValidUpdatePolicy(policy) {
		return ActionOutcome{}, fmt.Errorf("unknown policy %q", policy)
	}
	idx := p.findInstalledIndex(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	p.activeMods()[idx].UpdatePolicy = policy
	return ActionOutcome{Message: fmt.Sprintf("%s update policy: %s", item.Name, policy)}, nil
}

// PurgeProfile emits one fake progress tick per canned InstalledMods entry
// (mirroring purgeProgressLine's real per-mod PurgeModPurged tick in
// service_core.go, one level up - the prototype has no percentage-based
// phases to fake, so it ticks by mod rather than by 0/50/100%, unlike
// fakeProgressTicks' ApplyInstall/ApplyUpdate precedent), then flips every
// entry to "disabled" - the prototype's own "not deployed" terminal state
// (see DisableMod above; prototype.Mod has no separate "deployed"/"enabled"
// distinction the way coreProvider's installedModStatus does - see Mod.Status'
// doc comment), decrementing Stats.Enabled for whichever entries weren't
// already disabled. An empty InstalledMods list short-circuits to "no mods
// installed" with no ticks emitted at all, mirroring coreProvider's own
// empty-mods short-circuit (see its doc comment).
func (p *prototypeProvider) PurgeProfile(_ context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
	mods := p.activeMods()
	if len(mods) == 0 {
		return ActionOutcome{Message: "no mods installed"}, nil
	}

	purged := 0
	for i := range mods {
		mod := &mods[i]
		if progress != nil {
			progress(ActionProgress{Line: fmt.Sprintf("✓ %s", mod.Name), Percent: -1})
		}
		if mod.Status != "disabled" {
			p.adjustStats(0, -1)
			mod.Status = "disabled"
		}
		purged++
	}

	return ActionOutcome{Message: fmt.Sprintf("Purged %d mod(s)", purged)}, nil
}
