package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// capturingActions wraps recordingActions to capture the context
// ActionProvider.AvailableVersions was called with, so
// TestLockKeyQuitWhileFetchingCancelsContext can assert it was cancelled -
// mirrors TestQuitWhileRunningCancelsContextImmediatelyButDoesNotQuitYet's
// own capturedCtx closure (actions_test.go), which that test can build
// inline since buildAction's do parameter is caller-supplied; the lock
// fetch's ActionProvider call has no such caller-supplied hook, so a fake
// implementation is used instead.
type capturingActions struct {
	recordingActions
	ctx context.Context
}

func (c *capturingActions) AvailableVersions(ctx context.Context, item ModItem) ([]string, error) {
	c.ctx = ctx
	return c.recordingActions.AvailableVersions(ctx, item)
}

// --- Lock/unlock version picker ('L' on Installed Mods, #97) ---
//
// Mirrors policy_test.go's coverage shape (see that file's own comment
// block): the async fetch (checkForUpdates' three-step pattern) is exercised
// end to end - key press dispatches a Cmd, the Cmd resolves to a
// versionsFetchedMsg/versionsFetchFailedMsg tagged with a gen guard, and the
// picker it builds dispatches lockChosenMsg/unlockChosenMsg through Update()
// rather than mutating the Model from inside the picker's own choose
// closure (task-7-brief.md).

// TestLockKeyWrongScreenIsNoop proves 'L' only fires on Installed Mods.
func TestLockKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.Empty(t, rec.AvailableVersionsCalls)
}

// TestLockKeyEmptyListIsNoop proves an empty mods list can never crash or
// dispatch a fetch for a nonexistent selection.
func TestLockKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.mods = nil
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.Empty(t, rec.AvailableVersionsCalls)
}

// TestLockKeyNoActionProviderIsNoop proves 'L' is inert with no
// ActionProvider configured, mirroring TestPolicyKeyNoActionProviderIsNoop.
func TestLockKeyNoActionProviderIsNoop(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider()})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
}

// TestLockKeyInertWhileRunning proves 'L' is a no-op while another
// action/fetch is already in flight - the single-flight guard shared by
// every other async-fetch key (checkForUpdates/switchSelectedProfile).
func TestLockKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.action.running = true

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.Empty(t, rec.AvailableVersionsCalls)
}

// TestLockKeyInertWhileAnotherModalPending proves a DIFFERENT already-pending
// confirmation modal is left completely undisturbed by 'L', mirroring
// TestPolicyKeyInertWhileAnotherModalPending.
func TestLockKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("D")) // opens the Deploy modal
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending, "the original modal must still be showing")
	require.Equal(t, actionDeploy, model.action.pending.kind)
	require.Nil(t, model.picker, "L must not open a picker behind the confirm modal")
}

// TestLockKeyDispatchesAsyncFetch proves 'L' on a selected mod shows a
// "Fetching versions for <name>…" status and returns a command instead of
// synchronously calling the provider - mirroring
// TestSwitchKeyDispatchesAsyncPlanFetch/TestCheckUpdatesKeyDispatchesAsyncFetchFromInstalledMods.
func TestLockKeyDispatchesAsyncFetch(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"1.2", "1.1", "1.0"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI"

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Fetching versions for SkyUI…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Nil(t, model.picker)
	require.Empty(t, rec.AvailableVersionsCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := cmd()
	require.IsType(t, versionsFetchedMsg{}, msg)
	require.Len(t, rec.AvailableVersionsCalls, 1)
	require.Equal(t, "skyui", rec.AvailableVersionsCalls[0].ID)
}

// TestLockFetchStaleResultDiscarded proves a versionsFetchedMsg tagged with
// an old gen is discarded entirely - no picker, no status change, no running
// flip - mirroring TestSwitchPlanStaleResultDiscarded.
func TestLockFetchStaleResultDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true
	model.action.status = ""

	updated, cmd := model.Update(versionsFetchedMsg{gen: 4, item: model.mods[0], versions: []string{"1.0"}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running, "stale result must not clear running")
	require.Nil(t, m.picker, "stale result must never open a picker")
	require.Empty(t, m.action.status)
}

// TestLockFetchStaleFailureDiscarded mirrors TestLockFetchStaleResultDiscarded
// for the failure path.
func TestLockFetchStaleFailureDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true

	updated, cmd := model.Update(versionsFetchFailedMsg{gen: 1, err: errors.New("boom")})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running)
	require.Empty(t, m.action.status)
}

// TestLockFetchFailureShowsStatusNoPicker covers the fetch-error path end to
// end: 'L' -> async fetch -> versionsFetchFailedMsg -> error status, no
// picker - mirroring TestSwitchPlanErrorShowsStatusNoModal.
func TestLockFetchFailureShowsStatusNoPicker(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsErr: errors.New("version fetch boom")}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, versionsFetchFailedMsg{}, msg)

	updated, refreshCmd := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, refreshCmd, "a fetch failure - unlike a mutation failure - has nothing to refresh")
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "version fetch boom")
	require.Nil(t, model.picker)
}

// TestLockFetchErrorNamesPinningForNotSupported proves a source's
// version-listing failure (coreProvider.AvailableVersions already maps
// source.ErrNotSupported through mapNetworkError naming "pin it instead (P)"
// - service_core.go) reaches the status line unchanged, so the TUI dispatch
// layer doesn't lose or reword that fallback guidance.
func TestLockFetchErrorNamesPinningForNotSupported(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsErr: errors.New("listing versions for SkyUI: nexusmods does not support version resolution — pin it instead (P)")}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "pin it instead (P)")
}

// TestLockFetchEmptyVersionsUnlockedShowsStatusNoPicker covers PR #142
// Copilot round-2's low-confidence finding (independently confirming an
// item our own final whole-branch review had deferred):
// ActionProvider.AvailableVersions is permitted by the interface to return
// an empty, error-free slice (neither shipped provider does this today -
// coreProvider maps a versionless source to source.ErrNotSupported instead,
// and prototypeProvider's canned list is always non-empty - but nothing in
// the interface forbids it). For an UNLOCKED mod, an empty versions slice
// has no trailing unlock row either, so the picker resolveVersionsFetched
// used to build would have zero options - choosable but empty, and
// choosing (Enter/a digit) would index past the end of both the options and
// (pre-fix) the parallel versions slice. resolveVersionsFetched must refuse
// to open a picker at all in this case, surfacing an error status instead -
// worded to match mapNetworkError's own capability-gap phrasing ("...; pin
// it instead (P)") so the two read as one voice.
func TestLockFetchEmptyVersionsUnlockedShowsStatusNoPicker(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI", unlocked
	require.False(t, model.mods[0].Locked)

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	require.IsType(t, versionsFetchedMsg{}, msg)

	updated, refreshCmd := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, refreshCmd, "an empty-versions refusal - like a fetch failure - has nothing to refresh")
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "no versions reported for SkyUI")
	require.Contains(t, model.action.status, "pin it instead (P)")
	require.Nil(t, model.picker, "an unlocked mod with zero versions must not open a choosable-empty picker")
}

// TestLockFetchEmptyVersionsLockedOpensUnlockOnlyPicker is
// TestLockFetchEmptyVersionsUnlockedShowsStatusNoPicker's control: a LOCKED
// mod with an empty versions slice still yields a VALID picker - the
// trailing unlock row alone is a real, choosable option - so
// resolveVersionsFetched must let it through rather than treating "zero
// versions" as unconditionally an error.
func TestLockFetchEmptyVersionsLockedOpensUnlockOnlyPicker(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: nil}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.mods[0].Locked = true
	model.mods[0].LockedVersion = "5.1"

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.False(t, model.action.statusIsError)
	require.NotNil(t, model.picker)
	require.Len(t, model.picker.options, 1, "zero versions plus the trailing unlock option")
	require.Equal(t, "unlock", model.picker.options[0].Label)
	require.Equal(t, 0, model.picker.selected)

	updated, chooseCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, model.picker, "choosing must clear the picker")
	require.NotNil(t, chooseCmd)
	chosenMsg := chooseCmd()
	require.IsType(t, unlockChosenMsg{}, chosenMsg)
	picked := chosenMsg.(unlockChosenMsg)
	require.Equal(t, "skyui", picked.item.ID)
}

// TestLockPickerRowsNotesAndPreselectionWhenUnlocked covers an UNLOCKED mod:
// the installed version is noted "installed" and pre-selected, no other row
// carries a note, and no trailing "unlock" option is appended.
func TestLockPickerRowsNotesAndPreselectionWhenUnlocked(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	require.Equal(t, "5.2", model.mods[0].Version)
	require.False(t, model.mods[0].Locked)

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.picker)
	require.Len(t, model.picker.options, 3, "no trailing unlock option for an unlocked mod")
	require.Equal(t, "5.3", model.picker.options[0].Label)
	require.Empty(t, model.picker.options[0].Note)
	require.Equal(t, "5.2", model.picker.options[1].Label)
	require.Equal(t, "installed", model.picker.options[1].Note)
	require.Equal(t, "5.1", model.picker.options[2].Label)
	require.Empty(t, model.picker.options[2].Note)
	require.Equal(t, 1, model.picker.selected, "the installed version must start pre-selected")
}

// TestLockPickerRowsNotesAndPreselectionWhenLocked covers a LOCKED mod whose
// lock target differs from the installed version: the locked target is noted
// "locked" and pre-selected, the installed version is separately noted
// "installed", and a trailing "unlock" option is appended.
func TestLockPickerRowsNotesAndPreselectionWhenLocked(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.mods[0].Locked = true
	model.mods[0].LockedVersion = "5.1"
	require.Equal(t, "5.2", model.mods[0].Version)

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.picker)
	require.Len(t, model.picker.options, 4, "3 versions plus a trailing unlock option")
	require.Equal(t, "installed", model.picker.options[1].Note)
	require.Equal(t, "locked", model.picker.options[2].Note)
	require.Equal(t, 2, model.picker.selected, "the locked target must start pre-selected")
	require.Equal(t, "unlock", model.picker.options[3].Label)
	require.Empty(t, model.picker.options[3].Note)
}

// TestLockPickerRowNotesCombineWhenLockedAtInstalledVersion covers the case
// both notes land on the SAME row (task-7-brief.md: "both notes may land on
// the same row"): a mod locked at exactly its installed version.
func TestLockPickerRowNotesCombineWhenLockedAtInstalledVersion(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.mods[0].Locked = true
	model.mods[0].LockedVersion = "5.2" // same as the installed Version
	require.Equal(t, "5.2", model.mods[0].Version)

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.picker)
	require.Equal(t, "installed, locked", model.picker.options[1].Note)
	require.Equal(t, 1, model.picker.selected)
}

// TestLockPickerChooseVersionDispatchesSetLockAtDifferentVersion drives the
// full round trip for locking at a DIFFERENT version than what's installed:
// choosing a version immediately dispatches SetLock - no second confirm gate
// - and the outcome status names the convergence caveat.
func TestLockPickerChooseVersionDispatchesSetLockAtDifferentVersion(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI", ID "skyui", installed "5.2"

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.picker)

	// Digit quick-select "1" chooses index 0 ("5.3").
	updated, chooseCmd := model.Update(keyRunes("1"))
	model = updated.(Model)
	require.Nil(t, model.picker, "choosing must clear the picker")
	require.NotNil(t, chooseCmd)
	require.Empty(t, rec.SetLockCalls, "nothing must mutate before the deferred cmd runs")

	chosenMsg := chooseCmd()
	require.IsType(t, lockChosenMsg{}, chosenMsg)
	picked := chosenMsg.(lockChosenMsg)
	require.Equal(t, "5.3", picked.version)
	require.Equal(t, "skyui", picked.item.ID)

	updated, actionCmd := model.Update(chosenMsg)
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, actionCmd)

	doneMsg := runActionCmd(t, actionCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.SetLockCalls, 1)
	require.Equal(t, "skyui", rec.SetLockCalls[0].ModID)
	require.Equal(t, "5.3", rec.SetLockCalls[0].Version)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Contains(t, model.action.status, "SkyUI locked at v5.3")
	require.Contains(t, model.action.status, "apply the profile to converge")
	require.False(t, model.action.statusIsError)
}

// TestLockPickerChooseInstalledVersionOmitsConvergeCaveat covers the other
// half: locking at the ALREADY-installed version needs no convergence
// caveat, since nothing has to change on next deploy.
func TestLockPickerChooseInstalledVersionOmitsConvergeCaveat(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // installed "5.2"

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.Equal(t, 1, model.picker.selected, "the installed version (index 1, \"5.2\") starts pre-selected")

	updated, chooseCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	chosenMsg := chooseCmd()

	updated, actionCmd := model.Update(chosenMsg)
	model = updated.(Model)
	doneMsg := runActionCmd(t, actionCmd)

	updated, _ = model.Update(doneMsg)
	model = updated.(Model)
	require.Equal(t, `SkyUI locked at v5.2`, model.action.status)
	require.NotContains(t, model.action.status, "converge")
}

// TestLockPickerChooseUnlockDispatchesUnlock drives the full round trip for
// the trailing "unlock" option on a locked mod.
func TestLockPickerChooseUnlockDispatchesUnlock(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.mods[0].Locked = true
	model.mods[0].LockedVersion = "5.1"

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.Len(t, model.picker.options, 4)

	// Digit quick-select "4" chooses index 3, the trailing "unlock" row.
	updated, chooseCmd := model.Update(keyRunes("4"))
	model = updated.(Model)
	require.Nil(t, model.picker)
	chosenMsg := chooseCmd()
	require.IsType(t, unlockChosenMsg{}, chosenMsg)
	picked := chosenMsg.(unlockChosenMsg)
	require.Equal(t, "skyui", picked.item.ID)

	updated, actionCmd := model.Update(chosenMsg)
	model = updated.(Model)
	require.True(t, model.action.running)

	doneMsg := runActionCmd(t, actionCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.UnlockCalls, 1)
	require.Equal(t, "skyui", rec.UnlockCalls[0].ID)
	require.Empty(t, rec.SetLockCalls, "unlock must never also call SetLock")

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `SkyUI unlocked`, model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestLockChoiceMapsProviderErrorToActionFailedMsg proves an error from
// SetLock surfaces as an actionFailedMsg through the same immediate-dispatch
// path, mirroring TestPolicyChoiceMapsProviderErrorToActionFailedMsg.
func TestLockChoiceMapsProviderErrorToActionFailedMsg(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lock write failed")
	rec := &recordingActions{AvailableVersionsOut: []string{"5.2"}, SetLockErr: sentinel}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	updated, chooseCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	chosenMsg := chooseCmd()

	updated, actionCmd := model.Update(chosenMsg)
	model = updated.(Model)
	require.True(t, model.action.running)

	doneMsg := runActionCmd(t, actionCmd)
	require.IsType(t, actionFailedMsg{}, doneMsg)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, sentinel.Error())
	require.NotNil(t, refreshCmd)
}

// TestLockChoiceSecondPickInFlightIsDropped guards the in-flight-message
// window between a pick and its resolution, mirroring
// TestPolicyChoiceSecondPickInFlightIsDropped: exactly ONE SetLock call must
// result from two back-to-back picks landing before the first resolves.
func TestLockChoiceSecondPickInFlightIsDropped(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{AvailableVersionsOut: []string{"5.3", "5.2", "5.1"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	fetchMsg := cmd()
	updated, _ = model.Update(fetchMsg)
	model = updated.(Model)

	updated, chooseCmd1 := model.Update(keyRunes("1"))
	model = updated.(Model)
	msg1 := chooseCmd1()

	// Second pick in the window: running is still false, so 'L' re-dispatches
	// a fresh fetch, and its result (arriving synchronously here, in test
	// order) reopens the picker before the first pick has resolved.
	updated, cmd2 := model.Update(keyRunes("L"))
	model = updated.(Model)
	fetchMsg2 := cmd2()
	updated, _ = model.Update(fetchMsg2)
	model = updated.(Model)
	require.NotNil(t, model.picker, "the window is real: a second fetch/picker can complete before the first pick resolves")
	updated, chooseCmd2 := model.Update(keyRunes("2"))
	model = updated.(Model)
	msg2 := chooseCmd2()

	updated, actionCmd1 := model.Update(msg1)
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, actionCmd1)
	genAfterFirst := model.action.gen

	updated, actionCmd2 := model.Update(msg2)
	model = updated.(Model)
	require.Nil(t, actionCmd2, "the dropped second pick must not dispatch anything")
	require.Equal(t, genAfterFirst, model.action.gen)
	require.True(t, model.action.running)

	doneMsg := runActionCmd(t, actionCmd1)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.SetLockCalls, 1, "exactly one SetLock dispatch for two back-to-back picks")
	require.Equal(t, "5.3", rec.SetLockCalls[0].Version, "the FIRST pick wins")
}

// TestLockKeyQuitWhileFetchingCancelsContext proves quit pressed while the
// version fetch is in flight cancels the context passed to
// ActionProvider.AvailableVersions immediately, mirroring
// TestQuitWhileRunningCancelsContextImmediatelyButDoesNotQuitYet (which
// exercises this generically for any running action - this test proves the
// lock fetch specifically wires into that same m.action.cancel machinery).
func TestLockKeyQuitWhileFetchingCancelsContext(t *testing.T) {
	t.Parallel()

	captured := &capturingActions{recordingActions: recordingActions{AvailableVersionsOut: []string{"1.0"}}}
	model := modelWithActions(t, captured)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("L"))
	model = updated.(Model)
	require.True(t, model.action.running)

	updated, quitCmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(Model)
	require.NotNil(t, quitCmd, "a running fetch's quit must still schedule the drain timeout")
	require.True(t, model.action.draining)

	cmd()
	require.Error(t, captured.ctx.Err())
	require.ErrorIs(t, captured.ctx.Err(), context.Canceled)
}
