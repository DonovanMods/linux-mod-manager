package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// Task 7: purge behind confirmation ('X' on Dashboard/Installed Mods) -
// mutations.go's purgeProfilePrompt, wired onto ActionProvider.PurgeProfile.
// Mirrors deployActiveProfile's own test shape (mutations_test.go's "---
// Deploy ('D' on Dashboard and Installed Mods) ---" section): same two
// screens, no row selection required, ordinary confirm-modal flow (not the
// deferred plan-fetch pattern install/switch/updates use).

func TestPurgeKeyPromptsWithModCountAndNames(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	require.Len(t, model.mods, 5, "sanity: the prototype fixture's canned InstalledMods count")

	updated, cmd := model.Update(keyRunes("X"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionPurge, model.action.pending.kind)
	require.Equal(t, `Purge 5 mod(s) from Skyrim Special Edition?`, model.action.pending.title)
	for _, mod := range model.mods {
		require.Contains(t, model.action.pending.detail, mod.Name)
	}
	require.Zero(t, rec.PurgeCalls, "nothing must mutate before confirm")
}

func TestPurgeKeyNoModsShortCircuitsToStatus(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.mods = nil

	updated, cmd := model.Update(keyRunes("X"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending, "an empty mods list must never open a confirmation modal")
	require.Equal(t, "no mods installed", model.action.status)
	require.False(t, model.action.statusIsError, "nothing to purge is a benign outcome, not a refusal")
	require.Zero(t, rec.PurgeCalls)
}

// TestPurgeConfirmStreamsProgressAndReportsOutcome drives the full pump
// pipeline (mirroring TestInstallProgressStreamsIntoStatusLine/
// TestActionProgressStreamsWhileRunningThenActionDoneClearsIt in
// mutations_test.go/actions_test.go): confirming replays the provider's
// progress ticks through the SAME single-slot channel every other streaming
// action uses (sendActionProgress's documented "last value wins"
// coalescing - see actions.go), so only the LAST of the two configured
// ticks survives to the listener by the time it's read; the completed
// outcome's two warnings then drive formatOutcomeStatus's "(N warnings)"
// count-suffix branch, and the done message triggers the standard refresh.
func TestPurgeConfirmStreamsProgressAndReportsOutcome(t *testing.T) {
	t.Parallel()

	outcome := ActionOutcome{
		Message: "Purged 2 mod(s)",
		Warnings: []string{
			"Skipped USSEP: uninstall.before_each hook failed: boom",
			"uninstall.after_each hook failed for SkyUI: boom",
		},
	}
	rec := &recordingActions{
		PurgeTicks: []ActionProgress{
			{Line: "purging 2 mod(s)…", Percent: -1},
			{Line: "✓ SkyUI (1/2)", Percent: -1},
		},
		PurgeOutcome: outcome,
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods

	updated, cmd := model.Update(keyRunes("X"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)

	batchMsg := confirmCmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	require.True(t, ok, "confirm must return tea.Batch(actionCmd, listenerCmd)")
	require.Len(t, batch, 2)

	actionMsg := batch[0]()
	require.IsType(t, actionDoneMsg{}, actionMsg)
	require.Equal(t, 1, rec.PurgeCalls)

	progressMsg := batch[1]()
	updated, _ = model.Update(progressMsg)
	model = updated.(Model)
	require.Contains(t, model.statusLine(), "✓ SkyUI (1/2)",
		"the pump must observe the freshest tick the provider reported")

	updated, refreshCmd := model.Update(actionMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd, "a completed purge must trigger the standard refresh")
	require.Equal(t, formatOutcomeStatus(outcome), model.action.status)
	require.Contains(t, model.action.status, "(2 warnings)", "the final status must surface the warning count")
	require.False(t, model.action.statusIsError)
	require.IsType(t, dataLoadedMsg{}, refreshCmd())
}

func TestPurgeKeyWorksFromDashboardToo(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("X"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionPurge, model.action.pending.kind)
}

// TestPurgeKeySwallowedByFocusedSearchInput proves 'X' types into the search
// box instead of triggering purge while ScreenSearch is focused - mirrors
// TestToggleEnableKeyInertWhileSearchFocused's identical shape (the
// focused-input branch in updateKey runs before the mutation-key switch, so
// this is inertness by construction, not a special case this handler needs
// to implement itself).
func TestPurgeKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "X")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "X")
	require.Nil(t, updated.action.pending)
}

// --- Extra coverage mirroring Deploy's own sibling tests ---

func TestPurgeKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenSearch, ScreenProfiles, ScreenSources} {
		model := modelWithActions(t, &recordingActions{})
		model.screen = screen

		updated, cmd := model.Update(keyRunes("X"))
		model = updated.(Model)
		require.Nil(t, cmd)
		require.Nil(t, model.action.pending, "screen %v", screen)
	}
}

func TestPurgeKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.action.running = true

	updated, cmd := model.Update(keyRunes("X"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Zero(t, rec.PurgeCalls)
}
