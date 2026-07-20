package tui

import (
	"context"
	"errors"
	"fmt"
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
// Every method that returns an error must not mutate anything: EnableMod/
// DisableMod/UninstallMod/DeployProfile propagate the underlying flow's own
// error-path guarantees, and ApplyProfileSwitch's own NeedsDownloads refusal
// (see its doc comment) is checked before any mutation is attempted.
type ActionProvider interface {
	EnableMod(ctx context.Context, item ModItem) (ActionOutcome, error)
	DisableMod(ctx context.Context, item ModItem) (ActionOutcome, error)
	UninstallMod(ctx context.Context, item ModItem) (ActionOutcome, error)
	DeployProfile(ctx context.Context) (ActionOutcome, error)
	PlanProfileSwitch(ctx context.Context, profile string) (SwitchPlanView, error)
	ApplyProfileSwitch(ctx context.Context, profile string) (ActionOutcome, error)
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
// (there is no real "mod exists on some source but isn't cached" concept in
// canned data), so ApplyProfileSwitch's NeedsDownloads refusal exists here
// purely for interface-contract parity with coreProvider and is not
// exercised by the prototype's own deterministic planner.

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

func (p *prototypeProvider) profileExists(name string) bool {
	for _, pr := range p.data.Profiles {
		if pr.Name == name {
			return true
		}
	}
	return false
}

// PlanProfileSwitch computes a fake-but-consistent plan from prototype
// data: alternating InstalledMods entries (by index) toggle between the two
// buckets, giving a plausible, deterministic, non-empty mix of Enable/
// Disable for any target profile other than the active one. See this file's
// package doc comment on why NeedsDownloads is always empty here.
func (p *prototypeProvider) PlanProfileSwitch(_ context.Context, profileName string) (SwitchPlanView, error) {
	if !p.profileExists(profileName) {
		return SwitchPlanView{}, fmt.Errorf("profile not found: %s", profileName)
	}

	current := p.activeProfileName()
	if profileName == current {
		return SwitchPlanView{From: current, To: profileName, AlreadyActive: true}, nil
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

// ApplyProfileSwitch re-plans and applies, refusing (mirroring coreProvider,
// for interface-contract parity - see this file's package doc comment on
// why this branch is unreachable via the prototype's own planner today) if
// the fresh plan has NeedsDownloads entries.
func (p *prototypeProvider) ApplyProfileSwitch(ctx context.Context, profileName string) (ActionOutcome, error) {
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
