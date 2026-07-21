package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// This file wires Task 6's generic confirmation-modal/action machinery
// (actions.go) to concrete keybindings (keys.go's ToggleEnable/Uninstall/
// Deploy, and Select reused for profile switch - see updateKey's Select
// case): what each key builds as a pendingAction, and - for profile switch,
// which needs an async read before it can even show a modal - the extra
// plan-fetch state machine. See task-7-brief.md for the exact modal
// copy/keybindings this implements.

// selectedMod returns the currently-selected Installed Mods row, or false
// if the selection is out of range (covers both an empty list and the
// general "selection can't have drifted past clampSelections, but a nil
// mods slice with a stale selected index is still possible on the very
// first render before any data has loaded" case).
func (m Model) selectedMod() (ModItem, bool) {
	idx := m.selected[ScreenInstalledMods]
	if idx < 0 || idx >= len(m.mods) {
		return ModItem{}, false
	}
	return m.mods[idx], true
}

// gameProfileDetail renders the "Game: <name>" / "Profile: <name>" detail
// lines shared by the Enable/Disable/Uninstall modals, plus one caller-owned
// trailing line describing the mutation's effect.
func (m Model) gameProfileDetail(effect string) []string {
	return []string{
		fmt.Sprintf("Game: %s", m.summary.GameName),
		fmt.Sprintf("Profile: %s", m.summary.ProfileName),
		effect,
	}
}

// toggleSelectedModEnable handles 'e' on Installed Mods: the direction
// comes from the selected item's Status (task-7-brief.md's Keybindings
// section) - "disabled" enables, anything else (coreProvider's "enabled"/
// "deployed", or any other in-progress-flavor status a source might report)
// disables. A no-op on the wrong screen, an empty list, or with no
// ActionProvider configured.
func (m Model) toggleSelectedModEnable() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}
	if item.Status == "disabled" {
		return m.promptEnable(item)
	}
	return m.promptDisable(item)
}

func (m Model) promptEnable(item ModItem) (Model, tea.Cmd) {
	title := fmt.Sprintf("Enable %q?", item.Name)
	detail := m.gameProfileDetail("Files will be deployed to the game directory.")
	model, pa := m.buildAction(actionEnable, title, detail, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.EnableMod(ctx, item)
	})
	return model.promptAction(pa), nil
}

func (m Model) promptDisable(item ModItem) (Model, tea.Cmd) {
	title := fmt.Sprintf("Disable %q?", item.Name)
	detail := m.gameProfileDetail("Files will be removed from the game directory (cache kept).")
	model, pa := m.buildAction(actionDisable, title, detail, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.DisableMod(ctx, item)
	})
	return model.promptAction(pa), nil
}

// uninstallSelectedMod handles 'x' on Installed Mods. A no-op on the wrong
// screen, an empty list, or with no ActionProvider configured.
func (m Model) uninstallSelectedMod() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}
	title := fmt.Sprintf("Uninstall %q?", item.Name)
	detail := m.gameProfileDetail("Removes deployed files, cache, and profile entry. Uninstall hooks will run.")
	model, pa := m.buildAction(actionUninstall, title, detail, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.UninstallMod(ctx, item)
	})
	return model.promptAction(pa), nil
}

// deployActiveProfile handles 'D' on Dashboard or Installed Mods
// (task-7-brief.md's Keybindings section). Unlike the mod-scoped actions
// above, deploying doesn't depend on any row selection, so an empty mods
// list is not a no-op case here - deploying zero enabled mods is a valid
// (if unusual) outcome the provider itself reports. Link method is omitted
// from the detail: it isn't exposed anywhere in DataProvider/Summary, and
// this task's scope keeps DataProvider frozen, so it isn't "cheaply
// available" per the brief's own qualifier.
func (m Model) deployActiveProfile() (Model, tea.Cmd) {
	if (m.screen != ScreenDashboard && m.screen != ScreenInstalledMods) || m.actions == nil {
		return m, nil
	}
	title := fmt.Sprintf("Deploy profile %q?", m.summary.ProfileName)
	detail := []string{
		fmt.Sprintf("Game: %s", m.summary.GameName),
		fmt.Sprintf("Mods: %d enabled", m.summary.Enabled),
	}
	model, pa := m.buildAction(actionDeploy, title, detail, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.DeployProfile(ctx)
	})
	return model.promptAction(pa), nil
}

// planResultMsg carries a successful PlanProfileSwitch result, tagged with
// the generation established when the fetch was dispatched (see
// switchSelectedProfile) so a superseded result can be discarded exactly
// like actionDoneMsg/actionFailedMsg (actions.go).
type planResultMsg struct {
	gen  int
	view SwitchPlanView
}

// planFailedMsg carries a failed PlanProfileSwitch call, tagged like
// planResultMsg.
type planFailedMsg struct {
	gen int
	err error
}

// switchSelectedProfile handles enter on Profiles (task-7-brief.md's
// profile-switch flow): a no-op on the wrong screen, an empty list, with no
// ActionProvider, or while another action/plan is already in flight
// (single-flight, checked explicitly here since this branch runs before any
// pendingAction exists for buildAction/promptAction's own guard to catch).
// The active profile resolves synchronously ("Already on profile <name>",
// no modal - see resolvePlanResult's AlreadyActive branch for the
// defensive counterpart of this same check); any other profile dispatches
// an async PlanProfileSwitch, reusing action.gen/action.cancel exactly like
// buildAction does, so the result is subject to the same staleness
// discipline before it's allowed to open a modal.
func (m Model) switchSelectedProfile() (Model, tea.Cmd) {
	if m.screen != ScreenProfiles || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}

	idx := m.selected[ScreenProfiles]
	if idx < 0 || idx >= len(m.profiles) {
		return m, nil
	}
	profile := m.profiles[idx]
	if profile.Active {
		m.action.status = fmt.Sprintf("Already on profile %q", profile.Name)
		m.action.statusIsError = false
		return m, nil
	}

	if m.action.cancel != nil {
		m.action.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.action.cancel = cancel
	m.action.gen++
	gen := m.action.gen
	m.action.running = true
	m.action.status = "Planning switch…"
	m.action.statusIsError = false

	actions := m.actions
	name := profile.Name
	return m, func() tea.Msg {
		view, err := actions.PlanProfileSwitch(ctx, name)
		if err != nil {
			return planFailedMsg{gen: gen, err: err}
		}
		return planResultMsg{gen: gen, view: view}
	}
}

// resolvePlanResult handles a fresh (non-stale - callers check msg.gen
// first) planResultMsg. AlreadyActive resolves to a status-line message
// with no modal (task-7-brief.md's profile-switch flow); this is defensive
// only, since switchSelectedProfile already pre-filters the active profile
// synchronously and never reaches the async fetch for it. Any other plan -
// including one with NeedsDownloads entries (Phase 5b Task 4 LIFTED the
// refusal that used to short-circuit those here; see
// errProfileNeedsDownloads's removal in actions_provider.go/
// service_core.go - ApplyProfileSwitch can download now) - opens the switch
// confirmation modal via buildAction, which establishes its OWN fresh
// gen/cancel/progress-channel for the eventual ApplyProfileSwitch call -
// running/cancel from the plan fetch are cleared first, so buildAction's
// single-flight guard passes cleanly. The progress adapter buildAction
// wires in is threaded straight through to ApplyProfileSwitch, so a plan
// that needs downloads streams them via the same pump every other network
// action uses. switchDetailLines does not yet render the NeedsDownloads
// list itself - Task 5/7 polish, out of this task's scope - so today's
// modal shows only the Enable/Disable buckets for such a plan.
func (m Model) resolvePlanResult(msg planResultMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}

	view := msg.view
	if view.AlreadyActive {
		m.action.status = fmt.Sprintf("Already on profile %q", view.To)
		m.action.statusIsError = false
		return m, nil
	}

	title := fmt.Sprintf("Switch to %q?", view.To)
	model, pa := m.buildAction(actionSwitch, title, switchDetailLines(view), view.To, func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.ApplyProfileSwitch(ctx, view.To, progress)
	})
	return model.promptAction(pa), nil
}

// resolvePlanFailure handles a fresh planFailedMsg: status line error, no
// modal, mirroring actionFailedMsg's own rendering (actions.go's
// singleLine/statusIsError contract).
func (m Model) resolvePlanFailure(msg planFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	m.action.status = singleLine(msg.err.Error())
	m.action.statusIsError = true
	return m, nil
}

// switchDetailLines renders a SwitchPlanView as the switch modal's detail
// lines: "From: <From>", then one "+ <name>" line per mod to enable and one
// "- <name>" line per mod to disable - the CLI's own +/- convention (see
// switchPlanView's doc comment in service_core.go) - so the modal's
// existing "+N more" overflow collapsing (actionModalView) applies per mod
// instead of truncating one giant joined line. NoChanges plans render a
// single explanatory line instead, per task-7-brief.md.
func switchDetailLines(view SwitchPlanView) []string {
	if view.NoChanges {
		return []string{fmt.Sprintf("From: %s", view.From), "No mod changes; set as default."}
	}
	lines := []string{fmt.Sprintf("From: %s", view.From)}
	for _, name := range view.Enable {
		lines = append(lines, fmt.Sprintf("+ %s", name))
	}
	for _, name := range view.Disable {
		lines = append(lines, fmt.Sprintf("- %s", name))
	}
	return lines
}
