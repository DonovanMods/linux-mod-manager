package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

// modelWithActions builds a fully-loaded Model (data ready) backed by the
// prototype DataProvider but with actions as its ActionProvider. This
// isolates action-machinery tests from data-loading concerns, mirroring
// recordingActions/failingActions' own doc comment on why they exist
// independent of any specific DataProvider dataset.
func modelWithActions(t *testing.T, actions ActionProvider) Model {
	t.Helper()
	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider(), Actions: actions})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	return loaded.(Model)
}

func sizedModelWithActions(t *testing.T, actions ActionProvider, width, height int) Model {
	t.Helper()
	model := modelWithActions(t, actions)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(Model)
}

// --- Rule 1: promptAction shows the modal; nothing mutates before confirm ---

func TestPromptActionShowsModalWithoutMutating(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	item := ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"}

	model, pa := model.buildAction(actionUninstall, `Uninstall "SkyUI"?`, []string{"Removes SkyUI from the active profile."}, "",
		func(ctx context.Context) (ActionOutcome, error) { return rec.UninstallMod(ctx, item) })
	model = model.promptAction(pa)

	require.NotNil(t, model.action.pending)
	require.Contains(t, model.screenView(), `Uninstall "SkyUI"?`)
	require.Empty(t, rec.UninstallCalls, "nothing must mutate before confirm")
}

// --- Rule 2: pending-modal key interception ---

func TestConfirmKeysDispatchActionAndClearPending(t *testing.T) {
	t.Parallel()

	for _, k := range []string{"y", "enter"} {
		t.Run(k, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{UninstallOutcome: ActionOutcome{Message: `Uninstalled "SkyUI"`}}
			model := modelWithActions(t, rec)
			item := ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"}
			model, pa := model.buildAction(actionUninstall, `Uninstall "SkyUI"?`, nil, "",
				func(ctx context.Context) (ActionOutcome, error) { return rec.UninstallMod(ctx, item) })
			model = model.promptAction(pa)

			var updated tea.Model
			var cmd tea.Cmd
			if k == "enter" {
				updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			} else {
				updated, cmd = model.Update(keyRunes(k))
			}
			model = updated.(Model)

			require.Nil(t, model.action.pending, "pending must clear on confirm")
			require.True(t, model.action.running, "running must be set on confirm")
			require.NotNil(t, cmd)
			require.Empty(t, rec.UninstallCalls, "the provider call happens when the returned cmd runs, not synchronously in Update")

			msg := cmd()
			require.IsType(t, actionDoneMsg{}, msg)
			require.Len(t, rec.UninstallCalls, 1)
			require.Equal(t, item, rec.UninstallCalls[0])
		})
	}
}

func TestConfirmClosureMapsProviderErrorToActionFailedMsg(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("provider error: deployment failed")
	failing := failingActions{Err: sentinel}
	model := modelWithActions(t, failing)
	item := ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"}

	// buildAction increments gen and captures it in the closure
	model, pa := model.buildAction(actionUninstall, `Uninstall "SkyUI"?`, nil, "",
		func(ctx context.Context) (ActionOutcome, error) { return failing.UninstallMod(ctx, item) })

	genAtBuild := model.action.gen
	model = model.promptAction(pa)

	// Confirm via key, which dispatches pa.confirm() and sets running
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)

	require.Nil(t, model.action.pending, "pending must clear on confirm")
	require.True(t, model.action.running, "running must be set on confirm")
	require.NotNil(t, cmd)

	// Run the cmd to trigger the provider call and error path
	msg := cmd()

	// Assert the error branch maps to actionFailedMsg correctly
	require.IsType(t, actionFailedMsg{}, msg, "provider error must produce actionFailedMsg")
	failedMsg := msg.(actionFailedMsg)
	require.Equal(t, genAtBuild, failedMsg.gen, "actionFailedMsg must carry the gen from buildAction")
	require.Equal(t, actionUninstall, failedMsg.kind, "actionFailedMsg must carry the action kind")
	require.Equal(t, sentinel, failedMsg.err, "actionFailedMsg must carry the provider's error")
}

func TestCancelKeysLeaveStateUntouched(t *testing.T) {
	t.Parallel()

	for _, k := range []string{"n", "esc"} {
		t.Run(k, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{}
			model := modelWithActions(t, rec)
			model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "",
				func(ctx context.Context) (ActionOutcome, error) { return rec.DeployProfile(ctx) })
			model = model.promptAction(pa)
			genBefore := model.action.gen

			var updated tea.Model
			var cmd tea.Cmd
			if k == "esc" {
				updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
			} else {
				updated, cmd = model.Update(keyRunes(k))
			}
			model = updated.(Model)

			require.Nil(t, model.action.pending)
			require.False(t, model.action.running)
			require.Nil(t, cmd)
			require.Equal(t, genBefore, model.action.gen, "cancel must not touch gen")
			require.Equal(t, 0, rec.DeployCalls, "cancel must never call the provider")
		})
	}
}

func TestPendingModalIgnoresNavigationButQuitStillQuits(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.selected[ScreenDashboard] = 0
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "",
		func(ctx context.Context) (ActionOutcome, error) { return rec.DeployProfile(ctx) })
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("j"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending, "modal must still be shown")
	require.Equal(t, 0, model.selected[ScreenDashboard], "navigation must not reach the dashboard while the modal is up")

	updated, cmd = model.Update(keyRunes("2"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, ScreenDashboard, model.CurrentScreen(), "screen must not change while the modal is up")

	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	require.Equal(t, tea.Quit(), cmd())
}

// --- Rule 3: single-flight ---

func TestSingleFlightBlocksNewPromptsWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "",
		func(ctx context.Context) (ActionOutcome, error) { return rec.DeployProfile(ctx) })
	model = model.promptAction(pa)
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, cmd)
	genBefore := model.action.gen

	// A second action attempted while the first is still running must not
	// disturb the in-flight action's gen or show a modal.
	model2, pa2 := model.buildAction(actionEnable, "Enable something?", nil, "",
		func(ctx context.Context) (ActionOutcome, error) { return rec.EnableMod(ctx, ModItem{}) })
	model2 = model2.promptAction(pa2)
	require.Nil(t, model2.action.pending, "single-flight must ignore the new prompt")
	require.Equal(t, genBefore, model2.action.gen, "buildAction must not bump gen while another action is running")

	// Navigation still works while running.
	navUpdated, _ := model.Update(keyRunes("2"))
	require.Equal(t, ScreenInstalledMods, navUpdated.(Model).CurrentScreen())

	// A stray confirm key (pending is already nil) cannot double-dispatch.
	_, strayCmd := model.Update(keyRunes("y"))
	require.Nil(t, strayCmd)
}

// --- Rule 4: actionDoneMsg staleness + refresh ---

func TestStaleActionDoneMsgIsDiscardedEntirely(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 5
	model.action.running = true
	model.action.status = ""

	updated, cmd := model.Update(actionDoneMsg{gen: 4, kind: actionEnable, outcome: ActionOutcome{Message: "stale"}})
	m := updated.(Model)
	require.Nil(t, cmd, "a stale result must not dispatch a refresh")
	require.True(t, m.action.running, "stale result must not clear running")
	require.Empty(t, m.action.status, "stale result must not set status")
}

func TestFreshActionDoneMsgUpdatesStatusAndRefreshes(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 3
	model.action.running = true

	updated, cmd := model.Update(actionDoneMsg{gen: 3, kind: actionEnable, outcome: ActionOutcome{Message: `Enabled "SkyUI"`}})
	m := updated.(Model)
	require.False(t, m.action.running)
	require.Equal(t, `Enabled "SkyUI"`, m.action.status)
	require.False(t, m.action.statusIsError)
	require.NotNil(t, cmd, "a fresh result must dispatch a data refresh")

	refreshMsg := cmd()
	require.IsType(t, dataLoadedMsg{}, refreshMsg)
}

func TestFormatOutcomeStatusWarningSuffix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Deployed 3 mod(s)", formatOutcomeStatus(ActionOutcome{Message: "Deployed 3 mod(s)"}))
	require.Equal(t, "Deployed 3 mod(s) — one broke",
		formatOutcomeStatus(ActionOutcome{Message: "Deployed 3 mod(s)", Warnings: []string{"one broke"}}))
	require.Equal(t, "Deployed 3 mod(s) (2 warnings)",
		formatOutcomeStatus(ActionOutcome{Message: "Deployed 3 mod(s)", Warnings: []string{"a", "b"}}))
}

// --- Rule 5: actionFailedMsg staleness + partial-mutation contract ---

func TestFreshActionFailedMsgShowsErrorRefreshesAndKeepsScreen(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 2
	model.action.running = true
	model.screen = ScreenInstalledMods

	err := fmt.Errorf("deploy failed:\nrollback ok")
	updated, cmd := model.Update(actionFailedMsg{gen: 2, kind: actionDeploy, err: err})
	m := updated.(Model)

	require.False(t, m.action.running)
	require.True(t, m.action.statusIsError)
	require.NotContains(t, m.action.status, "\n", "status must stay one line")
	require.Contains(t, m.action.status, "deploy failed")
	require.Equal(t, ScreenInstalledMods, m.CurrentScreen(), "a failure must not move the user off their screen")
	require.NotNil(t, cmd, "the partial-mutation contract still refreshes on failure")

	refreshMsg := cmd()
	require.IsType(t, dataLoadedMsg{}, refreshMsg)
}

func TestStaleActionFailedMsgIsDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true

	updated, cmd := model.Update(actionFailedMsg{gen: 1, kind: actionDeploy, err: errors.New("boom")})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running)
	require.Empty(t, m.action.status)
}

func TestFailedStatusTruncatesAtRenderTimeNotSetTime(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 300)
	model := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	model.action.gen = 1
	model.action.running = true
	updated, _ := model.Update(actionFailedMsg{gen: 1, kind: actionDeploy, err: errors.New(long)})
	model = updated.(Model)
	require.Equal(t, long, model.action.status, "the raw message is stored untruncated")

	wide := model.statusLine()
	require.LessOrEqual(t, lipgloss.Width(wide), model.availableWidth())

	resized, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	model = resized.(Model)
	narrow := model.statusLine()
	require.LessOrEqual(t, lipgloss.Width(narrow), model.availableWidth())
	require.Less(t, lipgloss.Width(narrow), lipgloss.Width(wide),
		"the SAME stored status re-truncates shorter at a narrower width, proving truncation happens at render time")
}

// --- Rule 6: dataLoadedMsg clamps selections ---

func TestDataLoadedClampsSelectionToShrunkLists(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.selected[ScreenInstalledMods] = 4
	model.selected[ScreenProfiles] = 3

	updated, _ := model.Update(dataLoadedMsg{
		summary:  Summary{},
		mods:     []ModItem{{ID: "a"}, {ID: "b"}},
		profiles: []ProfileItem{{Name: "solo"}},
	})
	m := updated.(Model)
	require.Equal(t, 1, m.selected[ScreenInstalledMods], "clamped to len(mods)-1")
	require.Equal(t, 0, m.selected[ScreenProfiles], "clamped to len(profiles)-1")
}

func TestDataLoadedClampsSelectionToZeroOnEmptyList(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.selected[ScreenInstalledMods] = 4

	updated, _ := model.Update(dataLoadedMsg{summary: Summary{}, mods: nil, profiles: nil})
	m := updated.(Model)
	require.Equal(t, 0, m.selected[ScreenInstalledMods])
}

// --- Rule 7: modal rendering ---

func TestActionModalReplacesContentAreaAndKeepsChrome(t *testing.T) {
	t.Parallel()

	model := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	model.screen = ScreenInstalledMods
	model, pa := model.buildAction(actionUninstall, `Uninstall "SkyUI"?`, []string{"Removes SkyUI."}, "", func(context.Context) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	view := model.View()
	require.Contains(t, view, "LMM // Linux Mod Manager", "chrome (title) stays")
	require.Contains(t, view, `Uninstall "SkyUI"?`)
	require.Contains(t, view, "Removes SkyUI.")
	require.NotContains(t, view, "SPELLBOOK: INSTALLED MODS", "the modal replaces the underlying screen, not overlays it")
}

func TestActionModalTruncatesDetailToPanelContentWidth(t *testing.T) {
	t.Parallel()

	model := sizedModelWithActions(t, &recordingActions{}, 40, 24)
	long := strings.Repeat("mod-name-", 20)
	model, pa := model.buildAction(actionUninstall, "Uninstall?", []string{long}, "", func(context.Context) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	for _, line := range strings.Split(model.screenView(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), model.availableWidth())
	}
}

func TestActionModalCollapsesLongDetailListWithMoreLine(t *testing.T) {
	t.Parallel()

	model := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	detail := make([]string, 20)
	for i := range detail {
		detail[i] = fmt.Sprintf("mod-%02d", i)
	}
	model, pa := model.buildAction(actionSwitch, "Switch profile?", detail, "", func(context.Context) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	view := model.screenView()
	require.Contains(t, view, "mod-00")
	require.Contains(t, view, "+", "must summarize the overflow instead of listing all 20")
	require.NotContains(t, view, "mod-19", "must not render every entry once capped")
}

func TestActionModalHeightInvariantAt80x24AndWidthFloor40(t *testing.T) {
	t.Parallel()

	detail := make([]string, 20)
	for i := range detail {
		detail[i] = fmt.Sprintf("a very long mod entry name number %02d that could overflow", i)
	}

	for _, width := range []int{80, 40} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			t.Parallel()
			model := sizedModelWithActions(t, &recordingActions{}, width, 24)
			model, pa := model.buildAction(actionSwitch, "Switch to vanilla-plus?", detail, "", func(context.Context) (ActionOutcome, error) {
				return ActionOutcome{}, nil
			})
			model = model.promptAction(pa)

			view := model.screenView()
			require.Equal(t, model.availableContentHeight(), lipgloss.Height(view), "exact-height invariant")
			require.Equal(t, model.availableWidth(), lipgloss.Width(view), "exact-width invariant")
		})
	}
}

// --- Rule 8: status line height budget + clearing ---

func TestStatusLineOccupiesExactlyOneRowWhenVisible(t *testing.T) {
	t.Parallel()

	model := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	withoutStatus := model.availableContentHeight()
	require.Equal(t, 30, lipgloss.Height(model.View()))

	model.action.status = `Enabled "SkyUI"`
	withStatus := model.availableContentHeight()
	require.Equal(t, withoutStatus-1, withStatus, "the status row must shrink the content budget by exactly one line")
	require.Equal(t, 30, lipgloss.Height(model.View()), "total view height must still hit the terminal bounds exactly")
	require.Contains(t, model.View(), `Enabled "SkyUI"`)
}

func TestNonModalNonQuitKeypressClearsStatus(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.status = "old status"

	updated, _ := model.Update(keyRunes("j"))
	require.Empty(t, updated.(Model).action.status)
}

// TestRunningActionKeypressDoesNotClearStatus covers a gap rule 8 missed:
// while an action is in flight (m.action.running == true — set for both a
// running mutation and an in-flight plan fetch like "Planning switch…", see
// planProfileSwitch in mutations.go), a navigation keypress must not wipe
// the in-flight status message. Without an in-flight indicator, the user has
// no sign that anything is happening until the action resolves.
func TestRunningActionKeypressDoesNotClearStatus(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.status = "Planning switch…"
	model.action.running = true

	updated, _ := model.Update(keyRunes("j"))
	require.Equal(t, "Planning switch…", updated.(Model).action.status,
		"an in-flight action's status must survive a navigation keypress")
	require.False(t, updated.(Model).action.statusIsError)
}

// TestActionDoneRestoresNormalStatusClearing proves the rule-8 clearing
// behavior resumes as soon as the in-flight action finishes: once
// actionDoneMsg lands (which sets m.action.running back to false), the next
// non-modal, non-quit keypress clears the status line exactly as it always
// has.
func TestActionDoneRestoresNormalStatusClearing(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.status = "Planning switch…"
	model.action.running = true

	finished, _ := model.Update(actionDoneMsg{gen: model.action.gen, outcome: ActionOutcome{Message: "Switched to vanilla-plus"}})
	finishedModel := finished.(Model)
	require.False(t, finishedModel.action.running)
	require.Equal(t, "Switched to vanilla-plus", finishedModel.action.status)

	cleared, _ := finishedModel.Update(keyRunes("j"))
	require.Empty(t, cleared.(Model).action.status,
		"status must clear normally once the action is no longer running")
}

func TestQuitKeypressDoesNotClearStatus(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.status = "old status"

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	require.Equal(t, "old status", updated.(Model).action.status)
}

// --- Rule 9: quit-time context cleanup ---

func TestQuitCancelsInFlightActionContext(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	var capturedCtx context.Context
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(ctx context.Context) (ActionOutcome, error) {
		capturedCtx = ctx
		return ActionOutcome{Message: "ok"}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.NotNil(t, cmd)

	_, quitCmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, quitCmd)
	require.Equal(t, tea.Quit(), quitCmd())

	// The runtime eventually executes the in-flight command; the context it
	// hands to the ActionProvider call must already be cancelled.
	cmd()
	require.Error(t, capturedCtx.Err())
	require.ErrorIs(t, capturedCtx.Err(), context.Canceled)
}

func TestQuitCancelsInFlightSearchContext(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model.search.cancel = cancel

	_, quitCmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, quitCmd)
	require.Equal(t, tea.Quit(), quitCmd())
	require.Error(t, ctx.Err(), "search's cancel must be invoked on quit (#42 lifecycle carry-forward)")
}

// --- Rule 10: prototype end-to-end wiring ---

func TestPrototypeEndToEndPromptConfirmDoneRefreshChangesOverview(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	require.NotNil(t, model.actions, "NewPrototypeModel must wire the ActionProvider role")

	before := requireModByID(t, model.mods, "alternate-start")
	require.Equal(t, "disabled", before.Status)

	actions := model.actions
	model, pa := model.buildAction(actionEnable, fmt.Sprintf("Enable %q?", before.Name), nil, "",
		func(ctx context.Context) (ActionOutcome, error) { return actions.EnableMod(ctx, before) })
	model = model.promptAction(pa)

	updated, confirmCmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.NotNil(t, confirmCmd)

	doneMsg := confirmCmd()
	require.IsType(t, actionDoneMsg{}, doneMsg)
	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Enabled "Alternate Start"`, model.action.status)

	loadedMsg := refreshCmd()
	require.IsType(t, dataLoadedMsg{}, loadedMsg)
	updated, _ = model.Update(loadedMsg)
	model = updated.(Model)

	after := requireModByID(t, model.mods, "alternate-start")
	require.NotEqual(t, "disabled", after.Status, "Overview must reflect the action through the SAME prototype instance")
}
