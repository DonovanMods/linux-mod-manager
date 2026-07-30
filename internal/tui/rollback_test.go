package tui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Task 6: rollback behind confirmation ('<' on Installed Mods) -
// mutations.go's rollbackSelectedMod, wired onto ActionProvider.Rollback.
// Mirrors uninstallSelectedMod/purgeProfilePrompt's own test shape
// (mutations_test.go/purge_test.go): guard/selection checks, then the
// ordinary confirm-modal flow (not the deferred plan-fetch pattern install/
// switch/updates use) - Rollback needs no async plan fetch since
// ModItem.PreviousVersion is already known synchronously from the loaded
// mods list.

// rollbackReadyModIndex/noPreviousVersionModIndex pin the prototype
// fixture's canned InstalledMods rows this file's tests rely on (see
// prototype/data.go's Load): "skse-address-library" is the one canned mod
// with a non-empty PreviousVersion (added Task 6, for exactly this demo);
// "skyui" has none (it only carries an AvailableVersion - a pending update,
// not something already rolled back FROM).
const (
	rollbackReadyModIndex     = 2 // "skse-address-library", Version "11", PreviousVersion "10"
	noPreviousVersionModIndex = 0 // "skyui", Version "5.2", PreviousVersion ""
)

func TestRollbackKeyOpensConfirmModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = rollbackReadyModIndex
	item := model.mods[rollbackReadyModIndex]
	require.Equal(t, "skse-address-library", item.ID, "sanity: the prototype fixture's canned rollback-ready mod")
	require.Equal(t, "11", item.Version)
	require.Equal(t, "10", item.PreviousVersion)

	updated, cmd := model.Update(keyRunes("<"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionRollback, model.action.pending.kind)
	require.Equal(t, `Roll back "SKSE Address Library" v11 → v10?`, model.action.pending.title)
	// Pins the caveat line's exact shape: it must match sibling modals
	// (uninstall/deploy stop after the hook-mention sentence) and stop
	// there - no trailing "may leave a mix of both versions applied"
	// sentence, which both broke tone parity with siblings and truncated
	// on screen at 80 cols. require.Contains on a []string checks for an
	// exact matching element, so this also proves no extra sentence was
	// appended to it.
	require.Contains(t, model.action.pending.detail, "Replaces deployed files with the previous version; rollback hooks will run.")
	require.Empty(t, rec.RollbackCalls, "nothing must mutate before confirm")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.Nil(t, model.action.pending)
	require.NotNil(t, confirmCmd)

	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.RollbackCalls, 1)
	require.Equal(t, "skse-address-library", rec.RollbackCalls[0].ID)
	require.Equal(t, "nexusmods", rec.RollbackCalls[0].Source)
}

// TestRollbackKeyNoPreviousVersion proves a mod with no PreviousVersion is
// refused synchronously, on the status line, with no modal - mirroring
// purgeProfilePrompt's own "nothing to do" benign-outcome shape (statusIsError
// false), not deleteSelectedProfile's active-profile refusal (an actual
// error, statusIsError true).
func TestRollbackKeyNoPreviousVersion(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = noPreviousVersionModIndex
	item := model.mods[noPreviousVersionModIndex]
	require.Equal(t, "skyui", item.ID, "sanity: the prototype fixture's canned no-previous-version mod")
	require.Empty(t, item.PreviousVersion)

	updated, cmd := model.Update(keyRunes("<"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending, "a mod with no previous version must never open a confirmation modal")
	require.Equal(t, "no previous version to roll back to", model.action.status)
	require.False(t, model.action.statusIsError, "nothing to roll back to is a benign outcome, not a refusal")
	require.Empty(t, rec.RollbackCalls)
}

// TestRollbackKeyLockedRefusesBeforeModal (#143 polish): a locked mod's
// rollback is refused synchronously, on the status line, BEFORE the confirm
// modal ever opens - item.Locked is already in hand, so making the user
// confirm an action the core gate will then refuse is a pointless round
// trip. Unlike the no-previous-version case (benign "nothing to do"),
// this mirrors deleteSelectedProfile's active-profile refusal shape: the
// row IS otherwise actionable, so statusIsError is true, and the message
// points at the TUI's own L key rather than any CLI command.
func TestRollbackKeyLockedRefusesBeforeModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = rollbackReadyModIndex
	model.mods[rollbackReadyModIndex].Locked = true
	model.mods[rollbackReadyModIndex].LockedVersion = "11"

	updated, cmd := model.Update(keyRunes("<"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending, "a locked mod must never open the rollback confirm modal")
	require.Equal(t, "SKSE Address Library is locked at v11 — unlock or move the lock (L) to roll back", model.action.status)
	require.True(t, model.action.statusIsError, "refusing an otherwise-actionable row is a refusal, not a benign no-op")
	require.Empty(t, rec.RollbackCalls)
}

// TestRollbackConfirmAppliesAndRefreshes proves confirming calls
// ActionProvider.Rollback with the selected item, and that the standard
// post-action refresh follows BOTH a successful and a failed outcome -
// buildAction/promptAction's machinery (actions.go), not something this
// handler has to implement itself (see actions_provider.go's own doc comment
// on "refresh data after every action, success or failure").
func TestRollbackConfirmAppliesAndRefreshes(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		rec := &recordingActions{RollbackOutcome: ActionOutcome{Message: `Rolled back "SKSE Address Library" to 10`}}
		model := modelWithActions(t, rec)
		model.screen = ScreenInstalledMods
		model.selected[ScreenInstalledMods] = rollbackReadyModIndex

		updated, _ := model.Update(keyRunes("<"))
		model = updated.(Model)
		require.NotNil(t, model.action.pending)

		confirmed, confirmCmd := model.Update(keyRunes("y"))
		model = confirmed.(Model)
		doneMsg := runActionCmd(t, confirmCmd)
		require.IsType(t, actionDoneMsg{}, doneMsg)
		require.Len(t, rec.RollbackCalls, 1)
		require.Equal(t, "skse-address-library", rec.RollbackCalls[0].ID)

		updated, refreshCmd := model.Update(doneMsg)
		model = updated.(Model)
		require.NotNil(t, refreshCmd, "a completed rollback must trigger the standard refresh")
		require.Equal(t, `Rolled back "SKSE Address Library" to 10`, model.action.status)
		require.False(t, model.action.statusIsError)
		require.IsType(t, dataLoadedMsg{}, refreshCmd())
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()

		rec := &recordingActions{RollbackErr: errors.New("boom")}
		model := modelWithActions(t, rec)
		model.screen = ScreenInstalledMods
		model.selected[ScreenInstalledMods] = rollbackReadyModIndex

		updated, _ := model.Update(keyRunes("<"))
		model = updated.(Model)
		require.NotNil(t, model.action.pending)

		confirmed, confirmCmd := model.Update(keyRunes("y"))
		model = confirmed.(Model)
		failMsg := runActionCmd(t, confirmCmd)
		require.IsType(t, actionFailedMsg{}, failMsg)
		require.Len(t, rec.RollbackCalls, 1)

		updated, refreshCmd := model.Update(failMsg)
		model = updated.(Model)
		require.NotNil(t, refreshCmd, "a failed rollback must ALSO trigger the standard refresh - the partial-mutation contract")
		require.Equal(t, "boom", model.action.status)
		require.True(t, model.action.statusIsError)
		require.IsType(t, dataLoadedMsg{}, refreshCmd())
	})
}

// TestRollbackKeySwallowedByFocusedSearchInput proves '<' types into the
// search box instead of triggering rollback while ScreenSearch is focused -
// mirrors TestPurgeKeySwallowedByFocusedSearchInput's identical shape (the
// focused-input branch in updateKey runs before the mutation-key switch, so
// this is inertness by construction, not a special case this handler needs
// to implement itself).
func TestRollbackKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "<")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "<")
	require.Nil(t, updated.action.pending)
}

// --- Extra coverage mirroring Purge/Uninstall's own sibling tests ---

func TestRollbackKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenSearch, ScreenProfiles, ScreenSources} {
		model := modelWithActions(t, &recordingActions{})
		model.screen = screen

		updated, cmd := model.Update(keyRunes("<"))
		model = updated.(Model)
		require.Nil(t, cmd)
		require.Nil(t, model.action.pending, "screen %v", screen)
	}
}

func TestRollbackKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenInstalledMods
	model.mods = nil

	updated, cmd := model.Update(keyRunes("<"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestRollbackKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = rollbackReadyModIndex
	model.action.running = true

	updated, cmd := model.Update(keyRunes("<"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.RollbackCalls)
}
