package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// runActionCmd invokes cmd - expected to be the tea.Cmd a confirmed
// pendingAction returns (buildAction's confirm: tea.Batch(actionCmd,
// waitForActionProgress(...)), see that function's doc comment) - and
// returns the actionDoneMsg/actionFailedMsg the FIRST sub-cmd (the actual
// ActionProvider call) produces. This mirrors what Bubble Tea's real
// runtime does with a tea.BatchMsg (run every sub-cmd), narrowed to just
// the action cmd for the many pre-existing tests that only care about its
// outcome, not the progress listener's (those are covered by their own
// dedicated pump tests below). The provider call's side effects
// (recordingActions' *Calls slices, a captured context, etc.) happen
// exactly once, synchronously, when this runs.
func runActionCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	require.NotNil(t, cmd, "expected a non-nil confirmed-action cmd")
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "a confirmed buildAction cmd must be tea.Batch(actionCmd, listenerCmd), got %T", msg)
	require.Len(t, batch, 2, "buildAction always batches exactly the action cmd and the progress listener cmd")
	return batch[0]()
}

// --- Rule 1: promptAction shows the modal; nothing mutates before confirm ---

func TestPromptActionShowsModalWithoutMutating(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	item := ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"}

	model, pa := model.buildAction(actionUninstall, `Uninstall "SkyUI"?`, []string{"Removes SkyUI from the active profile."}, "",
		func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
			return rec.UninstallMod(ctx, item)
		})
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
				func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
					return rec.UninstallMod(ctx, item)
				})
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

			msg := runActionCmd(t, cmd)
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
		func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
			return failing.UninstallMod(ctx, item)
		})

	genAtBuild := model.action.gen
	model = model.promptAction(pa)

	// Confirm via key, which dispatches pa.confirm() and sets running
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)

	require.Nil(t, model.action.pending, "pending must clear on confirm")
	require.True(t, model.action.running, "running must be set on confirm")
	require.NotNil(t, cmd)

	// Run the cmd to trigger the provider call and error path
	msg := runActionCmd(t, cmd)

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
				func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
					return rec.DeployProfile(ctx)
				})
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
		func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
			return rec.DeployProfile(ctx)
		})
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
		func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
			return rec.DeployProfile(ctx)
		})
	model = model.promptAction(pa)
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, cmd)
	genBefore := model.action.gen

	// A second action attempted while the first is still running must not
	// disturb the in-flight action's gen or show a modal.
	model2, pa2 := model.buildAction(actionEnable, "Enable something?", nil, "",
		func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
			return rec.EnableMod(ctx, ModItem{})
		})
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
	model, pa := model.buildAction(actionUninstall, `Uninstall "SkyUI"?`, []string{"Removes SkyUI."}, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
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
	model, pa := model.buildAction(actionUninstall, "Uninstall?", []string{long}, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
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
	model, pa := model.buildAction(actionSwitch, "Switch profile?", detail, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
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
			model, pa := model.buildAction(actionSwitch, "Switch to vanilla-plus?", detail, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
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
//
// Task 6 item d (cancel-then-drain) changed WHAT quit does while an action
// is running: 5a's quitCmd cancelled the action's context but returned
// tea.Quit immediately regardless, killing a mutation goroutine mid-step at
// process exit - unacceptable with 5b's long downloads. Quit while running
// now cancels the context (still, immediately - proven below) but DEFERS
// tea.Quit until the action's own done/failed message arrives or a bounded
// timeout elapses (see TestQuitWhileRunningDrainsUntilActionSettles and
// TestQuitWhileRunningTimesOutAndQuitsAnyway). Quit while IDLE (no action
// running) is unchanged - still immediate (see
// TestQuitCancelsInFlightSearchContext, TestPendingModalIgnoresNavigationButQuitStillQuits).

// TestQuitWhileRunningCancelsContextImmediatelyButDoesNotQuitYet guards the
// FIRST half of item d's contract: the in-flight action's context is
// cancelled the instant quit is pressed (so a well-behaved flow aborts
// between steps almost immediately - see flows.go's ctx.Err() checks) -
// but the returned cmd must NOT be tea.Quit itself; the process stays
// alive, draining, until the action's own message arrives (or the timeout
// - see the next two tests).
func TestQuitWhileRunningCancelsContextImmediatelyButDoesNotQuitYet(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	var capturedCtx context.Context
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
		capturedCtx = ctx
		return ActionOutcome{Message: "ok"}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)

	// quitCmd is deliberately never invoked here: it's the REAL
	// actionDrainTimeout tea.Tick command (see startQuit), and calling it
	// would block this test for the real ~5s duration. Its eventual
	// message (actionDrainTimeoutMsg) is exercised directly, without the
	// real timer, by TestQuitWhileRunningTimesOutAndQuitsAnyway below - the
	// model-state assertions here (draining, statusLine) are what prove
	// quit did NOT fire immediately.
	updated, quitCmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(Model)
	require.NotNil(t, quitCmd, "a running action's quit must still schedule the drain timeout")
	require.True(t, model.action.draining, "quit while running must enter the draining state")
	require.Contains(t, model.statusLine(), "Finishing current step",
		"the draining state must show 'Finishing current step…' in the status line")

	// The runtime eventually executes the in-flight command; the context it
	// hands to the ActionProvider call must already be cancelled, same as
	// pre-item-d.
	runActionCmd(t, cmd)
	require.Error(t, capturedCtx.Err())
	require.ErrorIs(t, capturedCtx.Err(), context.Canceled)
}

// TestQuitWhileRunningDrainsUntilActionSettles guards the drain sequence
// end to end: quit -> draining state -> the action's own done message
// (matching gen) arrives -> a tea.Quit cmd is finally emitted, and draining
// clears.
func TestQuitWhileRunningDrainsUntilActionSettles(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{Message: "ok"}, nil
	})
	model = model.promptAction(pa)
	updated, _ := model.Update(keyRunes("y"))
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(Model)
	require.True(t, model.action.draining)
	gen := model.action.gen

	updated, doneCmd := model.Update(actionDoneMsg{gen: gen, kind: actionDeploy, outcome: ActionOutcome{Message: "ok"}})
	model = updated.(Model)
	require.False(t, model.action.draining, "the settled action must clear the draining state")
	require.NotNil(t, doneCmd)
	require.Equal(t, tea.Quit(), doneCmd(), "the drain must resolve to tea.Quit once the action's own message arrives")
}

// TestQuitWhileRunningDrainsUntilActionFails mirrors the test above for the
// FAILURE path (actionFailedMsg) - a drain must resolve on either outcome,
// not just success.
func TestQuitWhileRunningDrainsUntilActionFails(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, errors.New("boom")
	})
	model = model.promptAction(pa)
	updated, _ := model.Update(keyRunes("y"))
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(Model)
	require.True(t, model.action.draining)
	gen := model.action.gen

	updated, failCmd := model.Update(actionFailedMsg{gen: gen, kind: actionDeploy, err: errors.New("boom")})
	model = updated.(Model)
	require.False(t, model.action.draining)
	require.NotNil(t, failCmd)
	require.Equal(t, tea.Quit(), failCmd())
}

// TestQuitWhileRunningTimesOutAndQuitsAnyway guards the bounded-timeout
// path: if the action never settles (or the runtime never gets to deliver
// its message), an actionDrainTimeoutMsg tagged with the SAME gen the
// drain started with must still resolve to tea.Quit. Fires the message
// directly (not via the real ~5s tea.Tick) - the timer primitive itself is
// bubbletea's, not this package's, to test.
func TestQuitWhileRunningTimesOutAndQuitsAnyway(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{Message: "ok"}, nil
	})
	model = model.promptAction(pa)
	updated, _ := model.Update(keyRunes("y"))
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(Model)
	require.True(t, model.action.draining)
	gen := model.action.gen

	updated, timeoutCmd := model.Update(actionDrainTimeoutMsg{gen: gen})
	model = updated.(Model)
	require.NotNil(t, timeoutCmd)
	require.Equal(t, tea.Quit(), timeoutCmd(), "a drain that never settles must still quit once the timeout elapses")
}

// TestStaleActionDrainTimeoutIsNoop guards against a timeout from an
// already-resolved drain (or a stale gen) forcing a spurious quit.
func TestStaleActionDrainTimeoutIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 3
	model.action.draining = false // already resolved

	updated, cmd := model.Update(actionDrainTimeoutMsg{gen: 3})
	m := updated.(Model)
	require.Nil(t, cmd, "a timeout for an already-resolved drain must be a no-op")
	require.False(t, m.action.draining)

	model.action.draining = true
	updated, cmd = model.Update(actionDrainTimeoutMsg{gen: 2}) // stale gen
	m = updated.(Model)
	require.Nil(t, cmd, "a timeout tagged with a stale gen must be a no-op")
	require.True(t, m.action.draining, "must not clear draining for a DIFFERENT (stale) drain")
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

// TestIdleQuitStaysImmediateEvenWithPriorDrainFields guards "idle quit
// stays immediate" explicitly at the Model level (TestQuitCancelsInFlightSearchContext
// above already covers the ordinary case; this pins it even when the
// action struct carries a non-zero gen from an EARLIER, already-settled
// action - a stale gen/cancel must never make an idle quit drain).
func TestIdleQuitStaysImmediateEvenWithPriorDrainFields(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 7
	model.action.running = false

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m := updated.(Model)
	require.NotNil(t, cmd)
	require.Equal(t, tea.Quit(), cmd(), "idle quit must fire immediately, unchanged by item d")
	require.False(t, m.action.draining)
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
		func(ctx context.Context, _ func(ActionProgress)) (ActionOutcome, error) {
			return actions.EnableMod(ctx, before)
		})
	model = model.promptAction(pa)

	updated, confirmCmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.NotNil(t, confirmCmd)

	doneMsg := runActionCmd(t, confirmCmd)
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

// --- Rule 11: streaming progress pump (Phase 5b Task 4) ---

// TestActionProgressPumpNeverBlocksFlowAndCoalescesForSlowConsumer pins the
// pump's two correctness requirements together: sendActionProgress must
// never block its caller (the flow goroutine running inside a provider's
// Apply* method) regardless of whether anything is reading yet, and a
// consumer that only starts draining AFTER a burst of sends must still
// observe the freshest tick - not an early one stranded by a naive
// buffered-and-drop implementation. Both properties are proven with hard
// time bounds so a regression (e.g. a blocking send) fails fast instead of
// hanging the test suite.
func TestActionProgressPumpNeverBlocksFlowAndCoalescesForSlowConsumer(t *testing.T) {
	t.Parallel()

	ch := make(chan ActionProgress, 1)
	const n = 500
	sent := make(chan struct{})

	start := time.Now()
	go func() {
		defer close(sent)
		for i := 0; i < n; i++ {
			sendActionProgress(ch, ActionProgress{Line: fmt.Sprintf("tick %d", i), Percent: float64(i)})
		}
	}()

	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("sendActionProgress must never block the flow goroutine, even with no consumer draining yet")
	}
	require.Less(t, time.Since(start), time.Second,
		"the whole send burst must complete quickly regardless of the (absent) consumer")

	// A late, slow consumer must still observe the freshest coalesced tick -
	// not get stuck with an early one from before it started reading.
	var last ActionProgress
	var sawAny bool
drain:
	for {
		select {
		case p, ok := <-ch:
			if !ok {
				break drain
			}
			last, sawAny = p, true
			time.Sleep(time.Millisecond) // deliberately slow
		case <-time.After(50 * time.Millisecond):
			break drain
		}
	}
	require.True(t, sawAny, "the slow consumer must observe at least the coalesced final tick")
	require.Equal(t, "tick 499", last.Line,
		"a slow/late consumer must observe the freshest coalesced tick, not a stale early one dropped-and-kept by a naive buffer")
}

// TestActionProgressMsgStaleGenIsDiscardedAndDoesNotReissue mirrors rule 4's
// staleness contract (actionDoneMsg/actionFailedMsg) for progress ticks: a
// tick tagged with a superseded gen must not update the displayed progress
// and must not re-issue the listener.
func TestActionProgressMsgStaleGenIsDiscardedAndDoesNotReissue(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 5
	model.action.running = true

	updated, cmd := model.Update(actionProgressMsg{gen: 4, progress: ActionProgress{Line: "stale", Percent: 10}})
	m := updated.(Model)
	require.Nil(t, cmd, "a stale progress tick must not re-issue the listener")
	require.Empty(t, m.action.progress.Line, "a stale progress tick must not update the displayed progress")
}

// TestActionProgressMsgFreshGenUpdatesProgressAndReissuesListener proves a
// fresh (matching-gen) tick updates m.action.progress and returns a
// listener cmd bound to the SAME channel, so the next tick (or eventual
// close) is still observed.
func TestActionProgressMsgFreshGenUpdatesProgressAndReissuesListener(t *testing.T) {
	t.Parallel()

	ch := make(chan ActionProgress, 1)
	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 7
	model.action.running = true
	model.action.progressCh = ch

	updated, cmd := model.Update(actionProgressMsg{gen: 7, progress: ActionProgress{Line: "fresh 10%", Percent: 10}})
	m := updated.(Model)
	require.Equal(t, "fresh 10%", m.action.progress.Line)
	require.NotNil(t, cmd, "a fresh tick must re-issue the listener")

	sendActionProgress(ch, ActionProgress{Line: "fresh 20%", Percent: 20})
	msg := cmd()
	require.Equal(t, actionProgressMsg{gen: 7, progress: ActionProgress{Line: "fresh 20%", Percent: 20}}, msg,
		"the re-issued listener must read from the SAME channel")
}

// TestActionProgressStreamsWhileRunningThenActionDoneClearsIt drives the
// FULL pump pipeline through Model.Update - buildAction's confirm returns
// tea.Batch(actionCmd, listenerCmd); running each sub-cmd and feeding its
// message back through Update is exactly what Bubble Tea's real runtime
// does with a tea.BatchMsg. Proves: the status line shows the progress tick
// while running (rule 8's extension), the listener re-issues on a fresh
// tick, a closed channel is terminal (no further re-issue - the pump's
// "never block, always eventually terminate" contract), and
// actionDoneMsg clears the progress line and restores the outcome text
// (rule 8's clearing contract, unweakened).
func TestActionProgressStreamsWhileRunningThenActionDoneClearsIt(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model, pa := model.buildAction(actionEnable, "Enable?", nil, "", func(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
		progress(ActionProgress{Line: "Installing SkyUI: 42%", Percent: 42})
		return ActionOutcome{Message: `Enabled "SkyUI"`}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, cmd)

	batchMsg := cmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	require.True(t, ok, "confirm must return tea.Batch(actionCmd, listenerCmd)")
	require.Len(t, batch, 2)

	actionMsg := batch[0]()
	require.IsType(t, actionDoneMsg{}, actionMsg)

	// The action cmd already ran do() to completion (sending its one tick
	// and closing the channel) before actionMsg was produced above, so the
	// listener's first receive gets that buffered tick even though the
	// channel is already closed (Go delivers buffered values before
	// signaling closed).
	progressMsg := batch[1]()
	require.IsType(t, actionProgressMsg{}, progressMsg)

	updated, reissue := model.Update(progressMsg)
	model = updated.(Model)
	require.Equal(t, "Installing SkyUI: 42%", model.action.progress.Line)
	require.Contains(t, model.statusLine(), "Installing SkyUI: 42%")
	require.NotNil(t, reissue, "a fresh tick must re-issue the listener")

	// The re-issued listener now hits the closed, drained channel -
	// terminal, no message, so nothing re-issues it again.
	require.Nil(t, reissue(), "a closed channel is terminal: no further re-issue")

	updated, _ = model.Update(actionMsg)
	model = updated.(Model)
	require.False(t, model.action.running)
	require.Equal(t, `Enabled "SkyUI"`, model.action.status)
	require.Empty(t, model.action.progress.Line, "actionDoneMsg must clear the progress line")
	require.NotContains(t, model.statusLine(), "42%")
}

// TestProgressLineOccupiesExactlyOneRowWhileRunning extends rule 8's height-
// budget test (TestStatusLineOccupiesExactlyOneRowWhenVisible) to the
// in-flight progress line: it must reserve exactly one row too, and the
// view must still hit the terminal's exact height.
func TestProgressLineOccupiesExactlyOneRowWhileRunning(t *testing.T) {
	t.Parallel()

	model := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	withoutStatus := model.availableContentHeight()

	model.action.running = true
	model.action.progress = ActionProgress{Line: "Installing SkyUI: 42%", Percent: 42}
	withProgress := model.availableContentHeight()
	require.Equal(t, withoutStatus-1, withProgress,
		"an in-flight progress line must shrink the content budget by exactly one line, matching rule 8's status-line accounting")
	require.Equal(t, 30, lipgloss.Height(model.View()))
	require.Contains(t, model.View(), "Installing SkyUI: 42%")
}

// TestProgressLineOnlyShownWhileRunning proves the priority rule explicitly:
// a leftover progress value that ISN'T running (e.g. after actionDoneMsg
// forgot to clear it - guarded structurally here rather than relying only
// on the clearing test above) must never be shown; the stored status text
// takes over instead.
func TestProgressLineOnlyShownWhileRunning(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.running = false
	model.action.progress = ActionProgress{Line: "stray progress", Percent: 10}
	model.action.status = `Enabled "SkyUI"`

	require.Equal(t, `Enabled "SkyUI"`, model.action.status)
	require.NotContains(t, model.statusLine(), "stray progress")
	require.Contains(t, model.statusLine(), `Enabled "SkyUI"`)
}
