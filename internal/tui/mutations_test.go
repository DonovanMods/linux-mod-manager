package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// --- Toggle enable/disable ('e' on Installed Mods) ---

// TestToggleEnableKeyOnDisabledModPromptsEnable covers the full round trip
// for the enable direction: 'e' on a disabled mod builds+shows the Enable
// modal (nothing calls the provider yet), 'y' dispatches EnableMod with the
// SELECTED item, and the resulting actionDoneMsg triggers a refresh.
func TestToggleEnableKeyOnDisabledModPromptsEnable(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{EnableOutcome: ActionOutcome{Message: `Enabled "Alternate Start"`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 4 // "Alternate Start", status "disabled"
	require.Equal(t, "disabled", model.mods[4].Status)

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionEnable, model.action.pending.kind)
	require.Equal(t, `Enable "Alternate Start"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Game: Skyrim Special Edition")
	require.Contains(t, model.action.pending.detail, "Profile: survival")
	require.Contains(t, model.action.pending.detail, "Files will be deployed to the game directory.")
	require.Empty(t, rec.EnableCalls, "nothing must mutate before confirm")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.Nil(t, model.action.pending)
	require.NotNil(t, confirmCmd)

	doneMsg := confirmCmd()
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.EnableCalls, 1)
	require.Equal(t, "alternate-start", rec.EnableCalls[0].ID)
	require.Equal(t, "nexusmods", rec.EnableCalls[0].Source)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Enabled "Alternate Start"`, model.action.status)
	require.IsType(t, dataLoadedMsg{}, refreshCmd())
}

// TestToggleEnableKeyOnEnabledModPromptsDisable covers the disable
// direction: any status other than "disabled" toggles to Disable.
func TestToggleEnableKeyOnEnabledModPromptsDisable(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{DisableOutcome: ActionOutcome{Message: `Disabled "SkyUI"`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI", status "installed"
	require.NotEqual(t, "disabled", model.mods[0].Status)

	updated, _ := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDisable, model.action.pending.kind)
	require.Equal(t, `Disable "SkyUI"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Files will be removed from the game directory (cache kept).")

	_, confirmCmd := model.Update(keyRunes("y"))
	require.NotNil(t, confirmCmd)
	confirmCmd()
	require.Len(t, rec.DisableCalls, 1)
	require.Equal(t, "skyui", rec.DisableCalls[0].ID)
}

// TestToggleEnableKeyCancelDoesNotCallProvider proves 'n' dismisses the
// modal without ever touching the provider.
func TestToggleEnableKeyCancelDoesNotCallProvider(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 4

	updated, _ := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("n"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.EnableCalls)
	require.Empty(t, rec.DisableCalls)
}

// TestToggleEnableKeyWrongScreenIsNoop proves 'e' only fires on Installed
// Mods.
func TestToggleEnableKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

// TestToggleEnableKeyEmptyListIsNoop proves an empty mods list can never
// crash or open a modal for a nonexistent selection.
func TestToggleEnableKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.mods = nil
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

// TestToggleEnableKeyInertWhileRunning proves the single-flight guard: an
// in-flight action blocks a brand-new prompt, verified via m.action.pending
// staying nil (buildAction's guard-refusal returns a no-op pendingAction
// rather than a nil return value - see actions.go's doc comment).
func TestToggleEnableKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 4
	model.action.running = true

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.EnableCalls)
}

// TestToggleEnableKeyInertWhileAnotherModalPending proves a DIFFERENT
// already-pending modal is left completely undisturbed by a second
// mutation key.
func TestToggleEnableKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("D")) // opens the Deploy modal
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.EnableCalls)
	require.Empty(t, rec.DisableCalls)
}

// TestToggleEnableKeyInertWhileSearchFocused proves 'e' types into the
// search box instead of triggering a mutation while ScreenSearch is
// focused - the existing focused-input swallow branch runs before the
// mutation-key switch, so this is inertness by construction, not a special
// case.
func TestToggleEnableKeyInertWhileSearchFocused(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "e")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "e")
	require.Nil(t, updated.action.pending)
}

// --- Uninstall ('x' on Installed Mods) ---

func TestUninstallKeyPromptsAndConfirmCallsProviderWithSelectedItem(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UninstallOutcome: ActionOutcome{Message: `Uninstalled "SkyUI"`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI"

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionUninstall, model.action.pending.kind)
	require.Equal(t, `Uninstall "SkyUI"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Removes deployed files, cache, and profile entry. Uninstall hooks will run.")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := confirmCmd()
	require.Len(t, rec.UninstallCalls, 1)
	require.Equal(t, "skyui", rec.UninstallCalls[0].ID)
	require.Equal(t, "nexusmods", rec.UninstallCalls[0].Source)

	updated, refreshCmd := model.Update(doneMsg)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Uninstalled "SkyUI"`, updated.(Model).action.status)
}

func TestUninstallKeyCancelDoesNotCallProvider(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("x"))
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.UninstallCalls)
}

func TestUninstallKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenProfiles

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestUninstallKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenInstalledMods
	model.mods = nil

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestUninstallKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.action.running = true

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.UninstallCalls)
}

// --- Deploy ('D' on Dashboard and Installed Mods) ---

func TestDeployKeyFromDashboardPromptsAndConfirmCallsProvider(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{DeployOutcome: ActionOutcome{Message: "Deployed 5 mod(s)"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)
	require.Equal(t, `Deploy profile "survival"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Game: Skyrim Special Edition")
	require.Contains(t, model.action.pending.detail, "Mods: 39 enabled")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := confirmCmd()
	require.Equal(t, 1, rec.DeployCalls)

	updated, refreshCmd := model.Update(doneMsg)
	require.NotNil(t, refreshCmd)
	require.Equal(t, "Deployed 5 mod(s)", updated.(Model).action.status)
}

func TestDeployKeyFromInstalledModsPrompts(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods

	updated, _ := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)
}

func TestDeployKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenSearch, ScreenProfiles, ScreenSources} {
		model := modelWithActions(t, &recordingActions{})
		model.screen = screen

		updated, cmd := model.Update(keyRunes("D"))
		model = updated.(Model)
		require.Nil(t, cmd)
		require.Nil(t, model.action.pending, "screen %v", screen)
	}
}

func TestDeployKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.action.running = true

	updated, cmd := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Equal(t, 0, rec.DeployCalls)
}

// --- Profile switch ('enter' on Profiles) ---

func TestSwitchKeyOnActiveProfileSetsStatusNoModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0 // "survival", active
	require.True(t, model.profiles[0].Active)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Equal(t, `Already on profile "survival"`, model.action.status)
	require.False(t, model.action.statusIsError)
	require.Empty(t, rec.PlanCalls, "must never call PlanProfileSwitch for the active profile")
}

func TestSwitchKeyEmptyProfileListIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenProfiles
	model.profiles = nil

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestSwitchKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenInstalledMods

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, updated.(Model).CurrentScreen())
	require.Nil(t, cmd)
}

func TestSwitchKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1
	model.action.running = true

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.PlanCalls)
}

// TestSwitchKeyDispatchesAsyncPlanFetch proves enter on a non-active
// profile shows a "Planning switch…" status and returns a command instead
// of synchronously calling the provider.
func TestSwitchKeyDispatchesAsyncPlanFetch(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanView: SwitchPlanView{From: "survival", To: "vanilla-plus", NoChanges: true}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1 // "vanilla-plus"

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Planning switch…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.PlanCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := cmd()
	require.IsType(t, planResultMsg{}, msg)
	require.Equal(t, []string{"vanilla-plus"}, rec.PlanCalls)
}

// TestSwitchPlanStaleResultDiscarded proves a plan result tagged with an
// old gen is discarded entirely - no modal, no status change, no running
// flip - mirroring actionDoneMsg's own staleness contract (rule 4 in
// actions_test.go).
func TestSwitchPlanStaleResultDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true
	model.action.status = ""

	updated, cmd := model.Update(planResultMsg{gen: 4, view: SwitchPlanView{From: "a", To: "b"}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running, "stale result must not clear running")
	require.Nil(t, m.action.pending, "stale result must never open a modal")
	require.Empty(t, m.action.status)
}

func TestSwitchPlanStaleFailureDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true

	updated, cmd := model.Update(planFailedMsg{gen: 1, err: errors.New("boom")})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running)
	require.Empty(t, m.action.status)
}

// TestSwitchPlanErrorShowsStatusNoModal covers the plan-error path end to
// end: enter -> async fetch -> planFailedMsg -> error status, no modal.
func TestSwitchPlanErrorShowsStatusNoModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanErr: errors.New("plan boom")}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, planFailedMsg{}, msg)

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "plan boom")
	require.Nil(t, model.action.pending)
}

// TestSwitchPlanAlreadyActiveDefensive exercises resolvePlanResult's
// defensive AlreadyActive branch directly (see task-7-brief.md's
// profile-switch flow: normally pre-filtered by the synchronous active
// check in the key handler, so this path is otherwise unreachable through
// a real keypress).
func TestSwitchPlanAlreadyActiveDefensive(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 1
	model.action.running = true

	updated, cmd := model.Update(planResultMsg{gen: 1, view: SwitchPlanView{From: "x", To: "x", AlreadyActive: true}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.False(t, m.action.running)
	require.Nil(t, m.action.pending)
	require.Equal(t, `Already on profile "x"`, m.action.status)
	require.False(t, m.action.statusIsError)
}

// TestSwitchPlanNeedsDownloadsRefusesNoModal covers the core-fake half of
// the NeedsDownloads refusal: the exact provider-contract message from
// errProfileNeedsDownloads must reach the status line, in error styling,
// with no modal and no ApplyProfileSwitch call.
func TestSwitchPlanNeedsDownloadsRefusesNoModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanView: SwitchPlanView{From: "survival", To: "vanilla-plus", NeedsDownloads: []string{"nexusmods:foo v1.0"}}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := cmd()

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.Nil(t, model.action.pending, "must not open a modal")
	require.True(t, model.action.statusIsError)
	require.Equal(t, errProfileNeedsDownloads.Error(), model.action.status)
	require.Empty(t, rec.ApplyCalls, "must never call ApplyProfileSwitch")
}

// TestSwitchPlanNoChangesShowsSetAsDefaultModal covers the NoChanges
// wording mandated by task-7-brief.md.
func TestSwitchPlanNoChangesShowsSetAsDefaultModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanView: SwitchPlanView{From: "survival", To: "vanilla-plus", NoChanges: true}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Equal(t, actionSwitch, model.action.pending.kind)
	require.Equal(t, `Switch to "vanilla-plus"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "From: survival")
	require.Contains(t, model.action.pending.detail, "No mod changes; set as default.")
}

// TestSwitchConfirmCallsApplyProfileSwitchWithTargetName covers the full
// happy path: plan with real Enable/Disable buckets -> modal detail lines
// -> confirm -> ApplyProfileSwitch(target) -> refresh.
func TestSwitchConfirmCallsApplyProfileSwitchWithTargetName(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		PlanView:     SwitchPlanView{From: "survival", To: "vanilla-plus", Enable: []string{"Frostfall"}, Disable: []string{"SkyUI"}},
		ApplyOutcome: ActionOutcome{Message: `Switched to "vanilla-plus"`},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Contains(t, model.action.pending.detail, "+ Frostfall")
	require.Contains(t, model.action.pending.detail, "- SkyUI")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := confirmCmd()
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []string{"vanilla-plus"}, rec.ApplyCalls)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Switched to "vanilla-plus"`, model.action.status)
}

// TestPrototypeSwitchPlanNeedsDownloadsCannedScenario exercises the
// mandated prototype demo data addition end to end: a canned profile whose
// mod list references an ID absent from InstalledMods must drive the exact
// same refusal path as the core fake above, through the REAL
// prototypeProvider (not recordingActions).
func TestPrototypeSwitchPlanNeedsDownloadsCannedScenario(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenProfiles

	idx := -1
	for i, p := range model.profiles {
		if p.Name == prototype.NeedsDownloadProfileName {
			idx = i
		}
	}
	require.GreaterOrEqualf(t, idx, 0, "canned needs-download profile %q must be present in prototype data", prototype.NeedsDownloadProfileName)
	model.selected[ScreenProfiles] = idx

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, planResultMsg{}, msg, "a needs-downloads plan is a successful plan fetch, not a fetch error")

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.Nil(t, model.action.pending, "must not open a modal")
	require.True(t, model.action.statusIsError)
	require.Equal(t, errProfileNeedsDownloads.Error(), model.action.status)
}

// --- Help/footer content ---

func TestHelpOverlayDocumentsMutationKeysAndDropsStaleReadOnlyClaim(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 38)
	model = updateWithRunes(t, model, "?")
	view := model.View()

	require.Contains(t, view, "toggle enable/disable")
	require.Contains(t, view, "uninstall selected mod")
	require.Contains(t, view, "deploy active profile")
	require.Contains(t, view, "switch profile")
	require.NotContains(t, view, "Browsing is read-only",
		"the help copy must no longer claim the TUI is read-only now that mutations exist")
}

func TestFooterHintMentionsMutationKeys(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 36)
	view := model.View()

	require.Contains(t, view, "e/x/D: mutate")
	require.Contains(t, view, "enter: switch")
}
