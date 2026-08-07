package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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

// policyModalTitle formats the title for policy picker and confirmation.
func policyModalTitle(modName string) string {
	return fmt.Sprintf("Update policy — %s", modName)
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
// (task-7-brief.md's Keybindings section), and - since Phase 6b Task 3's
// review fix wave - on ScreenHealth too (with nothing pushed): a stale
// conflict's remedy copy reads "deploy (D) to apply" (conflictNoteText/
// conflictDetailHint, app.go - originally the standalone Conflicts screen's
// own conflictsDetailPane hint, folded into the Health table by #224 Task
// 15), so the key it names must actually fire there rather than being a
// silent no-op. The Health guard mirrors FullCheck/FixHealth's own
// "ScreenHealth, no pushed context" shape (updateKey, app.go) - pushed
// context content is a different full-screen view with no deploy remedy to
// act on. Same confirmation modal and machinery on all screens; no other
// behavior differs by screen. Unlike the mod-scoped actions above,
// deploying doesn't depend on any row selection, so an empty mods list is
// not a no-op case here - deploying zero enabled mods is a valid (if
// unusual) outcome the provider itself reports. Link method is omitted
// from the detail: it isn't exposed anywhere in DataProvider/Summary, and
// this task's scope keeps DataProvider frozen, so it isn't "cheaply
// available" per the brief's own qualifier.
func (m Model) deployActiveProfile() (Model, tea.Cmd) {
	deployScreen := m.screen == ScreenDashboard || m.screen == ScreenInstalledMods ||
		(m.screen == ScreenHealth && m.contextContent == nil)
	if !deployScreen || m.actions == nil {
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

// --- Rollback ('<' on Installed Mods) ---

// rollbackSelectedMod handles '<' on Installed Mods (Task 6): a no-op on the
// wrong screen, an empty list, or with no ActionProvider configured -
// mirrors uninstallSelectedMod's guard/selection shape. A mod with no
// PreviousVersion (ModItem.PreviousVersion == "") is refused SYNCHRONOUSLY,
// on the status line, with no modal at all - mirroring
// deleteSelectedProfile's own active-profile refusal shape (see that
// method's doc comment): there is nothing to confirm when there is no
// previous version to roll back to, and ActionProvider.Rollback's own guard
// repeats this defense-in-depth exactly like DeleteProfile's does. Unlike
// deleteSelectedProfile's refusal (an actual error - deleting the active
// profile IS a valid row the user could otherwise act on), this is a benign
// "nothing to do" outcome for the selected row - mirroring
// purgeProfilePrompt's own "no mods installed" short-circuit - so
// statusIsError is false here, not true. A locked mod is refused
// synchronously too (#143), but as an actual refusal (statusIsError true,
// pointing at the TUI's own L key) - see the inline comment.
//
// Otherwise, opens the standard y/n confirmation modal titled with both
// versions so the user can see exactly what's about to change, then calls
// ActionProvider.Rollback with the selected item on confirm.
func (m Model) rollbackSelectedMod() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}
	if item.PreviousVersion == "" {
		m.action.status = "no previous version to roll back to"
		m.action.statusIsError = false
		return m, nil
	}
	// #143 polish: item.Locked is already in hand, so refuse here instead of
	// opening a confirm modal for an action the core gate (ApplyRollback)
	// would then refuse anyway. This one IS deleteSelectedProfile's refusal
	// shape (statusIsError true): the row is otherwise actionable, and the
	// remedy named is the TUI's own L key, not a CLI command.
	if item.Locked {
		m.action.status = fmt.Sprintf("%s is locked at v%s — unlock or move the lock (L) to roll back", item.Name, item.LockedVersion)
		m.action.statusIsError = true
		return m, nil
	}

	title := fmt.Sprintf("Roll back %q v%s → v%s?", item.Name, item.Version, item.PreviousVersion)
	detail := m.gameProfileDetail("Replaces deployed files with the previous version; rollback hooks will run.")
	model, pa := m.buildAction(actionRollback, title, detail, "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.Rollback(ctx, item, progress)
	})
	return model.promptAction(pa), nil
}

// --- List-scoped changelog viewing ('v' on Installed Mods, no modal) ---

// viewSelectedModChangelog handles 'v' on Installed Mods OUTSIDE any modal
// (fix-wave-2 smoke finding #1): the modal-scoped 'v'
// (updatePendingActionKey/openChangelogFromUpdateModal, actions.go) only
// exists while the apply-updates confirmation modal is up - once that modal
// closes (confirm or cancel) or a check found zero updates and never opened
// one at all, changelogs became unreachable even though the user is still
// looking at the very same Installed Mods list. This is the same key,
// dispatched from updateKey's outer switch (which the focused-search-input
// branch and every modal already run ahead of - see updateKey's own doc
// comment), reading m.lastUpdates - the retained CheckUpdates result that
// outlives the modal - instead of m.pendingUpdates, which dies with it (see
// Model.lastUpdates' own doc comment).
//
// Guards mirror this file's other list keys: wrong screen or an out-of-range
// selection (empty list) is a silent no-op, mirroring
// rollbackSelectedMod/showDeployedFiles' own guard/selection shape. This
// needs no m.actions/m.provider != nil check (it's a pure read over
// already-retained Model state, no provider call at all) - but DOES need the
// single-flight guard (m.action.running || m.action.pending != nil)
// explicitly, mirroring moveSelectedMod's own reasoning: updateKey only
// routes here when picker/inputModal/overlay/action.pending are already all
// nil, but a CheckUpdates fetch in flight (m.action.running, no modal up
// yet) isn't covered by that routing guard, and opening a changelog overlay
// mid-fetch would be confusing.
//
// Three outcomes:
//   - m.lastUpdates == nil (no check has run this session, or a game switch
//     cleared it - resolveGameSwitch): a benign status line, not an error,
//     directing the user to 'u' first.
//   - checked (even a zero-updates check sets lastUpdates - see
//     resolveCheckUpdatesResult), but no entry in m.lastUpdates.Updates
//     matches the selected mod's (Source, ID): a benign status line naming
//     the mod.
//   - a match: opens its changelog overlay via the existing changelogOverlay
//     helper (actions.go) - identical title/empty-text rendering to the
//     modal-scoped path, so the two entry points are indistinguishable once
//     the overlay is open.
func (m Model) viewSelectedModChangelog() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}

	if m.lastUpdates == nil {
		m.action.status = "no update info — check updates (u) first"
		m.action.statusIsError = false
		return m, nil
	}

	for _, u := range m.lastUpdates.Updates {
		if u.Source == item.Source && u.ID == item.ID {
			m = m.promptOverlay(*changelogOverlay(u))
			return m, nil
		}
	}

	m.action.status = fmt.Sprintf("no update changelog for %q", item.Name)
	m.action.statusIsError = false
	return m, nil
}

// --- Load-order reorder (J/K on Installed Mods) ---

// moveSelectedMod handles MoveDown ('J', delta=+1) and MoveUp ('K',
// delta=-1) on Installed Mods (task-4-brief.md): swaps the selected mod with
// its delta-neighbor in m.mods and persists the FULL new load order
// immediately via ActionProvider.ReorderMods.
//
// Unlike every buildAction-routed handler in this file, the ReorderMods call
// below is made SYNCHRONOUSLY, exactly like showDeployedFiles' DeployedFiles
// call above (see that method's own doc comment for the documented
// exception this repeats): it's a local YAML write, not a network call, and
// - unlike Enable/Disable/Uninstall/Deploy - a reorder isn't destructive, so
// there's nothing for a y/n confirmation modal to gate and no progress to
// stream.
//
// Guards, in order: wrong screen or no ActionProvider configured (mirrors
// uninstallSelectedMod/showDeployedFiles); a single-flight conflict (checked
// explicitly here, mirroring switchSelectedProfile/checkForUpdates' own
// explicit check - required because, unlike those, this method never
// reaches buildAction's own guard at all); an out-of-range selection
// (covers an empty list); and the target slot being off either end of the
// list (the top row can't move up, the bottom row can't move down) - all
// silent no-ops, matching this file's existing precedent for a
// selection/edge condition that isn't itself an error.
//
// On success: the swapped order becomes m.mods, selection follows the moved
// mod to its new slot, m.orderChanged is set (see its own doc comment) so the
// status line reminds the user to deploy, and a refresh (m.loadData) is
// dispatched so the Conflicts screen and dashboard conflict count - both of
// which depend on load order - pick up the new winner immediately rather
// than lagging one unrelated action behind. On failure: m.mods is left
// untouched (the write never took effect) and a refresh is dispatched so the
// list reflects disk truth - mirroring the design's own "errors surface in
// the status line and the list refreshes to disk truth" contract.
func (m Model) moveSelectedMod(delta int) (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	// If a filtered or otherwise PARTIAL view of the installed list ever
	// lands on this screen, reorder must go inert here (with a status-line
	// explanation): a partial view cannot express a total load order, so
	// persisting one would silently truncate the profile (design doc §2,
	// docs/plans/2026-07-23-tui-phase6b-workflows-design.md). No such view
	// exists today - m.mods is always the full profile's list - so there is
	// deliberately no guard to write yet (YAGNI).

	idx := m.selected[ScreenInstalledMods]
	if idx < 0 || idx >= len(m.mods) {
		return m, nil
	}
	target := idx + delta
	if target < 0 || target >= len(m.mods) {
		return m, nil
	}

	mods := append([]ModItem(nil), m.mods...)
	mods[idx], mods[target] = mods[target], mods[idx]

	keys := make([]string, len(mods))
	for i, mod := range mods {
		keys[i] = domain.ModKey(mod.Source, mod.ID)
	}

	if _, err := m.actions.ReorderMods(m.ctx, keys); err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, m.loadData
	}

	m.mods = mods
	m.selected[ScreenInstalledMods] = target
	m.orderChanged = true
	return m, m.loadData
}

// --- Pak conversion toggle ('m' on Installed Mods, #221) ---

// toggleSelectedModConvert handles 'm' on Installed Mods (#221): flips the
// selected mod's pak-to-exmod conversion flag via ActionProvider.
// SetConvertPaks. Direct, synchronous call - mirroring moveSelectedMod's own
// "local write, not network I/O, nothing for a confirm modal to gate"
// exception immediately above - and confirmation-free for the same reason
// the update-policy picker is (resolvePolicyChoice's doc comment): this is a
// reversible metadata write, so the keypress itself IS the confirmation.
//
// Guards, in order: wrong screen or no ActionProvider configured, and an
// out-of-range selection (empty list) - both mirror moveSelectedMod's own
// guard/selection shape; a single-flight conflict, checked explicitly here
// (this method never reaches buildAction's own guard, exactly like
// moveSelectedMod - it makes no buildAction call at all); and
// !item.CompileGame - the flag only affects DeployMode == DeployCompile
// games (ModItem.CompileGame, populated by coreProvider's Overview mapping
// from the ACTIVE game, not the mod), so a non-compile game's toggle is
// refused synchronously on the status line rather than silently persisting a
// flag with no effect - mirroring rollbackSelectedMod's "benign, not an
// error" PreviousVersion=="" refusal (statusIsError false: the row itself
// isn't wrong, the game's deploy mode just makes the flag inert); and
// !item.HasPakSource (#221 round-4 fix) - conversion flags only affect a mod
// that actually has a pak merge source, so an exmodz-only mod's toggle is
// refused the same synchronous, benign way BEFORE the provider is ever
// called, rather than persisting a flag that can never have a deploy-time
// effect.
//
// On success: the outcome's message (coreProvider.SetConvertPaks's "<name>
// pak conversion: on/off (deploy to apply)", or a game-disabled variant when
// the active game's own convert_paks is false - see that method's doc
// comment) becomes the status line, and a refresh (m.loadData) is dispatched
// so the "raw" flag column (app.go's modFlags) picks up the new state
// immediately - mirroring moveSelectedMod's own "refresh on both success and
// failure" contract.
func (m Model) toggleSelectedModConvert() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	if !item.CompileGame {
		m.action.status = "pak conversion applies only to merge-compile games"
		m.action.statusIsError = false
		return m, nil
	}
	if !item.HasPakSource {
		m.action.status = "pak conversion applies only to mods with a pak merge source"
		m.action.statusIsError = false
		return m, nil
	}

	outcome, err := m.actions.SetConvertPaks(m.ctx, item, !item.ConvertPaks)
	if err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, m.loadData
	}
	m.action.status = outcome.Message
	m.action.statusIsError = false
	return m, m.loadData
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
		title:    policyModalTitle(item.Name),
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
//
// #68: this drop used to be perfectly silent, leaving the user with no sign
// their picker choice had no effect. setIdleStatus (actions.go) sets a muted
// "busy" hint, but only when hasVisibleStatus reports the line is free -
// preserving the one thing that actually matters here, a running action's
// own live status/progress text, which must never be stomped by this hint.
func (m Model) resolvePolicyChoice(msg policyChosenMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		m.setIdleStatus("busy — choice ignored", false)
		return m, nil
	}
	item := msg.item
	policy := msg.policy
	model, pa := m.buildAction(actionSetPolicy, policyModalTitle(item.Name), nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.SetUpdatePolicy(ctx, item, policy)
	})
	model.action.running = true
	return model, pa.confirm()
}

// --- Lock/unlock version picker ('L' on Installed Mods, #97) ---

// lockModalTitle formats the title for the lock version picker, mirroring
// policyModalTitle's own shape above.
func lockModalTitle(modName string) string {
	return fmt.Sprintf("Lock version — %s", modName)
}

// versionsFetchedMsg carries a successful ActionProvider.AvailableVersions
// result, tagged with the generation established when the fetch was
// dispatched (see editSelectedModLock) so a superseded result can be
// discarded - mirrors checkUpdatesResultMsg/planResultMsg's own gen-guard
// shape (this file's template for every async network fetch).
type versionsFetchedMsg struct {
	gen      int
	item     ModItem
	versions []string
}

// versionsFetchFailedMsg carries a failed AvailableVersions call, tagged
// like versionsFetchedMsg. coreProvider.AvailableVersions (service_core.go)
// already maps a source.ErrNotSupported failure through mapNetworkError,
// naming pin (P) as the fallback - this type carries that mapped error
// string through unchanged; resolveVersionsFetchFailed renders it verbatim
// on the status line, exactly like resolvePlanFailure/
// resolveCheckUpdatesFailure render every other mapped provider error.
type versionsFetchFailedMsg struct {
	gen int
	err error
}

// editSelectedModLock handles 'L' on Installed Mods (task-7-brief.md): a
// no-op on the wrong screen, an empty list, with no ActionProvider
// configured, or while another action/fetch is already in flight - mirrors
// checkForUpdates' own guard shape (this is network I/O, unlike Policy's
// synchronous fixed-option picker, so it follows checkForUpdates' async
// three-step pattern rather than editSelectedModPolicy's synchronous one).
// Sets the running/status state ("Fetching versions for <name>…") and
// returns a Cmd calling ActionProvider.AvailableVersions, tagged with a
// fresh generation so a stale result from a superseded fetch (e.g. the user
// presses 'L' again, or navigates and quits, before this one lands) is
// dropped by app.go's gen check before it ever reaches
// resolveVersionsFetched/resolveVersionsFetchFailed.
func (m Model) editSelectedModLock() (Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
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
	m.action.status = fmt.Sprintf("Fetching versions for %s…", item.Name)
	m.action.statusIsError = false

	return m, func() tea.Msg {
		versions, err := m.actions.AvailableVersions(ctx, item)
		if err != nil {
			return versionsFetchFailedMsg{gen: gen, err: err}
		}
		return versionsFetchedMsg{gen: gen, item: item, versions: versions}
	}
}

// lockPickerOptions builds one pickerOption per entry in versions for
// editSelectedModLock's picker (task-7-brief.md): the option matching
// item.Version is noted "installed", the option matching item.LockedVersion
// (only when item.Locked) is noted "locked" - both notes land on the SAME
// row, joined "installed, locked", when the mod is locked at exactly its
// installed version. Returns the options alongside the index to pre-select:
// the locked target when item.Locked, else the installed version - index 0
// (with nothing marked) when neither is found in versions, mirroring
// editSelectedModPolicy's own "never guesses" fallback for an unmatched
// current value. A locked item whose lock target is absent from versions is
// the one case the caller overrides that fallback (#143): the vanished
// target is signalled on the trailing unlock row instead - see
// resolveVersionsFetched.
//
// versions is read only, never mutated or reordered here - per
// ActionProvider.AvailableVersions' own doc comment, a caller that needs to
// sort/filter/annotate the result should copy first; this function does
// neither (it walks the slice once, in the order the provider returned it,
// building a SEPARATE []pickerOption instead of writing into versions
// itself), so no copy is needed.
func lockPickerOptions(item ModItem, versions []string) ([]pickerOption, int) {
	options := make([]pickerOption, len(versions))
	selected := 0
	for i, v := range versions {
		var notes []string
		if v == item.Version {
			notes = append(notes, "installed")
		}
		if item.Locked && v == item.LockedVersion {
			notes = append(notes, "locked")
		}
		options[i] = pickerOption{Label: v, Note: strings.Join(notes, ", ")}
		switch {
		case item.Locked && v == item.LockedVersion:
			selected = i
		case !item.Locked && v == item.Version:
			selected = i
		}
	}
	return options, selected
}

// resolveVersionsFetched handles a fresh (non-stale - callers check msg.gen
// first) versionsFetchedMsg: builds the lock-version picker via
// lockPickerOptions, appending a trailing "unlock" option when item.Locked
// (task-7-brief.md). choose dispatches lockChosenMsg/unlockChosenMsg rather
// than mutating the Model directly - mirrors editSelectedModPolicy's own
// choose closure, and policyChosenMsg's doc comment explains in full why
// that indirection is required (pendingPicker.choose can only return a
// tea.Cmd, never a mutated Model).
func (m Model) resolveVersionsFetched(msg versionsFetchedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Copilot PR #63 finding (mirrors resolvePlanResult/
	// resolveCheckUpdatesResult): resolve a quit-triggered drain immediately
	// instead of opening the picker below - the app is exiting.
	if m.action.draining {
		return m.resolveDrainedQuit()
	}

	item := msg.item
	versions := msg.versions

	// PR #142 Copilot round-2 (independently confirms a final-review item
	// we'd deferred): ActionProvider.AvailableVersions is permitted by the
	// interface to return an empty, error-free slice (neither shipped
	// provider does this today - coreProvider maps a versionless source to
	// source.ErrNotSupported instead, and prototypeProvider's canned list
	// is always non-empty - but nothing in the interface forbids a future
	// one). For an UNLOCKED item, an empty slice means no rows at all (no
	// versions AND no trailing unlock row below), which would open a
	// choosable-but-empty picker - refuse instead, with an error status
	// worded to match mapNetworkError's own capability-gap phrasing
	// ("...; pin it instead (P)", service_core.go) so the two read as one
	// voice. A LOCKED item still gets a valid picker even with zero
	// versions: the unlock row alone is a real, choosable option, so that
	// case is deliberately let through below.
	if len(versions) == 0 && !item.Locked {
		m.action.status = fmt.Sprintf("no versions reported for %s; pin it instead (P)", item.Name)
		m.action.statusIsError = true
		return m, nil
	}

	options, selected := lockPickerOptions(item, versions)
	unlockIdx := -1
	if item.Locked {
		unlockIdx = len(options)
		unlockOption := pickerOption{Label: "unlock"}
		// #143 polish: when the locked version vanished from the source's
		// list (removed/archived upstream), lockPickerOptions fell back to
		// index 0 - the newest version, with nothing marked - which read as
		// if the lock pointed there. Pre-select the unlock row and say why,
		// so the vanished target is signalled rather than papered over.
		if !slices.Contains(versions, item.LockedVersion) {
			unlockOption.Note = fmt.Sprintf("locked v%s no longer listed", item.LockedVersion)
			selected = unlockIdx
		}
		options = append(options, unlockOption)
	}

	picker := pendingPicker{
		title:    lockModalTitle(item.Name),
		options:  options,
		selected: selected,
		choose: func(idx int) tea.Cmd {
			if unlockIdx >= 0 && idx == unlockIdx {
				return func() tea.Msg { return unlockChosenMsg{item: item} }
			}
			// Defense in depth (PR #142 Copilot round-2): read the picked
			// version from the SAME options slice the picker rendered and
			// dispatched idx against, instead of indexing the separate
			// versions slice in parallel - options[i].Label is exactly
			// versions[i] for every non-unlock row (lockPickerOptions
			// builds it that way), so this is behavior-identical, but
			// removes a second slice that could silently drift out of sync
			// with what the user actually saw and chose.
			version := options[idx].Label
			return func() tea.Msg { return lockChosenMsg{item: item, version: version} }
		},
	}
	return m.promptPicker(picker), nil
}

// resolveVersionsFetchFailed handles a fresh versionsFetchFailedMsg: status
// line error, no picker, mirroring resolvePlanFailure/
// resolveCheckUpdatesFailure.
func (m Model) resolveVersionsFetchFailed(msg versionsFetchFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	if m.action.draining {
		return m.resolveDrainedQuit()
	}
	m.action.status = singleLine(msg.err.Error())
	m.action.statusIsError = true
	return m, nil
}

// lockOutcomeMessage renders the status line for a successful SetLock call
// (task-7-brief.md): "<name> locked at v<version>", plus a " — apply the
// profile to converge" caveat when version differs from item.Version - the
// mod's CURRENTLY DEPLOYED version - since SetLock never deploys itself (see
// ActionProvider.SetLock's own doc comment: "convergence happens on the next
// profile apply/switch"). Locking at the already-installed version needs no
// such caveat: nothing has to change on the next deploy for that case.
func lockOutcomeMessage(item ModItem, version string) string {
	msg := fmt.Sprintf("%s locked at v%s", item.Name, version)
	if version != item.Version {
		msg += " — apply the profile to converge"
	}
	return msg
}

// lockChosenMsg carries the version the user picked in the lock-version
// picker (see resolveVersionsFetched/resolveLockChosen), naming both the
// ModItem the picker was opened for and the chosen version - mirrors
// policyChosenMsg's own shape and role in full (see that type's doc comment
// for why this indirection through Update() is required).
type lockChosenMsg struct {
	item    ModItem
	version string
}

// unlockChosenMsg carries the picker's trailing "unlock" row being chosen on
// a locked mod - mirrors lockChosenMsg's own role, with no version argument
// since Unlock takes none.
type unlockChosenMsg struct {
	item ModItem
}

// resolveLockChosen handles a lockChosenMsg: builds the actionSetLock action
// for msg.item/msg.version and confirms it immediately - "the pick IS the
// confirmation" (task-7-brief.md), mirroring resolvePolicyChoice's own
// shape and single-flight drop guard in full (see that method's doc comment
// for why the guard is checked HERE rather than left to buildAction's own
// refusal). The outcome's Message is overwritten with lockOutcomeMessage
// (SetLock's own coreProvider/prototypeProvider wording doesn't carry the
// convergence caveat this task's status line needs) - Warnings, if any, are
// left untouched, so formatOutcomeStatus still appends them normally.
func (m Model) resolveLockChosen(msg lockChosenMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		m.setIdleStatus("busy — choice ignored", false)
		return m, nil
	}
	item := msg.item
	version := msg.version
	model, pa := m.buildAction(actionSetLock, lockModalTitle(item.Name), nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		outcome, err := m.actions.SetLock(ctx, item, version)
		if err != nil {
			return outcome, err
		}
		outcome.Message = lockOutcomeMessage(item, version)
		return outcome, nil
	})
	model.action.running = true
	return model, pa.confirm()
}

// resolveUnlockChosen handles an unlockChosenMsg: builds the actionUnlock
// action for msg.item and confirms it immediately, mirroring
// resolveLockChosen immediately above in full (including its single-flight
// drop guard) except for the ActionProvider call and outcome message.
func (m Model) resolveUnlockChosen(msg unlockChosenMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		m.setIdleStatus("busy — choice ignored", false)
		return m, nil
	}
	item := msg.item
	model, pa := m.buildAction(actionUnlock, lockModalTitle(item.Name), nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		outcome, err := m.actions.Unlock(ctx, item)
		if err != nil {
			return outcome, err
		}
		outcome.Message = fmt.Sprintf("%s unlocked", item.Name)
		return outcome, nil
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
// profile-switch flow): a no-op on the wrong screen, an empty list, or with
// no ActionProvider - the screen/selection-specific guards switchToProfileNamed
// itself doesn't need (see that method's own doc comment for the rest of
// the flow, shared with Task 9's post-import "switch to it now?" offer).
func (m Model) switchSelectedProfile() (Model, tea.Cmd) {
	if m.screen != ScreenProfiles || m.actions == nil {
		return m, nil
	}
	idx := m.selected[ScreenProfiles]
	if idx < 0 || idx >= len(m.profiles) {
		return m, nil
	}
	return m.switchToProfileNamed(m.profiles[idx].Name)
}

// switchToProfileNamed is the profile-name-addressed core of
// switchSelectedProfile (task-7-brief.md's profile-switch flow, above -
// looks up name from the currently selected Profiles row) and Task 9's
// resolveImportSwitchConfirmed (the post-import "switch to it now?" offer,
// which already knows the name it wants and has no row selection involved
// at all - task-9-brief.md: "reuse switchSelectedProfile's machinery - do
// not duplicate the switch flow"). Both funnel through here so there is
// exactly one PlanProfileSwitch dispatch/gen/cancel/status-line shape to
// maintain: single-flight (running/pending), the active-profile short
// circuit ("Already on profile <name>", no modal - mirroring
// resolvePlanResult's AlreadyActive branch, the defensive counterpart of
// this same check), and an async PlanProfileSwitch dispatch reusing
// action.gen/action.cancel exactly like buildAction does, so the result is
// subject to the same staleness discipline before it's allowed to open a
// modal.
func (m Model) switchToProfileNamed(name string) (Model, tea.Cmd) {
	if m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}

	for _, p := range m.profiles {
		if p.Name == name && p.Active {
			m.action.status = fmt.Sprintf("Already on profile %q", name)
			m.action.statusIsError = false
			return m, nil
		}
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

	return m, func() tea.Msg {
		view, err := m.actions.PlanProfileSwitch(ctx, name)
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

	return m, func() tea.Msg {
		view, err := m.actions.PlanInstall(ctx, item)
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
	for _, w := range view.DependencyWarnings {
		lines = append(lines, fmt.Sprintf("⚠ %s", w))
	}
	return lines
}

// refreshSearchAfterInstall re-issues the CURRENT search session's already-
// fetched rounds after a successful install, so the just-installed result's
// "installed" marker updates immediately instead of waiting for the user to
// search again by hand (task-5-brief.md: "verify the refresh path covers
// the search results' installed-flag, and fix within internal/tui if not" -
// the generic post-action refresh, m.loadData, only re-fetches Overview/
// Profiles, never the search buffer, since no other mutation needs it to).
//
// #111 Tier 3 (infinite scroll): a session's visible history can now span
// MULTIPLE provider rounds (searchModel.buffer's doc comment), so
// refreshing only the most-recently-fetched round would leave every
// EARLIER round's installed-markers stale the moment the user has scrolled
// past round 0. This refetches EVERY round the session has already fetched
// (0..fetchRound-1), sequentially, inside ONE tea.Cmd, accumulating into a
// LOCAL scratch buffer that never touches m.search at all until the whole
// rebuild has fully succeeded - see beginRefresh's doc comment (search.go)
// for why this is a non-destructive sibling of startSearch's
// beginNewSession, not beginNewSession itself.
//
// #111 Tier 3 fix round 4 (task review finding): the EARLIER version of
// this function called beginNewSession up front, which reset the buffer to
// nil and flipped state to searchLoading BEFORE the rebuild Cmd even ran -
// so a transient mid-loop error (a single flaky round out of N) discarded
// an arbitrarily deep scrolled buffer, contradicting refillSearch's own
// established non-destructive principle (see its searchFailedMsg handling
// in app.go). A failed round now returns searchFailedMsg{kind:
// searchResultRefresh}, which Update (app.go) handles by leaving
// state/buffer/everything else EXACTLY as they were and surfacing only a
// muted status notice - there is nothing to roll back, because nothing was
// ever mutated on the failure path.
//
// On SUCCESS, the merged result reaches Update via searchResultMsg{kind:
// searchResultRefresh}, which clamps the selection to the rebuilt buffer's
// new length rather than resetting it to the top, and restores fetchRound
// (carried on the message as rounds, since a plain generation bump doesn't
// touch it - unlike beginNewSession, beginRefresh never resets fetchRound
// either) so a SUBSEQUENT scroll-triggered refill correctly requests the
// round AFTER what this refresh already covered, not round 0 again.
// Ordering within the rebuilt buffer may shift slightly if the underlying
// catalog changed between the original fetch and now (a source's own
// internal ordering is not guaranteed stable across independent calls) -
// accepted, since staying wrong (a stale "available" marker on a mod the
// user just installed) is worse than a harmless reorder.
//
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

	query := m.search.page.Query
	source := m.search.page.Source
	rounds := m.search.fetchRound

	m, ctx, gen := m.beginRefresh()
	provider := m.provider
	fetchSize := m.search.fetchSize

	return m, func() tea.Msg {
		merged := SearchPage{Query: query, Source: source, PageSize: fetchSize}
		var buffer []ModItem
		var warnings []string
		exhausted := false
		for round := 0; round < rounds; round++ {
			result, err := provider.Search(ctx, source, query, round, fetchSize)
			if err != nil {
				return searchFailedMsg{gen: gen, kind: searchResultRefresh, err: err, source: source}
			}
			buffer = append(buffer, result.Results...)
			warnings = mergeWarnings(warnings, result.Warnings)
			merged.TotalCount = result.TotalCount
			merged.AttemptedCount = result.AttemptedCount
			exhausted = roundExhausted(result)
		}
		merged.Results = buffer
		merged.Warnings = warnings
		merged.Exhausted = exhausted
		return searchResultMsg{gen: gen, kind: searchResultRefresh, page: merged, rounds: rounds}
	}
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

	return m, func() tea.Msg {
		view, err := m.actions.CheckUpdates(ctx)
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
		// lastUpdates is set here too (fix-wave-2 smoke finding #1), even
		// though no modal ever opens on this path: an EMPTY retained result
		// still answers 'v' on Installed Mods with the correct "checked, no
		// entry for this mod" status line rather than the "never checked at
		// all" one - see viewSelectedModChangelog's own doc comment.
		m.lastUpdates = &view
		return m, nil
	}

	title := fmt.Sprintf("Apply %d update(s)?", len(view.Updates))
	updates := view.Updates
	model, pa := m.buildAction(actionUpdate, title, updateDetailLines(view), "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return applyUpdatesSequentially(ctx, m.actions, updates, progress)
	})
	// pendingUpdates retains view for Task 7's changelog viewer ('v' -
	// updatePendingActionKey, actions.go): both the discriminator ("is the
	// pending action THIS update batch") and the data the viewer renders
	// (names, versions, changelogs). Cleared everywhere action.pending
	// itself is cleared - see Model.pendingUpdates' own doc comment.
	model.pendingUpdates = &view
	// lastUpdates shares the SAME view (fix-wave-2 smoke finding #1): unlike
	// pendingUpdates, it is NOT cleared when the modal above closes - see
	// Model.lastUpdates' own doc comment.
	model.lastUpdates = &view
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

// --- Health: 'c' full (network) check (#224 Task 11) ---

// fullHealthCheckResultMsg carries a successful Full-tier RunHealthCheck
// result, tagged with the generation established when the check was
// dispatched (see runFullHealthCheck) so a superseded result is discarded -
// mirrors checkUpdatesResultMsg's own gen-tag reasoning in full.
type fullHealthCheckResultMsg struct {
	gen  int
	view HealthView
}

// fullHealthCheckFailedMsg carries a failed Full-tier RunHealthCheck call,
// tagged like fullHealthCheckResultMsg.
type fullHealthCheckFailedMsg struct {
	gen int
	err error
}

// runFullHealthCheck handles 'c' on ScreenHealth (updateKey's own compound
// guard - m.screen == ScreenHealth && m.contextContent == nil - already
// keeps this from ever firing while ScreenHealth has pushed context content
// or on any other screen, where "c" is instead CreateProfile; the screen/
// actions checks below are defense-in-depth, matching every other mutation
// handler's own "TUI-level check repeats the caller's guard" convention -
// see deleteSelectedProfile's doc comment): a no-op with no ActionProvider
// or while another action/plan is already in flight (buildAction's own
// "busy" convention - checked directly here rather than via buildAction
// itself, since this dispatches a custom result message, not the generic
// actionDoneMsg/actionKind pair - see the other plan/check messages'
// precedent already established in mutations.go).
//
// Deliberately hand-rolls buildAction's own gen/cancel/progress-channel
// bookkeeping instead of calling it: buildAction ties its result to
// actionDoneMsg{kind, outcome} for the generic confirmation-modal flow, but
// this action has no modal and needs to store a whole HealthView (not just
// an ActionOutcome string) plus a bespoke status line - exactly the shape
// checkForUpdates/resolveCheckUpdatesResult already use for the identical
// reason, extended here with the SAME progress pump buildAction wires
// (ch/waitForActionProgress/tea.Batch) since, unlike CheckUpdates, the Full
// tier genuinely streams progress.
func (m Model) runFullHealthCheck() (Model, tea.Cmd) {
	if m.screen != ScreenHealth || m.actions == nil {
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
	m.action.status = "Running full health check…"
	m.action.statusIsError = false

	ch := make(chan ActionProgress, 1)
	m.action.progressCh = ch

	actionCmd := func() tea.Msg {
		view, err := m.actions.RunHealthCheck(ctx, true, false, func(p ActionProgress) { sendActionProgress(ch, p) })
		close(ch)
		if err != nil {
			return fullHealthCheckFailedMsg{gen: gen, err: err}
		}
		return fullHealthCheckResultMsg{gen: gen, view: view}
	}
	return m, tea.Batch(actionCmd, waitForActionProgress(ch, gen))
}

// resolveFullHealthCheckResult handles a fresh fullHealthCheckResultMsg:
// stores the returned view as the Health screen's new content (m.health,
// healthAt stamped at m.now() - matching dataLoadedMsg's own "stamp at
// receipt" convention, see healthAt's doc comment), clears any stale scan
// error, and folds Issues/Warnings into the dashboard's summary signal
// (mirroring dataLoadedMsg's identical Health tie-in in app.go). The status
// line reports the outcome directly - task-11-brief.md's exact wording -
// rather than going through formatOutcomeStatus, since there is no
// ActionOutcome here to format.
func (m Model) resolveFullHealthCheckResult(msg fullHealthCheckResultMsg) (Model, tea.Cmd) {
	m.action.running = false
	// Mirrors actionDoneMsg's own progress clear (app.go): this action can
	// stream "checking versions N/M" ticks (runFullHealthCheck's progress
	// pump), so leaving a stale one behind would let it wrongly surface as
	// the NEXT action's status line - statusLine prefers a running action's
	// progress.Line over its own stored status, and not every action posts
	// one of its own (#224 Copilot round 3 finding).
	m.action.progress = ActionProgress{}
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	// Mirrors resolveCheckUpdatesResult's identical drain-first check (Copilot
	// PR #63 finding): the app is exiting, so none of the state below - the
	// new view, the summary counts, the status line - would ever be seen.
	if m.action.draining {
		return m.resolveDrainedQuit()
	}

	view := msg.view
	m.health = view
	now := m.now()
	m.healthAt = &now
	m.healthErr = ""
	m.summary.HealthIssues, m.summary.HealthWarnings = view.Issues, view.Warnings
	// Mirrors dataLoadedMsg's identical clamp after its own m.health
	// assignment (app.go): a fresh view can be SHORTER than whatever was
	// selected on the old one (e.g. this same check resolved several
	// findings away), and without this the Health screen's selection can
	// walk off the end of the new list - healthDetailPane's own bounds
	// check would then fall through to "No selection." (#224 Copilot round
	// 3 finding).
	m.clampSelections()

	if view.Issues == 0 && view.Warnings == 0 {
		m.action.status = "full check: all OK"
	} else {
		m.action.status = fmt.Sprintf("full check: %d issue(s), %d warning(s)", view.Issues, view.Warnings)
	}
	m.action.statusIsError = false
	return m, nil
}

// resolveFullHealthCheckFailure handles a fresh fullHealthCheckFailedMsg:
// status line error, no modal, and - unlike a success - m.health/healthAt
// are left completely untouched (task-11-brief.md: "on failure... KEEP the
// previous view"), matching dataLoadedMsg's own "a failed scan leaves the
// last known-good view in place" posture (app.go).
func (m Model) resolveFullHealthCheckFailure(msg fullHealthCheckFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	// See resolveFullHealthCheckResult's identical clear, above, for why a
	// failure must not leave a stale progress tick behind either (#224
	// Copilot round 3 finding).
	m.action.progress = ActionProgress{}
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	if m.action.draining {
		return m.resolveDrainedQuit()
	}
	m.action.status = singleLine(msg.err.Error())
	m.action.statusIsError = true
	return m, nil
}

// --- Health: 'F' batch fix behind confirmation (#224 Task 12) ---

// healthUnfixableStatus reports whether status is one of the four the
// verify engine's fix mode can never touch (task-12-brief.md's exact list):
// "skipped" (the row couldn't even be checked), "version_unverifiable" and
// "file_count_mismatch" (both diagnostic-only - no repair exists for
// either), and "ok" (already fine, including a kept lock-pending note row -
// see HealthView's own doc comment on why those survive the quiet-ok
// filter). Every other status - including ones the engine happens not to
// actually repair today, like "stale_compile"/"conversion_failed" - counts
// as "actionable" for the fix-prompt's own gating purposes; this predicate
// is deliberately the brief's literal four-status list, not a mirror of
// core/verify.go's real repair coverage.
//
// Also excludes any fixed_* status (fixed_stale_deployment,
// fixed_needs_reingest today - same prefix check healthStatusClass uses,
// app.go, to bucket a resolved row into its "ok" tint): a row already
// repaired by a prior fix run is exactly as unactionable as "ok" itself, and
// without this a view containing only fixed_*/ok rows after a successful
// fix would still let 'F' open an empty "Fix N finding(s)?" modal (Copilot
// round 6 finding, #224).
func healthUnfixableStatus(status string) bool {
	if strings.HasPrefix(status, "fixed_") {
		return true
	}
	switch status {
	case "skipped", "version_unverifiable", "file_count_mismatch", "ok":
		return true
	default:
		return false
	}
}

// healthFixableFindings filters findings down to the ones fixHealthPrompt
// counts as actionable - see healthUnfixableStatus's own doc comment for
// exactly which statuses that excludes. nil in, nil out (an empty/unscanned
// view's zero-value Findings needs no special-casing here).
func healthFixableFindings(findings []HealthFinding) []HealthFinding {
	var out []HealthFinding
	for _, f := range findings {
		if !healthUnfixableStatus(f.Status) {
			out = append(out, f)
		}
	}
	return out
}

// healthFixCategoryLine renders one status class's confirmation-modal detail
// line for n findings of that status - task-12-brief.md's exact phrasings
// for the four statuses it names outright (missing/version_mismatch/
// stale_deployment/needs_reingest, the ones the CLI --fix pass genuinely
// repairs), plus a "no_checksum" line following the same voice (the CLI's
// own redownload-to-backfill repair, healthRemedy's "run a fix (F) to
// backfill" wording, app.go). A status with no specific phrasing (e.g.
// "stale_compile"/"conversion_failed" - see healthFixableFindings' own doc
// comment for why those still reach here) falls back to a generic "<n>
// <status, spaces for underscores> finding(s)" line rather than guessing at
// remedy copy fix mode doesn't actually provide.
func healthFixCategoryLine(status string, n int) string {
	switch status {
	case "missing":
		return fmt.Sprintf("%d missing file(s) — re-download", n)
	case "no_checksum":
		return fmt.Sprintf("%d missing checksum(s) — backfill", n)
	case "stale_deployment":
		word := "deployment"
		if n != 1 {
			word = "deployments"
		}
		return fmt.Sprintf("%d stale %s — remove", n, word)
	case "version_mismatch":
		word := "mismatch"
		if n != 1 {
			word = "mismatches"
		}
		return fmt.Sprintf("%d version %s — re-key records (locked mods refused)", n, word)
	case "needs_reingest":
		if n == 1 {
			return "1 pak needs re-ingest"
		}
		return fmt.Sprintf("%d paks need re-ingest", n)
	default:
		return fmt.Sprintf("%d %s finding(s)", n, strings.ReplaceAll(status, "_", " "))
	}
}

// healthFixDetailLines groups findings (already filtered to the actionable
// set by healthFixableFindings) by Status and renders one
// healthFixCategoryLine per status class present, in FIRST-APPEARANCE order
// - task-12-brief.md: "one per status class present"; actionModalView's own
// "+N more" collapsing (actions.go) handles anything past the modal's
// display budget, so no capping happens here.
func healthFixDetailLines(findings []HealthFinding) []string {
	counts := make(map[string]int, len(findings))
	var order []string
	for _, f := range findings {
		if _, seen := counts[f.Status]; !seen {
			order = append(order, f.Status)
		}
		counts[f.Status]++
	}
	lines := make([]string, len(order))
	for i, status := range order {
		lines[i] = healthFixCategoryLine(status, counts[status])
	}
	return lines
}

// healthFixResultLine renders one finding as a fix-results overlay row: a
// "✓" line for a fixed_* row (Status itself already says what was fixed -
// core/verify.go only ever produces "fixed_stale_deployment"/
// "fixed_needs_reingest"), a "✗" line for anything still outstanding
// (Note appended after an em dash when present, e.g. a locked mod's
// version_mismatch/"locked" refusal).
func healthFixResultLine(f HealthFinding) string {
	glyph := "✗"
	if strings.HasPrefix(f.Status, "fixed_") {
		glyph = "✓"
	}
	line := fmt.Sprintf("%s %s: %s", glyph, healthFindingSubject(f), healthStatusLabel(f.Status))
	if f.Note != "" {
		line += " — " + f.Note
	}
	return line
}

// healthFixResultLines renders the fix-results info overlay's lines from a
// completed fix pass's returned HealthView.Findings (task-12-brief.md:
// "overlay lists the returned view's fixed_* rows and remaining findings" -
// reusing applyUpdatesSequentially's ResultLines/"update results" overlay
// shape, actions.go/app.go, as the layout reference). Two passes, not one:
// every fixed_* row first, then every other still-actionable row (an
// unfixable status - healthUnfixableStatus - is dropped entirely here, same
// as it never entered the confirmation modal's counts) - so "what got
// fixed" always reads before "what's still wrong" regardless of the
// underlying Findings order.
func healthFixResultLines(findings []HealthFinding) []string {
	var lines []string
	for _, f := range findings {
		if strings.HasPrefix(f.Status, "fixed_") {
			lines = append(lines, healthFixResultLine(f))
		}
	}
	for _, f := range findings {
		if !strings.HasPrefix(f.Status, "fixed_") && !healthUnfixableStatus(f.Status) {
			lines = append(lines, healthFixResultLine(f))
		}
	}
	return lines
}

// fixHealthCheckResultMsg carries a successful fix-mode (Full tier, fix=true)
// RunHealthCheck result, tagged with the generation established when the fix
// was confirmed (see fixHealthPrompt) so a superseded result is discarded -
// mirrors fullHealthCheckResultMsg's own gen-tag reasoning in full. A
// dedicated message rather than the generic actionDoneMsg/ActionOutcome pair
// every other confirmation modal produces: RunHealthCheck returns a whole
// HealthView, which actionDoneMsg has no field for (see actionFixHealth's
// own doc comment, actions.go).
type fixHealthCheckResultMsg struct {
	gen  int
	view HealthView
}

// fixHealthCheckFailedMsg carries a failed fix-mode RunHealthCheck call,
// tagged like fixHealthCheckResultMsg.
type fixHealthCheckFailedMsg struct {
	gen int
	err error
}

// fixHealthPrompt handles 'F' on ScreenHealth (task-12-brief.md): a no-op on
// any other screen, with pushed context content, with no ActionProvider
// configured, or while another action/confirmation is already in flight -
// mirrors runFullHealthCheck's own guard shape in full (no inline compound
// switch-case guard is needed here - see keys.go's FixHealth doc comment for
// why, unlike FullCheck's "c" collision with CreateProfile).
//
// Counts the CURRENT view's (m.health, not a fresh scan - "F" fixes what's
// already on screen, "c" is how you refresh it first) actionable findings
// via healthFixableFindings. An empty result - either healthAt is nil (no
// scan has ever run) or every finding is one of the four fix can never touch
// - refuses on the status line ("nothing fixable — run a full check (c)
// first", statusIsError true: like resolveVersionsFetched's "no versions
// reported" refusal, this names a concrete remedy the user should press
// next, not just "nothing to do").
//
// Otherwise opens the standard y/n confirmation modal (title "Fix N
// finding(s)?", one detail line per status class - healthFixDetailLines).
// Confirming does NOT go through buildAction: it hand-rolls the identical
// gen/cancel/progress-pump bookkeeping buildAction would (mirroring
// runFullHealthCheck's own precedent, extended here with a confirm modal in
// front of it) because RunHealthCheck's result is a whole HealthView, not
// the ActionOutcome buildAction's do parameter requires - see
// fixHealthCheckResultMsg's own doc comment. The RunHealthCheck call itself
// is ALWAYS full=true, fix=true: this is the enforcement point T8's review
// deferred to this task - CLI `verify --fix` parity includes the version
// pass, so a Local-tier fix would silently skip repairs the CLI's own --fix
// performs, without ever being asked to.
func (m Model) fixHealthPrompt() (Model, tea.Cmd) {
	if m.screen != ScreenHealth || m.contextContent != nil || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}

	findings := healthFixableFindings(m.health.Findings)
	if len(findings) == 0 {
		m.action.status = "nothing fixable — run a full check (c) first"
		m.action.statusIsError = true
		return m, nil
	}

	title := fmt.Sprintf("Fix %d finding(s)?", len(findings))
	detail := healthFixDetailLines(findings)

	if m.action.cancel != nil {
		m.action.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.action.cancel = cancel
	m.action.gen++
	gen := m.action.gen

	ch := make(chan ActionProgress, 1)
	m.action.progressCh = ch

	pa := pendingAction{
		kind:   actionFixHealth,
		title:  title,
		detail: detail,
		confirm: func() tea.Cmd {
			actionCmd := func() tea.Msg {
				// ALWAYS full=true, fix=true - see this method's own doc
				// comment for why a Local-tier fix is never an option here.
				view, err := m.actions.RunHealthCheck(ctx, true, true, func(p ActionProgress) { sendActionProgress(ch, p) })
				close(ch)
				if err != nil {
					return fixHealthCheckFailedMsg{gen: gen, err: err}
				}
				return fixHealthCheckResultMsg{gen: gen, view: view}
			}
			return tea.Batch(actionCmd, waitForActionProgress(ch, gen))
		},
	}
	return m.promptAction(pa), nil
}

// resolveFixHealthCheckResult handles a fresh fixHealthCheckResultMsg:
// stores the returned view exactly like resolveFullHealthCheckResult does
// (m.health/healthAt/healthErr, summary counts) - including a LOCKED mod's
// version_mismatch row, which the engine returns unchanged rather than
// rewriting to a fixed_* status (an engine refusal, not this task's own
// filtering - see healthFixResultLines' doc comment). Additionally opens the
// fix-results info overlay (task-12-brief.md: the update-results overlay
// pattern) listing the fixed_* rows first, then whatever's still
// outstanding, and returns the ordinary data refresh (m.loadData) so the
// Health screen and dashboard both pick up the change - see the
// documented decay this causes on dataLoadedMsg's own health assignment
// (app.go).
func (m Model) resolveFixHealthCheckResult(msg fixHealthCheckResultMsg) (Model, tea.Cmd) {
	m.action.running = false
	// See resolveFullHealthCheckResult's identical clear for why (#224
	// Copilot round 3 finding): the fix flow's own progress pump can stream
	// the same "checking versions N/M" ticks (it always requests the Full
	// tier - see fixHealthPrompt's own doc comment), so this settle needs
	// the same clear.
	m.action.progress = ActionProgress{}
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
	if m.action.draining {
		return m.resolveDrainedQuit()
	}

	view := msg.view
	m.health = view
	now := m.now()
	m.healthAt = &now
	m.healthErr = ""
	m.summary.HealthIssues, m.summary.HealthWarnings = view.Issues, view.Warnings
	// See resolveFullHealthCheckResult's identical clamp for why (#224
	// Copilot round 3 finding): a fix can resolve findings away, leaving the
	// new view SHORTER than whatever was selected on the old one.
	m.clampSelections()

	if view.Issues == 0 && view.Warnings == 0 {
		m.action.status = "fix: all OK"
	} else {
		m.action.status = fmt.Sprintf("fix: %d issue(s), %d warning(s)", view.Issues, view.Warnings)
	}
	m.action.statusIsError = false

	if lines := healthFixResultLines(view.Findings); len(lines) > 0 && m.overlay == nil {
		m.overlay = &infoOverlay{title: "fix results", lines: lines}
	}

	return m, m.loadData
}

// resolveFixHealthCheckFailure handles a fresh fixHealthCheckFailedMsg:
// status line error, no overlay, and m.health/healthAt left completely
// untouched - mirroring resolveFullHealthCheckFailure's own "keep the
// previous view" posture in full.
func (m Model) resolveFixHealthCheckFailure(msg fixHealthCheckFailedMsg) (Model, tea.Cmd) {
	m.action.running = false
	// See resolveFullHealthCheckFailure's identical clear for why (#224
	// Copilot round 3 finding).
	m.action.progress = ActionProgress{}
	if m.action.cancel != nil {
		m.action.cancel()
		m.action.cancel = nil
	}
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
// a trailing "[locked@<version>]" marker - the CLI bulk table's own
// wording (#143) - on rows the apply below will refuse, plus a trailing
// warning-count line when CheckUpdates surfaced any per-source
// diagnostics alongside the updates it did resolve.
func updateDetailLines(view UpdatesView) []string {
	lines := make([]string, 0, len(view.Updates)+1)
	for _, u := range view.Updates {
		line := fmt.Sprintf("%s %s", u.Name, u.VersionLabel())
		if u.Locked {
			line += fmt.Sprintf(" [locked@%s]", u.LockedVersion)
		}
		lines = append(lines, line)
	}
	if len(view.Warnings) > 0 {
		lines = append(lines, fmt.Sprintf("%d warning(s) during check", len(view.Warnings)))
	}
	return lines
}

// --- Changelog viewer ('v' on the apply-updates modal) ---

// changelogPickedMsg carries the update the user selected in the "View
// changelog" picker (see openChangelogFromUpdateModal, actions.go), routed
// through Update() to resolveChangelogPicked rather than opening the
// overlay directly from the picker's own choose closure - mirrors
// policyChosenMsg's own reasoning in full (see that type's doc comment):
// pendingPicker.choose can only return a tea.Cmd, never a mutated Model, so
// the actual m.overlay assignment must happen from Update(), against the
// LIVE model, not a Model the choose closure captured when the picker was
// built.
type changelogPickedMsg struct {
	update UpdateItem
}

// resolveChangelogPicked handles a changelogPickedMsg: opens the changelog
// overlay for msg.update against the live Model. m.action.pending should
// still be the SAME update batch at this point - updateKey routes every key
// to updatePickerKey while the picker openChangelogFromUpdateModal opened is
// up (picker is checked before action.pending - see updateKey's own doc
// comment), so nothing else can touch action.pending in the window between
// the pick and this message's arrival - but the guard below is kept anyway,
// defense-in-depth, mirroring resolvePolicyChoice/resolveGameSwitch's own
// "the window between the pick and this resolution is real" precedent for
// every OTHER *ChosenMsg resolver in this file.
func (m Model) resolveChangelogPicked(msg changelogPickedMsg) (Model, tea.Cmd) {
	if m.action.pending == nil || m.overlay != nil {
		return m, nil
	}
	m.overlay = changelogOverlay(msg.update)
	return m, nil
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
//
// The returned outcome's ResultLines (fix-wave-2 smoke finding #2) gives the
// batch a durable per-mod record beyond the aggregate "Applied N update(s)"
// Message: one "✓ <name> <from> → <to>" line per successful update, one
// "✗ <name>: <error>" line per failed one, in the SAME order as the batch -
// a ctx-cancelled remainder gets NO line at all (mirroring the Warnings
// behavior just above: those mods never ran, which isn't the same thing as
// failing). app.go's actionDoneMsg handler renders these as a scrollable
// info overlay titled "update results" once the batch resolves.
func applyUpdatesSequentially(ctx context.Context, actions ActionProvider, updates []UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	applied := 0
	var warnings []string
	var resultLines []string
	for _, u := range updates {
		if ctx.Err() != nil {
			break
		}
		outcome, err := actions.ApplyUpdate(ctx, u, progress)
		if err != nil {
			failure := singleLine(err.Error())
			// #143 polish: the core lock gate's refusal names CLI remedy
			// commands ('lmm mod lock ...') that mean nothing inside the
			// TUI - reword it for the TUI's own L key. Keyed off the error,
			// not u.Locked, so a lock raced in after CheckUpdates still gets
			// the TUI wording (falling back to a version-less message when
			// the stale UpdateItem carries no LockedVersion).
			if errors.Is(err, core.ErrModLocked) {
				failure = "locked — unlock or move the lock (L) to update"
				if u.LockedVersion != "" {
					failure = fmt.Sprintf("locked at v%s — unlock or move the lock (L) to update", u.LockedVersion)
				}
			}
			warnings = append(warnings, fmt.Sprintf("%s: %s", u.Name, failure))
			resultLines = append(resultLines, fmt.Sprintf("✗ %s: %s", u.Name, failure))
			continue
		}
		applied++
		warnings = append(warnings, outcome.Warnings...)
		resultLines = append(resultLines, fmt.Sprintf("✓ %s %s", u.Name, u.VersionLabel()))
	}
	return ActionOutcome{
		Message:     fmt.Sprintf("Applied %d update(s)", applied),
		Warnings:    warnings,
		ResultLines: resultLines,
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
// no row selected. Opens the input modal with validate rejecting names
// containing path separators or ".." (such names would be interpreted as
// file paths under the profiles directory; the config layer refuses them
// too, but checking here surfaces the refusal inline instead of only after
// submit) and an EXACT (case-sensitive) match against a name already in
// m.profiles - the input modal's own "name required" handling already
// covers the empty case (see pendingInput's doc comment), so validate here
// never needs to.
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

	input := newInputModalTextInput("profile name", 64, m.availableWidth(), m.theme.Panel.GetHorizontalFrameSize())
	pi := pendingInput{
		title: "new profile",
		input: input,
		validate: func(value string) string {
			if strings.ContainsAny(value, `/\`) || strings.Contains(value, "..") {
				return `name must not contain path separators or ".."`
			}
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
	model, pa := m.buildAction(actionCreateProfile, "", nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.CreateProfile(ctx, name)
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
//
// #68: this refusal is reachable even while a DIFFERENT action is already
// running (m.action.running gates the status-clearing rule 8 in updateKey,
// not this switch's key dispatch - see updateKey's own doc comment), so it
// used to stomp that running action's own live status/progress text
// unconditionally. setIdleStatus (actions.go) applies the same
// hasVisibleStatus-gated guard resolvePolicyChoice's picker-drop hint uses.
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
		m.setIdleStatus(singleLine(errCannotDeleteActiveProfile), true)
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
//
// #68: like deleteSelectedProfile's active-profile refusal, this
// zero-mods short-circuit is reachable while a DIFFERENT action is already
// running (m.action.running doesn't gate this switch's key dispatch in
// updateKey - only rule 8's status-clearing does), so it used to stomp that
// running action's own live status/progress text unconditionally.
// setIdleStatus (actions.go) applies the same hasVisibleStatus-gated guard
// resolvePolicyChoice's picker-drop hint uses.
func (m Model) purgeProfilePrompt() (Model, tea.Cmd) {
	if (m.screen != ScreenDashboard && m.screen != ScreenInstalledMods) || m.actions == nil {
		return m, nil
	}
	if len(m.mods) == 0 {
		m.setIdleStatus("no mods installed", false)
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
//   - sources/search.sources re-seed from the NEW game's SourceInfos(false)/
//     Sources() (sourcesShowAll also resets to false) - a different game can
//     have an entirely different set of configured/registered sources
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
	// #111 Tier 3 fix round 5 - see the profile-switch reset (app.go's
	// actionDoneMsg switchedTo handling) for why these are reset here too,
	// not just left to the next submit.
	m.search.refilling = false
	m.search.refreshing = false

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
	// pendingUpdates is Task 7's retained-changelog-view sibling of
	// action.pending itself (see Model.pendingUpdates' own doc comment) -
	// action.pending is already guaranteed nil above (this method's own
	// single-flight guard), so pendingUpdates should already be nil too, but
	// this is reset here anyway, same defense-in-depth reasoning as the
	// three modal resets just above.
	m.pendingUpdates = nil
	// lastUpdates (fix-wave-2 smoke finding #1) is pendingUpdates' list-
	// scoped sibling (see Model.lastUpdates' own doc comment): NOT already
	// implied nil by the guard above (unlike pendingUpdates, it deliberately
	// outlives the modal), so this reset is the ONLY place it's ever
	// cleared - a different game's retained CheckUpdates result must never
	// answer 'v' on the new game's Installed Mods list.
	m.lastUpdates = nil

	m.summary = Summary{Updates: -1, Conflicts: -1, HealthIssues: -1, HealthWarnings: -1}
	m.mods = nil
	m.profiles = nil
	m.conflicts = nil
	// Health (#224 Task 10 fix round 1): the OLD game's findings/scan-age
	// are just as stale as mods/profiles/conflicts above - left standing,
	// ScreenHealth's home view would render the wrong game's data during
	// the stateLoading window until the fresh loadData below lands.
	m.health = HealthView{}
	m.healthAt = nil
	m.healthErr = ""
	for screen := range m.selected {
		m.selected[screen] = 0
	}
	// Task 4's post-reorder deploy hint (m.orderChanged) described the OLD
	// game's profile - meaningless (and potentially confusing) once every
	// other piece of data-derived state above has just been reset for the
	// new one.
	m.orderChanged = false

	// Task 4/#75: a game switch reverts the Sources screen to its scoped
	// default view too, same as every other piece of data-derived state
	// reset above - the OLD game's "show all" choice describes a registry
	// view that no longer applies.
	m.sourcesShowAll = false
	m.sources = m.provider.SourceInfos(false)
	m.search.sources = append([]string{""}, m.provider.Sources()...)
	m.search.sourceIdx = 0

	m.state = stateLoading
	return m, m.loadData
}

// --- Profile import ('I' on Profiles) ---

// importDataReadMsg carries the raw bytes read from the path the user typed
// into the "import profile — path to yaml" input modal (see
// importProfilePrompt) - dispatched by that modal's own submit closure once
// its validate step has already confirmed the file is readable (see
// importProfilePrompt's own doc comment for why the read itself happens
// there, not here), routed through Update() to resolveImportDataRead exactly
// like profileCreateSubmittedMsg/policyChosenMsg are routed to their own
// resolvers (see policyChosenMsg's doc comment for the shared reasoning):
// pendingInput.submit can only return a tea.Cmd, never a mutated Model, so
// the actual PlanImport call - which needs the LIVE m.actions, in case a
// game switch rebound it in the window between submit and this resolving -
// must run inside Update().
type importDataReadMsg struct{ data []byte }

// importProfilePrompt handles 'I' on Profiles (task-9-brief.md's profile
// import flow): a no-op on the wrong screen, with no ActionProvider
// configured, or while another action/plan/modal is already in flight -
// mirrors createProfilePrompt's own guard shape. Opens the input modal
// titled "import profile — path to yaml".
//
// Unlike every other pendingInput in this file, validate here performs I/O
// (os.ReadFile) rather than a pure string check: this is what lets a bad
// path fail INSIDE the modal (TestImportUnreadablePathErrorsInModal - the
// modal stays open with the OS error as errMsg, exactly like a duplicate
// profile name does for createProfilePrompt) instead of round-tripping
// through a deferred message first. Reading a small local YAML file
// synchronously here is the same category of "local, not network I/O"
// exception openGameSwitcher's ListGames call documents - not something
// that needs the deferred-message treatment network calls (PlanProfileSwitch
// et al.) get.
//
// validate and submit share the read bytes through the closure-captured
// `data` variable: submitInputModal (input_modal.go) always calls validate
// BEFORE submit, synchronously, in the same call - so by the time submit
// runs, data is already populated whenever validate returned "" (ok). A
// validation error leaves data untouched, but submit is never reached in
// that case (submitInputModal returns before calling it), so no stale bytes
// from an earlier attempt can leak into a later successful one.
func (m Model) importProfilePrompt() (Model, tea.Cmd) {
	if m.screen != ScreenProfiles || m.actions == nil {
		return m, nil
	}
	if m.action.running || m.action.pending != nil {
		return m, nil
	}

	var data []byte
	// 256, not createProfilePrompt's 64: a filesystem path (unlike a short
	// profile name) routinely runs well past 64 characters (e.g. a deeply
	// nested home directory), so the input needs considerably more room.
	input := newInputModalTextInput("path to profile.yaml", 256, m.availableWidth(), m.theme.Panel.GetHorizontalFrameSize())
	pi := pendingInput{
		title:       "import profile — path to yaml",
		input:       input,
		hint:        "enter import · esc cancel",
		requiredMsg: "path required",
		validate: func(value string) string {
			read, err := os.ReadFile(value)
			if err != nil {
				return err.Error()
			}
			data = read
			return ""
		},
		submit: func(string) tea.Cmd {
			return func() tea.Msg { return importDataReadMsg{data: data} }
		},
	}
	return m.promptInput(pi), nil
}

// activeGameID returns the ID of the game DataProvider.ListGames reports as
// active ("exactly one entry has Active set" - see that method's own doc
// comment), or "" if ListGames errors or (unreachably, per its own contract)
// reports none. A defensive fallback, never itself surfaced as an error: its
// only consumer (importDetailLines' cross-game warning) treats "" as "can't
// tell, don't warn" rather than risking a false positive.
func (m Model) activeGameID() string {
	games, err := m.provider.ListGames()
	if err != nil {
		return ""
	}
	for _, g := range games {
		if g.Active {
			return g.ID
		}
	}
	return ""
}

// importDetailLines renders an ImportPlanView as the import preview modal's
// detail lines (task-9-brief.md): a profile-name header, an "overwrites
// existing profile" warning when Exists, a "different game: <id>" warning
// when view.GameID names a game other than the session's own active one
// (activeGameID - "" from that means undeterminable, so no warning rather
// than a guess), then per-category counts + mod names for
// Installed/NeedsDownload/Missing, in that order - the same three-way split
// core.ImportPlan itself uses. Mirrors switchDetailLines/installDetailLines'
// own per-category rendering convention; the modal's existing "+N more"
// overflow cap (actionModalView) handles a long list without this needing to
// truncate itself.
func importDetailLines(view ImportPlanView, activeGameID string) []string {
	lines := []string{fmt.Sprintf("Profile: %s", view.Name)}
	if view.Exists {
		lines = append(lines, "overwrites existing profile")
	}
	if activeGameID != "" && view.GameID != "" && view.GameID != activeGameID {
		lines = append(lines, fmt.Sprintf("different game: %s", view.GameID))
	}
	if len(view.Installed) > 0 {
		lines = append(lines, fmt.Sprintf("%d already installed:", len(view.Installed)))
		for _, name := range view.Installed {
			lines = append(lines, fmt.Sprintf("  %s", name))
		}
	}
	if len(view.NeedsDownload) > 0 {
		lines = append(lines, fmt.Sprintf("%d need re-download:", len(view.NeedsDownload)))
		for _, name := range view.NeedsDownload {
			lines = append(lines, fmt.Sprintf("  ↓ %s", name))
		}
	}
	if len(view.Missing) > 0 {
		lines = append(lines, fmt.Sprintf("%d need to be downloaded:", len(view.Missing)))
		for _, name := range view.Missing {
			lines = append(lines, fmt.Sprintf("  ↓ %s", name))
		}
	}
	return lines
}

// resolveImportDataRead handles a fresh importDataReadMsg: calls
// actions.PlanImport SYNCHRONOUSLY (a local parse - no async
// dispatch/gen/cancel bookkeeping needed, unlike PlanProfileSwitch/
// PlanInstall's network-backed plans) against the LIVE model's actions - a
// parse/categorize failure lands on the status line as an error, matching
// resolvePlanFailure's own rendering; a success builds and shows the import
// preview modal via buildAction, with ApplyImport (fed the SAME data bytes)
// as the confirm body.
func (m Model) resolveImportDataRead(msg importDataReadMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		return m, nil
	}

	view, err := m.actions.PlanImport(m.ctx, msg.data)
	if err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, nil
	}

	data := msg.data
	title := fmt.Sprintf("Import profile %q?", view.Name)
	model, pa := m.buildAction(actionImport, title, importDetailLines(view, m.activeGameID()), "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		return m.actions.ApplyImport(ctx, data, progress)
	})
	return model.promptAction(pa), nil
}

// importAppliedMsg is dispatched by app.go's actionDoneMsg handler
// immediately after a successful actionImport whose outcome named a profile
// to offer switching to (ActionOutcome.ImportedProfile - see its own doc
// comment: set only for a same-game import). Carries just the name, exactly
// like gameChosenMsg carries just an id (see that type's doc comment for the
// shared "resolve against the LIVE model" reasoning) - dispatched as a Cmd
// rather than opened inline in the actionDoneMsg case itself so this
// resolves through the same "deferred msg, guarded resolution" idiom every
// other modal-opening resolve* handler in this file uses, rather than being
// a one-off exception.
type importAppliedMsg struct{ name string }

// resolveImportApplied handles a fresh importAppliedMsg: opens a "switch to
// <name> now?" yes/no picker for the profile actionImport just saved -
// reachable only when ApplyImport's outcome named one (see
// ActionOutcome.ImportedProfile's own doc comment). This is a pendingPicker,
// not a pendingAction/buildAction confirm: confirming it never calls an
// ActionProvider method directly (see importSwitchConfirmedMsg's doc
// comment for why) - it re-enters switchToProfileNamed's own async
// plan-fetch chain instead, which needs a picker-style "choose, don't touch
// action.running" resolution (choosePickerOption, picker.go), unlike
// buildAction's "one direct ActionProvider call" shape. Guarded like every
// other picker-opening handler (running/pending); promptPicker's own guard
// repeats this defense-in-depth, same as every other promptX call here.
func (m Model) resolveImportApplied(msg importAppliedMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	name := msg.name
	picker := pendingPicker{
		title:   fmt.Sprintf("switch to %q now?", name),
		options: []pickerOption{{Label: "yes"}, {Label: "no"}},
		choose: func(idx int) tea.Cmd {
			if idx != 0 {
				return nil
			}
			return func() tea.Msg { return importSwitchConfirmedMsg{name: name} }
		},
	}
	return m.promptPicker(picker), nil
}

// importSwitchConfirmedMsg is dispatched by resolveImportApplied's "switch to
// <name> now?" picker when the user picks "yes" - mirroring
// policyChosenMsg/gameChosenMsg's own reasoning in full (see either's doc
// comment): pendingPicker.choose can only return a tea.Cmd, never a mutated
// Model, so the actual switchToProfileNamed call (which mutates
// action.gen/cancel/running/status) must run inside Update(), against the
// LIVE model, one tick later.
type importSwitchConfirmedMsg struct{ name string }

// resolveImportSwitchConfirmed handles an importSwitchConfirmedMsg - the
// "yes" branch of resolveImportApplied's offer - by routing into
// switchToProfileNamed for the newly imported profile's name, exactly like
// switchSelectedProfile's own Select-key path does (task-9-brief.md: "reuse
// switchSelectedProfile's machinery - do not duplicate the switch flow"). No
// separate ApplyProfileSwitch call is written here: switchToProfileNamed
// already owns single-flight guarding, the active-profile short circuit, and
// the async PlanProfileSwitch dispatch that eventually opens the REAL switch
// confirmation modal (with its own Enable/Disable/NeedsDownloads detail) -
// this "yes" is deliberately a SECOND, separate confirmation on top of that
// one, not a shortcut past it.
func (m Model) resolveImportSwitchConfirmed(msg importSwitchConfirmedMsg) (Model, tea.Cmd) {
	return m.switchToProfileNamed(msg.name)
}

// --- Profile export ('E' on Profiles) ---

// exportPathSubmittedMsg carries the selected profile's name and the path
// the user typed into the "export profile — path to save" input modal (see
// exportProfilePrompt), routed through Update() to resolveExportSubmitted -
// mirroring profileCreateSubmittedMsg's own reasoning in full (see its doc
// comment): pendingInput.submit can only return a tea.Cmd, never a mutated
// Model, so the actual ExportProfile call - which needs the LIVE m.actions,
// in case a game switch rebound it in the window between submit and this
// resolving - must run inside Update().
type exportPathSubmittedMsg struct{ name, path string }

// exportProfilePrompt handles 'E' on Profiles (task-10-brief.md's profile
// export flow): a no-op on the wrong screen, an out-of-range selection, with
// no ActionProvider configured, or while another action/plan/modal is
// already in flight - mirrors importProfilePrompt's own guard shape, plus a
// row-selection requirement (export always acts on ONE specific already-
// visible profile, unlike import which needs none).
//
// Opens the input modal titled "export profile — path to save" with the
// input PREFILLED "<gameID>-<name>.yaml" (m.activeGameID(), the selected
// profile's own Name) - unlike createProfilePrompt/importProfilePrompt's
// blank starting value, so the common case (accept the sensible default) is
// just enter. validate is a no-op (always ""): unlike importProfilePrompt's
// I/O-performing validate (which must confirm a path is READABLE before
// ever dispatching), there is nothing to check about a path meant for
// WRITING here - an unwritable directory or a pre-existing file both surface
// as an ActionProvider error on the status line after submit instead (see
// resolveExportSubmitted), exactly like every other ActionProvider error in
// this file. An empty/whitespace-only value never reaches validate at all:
// submitInputModal's own "name required" check (input_modal.go) already
// refuses it, on the already-trimmed value, before validate is ever called -
// the same generic guard createProfilePrompt's own doc comment notes it
// never needs to duplicate.
func (m Model) exportProfilePrompt() (Model, tea.Cmd) {
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

	input := newInputModalTextInput("path to save profile.yaml", 256, m.availableWidth(), m.theme.Panel.GetHorizontalFrameSize())
	input.SetValue(fmt.Sprintf("%s-%s.yaml", m.activeGameID(), profile.Name))
	input.CursorEnd()

	name := profile.Name
	pi := pendingInput{
		title:       "export profile — path to save",
		input:       input,
		hint:        "enter export · esc cancel",
		requiredMsg: "path required",
		validate:    func(string) string { return "" },
		submit: func(value string) tea.Cmd {
			return func() tea.Msg { return exportPathSubmittedMsg{name: name, path: value} }
		},
	}
	return m.promptInput(pi), nil
}

// resolveExportSubmitted handles a fresh exportPathSubmittedMsg: calls
// actions.ExportProfile SYNCHRONOUSLY (a local filesystem write - no async
// dispatch/gen/cancel bookkeeping needed, the same documented sync exception
// ReorderMods/DeployedFiles carry - see ReorderMods' own doc comment) against
// the LIVE model's actions. A failure (including the overwrite refusal,
// coreProvider's own "file exists: <path>") lands on the status line as an
// error; a success renders the outcome's message the same way
// actionDoneMsg's handler does (formatOutcomeStatus, app.go) - there is no
// confirmation modal for export to clear here, unlike every buildAction-
// routed mutation, since the modal's own submit WAS the user's confirmation
// (mirroring resolveProfileCreate's identical "no second confirm gate"
// framing).
func (m Model) resolveExportSubmitted(msg exportPathSubmittedMsg) (Model, tea.Cmd) {
	if m.action.running || m.action.pending != nil {
		return m, nil
	}
	outcome, err := m.actions.ExportProfile(m.ctx, msg.name, msg.path)
	if err != nil {
		m.action.status = singleLine(err.Error())
		m.action.statusIsError = true
		return m, nil
	}
	m.action.status = formatOutcomeStatus(outcome)
	m.action.statusIsError = false
	return m, nil
}

// --- Sources scope toggle ('a' on Sources, Task 4/#75) ---

// toggleSourcesAll flips the Sources screen between the game-scoped default
// list and the full registry, re-fetching m.sources from the provider in
// lockstep with m.sourcesShowAll (Model.sourcesShowAll's own doc comment)
// so the two can never disagree about what's on screen. A no-op on any
// other screen, mirroring ToggleEnable/Files/Policy's own screen-guarded
// shape (mutations.go's established convention for a single-screen key).
//
// Unlike every mutation above, this performs no I/O and touches no
// ActionProvider - SourceInfos is the same synchronous, side-effect-free
// read NewModel's own seeding comment (app.go) already documents - so there
// is no action.running/pending guard, no confirmation, and no status-line
// outcome: the view itself IS the confirmation, exactly like CycleSource's
// own instant, guard-free toggle on the Search screen.
//
// The selection resets to 0 rather than being clamped: the two views can
// have entirely different lengths and different sources at the same index
// (mirrors resolveGameSwitch's own "the OLD list is about to be replaced
// wholesale, not just resized" reasoning for why THAT reset uses 0 instead
// of clampSelections too), so preserving a numeric position across the
// toggle would highlight an unrelated row rather than the one the user was
// just looking at.
func (m Model) toggleSourcesAll() (Model, tea.Cmd) {
	if m.screen != ScreenSources {
		return m, nil
	}
	m.sourcesShowAll = !m.sourcesShowAll
	m.sources = m.provider.SourceInfos(m.sourcesShowAll)
	m.selected[ScreenSources] = 0
	return m, nil
}
