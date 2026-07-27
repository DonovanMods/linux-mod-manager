package tui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- Update policy ('P' on Installed Mods) ---

// TestPolicyKeyOpensPickerWithCurrentMarked covers editSelectedModPolicy's
// core contract: pressing 'P' on a selected mod opens a picker with exactly
// the three notify/auto/pin options, in that order, and the option matching
// the mod's current ModItem.UpdatePolicy is both labeled "current" and
// pre-selected.
func TestPolicyKeyOpensPickerWithCurrentMarked(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI" - canned UpdatePolicy "auto"
	require.Equal(t, "SkyUI", model.mods[0].Name)
	require.Equal(t, "auto", model.mods[0].UpdatePolicy)

	updated, cmd := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.Nil(t, cmd, "opening the picker is synchronous - no cmd yet")
	require.NotNil(t, model.picker)
	require.Contains(t, model.picker.title, "SkyUI")

	require.Len(t, model.picker.options, 3)
	wantLabels := []string{"notify", "auto", "pin"}
	for i, opt := range model.picker.options {
		require.Equal(t, wantLabels[i], opt.Label)
	}

	require.Equal(t, "current", model.picker.options[1].Note, `"auto" must be marked current`)
	require.Empty(t, model.picker.options[0].Note)
	require.Empty(t, model.picker.options[2].Note)
	require.Equal(t, 1, model.picker.selected, `"auto" (index 1) must start pre-selected`)
}

// TestPolicyKeyOpensPickerNotifyCurrentByDefault covers the other canned
// policy value (USSEP's "notify") so the "current" marking isn't hardcoded
// to whatever index the first test happens to use.
func TestPolicyKeyOpensPickerNotifyCurrentByDefault(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 1 // "USSEP" - canned UpdatePolicy "notify"
	require.Equal(t, "notify", model.mods[1].UpdatePolicy)

	updated, _ := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.NotNil(t, model.picker)
	require.Equal(t, "current", model.picker.options[0].Note)
	require.Equal(t, 0, model.picker.selected)
}

// TestPolicyPickerChoiceRunsActionAndRefreshes drives the full round trip:
// 'P' opens the picker, choosing "pin" (digit quick-select 3) immediately
// dispatches SetUpdatePolicy - no second confirm gate - and the resulting
// actionDoneMsg updates the status line and triggers a data refresh.
func TestPolicyPickerChoiceRunsActionAndRefreshes(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{SetPolicyOutcome: ActionOutcome{Message: `SkyUI update policy: pin`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI", ID "skyui"

	updated, cmd := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.picker)

	// Digit quick-select "3" chooses index 2 ("pin").
	updated, chooseCmd := model.Update(keyRunes("3"))
	model = updated.(Model)
	require.Nil(t, model.picker, "choosing must clear the picker")
	require.NotNil(t, chooseCmd, "choosing must return the deferred dispatch cmd")
	require.Empty(t, rec.SetPolicyCalls, "nothing must mutate before the deferred cmd runs")

	msg := chooseCmd()
	require.IsType(t, policyChosenMsg{}, msg)
	picked := msg.(policyChosenMsg)
	require.Equal(t, "pin", picked.policy)
	require.Equal(t, "skyui", picked.item.ID)

	updated, actionCmd := model.Update(msg)
	model = updated.(Model)
	require.True(t, model.action.running, "resolving the choice must mark the action running")
	require.NotNil(t, actionCmd)

	doneMsg := runActionCmd(t, actionCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.SetPolicyCalls, 1)
	require.Equal(t, "skyui", rec.SetPolicyCalls[0].ModID)
	require.Equal(t, "pin", rec.SetPolicyCalls[0].Policy)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `SkyUI update policy: pin`, model.action.status)
	require.False(t, model.action.statusIsError)

	loadedMsg := refreshCmd()
	require.IsType(t, dataLoadedMsg{}, loadedMsg)
}

// TestPolicyKeySwallowedByFocusedInput proves 'P' types into the search box
// instead of opening the picker while ScreenSearch is focused - the existing
// focused-input swallow branch (updateKey, app.go) runs before the
// mutation-key switch this is dispatched from, matching every other
// single-letter mutation key's own test of the same guard (e.g.
// TestFilesKeySwallowedByFocusedSearchInput).
func TestPolicyKeySwallowedByFocusedInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "P")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "P")
	require.Nil(t, updated.picker)
}

// TestPolicyKeyIgnoredOffInstalledMods proves 'P' only fires on Installed
// Mods.
func TestPolicyKeyIgnoredOffInstalledMods(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
}

// TestPolicyKeyEmptyListIsNoop proves an empty mods list can never crash or
// open a picker for a nonexistent selection.
func TestPolicyKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.mods = nil
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
}

// TestPolicyKeyInertWhileAnotherModalPending proves a DIFFERENT already-
// pending confirmation modal is left completely undisturbed by 'P'.
func TestPolicyKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("D")) // opens the Deploy modal
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending, "the original modal must still be showing")
	require.Equal(t, actionDeploy, model.action.pending.kind)
	require.Nil(t, model.picker, "P must not open a picker behind the confirm modal")
}

// TestPolicyKeyNoActionProviderIsNoop proves 'P' is inert with no
// ActionProvider configured, mirroring uninstallSelectedMod/
// toggleSelectedModEnable's own guard.
func TestPolicyKeyNoActionProviderIsNoop(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider()})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("P"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
}

// TestPolicyChoiceMapsProviderErrorToActionFailedMsg proves an error from
// SetUpdatePolicy (e.g. an unknown-policy rejection from a real
// coreProvider) surfaces as an actionFailedMsg through the SAME immediate-
// dispatch path, mirroring TestConfirmClosureMapsProviderErrorToActionFailedMsg
// for the two-step confirm modal.
func TestPolicyChoiceMapsProviderErrorToActionFailedMsg(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("unknown policy")
	rec := &recordingActions{SetPolicyErr: sentinel}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("P"))
	model = updated.(Model)
	updated, chooseCmd := model.Update(keyRunes("1"))
	model = updated.(Model)
	msg := chooseCmd()

	updated, actionCmd := model.Update(msg)
	model = updated.(Model)
	require.True(t, model.action.running)

	doneMsg := runActionCmd(t, actionCmd)
	require.IsType(t, actionFailedMsg{}, doneMsg)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, sentinel.Error())
	require.NotNil(t, refreshCmd, "the partial-mutation contract still refreshes on failure")
}

// TestPolicyChoiceSecondPickInFlightIsDropped guards the in-flight-message
// window between a pick and its resolution: after choosePickerOption clears
// the picker, action.running stays false until resolvePolicyChoice runs, so
// a second 'P' press in that window opens a second picker and yields a
// SECOND policyChosenMsg before the first has resolved. Resolving both must
// dispatch exactly ONE SetUpdatePolicy call - the second message is dropped
// by resolvePolicyChoice's own single-flight guard once the first
// resolution has set running=true - and the second resolution must leave
// the action state (gen, running, status) untouched.
func TestPolicyChoiceSecondPickInFlightIsDropped(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{SetPolicyOutcome: ActionOutcome{Message: `SkyUI update policy: pin`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	// First pick: P → picker → "3" chooses "pin"; the deferred msg exists
	// but has NOT resolved yet.
	updated, _ := model.Update(keyRunes("P"))
	model = updated.(Model)
	updated, chooseCmd1 := model.Update(keyRunes("3"))
	model = updated.(Model)
	msg1 := chooseCmd1()

	// Second pick in the window: the picker re-opens (running is still
	// false) and a second deferred msg is produced.
	updated, _ = model.Update(keyRunes("P"))
	model = updated.(Model)
	require.NotNil(t, model.picker, "the window is real: a second picker can open before the first pick resolves")
	updated, chooseCmd2 := model.Update(keyRunes("2"))
	model = updated.(Model)
	msg2 := chooseCmd2()

	// First resolution dispatches normally.
	updated, actionCmd1 := model.Update(msg1)
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, actionCmd1)
	genAfterFirst := model.action.gen

	// Second resolution, arriving while the first action is in flight, must
	// be a no-op: no second dispatch, no state disturbance.
	updated, actionCmd2 := model.Update(msg2)
	model = updated.(Model)
	require.Nil(t, actionCmd2, "the dropped second pick must not dispatch anything")
	require.Equal(t, genAfterFirst, model.action.gen, "the dropped pick must not bump gen")
	require.True(t, model.action.running, "the FIRST action stays in flight")

	doneMsg := runActionCmd(t, actionCmd1)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.SetPolicyCalls, 1, "exactly one SetUpdatePolicy dispatch for two back-to-back picks")
	require.Equal(t, "pin", rec.SetPolicyCalls[0].Policy, "the FIRST pick wins")
}

// TestPolicyChoiceDroppedWhileConfirmModalPending covers the other edge of
// the same window: a pendingAction confirm modal opened between the pick and
// its resolution (e.g. 'D' pressed in the gap). Without
// resolvePolicyChoice's guard, buildAction's own refusal leaves the model
// untouched but resolvePolicyChoice then set running=true anyway - sticking
// the single-flight guard permanently (no action is actually running, so no
// actionDoneMsg/actionFailedMsg would ever clear it). The stray message
// must be dropped entirely: running stays false, the modal stays up, no
// dispatch.
func TestPolicyChoiceDroppedWhileConfirmModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("P"))
	model = updated.(Model)
	updated, chooseCmd := model.Update(keyRunes("3"))
	model = updated.(Model)
	msg := chooseCmd()

	// A confirm modal opens in the window before the pick resolves.
	updated, _ = model.Update(keyRunes("D"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)

	updated, cmd := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd, "the dropped pick must not dispatch anything")
	require.False(t, model.action.running, "a dropped pick must never set running - nothing would ever clear it")
	require.NotNil(t, model.action.pending, "the confirm modal must stay up, undisturbed")
	require.Empty(t, rec.SetPolicyCalls)
	require.Equal(t, "busy — choice ignored", model.action.status, "the pending-confirm drop must surface the same busy hint as the running-action drop")
}

// TestPolicyChoiceDropShowsHintWhenNothingOwnsLine covers Task 6's #68 fix:
// resolvePolicyChoice's single-flight drop (see
// TestPolicyChoiceSecondPickInFlightIsDropped/
// TestPolicyChoiceDroppedWhileConfirmModalPending above) used to be
// perfectly silent - the user had no sign their picker choice never took
// effect. Now, whenever nothing more important already owns the status line
// (hasVisibleStatus - actions.go - is false), the drop sets a muted "busy"
// hint instead.
func TestPolicyChoiceDropShowsHintWhenNothingOwnsLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	// Simulates the drop-guard's "already busy" branch: some OTHER action is
	// running, with no status/progress of its own yet (hasVisibleStatus is
	// false), matching resolvePolicyChoice's own actionSetPolicy shape (no
	// in-flight status text is ever set for it - see resolvePolicyChoice's
	// doc comment).
	model.action.running = true

	msg := policyChosenMsg{item: model.mods[0], policy: "pin"}
	updated, cmd := model.Update(msg)
	model = updated.(Model)

	require.Nil(t, cmd, "the dropped pick must not dispatch anything")
	require.Equal(t, "busy — choice ignored", model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestPolicyChoiceDropDoesNotStompRunningActionStatus covers the other half
// of the same fix: the hint must never overwrite a running action's own
// live status text. hasVisibleStatus (actions.go) already treats a non-empty
// action.status as "owned" while running, so the drop-guard must leave it
// untouched instead of clobbering it with the "busy" hint.
func TestPolicyChoiceDropDoesNotStompRunningActionStatus(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.action.running = true
	model.action.status = "Deploying…"
	model.action.statusIsError = false

	msg := policyChosenMsg{item: model.mods[0], policy: "pin"}
	updated, cmd := model.Update(msg)
	model = updated.(Model)

	require.Nil(t, cmd, "the dropped pick must not dispatch anything")
	require.Equal(t, "Deploying…", model.action.status, "a running action's own live status must not be stomped")
	require.False(t, model.action.statusIsError)
}

// TestPolicyChoiceDropHintOutranksReorderHint covers the busy-drop hint's
// slice of the priority inversion Copilot found in PR #107 round 4 (see
// profile_mgmt_test.go's TestDeleteProfileActiveRefusalOutranksReorderHint
// for the full story): a pick dropped because a confirm modal snuck into
// the window (pending set, running false - the natural post-reorder flow is
// exactly this, 'D' opening the deploy modal while orderChanged is still
// pending) must still surface the hint even though the LOW-priority
// orderChanged fallback would otherwise render on the line - the fallback
// owns nothing, and statusLine gives an explicit status priority over it
// anyway.
func TestPolicyChoiceDropHintOutranksReorderHint(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.orderChanged = true // an undeployed reorder's fallback hint owns nothing

	updated, _ := model.Update(keyRunes("P"))
	model = updated.(Model)
	updated, chooseCmd := model.Update(keyRunes("3"))
	model = updated.(Model)
	msg := chooseCmd()

	// The deploy confirm modal opens in the window before the pick resolves.
	updated, _ = model.Update(keyRunes("D"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(msg)
	model = updated.(Model)

	require.Nil(t, cmd, "the dropped pick must not dispatch anything")
	require.Equal(t, "busy — choice ignored", model.action.status,
		"the hint must not be swallowed by the low-priority reorder hint")
	require.False(t, model.action.statusIsError)
	require.True(t, model.orderChanged, "the reorder-pending flag itself must survive")
}
