package tui

import (
	"context"
	"fmt"
	"strings"

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

// --- Deployed files ('f' on Installed Mods) ---

// showDeployedFiles handles 'f' on Installed Mods (task-4-brief.md): opens a
// read-only overlay listing the selected mod's deployed file paths. A no-op
// on the wrong screen, an empty list, or with no DataProvider configured -
// mirrors uninstallSelectedMod's guard/selection shape, using m.provider
// instead of m.actions since this is a read, not a mutation.
//
// Unlike every other handler in this file, the DeployedFiles call below is
// made SYNCHRONOUSLY rather than dispatched as an async tea.Cmd: it's a
// local DB read (coreProvider.DeployedFiles, service_core.go), not a network
// call, so the async-dispatch discipline installSelectedSearchResult/
// switchSelectedProfile/checkForUpdates follow (status line + tea.Cmd +
// staleness-checked result message) doesn't apply here - this is the one
// documented exception.
//
// No extra single-flight/other-modal guard is needed here: updateKey only
// ever reaches the outer switch this is dispatched from when
// m.action.pending, m.picker, m.inputModal, and m.overlay are ALL already
// nil, so promptOverlay's own guard (overlay.go) can never actually refuse
// on this path - it's kept anyway for defense-in-depth, exactly like every
// other promptX call in this file.
func (m Model) showDeployedFiles() (tea.Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.provider == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}

	files, err := m.provider.DeployedFiles(item.Source, item.ID)
	if err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, nil
	}

	lines := files
	if len(lines) == 0 {
		lines = []string{"no files deployed"}
	}
	m = m.promptOverlay(infoOverlay{title: fmt.Sprintf("Files — %s", item.Name), lines: lines})
	return m, nil
}

// --- Update policy ('P' on Installed Mods) ---

// updatePolicyOptions is the fixed notify/auto/pin option order the policy
// picker always shows (task-5-brief.md's Keybindings section), independent
// of the selected mod's actual current policy - which only decides which
// option starts pre-selected and marked "current" (see
// editSelectedModPolicy).
var updatePolicyOptions = []string{"notify", "auto", "pin"}

// policyChosenMsg carries the option the user picked in the update-policy
// picker (see editSelectedModPolicy), naming both the ModItem the picker was
// opened for and the chosen policy string.
//
// Unlike planResultMsg/installPlanResultMsg/checkUpdatesResultMsg below
// (which resolve an ASYNC network fetch dispatched earlier, tagged with a
// gen for staleness), this message exists purely so the actual buildAction
// call - which must run against the LIVE Model, not a value captured by the
// picker's own choose closure - happens from inside Update(), the same way
// updatePendingActionKey's ConfirmAction branch runs buildAction's result
// against the live model when the user presses y/enter. Bubble Tea models
// are values: a closure built when editSelectedModPolicy opened the picker
// closes over the Model as it was AT THAT MOMENT, but pendingPicker.choose's
// signature is `func(idx int) tea.Cmd` - it cannot hand back a mutated
// Model, only a Cmd. If buildAction ran directly inside that closure, the
// gen/cancel/progressCh bookkeeping it computes would be stamped onto a
// Model copy nothing ever adopts, while choosePickerOption (picker.go)
// separately returns the UNMODIFIED live model (with only .picker cleared)
// as the new current Model - so the eventual actionDoneMsg's gen would never
// match live m.action.gen and would be silently discarded as stale. Routing
// through this message instead means choose's Cmd carries no I/O at all: it
// fires on the very next Bubble Tea tick, and resolvePolicyChoice - which
// DOES receive the live Model as its method receiver - does the actual
// buildAction call there, exactly like the confirm-modal path does.
type policyChosenMsg struct {
	item   ModItem
	policy string
}

// editSelectedModPolicy handles 'P' on Installed Mods (task-5-brief.md's
// update-policy flow): a no-op on the wrong screen, an empty list, or with
// no ActionProvider configured - mirrors uninstallSelectedMod/
// showDeployedFiles' guard/selection shape. Opens a 3-option (notify/auto/
// pin) picker with the selected mod's CURRENT policy (item.UpdatePolicy -
// populated by coreProvider's Overview mapping / prototypeProvider's canned
// data, see ModItem's doc comment) pre-selected and labeled "current"; a
// mod whose UpdatePolicy doesn't match any of the three options (e.g. the
// zero value "") simply leaves the picker on its default selection (index
// 0, "notify") with no option marked "current" - it never guesses.
//
// Picking an option dispatches the action immediately - task-5-brief.md: "no
// second confirm gate, the pick IS the confirmation" - via policyChosenMsg/
// resolvePolicyChoice; see policyChosenMsg's own doc comment for why that
// indirection is required rather than calling buildAction directly inside
// choose.
func (m Model) editSelectedModPolicy() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}

	options := make([]pickerOption, len(updatePolicyOptions))
	selected := 0
	for i, policy := range updatePolicyOptions {
		options[i] = pickerOption{Label: policy}
		if policy == item.UpdatePolicy {
			options[i].Note = "current"
			selected = i
		}
	}

	picker := pendingPicker{
		title:    fmt.Sprintf("Update policy — %s", item.Name),
		options:  options,
		selected: selected,
		choose: func(idx int) tea.Cmd {
			policy := updatePolicyOptions[idx]
			return func() tea.Msg { return policyChosenMsg{item: item, policy: policy} }
		},
	}
	return m.promptPicker(picker), nil
}

// resolvePolicyChoice handles a policyChosenMsg: builds the actionSetPolicy
// action for msg.item/msg.policy and confirms it immediately, mirroring
// updatePendingActionKey's ConfirmAction branch (actions.go) - buildAction
// runs here, against m (this method's receiver, the CURRENT live Model), so
// its gen/cancel/progressCh bookkeeping stays consistent with whatever
// actionDoneMsg/actionProgressMsg eventually arrives (see policyChosenMsg's
// doc comment for why this can't happen inside the picker's choose closure
// itself). No pendingAction/confirmation modal is ever shown - the picker
// selection already WAS the user's confirmation (task-5-brief.md) - so this
// sets action.running directly instead of calling promptAction.
// Single-flight is checked HERE, not left to buildAction's own guard: a
// policyChosenMsg is an in-flight message, and the window between the pick
// (picker cleared, running still false) and this resolution is real - a
// second 'P' press there opens a second picker and yields a second
// policyChosenMsg, and a 'D' press opens a confirm modal. A message
// arriving while an action is already running or a confirmation is already
// pending is dropped entirely - mirroring the stale-gen discards the
// resolve* family's callers perform (app.go) - because relying on
// buildAction's refusal alone would leave this method setting
// running=true below for an action that never actually started, sticking
// the single-flight guard with nothing to ever clear it.
func (m Model) resolvePolicyChoice(msg policyChosenMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	item := msg.item
	policy := msg.policy
	title := fmt.Sprintf("Update policy — %s", item.Name)
	actions := m.actions
	model, pa := m.buildAction(actionSetPolicy, title, nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return actions.SetUpdatePolicy(ctx, item, policy)
	})
	model.action.running = true
	return model, pa.confirm()
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
// action uses. switchDetailLines renders a download-disclosure header plus
// one line per NeedsDownloads ref (see its own doc comment), so the modal
// makes it clear confirming a purely-downloading plan starts network
// downloads before the user commits.
func (m Model) resolvePlanResult(msg planResultMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Copilot PR #63 finding: a quit-triggered drain (see startQuit) that was
	// waiting on THIS plan fetch resolves the instant it lands, exactly like
	// actionDoneMsg/actionFailedMsg already do for a running mutation - see
	// resolveDrainedQuit's own doc comment. Checked BEFORE the AlreadyActive
	// status line and the switch-modal open below: the app is exiting, so
	// neither a status write nor a freshly-opened confirmation modal would
	// ever be seen.
	if m.action.draining {
		return m.resolveDrainedQuit()
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
	// Copilot PR #63 finding (mirrors resolvePlanResult above): resolve a
	// quit-triggered drain immediately rather than writing a status line no
	// one will ever see.
	if m.action.draining {
		return m.resolveDrainedQuit()
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
//
// A plan with NeedsDownloads entries (Phase 5b Task 4 lifted the refusal
// that used to short-circuit these here - ApplyProfileSwitch downloads and
// installs them itself now) additionally renders a "Will download & install
// N mod(s):" header plus one "↓ <ref>" line per entry, mirroring the CLI's
// own pre-confirm disclosure (cmd/lmm/profile.go's doProfileSwitch: "Will
// install %d mod(s):" + "  ↓ %s:%s v%s\n" per ref) - without this, the modal
// would open with no indication that confirming starts network downloads.
//
// The download disclosure is placed IMMEDIATELY after "From:", before the
// Enable/Disable buckets (I2 review finding): actionModalView's "+N more"
// truncation collapses whatever detail lines don't fit its budget, and a
// busy switch (many Enable/Disable rows) previously pushed the disclosure -
// appended last - past that budget, silently hiding the one line that warns
// confirming starts network downloads. Leading with it instead means
// truncation eats the less-critical Enable/Disable tail first.
func switchDetailLines(view SwitchPlanView) []string {
	if view.NoChanges {
		return []string{fmt.Sprintf("From: %s", view.From), "No mod changes; set as default."}
	}
	lines := []string{fmt.Sprintf("From: %s", view.From)}
	if len(view.NeedsDownloads) > 0 {
		lines = append(lines, fmt.Sprintf("Will download & install %d mod(s):", len(view.NeedsDownloads)))
		for _, ref := range view.NeedsDownloads {
			lines = append(lines, fmt.Sprintf("↓ %s", ref))
		}
	}
	for _, name := range view.Enable {
		lines = append(lines, fmt.Sprintf("+ %s", name))
	}
	for _, name := range view.Disable {
		lines = append(lines, fmt.Sprintf("- %s", name))
	}
	return lines
}

// --- Install from search ('i' on Search, blurred, a result selected) ---

// installPlanResultMsg carries a successful PlanInstall result, tagged with
// the generation established when the fetch was dispatched (see
// installSelectedSearchResult) so a superseded result can be discarded
// exactly like planResultMsg. item is the ModItem that was selected at
// DISPATCH time (not re-read from selection state on arrival): the search
// result list's selection isn't locked while "Planning install…" is
// in-flight (running only blocks a NEW buildAction/promptAction call, not
// plain navigation - see updateKey), so capturing item in the closure that
// dispatches the fetch, then carrying it through unchanged in this message,
// is the only way to guarantee ApplyInstall is later called with the SAME
// mod the user actually pressed 'i' on. InstallPlanView itself carries no
// (Source, ID) - only display fields - so item is this message's sole
// source of truth for what to install.
type installPlanResultMsg struct {
	gen  int
	item ModItem
	view InstallPlanView
}

// installPlanFailedMsg carries a failed PlanInstall call, tagged like
// installPlanResultMsg.
type installPlanFailedMsg struct {
	gen int
	err error
}

// installSelectedSearchResult handles 'i' on Search (task-5-brief.md's
// Install-from-search flow): a no-op on the wrong screen, with no
// ActionProvider, while another action/plan is already in flight, when the
// search isn't in searchReady (idle/loading/failed/auth-required - see
// below), or with no result selected (covers both an empty page and a stale
// selected index). The focused-input case never reaches here at all -
// updateKey's focused-input branch (app.go) intercepts every key, including
// 'i', before the outer switch this is dispatched from, so 'i' types into
// the query exactly like every other letter. Mirrors switchSelectedProfile's
// async plan-fetch shape (mutations.go's template for this pattern):
// dispatches PlanInstall and shows a "Planning install…" status instead of a
// modal until the result arrives.
//
// The searchReady check (Copilot review finding on PR #63): startSearch
// bumps m.search.state to searchLoading for a new query WITHOUT clearing
// m.search.page, so the previous query's results linger in m.search.page
// through searchLoading (and, more incidentally, through
// searchIdle/searchFailed/searchAuthRequired too - none of which should ever
// still show old results, but the state is re-checked defensively rather
// than assumed). Reading m.search.page.Results without this guard would let
// 'i' plan-and-install a result that isn't the one currently displayed -
// e.g. while the screen reads "Consulting the archive index…". Mirrors
// refreshSearchAfterInstall's own state check and app.go's next/prev-page
// guards, which already gate on searchReady before touching m.search.page.
func (m Model) installSelectedSearchResult() (Model, tea.Cmd) {
	if m.screen != ScreenSearch || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	if m.search.state != searchReady {
		return m, nil
	}

	idx := m.selected[ScreenSearch]
	results := m.search.page.Results
	if idx < 0 || idx >= len(results) {
		return m, nil
	}
	item := results[idx]

	if m.action.cancel != nil {
		m.action.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.action.cancel = cancel
	m.action.gen++
	gen := m.action.gen
	m.action.running = true
	m.action.status = "Planning install…"
	m.action.statusIsError = false

	actions := m.actions
	return m, func() tea.Msg {
		view, err := actions.PlanInstall(ctx, item)
		if err != nil {
			return installPlanFailedMsg{gen: gen, err: err}
		}
		return installPlanResultMsg{gen: gen, item: item, view: view}
	}
}

// resolveInstallPlanResult handles a fresh (non-stale) installPlanResultMsg:
// opens the install/reinstall confirmation modal, mirroring
// resolvePlanResult's shape. Confirming calls ApplyInstall with msg.item -
// the mod captured when the fetch was dispatched, per installPlanResultMsg's
// own doc comment - and the progress adapter buildAction wires in, so
// download/extract/deploy ticks stream into the status line exactly like
// every other network action.
func (m Model) resolveInstallPlanResult(msg installPlanResultMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Copilot PR #63 finding (mirrors resolvePlanResult): resolve a
	// quit-triggered drain immediately instead of opening the install/
	// reinstall confirmation modal below - the app is exiting.
	if m.action.draining {
		return m.resolveDrainedQuit()
	}

	view := msg.view
	item := msg.item
	model, pa := m.buildAction(actionInstall, installTitle(view), installDetailLines(view), "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.ApplyInstall(ctx, item, progress)
	})
	return model.promptAction(pa), nil
}

// resolveInstallPlanFailure handles a fresh installPlanFailedMsg: status
// line error, no modal, mirroring resolvePlanFailure. err is already the
// per-action mapped message from the provider (mapInstallNetworkError in
// service_core.go) when backed by coreProvider - this just renders it.
func (m Model) resolveInstallPlanFailure(msg installPlanFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Copilot PR #63 finding (mirrors resolvePlanFailure): resolve a
	// quit-triggered drain immediately rather than writing a status line no
	// one will ever see.
	if m.action.draining {
		return m.resolveDrainedQuit()
	}
	m.action.status = singleLine(msg.err.Error())
	m.action.statusIsError = true
	return m, nil
}

// installTitle renders an InstallPlanView's modal title: "Reinstall" when
// the mod is already installed (view.Reinstall), "Install" otherwise - the
// only distinction task-5-brief.md asks the title to carry for an
// already-installed search result.
func installTitle(view InstallPlanView) string {
	if view.Reinstall {
		return fmt.Sprintf("Reinstall %q?", view.Name)
	}
	return fmt.Sprintf("Install %q?", view.Name)
}

// installDetailLines renders an InstallPlanView as the install/reinstall
// modal's detail lines (task-5-brief.md's Install-from-search flow):
// version+size, source, the file(s) that will download, resolved
// dependencies with a "Will download & install N mod(s)" disclosure
// mirroring switchDetailLines' own NeedsDownloads wording, one line per
// conflicting file, and finally the two warning lines for a
// missing-dependency or circular-dependency plan.
//
// Files and Dependencies are each rendered as ONE comma-joined line rather
// than one line per entry (unlike switchDetailLines' +/- per-mod lines):
// task-5-brief.md leaves the choice to the implementer ("pick what reads
// best at 160 cols, document"). A mod's file/dependency list is typically
// short and reads naturally as a single sentence at the ~160-col design
// width, and keeping each to one line leaves more of
// actionModalMaxDetailLines' budget for the Conflicts/warning lines that
// matter more to a confirm decision - those stay one line per entry since
// each conflict is independently actionable information. Overlong lines
// still truncate individually at render time (actionModalView), same as
// every other detail line, so this degrades the same way below 160 cols.
func installDetailLines(view InstallPlanView) []string {
	lines := []string{
		fmt.Sprintf("Version: %s (%s)", view.Version, view.SizeLabel),
		fmt.Sprintf("Source: %s", view.Source),
	}
	if len(view.Files) > 0 {
		lines = append(lines, fmt.Sprintf("Files: %s", strings.Join(view.Files, ", ")))
	}
	if len(view.Dependencies) > 0 {
		lines = append(lines, fmt.Sprintf("Dependencies: %s", strings.Join(view.Dependencies, ", ")))
		lines = append(lines, fmt.Sprintf("Will download & install %d mod(s)", len(view.Dependencies)))
	}
	for _, c := range view.Conflicts {
		lines = append(lines, fmt.Sprintf("Conflicts: %s", c))
	}
	if len(view.MissingDependencies) > 0 {
		lines = append(lines, fmt.Sprintf("⚠ %d dependency(ies) unavailable", len(view.MissingDependencies)))
	}
	if view.CycleWarning {
		lines = append(lines, "⚠ circular dependency detected")
	}
	return lines
}

// refreshSearchAfterInstall re-issues the CURRENT search query after a
// successful install, so the just-installed result's "installed" marker
// updates immediately instead of waiting for the user to search again by
// hand (task-5-brief.md: "verify the refresh path covers the search
// results' installed-flag, and fix within internal/tui if not" - the
// generic post-action refresh, m.loadData, only re-fetches Overview/
// Profiles, never the search page, since no other mutation needs it to).
// A no-op (nil cmd, m unchanged) when there's no completed search to
// refresh (searchIdle/searchLoading/searchFailed/searchAuthRequired) -
// installSelectedSearchResult can only ever have been reached FROM
// searchReady (it requires a selected result), but a slow install leaves
// running enough time for the user to navigate off Search and even start a
// new query before this runs, so the state is re-checked here rather than
// assumed.
func (m Model) refreshSearchAfterInstall() (Model, tea.Cmd) {
	if m.search.state != searchReady {
		return m, nil
	}
	return m.startSearch(m.search.page.Query, m.search.page.Page)
}

// --- Check/apply updates ('u' on Dashboard and Installed Mods) ---

// checkUpdatesResultMsg carries a successful CheckUpdates result, tagged
// with the generation established when the fetch was dispatched (see
// checkForUpdates) so a superseded result can be discarded.
type checkUpdatesResultMsg struct {
	gen  int
	view UpdatesView
}

// checkUpdatesFailedMsg carries a failed CheckUpdates call, tagged like
// checkUpdatesResultMsg. CheckUpdates itself rarely returns a non-nil error
// (coreProvider folds per-source failures into UpdatesView.Warnings instead
// - see its own doc comment) - this path exists for the cases that still
// can (e.g. the installed-mods lookup itself failing).
type checkUpdatesFailedMsg struct {
	gen int
	err error
}

// checkForUpdates handles 'u' on Dashboard/Installed Mods (task-5-brief.md's
// Updates flow): a no-op on the wrong screen, with no ActionProvider, or
// while another action/plan is already in flight. Mirrors
// installSelectedSearchResult/switchSelectedProfile's async plan-fetch
// shape: dispatches CheckUpdates and shows a "Checking for updates…" status
// instead of a modal until the result arrives - resolveCheckUpdatesResult
// decides whether that becomes a status line (zero updates) or a
// confirmation modal (one or more).
func (m Model) checkForUpdates() (Model, tea.Cmd) {
	if (m.screen != ScreenDashboard && m.screen != ScreenInstalledMods) || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
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
	m.action.status = "Checking for updates…"
	m.action.statusIsError = false

	actions := m.actions
	return m, func() tea.Msg {
		view, err := actions.CheckUpdates(ctx)
		if err != nil {
			return checkUpdatesFailedMsg{gen: gen, err: err}
		}
		return checkUpdatesResultMsg{gen: gen, view: view}
	}
}

// resolveCheckUpdatesResult handles a fresh checkUpdatesResultMsg. Either
// way, m.summary.Updates is set to the real count (task-5-brief.md's
// Dashboard summary tie-in: Summary.Updates renders the "?" sentinel, -1,
// until a check has actually run) - this is the model's own in-memory
// count, not a DataProvider change (m.loadData re-reads Overview, which
// still reports -1 until Phase 6 gives DataProvider its own persistent
// Updates count - an accepted tradeoff per task-5-brief.md's own "no
// DataProvider change" framing). The dataLoadedMsg handler in app.go
// preserves this known count across an UNRELATED refresh rather than
// reverting it to the DataProvider's sentinel (a fix-wave-1 correction to
// this comment's earlier claim that it reverted); it re-sentinels back to
// -1 only when an update-apply batch actually completes (actionDoneMsg,
// app.go), since applying updates is the one case that genuinely makes the
// count stale.
//
// Zero updates resolves synchronously to a status line (formatOutcomeStatus
// reused for its Message-plus-Warnings rendering convention, rather than
// hand-rolling a second "(N warnings)" formatter - see mergeDiagnostics'
// sibling reasoning) with no modal; one or more updates opens the batch
// confirmation modal, whose confirm calls applyUpdatesSequentially with the
// WHOLE update list captured here (task-5-brief.md: "Sequential-apply loop
// lives in the confirm closure... one action gen/single-flight scope for
// the whole batch"). Per-item selection is explicitly out of scope
// (task-5-brief.md: "Per-item selection is Phase 6 - do not build it").
func (m Model) resolveCheckUpdatesResult(msg checkUpdatesResultMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Copilot PR #63 finding (mirrors resolvePlanResult): resolve a
	// quit-triggered drain immediately instead of touching m.summary.Updates,
	// writing the zero-updates status line, or opening the batch
	// confirmation modal below - the app is exiting, so none of that would
	// ever be seen.
	if m.action.draining {
		return m.resolveDrainedQuit()
	}

	view := msg.view
	m.summary.Updates = len(view.Updates)

	if len(view.Updates) == 0 {
		m.action.status = formatOutcomeStatus(ActionOutcome{Message: "No updates available.", Warnings: view.Warnings})
		m.action.statusIsError = false
		return m, nil
	}

	title := fmt.Sprintf("Apply %d update(s)?", len(view.Updates))
	updates := view.Updates
	model, pa := m.buildAction(actionUpdate, title, updateDetailLines(view), "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return applyUpdatesSequentially(ctx, m.actions, updates, progress)
	})
	return model.promptAction(pa), nil
}

// resolveCheckUpdatesFailure handles a fresh checkUpdatesFailedMsg: status
// line error, no modal, mirroring resolvePlanFailure/
// resolveInstallPlanFailure.
func (m Model) resolveCheckUpdatesFailure(msg checkUpdatesFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Copilot PR #63 finding (mirrors resolvePlanFailure): resolve a
	// quit-triggered drain immediately rather than writing a status line no
	// one will ever see.
	if m.action.draining {
		return m.resolveDrainedQuit()
	}
	m.action.status = singleLine(msg.err.Error())
	m.action.statusIsError = true
	return m, nil
}

// updateDetailLines renders an UpdatesView as the "Apply N update(s)?"
// modal's detail lines: one "<name> <from> → <to>" line per update (the
// machinery's own "+N more" collapsing, actionModalView, applies here
// exactly like switchDetailLines' per-mod lines when the list is long),
// plus a trailing warning-count line when CheckUpdates surfaced any
// per-source diagnostics alongside the updates it did resolve.
func updateDetailLines(view UpdatesView) []string {
	lines := make([]string, 0, len(view.Updates)+1)
	for _, u := range view.Updates {
		lines = append(lines, fmt.Sprintf("%s %s → %s", u.Name, u.FromVersion, u.ToVersion))
	}
	if len(view.Warnings) > 0 {
		lines = append(lines, fmt.Sprintf("%d warning(s) during check", len(view.Warnings)))
	}
	return lines
}

// applyUpdatesSequentially applies every entry in updates, in order,
// through actions.ApplyUpdate - the confirm-time body of the "Apply N
// update(s)?" modal (task-5-brief.md's Updates flow), running entirely
// within the ONE buildAction call resolveCheckUpdatesResult dispatches, so
// the whole batch shares a single action gen/single-flight scope rather
// than one per mod. A per-update failure is folded into the aggregate
// outcome's Warnings and the loop CONTINUES to the next update - matching
// the CLI's own batch-update behavior and mirroring ApplyInstall's own
// Failed-into-Warnings precedent (service_core.go) - rather than aborting
// the remaining updates; this function itself never returns a non-nil
// error, so a partial-batch failure always completes as an actionDoneMsg
// with warnings, never an actionFailedMsg. progress is forwarded to every
// call unchanged (nil-safe, like every other ActionProvider progress
// parameter), so each update's own download/extract ticks stream into the
// status line as the batch works through it.
//
// A ctx cancellation (quit-while-running - see the cancel-then-drain doc
// comment on the model's quit handling) BREAKS the loop before the next
// update's ApplyUpdate call, rather than letting a cancelled ctx churn every
// remaining update into its own "context canceled" warning entry - those
// mods simply never got a chance to apply, which is not the same thing as
// each of them individually failing.
func applyUpdatesSequentially(ctx context.Context, actions ActionProvider, updates []UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	applied := 0
	var warnings []string
	for _, u := range updates {
		if ctx.Err() != nil {
			break
		}
		outcome, err := actions.ApplyUpdate(ctx, u, progress)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", u.Name, singleLine(err.Error())))
			continue
		}
		applied++
		warnings = append(warnings, outcome.Warnings...)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Applied %d update(s)", applied),
		Warnings: warnings,
	}, nil
}

// --- Profile create/delete ('c'/'d' on Profiles) ---

// profileCreateSubmittedMsg carries the name the user typed and confirmed in
// the "new profile" input modal (see createProfilePrompt), routed through
// Update() to resolveProfileCreate exactly like policyChosenMsg is routed to
// resolvePolicyChoice - and for the identical reason (see policyChosenMsg's
// own doc comment, which this mirrors in full): pendingInput's submit
// closure has signature `func(value string) tea.Cmd` (input_modal.go), so it
// cannot hand back a mutated Model itself - it can only return a Cmd that,
// on the next tick, delivers this message to Update(), which runs the
// actual buildAction call against the LIVE model. This message type is the
// corrected mechanic for Task 6: the brief's own text describes create's
// submit as running buildAction directly inside the modal closure, which
// Task 5 already proved cannot work - see policyChosenMsg's doc comment for
// the full explanation of why (stranded Model writes, a permanently wedged
// single-flight guard on a refused buildAction).
type profileCreateSubmittedMsg struct {
	name string
}

// createProfilePrompt handles 'c' on Profiles (task-6-brief.md's profile
// create flow): a no-op on the wrong screen or with no ActionProvider
// configured - mirrors editSelectedModPolicy/uninstallSelectedMod's own
// guard shape, minus a selection requirement, since creating a profile needs
// no row selected. Opens the input modal with validate rejecting only an
// EXACT (case-sensitive) match against a name already in m.profiles - the
// input modal's own "name required" handling already covers the empty case
// (see pendingInput's doc comment), so validate here never needs to.
// submit dispatches profileCreateSubmittedMsg on the next tick rather than
// calling buildAction directly - see that message's own doc comment for why.
func (m Model) createProfilePrompt() (Model, tea.Cmd) {
	if m.screen != ScreenProfiles || m.actions == nil {
		return m, nil
	}

	existing := make(map[string]bool, len(m.profiles))
	for _, p := range m.profiles {
		existing[p.Name] = true
	}

	input := newInputModalTextInput("profile name", m.availableWidth(), m.theme.Panel.GetHorizontalFrameSize())
	pi := pendingInput{
		title: "new profile",
		input: input,
		validate: func(value string) string {
			if existing[value] {
				return "profile already exists"
			}
			return ""
		},
		submit: func(value string) tea.Cmd {
			return func() tea.Msg { return profileCreateSubmittedMsg{name: value} }
		},
	}
	return m.promptInput(pi), nil
}

// resolveProfileCreate handles a profileCreateSubmittedMsg: dispatches
// actionCreateProfile and confirms immediately - the modal's own submit WAS
// the user's confirmation (task-6-brief.md: no second confirm gate),
// mirroring resolvePolicyChoice's identical "no pendingAction, set running
// directly" shape. The single-flight guard is checked HERE, not left to
// buildAction's own guard, for the exact reason resolvePolicyChoice's doc
// comment gives: the window between promptInput's submit clearing the modal
// (running still false) and this resolution running is real, and relying on
// buildAction's own refusal alone would leave this method setting
// running=true below for an action that never actually started, sticking
// the single-flight guard with nothing to ever clear it.
func (m Model) resolveProfileCreate(msg profileCreateSubmittedMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	name := msg.name
	actions := m.actions
	title := fmt.Sprintf("Create profile %q?", name)
	model, pa := m.buildAction(actionCreateProfile, title, nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return actions.CreateProfile(ctx, name)
	})
	model.action.running = true
	return model, pa.confirm()
}

// deleteSelectedProfile handles 'd' on Profiles (task-6-brief.md's profile
// delete flow): a no-op on the wrong screen, an empty list, or with no
// ActionProvider configured - mirrors switchSelectedProfile's guard/
// selection shape. The active profile is refused SYNCHRONOUSLY, on the
// status line, with no modal at all: deleting the profile the session is
// currently on would leave the TUI's own state (and a real coreProvider's
// currentProfile) pointing at a profile that no longer exists, so this is
// checked before ever building a confirmation - the ActionProvider's own
// DeleteProfile repeats the same guard defense-in-depth (see its doc
// comment), but the TUI-level check is what keeps this a clean status-line
// refusal instead of a modal the user could still confirm into an error.
func (m Model) deleteSelectedProfile() (Model, tea.Cmd) {
	if m.screen != ScreenProfiles || m.actions == nil {
		return m, nil
	}
	idx := m.selected[ScreenProfiles]
	if idx < 0 || idx >= len(m.profiles) {
		return m, nil
	}
	profile := m.profiles[idx]
	if profile.Active {
		m.action.status = singleLine(errCannotDeleteActiveProfile)
		m.action.statusIsError = true
		return m, nil
	}

	title := fmt.Sprintf("Delete profile %q?", profile.Name)
	detail := []string{"mods keep their install records; only the profile list is removed"}
	name := profile.Name
	model, pa := m.buildAction(actionDeleteProfile, title, detail, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.DeleteProfile(ctx, name)
	})
	return model.promptAction(pa), nil
}

// --- Purge ('X' on Dashboard and Installed Mods) ---

// purgeProfilePrompt handles 'X' on Dashboard/Installed Mods (task-7-brief.md's
// purge flow): a no-op on the wrong screen or with no ActionProvider
// configured - mirrors deployActiveProfile's guard shape (both fire on the
// same two screens, and neither depends on a row selection - purging, like
// deploying, acts on the WHOLE active profile, not a single mod).
//
// An empty m.mods resolves SYNCHRONOUSLY to a status-line message with no
// modal, unlike deployActiveProfile (which lets a zero-enabled-mods deploy
// through as a valid, if unusual, outcome the provider itself reports):
// purging zero installed mods has nothing to confirm and nothing for the
// provider to do - coreProvider.PurgeProfile short-circuits identically
// (see its own doc comment in service_core.go) - so this mirrors that
// provider-side short-circuit at the TUI layer too, sparing a pointless
// confirm-then-no-op round trip. statusIsError is explicitly false: this is
// a benign "nothing to do" outcome, not a refusal (contrast
// deleteSelectedProfile's active-profile guard, which IS an error).
//
// The modal title names the game (task-7-brief.md's own example: "Purge 3
// mod(s) from <Game>?"): m.summary.GameName is already populated by the
// same Overview call that fills m.mods (see coreProvider.Overview), so it's
// cheaply available here exactly like deployActiveProfile's own detail
// lines already assume. detail lists every mod's name - the existing
// confirmation-modal "+N more" overflow cap (actionModalView,
// actionModalMaxDetailLines) handles a long list without this needing to
// truncate itself.
func (m Model) purgeProfilePrompt() (Model, tea.Cmd) {
	if (m.screen != ScreenDashboard && m.screen != ScreenInstalledMods) || m.actions == nil {
		return m, nil
	}
	if len(m.mods) == 0 {
		m.action.status = "no mods installed"
		m.action.statusIsError = false
		return m, nil
	}

	detail := make([]string, 0, len(m.mods))
	for _, mod := range m.mods {
		detail = append(detail, mod.Name)
	}

	title := fmt.Sprintf("Purge %d mod(s) from %s?", len(m.mods), m.summary.GameName)
	model, pa := m.buildAction(actionPurge, title, detail, "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.PurgeProfile(ctx, progress)
	})
	return model.promptAction(pa), nil
}

// --- Game switch ('g' from any screen) ---

// gameChosenMsg carries the game ID the user picked in the game-switcher
// picker (see openGameSwitcher), routed through Update() to
// resolveGameSwitch exactly like policyChosenMsg/profileCreateSubmittedMsg
// are routed to their own resolvers - and for the identical reason (see
// policyChosenMsg's own doc comment for the full explanation): the picker's
// choose closure (pendingPicker.choose, picker.go) can only return a
// tea.Cmd, never a mutated Model, so the actual rebind-and-reset must run
// inside Update(), against the LIVE model, not a Model captured when the
// picker was built.
//
// Unlike policyChosenMsg/profileCreateSubmittedMsg, a game switch is NOT a
// buildAction-built ActionProvider mutation at all - see resolveGameSwitch's
// own doc comment - so this message carries only the chosen id, nothing
// else buildAction's machinery would need.
type gameChosenMsg struct{ id string }

// openGameSwitcher handles 'g' from ANY screen (task-8-brief.md's in-TUI
// game switcher): unlike every other mutation handler in this file, it has
// no screen guard at all - switching games is meaningful regardless of
// which screen the user is currently looking at.
//
// A running action refuses synchronously, on the status line ("action in
// progress"), BEFORE promptPicker is ever called: promptPicker's own guard
// (picker.go) already refuses while running/pending/a picker is already up,
// but it does so SILENTLY (a plain no-op) - task-8-brief.md's own test
// (TestGameSwitchBlockedWhileActionRunning) requires an explicit status
// message here, mirroring switchSelectedProfile/checkForUpdates/
// installSelectedSearchResult's own explicit single-flight checks elsewhere
// in this file (rather than leaning on buildAction/promptAction's silent
// refusal the way editSelectedModPolicy/createProfilePrompt do - those are
// followed by a picker/input modal whose OWN "no second confirm needed"
// framing makes a silent refusal acceptable; a "why did nothing happen"
// keypress on ANY screen deserves better here).
//
// The inputModal/overlay check below is defense-in-depth, not a reachable
// guard: updateKey (app.go) only ever reaches the outer switch this is
// dispatched from when m.action.pending, m.picker, m.inputModal, AND
// m.overlay are all already nil - mirroring showDeployedFiles' own doc
// comment on the identical situation for that handler. It's kept anyway,
// same as every other promptX call in this file, in case that call-site
// invariant is ever weakened.
func (m Model) openGameSwitcher() (Model, tea.Cmd) {
	if m.action.running {
		m.action.status = "action in progress"
		m.action.statusIsError = true
		return m, nil
	}
	if m.inputModal != nil || m.overlay != nil {
		return m, nil
	}

	// Synchronous, mirroring showDeployedFiles' documented exception
	// (this file's own doc comment on that method): ListGames is a local
	// games.yaml/config read, not network I/O, for both coreProvider and
	// prototypeProvider.
	games, err := m.provider.ListGames()
	if err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, nil
	}
	// Zero games is unreachable via coreProvider (its session is always
	// bound to a configured game, so ListGames returns at least that one),
	// but a message claiming "only one" when there are NONE would lie -
	// guarded separately (review finding).
	if len(games) == 0 {
		m.action.status = "no games configured"
		m.action.statusIsError = false
		return m, nil
	}
	if len(games) == 1 {
		m.action.status = "only one game configured"
		m.action.statusIsError = false
		return m, nil
	}

	options := make([]pickerOption, len(games))
	ids := make([]string, len(games))
	selected := 0
	activeID := ""
	for i, g := range games {
		options[i] = pickerOption{Label: g.Name}
		ids[i] = g.ID
		if g.Active {
			options[i].Note = "active"
			selected = i
			activeID = g.ID
		}
	}

	picker := pendingPicker{
		title:    "switch game",
		options:  options,
		selected: selected,
		// Choosing the already-active game is a no-op: returning a nil
		// tea.Cmd here means choosePickerOption (picker.go) just clears the
		// picker and dispatches nothing - no gameChosenMsg is ever produced,
		// so resolveGameSwitch never runs, matching
		// TestGameSwitchSameGameIsNoop's "no SetGame calls, no reset"
		// expectation without needing its own guard in resolveGameSwitch.
		choose: func(idx int) tea.Cmd {
			id := ids[idx]
			if id == activeID {
				return nil
			}
			return func() tea.Msg { return gameChosenMsg{id: id} }
		},
	}
	return m.promptPicker(picker), nil
}

// resolveGameSwitch handles a gameChosenMsg: guards single-flight
// (running/pending), mirroring resolvePolicyChoice/resolveProfileCreate's
// own "the window between the pick and this resolution is real" reasoning
// (see either's doc comment in full) - a second 'g' press in that window
// opens a second picker, and a mutation key opens a confirm modal.
//
// Unlike every OTHER resolve* handler in this file, this is not a
// buildAction-built ActionProvider mutation at all: switching games is a
// direct, synchronous Model rebind (task-8-brief.md's own framing) - no
// confirm modal, no progress stream, no actionDoneMsg round trip. rebindGame
// (actions.go) rebinds every provider/actions instance that supports it; an
// error there (e.g. coreProvider.SetGame's own unknown-id guard, unreachable
// in practice since msg.id always comes from a ListGames-derived option,
// but checked anyway) renders on the status line and leaves every other
// piece of session state completely untouched - nothing about the OLD
// game's data is wrong just because the rebind itself failed.
//
// A successful rebind resets the session's data-derived state to the exact
// shape Init()/NewModel's zero state uses, mirroring actionDoneMsg's own
// switchedTo handling in app.go (the profile-switch analog of this reset,
// one layer down):
//   - any in-flight search is cancelled and its generation bumped (a search
//     built against the OLD game's sources/installed-marks is meaningless
//     for the new one - mirrors actionDoneMsg's identical search-cancel)
//   - summary/mods/profiles revert to "nothing loaded yet"; every screen's
//     selection zeroes (a stale selected row surviving into totally
//     different data is exactly the class of bug clampSelections exists to
//     prevent elsewhere, but the OLD list is about to be replaced wholesale
//     here, not just resized, so this resets rather than clamps)
//   - sources/search.sources re-seed from the NEW game's SourceInfos()/
//     Sources() - a different game can have an entirely different set of
//     configured/registered sources
//
// state = stateLoading + returning m.loadData re-fetches Overview/Profiles
// for the new game, mirroring Init()'s own first-load shape exactly.
func (m Model) resolveGameSwitch(msg gameChosenMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		return m, nil
	}

	if err := m.rebindGame(msg.id); err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, nil
	}

	if m.search.cancel != nil {
		m.search.cancel()
		m.search.cancel = nil
	}
	m.search.gen++
	m.search.state = searchIdle

	// Invalidate any in-flight DATA load the same way (see Model.loadGen's
	// doc comment, app.go): a load dispatched before this reset was reading
	// the OLD game's binding - without the bump, its message could land
	// after this reset (even after the fresh load below) and repopulate the
	// old game's rows while the providers are already bound to the new one.
	// Bumped before the m.loadData return below, so the fresh load's value
	// receiver captures the new gen and its own message still applies.
	m.loadGen++

	// Modals are session state too: type-ahead in the pick→resolution window
	// can open one (e.g. 'c' → the "new profile" input modal, whose validate
	// closure captured the OLD game's profile list) before this deferred
	// message resolves - left standing, it would operate over the reset
	// state below while bound to the new game's providers.
	m.picker = nil
	m.inputModal = nil
	m.overlay = nil

	m.summary = Summary{Updates: -1, Conflicts: -1}
	m.mods = nil
	m.profiles = nil
	for screen := range m.selected {
		m.selected[screen] = 0
	}

	m.sources = m.provider.SourceInfos()
	m.search.sources = append([]string{""}, m.provider.Sources()...)
	m.search.sourceIdx = 0

	m.state = stateLoading
	return m, m.loadData
}
