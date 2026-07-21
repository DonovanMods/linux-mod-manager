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
// failed action was a no-op. PlanProfileSwitch is the exception: planning is
// pure and never mutates. ApplyProfileSwitch's NeedsDownloads refusal is
// also mutation-free (it refuses before executing).
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
	NeedsDownloads []string // mod refs requiring download - 5a refuses to ApplyProfileSwitch a plan with any of these (see ApplyProfileSwitch's doc comment); Plan itself never refuses, so a modal can still explain why
	NoChanges      bool
	AlreadyActive  bool
}

// errProfileNeedsDownloads is ApplyProfileSwitch's exact refusal error (see
// its doc comment on coreProvider and the prototype implementation below):
// 5a's TUI has no download/install path yet, so a plan that would require
// one is refused outright rather than silently doing less than the CLI.
var errProfileNeedsDownloads = errors.New("profile needs downloads — use 'lmm profile switch' until TUI install ships")

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
// ApplyProfileSwitch's NeedsDownloads refusal end to end. Every other
// canned profile leaves Mods unset and is unaffected.

// findInstalledIndex returns the index of the InstalledMods entry matching
// (sourceID, id), or -1 if none matches.
func (p *prototypeProvider) findInstalledIndex(sourceID, id string) int {
	for i, mod := range p.data.InstalledMods {
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
	if p.data.InstalledMods[idx].Status != "disabled" {
		return ActionOutcome{Message: fmt.Sprintf("%q is already enabled", item.Name)}, nil
	}
	p.data.InstalledMods[idx].Status = "installed"
	p.data.Stats.Enabled++
	return ActionOutcome{Message: fmt.Sprintf("Enabled %q", item.Name)}, nil
}

func (p *prototypeProvider) DisableMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	idx := p.findInstalledIndex(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	if p.data.InstalledMods[idx].Status == "disabled" {
		return ActionOutcome{Message: fmt.Sprintf("%q is already disabled", item.Name)}, nil
	}
	p.data.InstalledMods[idx].Status = "disabled"
	p.data.Stats.Enabled--
	return ActionOutcome{Message: fmt.Sprintf("Disabled %q", item.Name)}, nil
}

func (p *prototypeProvider) UninstallMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	idx := p.findInstalledIndex(item.Source, item.ID)
	if idx < 0 {
		return ActionOutcome{}, fmt.Errorf("mod not found: %s", item.ID)
	}
	wasEnabled := p.data.InstalledMods[idx].Status != "disabled"
	p.data.InstalledMods = append(p.data.InstalledMods[:idx], p.data.InstalledMods[idx+1:]...)
	p.data.Stats.Installed--
	if wasEnabled {
		p.data.Stats.Enabled--
	}
	return ActionOutcome{Message: fmt.Sprintf("Uninstalled %q", item.Name)}, nil
}

func (p *prototypeProvider) DeployProfile(_ context.Context) (ActionOutcome, error) {
	deployed := 0
	for _, mod := range p.data.InstalledMods {
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
// NeedsDownloads-only plan instead - ApplyProfileSwitch refuses on that
// alone, so there's no need to also compute a bucket split that could never
// be applied.
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
	for i, mod := range p.data.InstalledMods {
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

// ApplyProfileSwitch re-plans and applies, refusing (mirroring coreProvider)
// if the fresh plan has NeedsDownloads entries - reachable via the
// prototype's own planner for prototype.NeedsDownloadProfileName (see this
// file's package doc comment).
//
// TODO(Phase 5b Task 4, part B): progress is accepted (interface parity with
// the pump - Part A) but not yet emitted, and the NeedsDownloads refusal
// below is not yet lifted; both land together in this task's second
// RED/GREEN pair, which replaces this refusal with a working download demo.
func (p *prototypeProvider) ApplyProfileSwitch(ctx context.Context, profileName string, progress func(ActionProgress)) (ActionOutcome, error) {
	_ = progress
	view, err := p.PlanProfileSwitch(ctx, profileName)
	if err != nil {
		return ActionOutcome{}, err
	}
	if view.AlreadyActive {
		return ActionOutcome{Message: fmt.Sprintf("Already on profile %q", profileName)}, nil
	}
	if len(view.NeedsDownloads) > 0 {
		return ActionOutcome{}, errProfileNeedsDownloads
	}

	enable := make(map[string]bool, len(view.Enable))
	for _, name := range view.Enable {
		enable[name] = true
	}
	disable := make(map[string]bool, len(view.Disable))
	for _, name := range view.Disable {
		disable[name] = true
	}
	for i := range p.data.InstalledMods {
		switch name := p.data.InstalledMods[i].Name; {
		case enable[name]:
			p.data.InstalledMods[i].Status = "installed"
		case disable[name]:
			p.data.InstalledMods[i].Status = "disabled"
		}
	}

	for i := range p.data.Profiles {
		p.data.Profiles[i].Active = p.data.Profiles[i].Name == profileName
	}
	p.data.Profile.Name = profileName

	return ActionOutcome{Message: fmt.Sprintf("Switched to %q", profileName)}, nil
}
