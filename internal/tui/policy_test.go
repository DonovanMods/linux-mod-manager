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
