package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// --- Profile create ('c' on Profiles) ---

// TestCreateProfileKeyOpensInputModal covers createProfilePrompt's core
// contract: pressing 'c' on Profiles opens the input modal titled "new
// profile", synchronously (no cmd yet - the modal itself is the only
// state change until a value is submitted).
func TestCreateProfileKeyOpensInputModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd, "opening the input modal is synchronous - no cmd yet")
	require.NotNil(t, model.inputModal)
	require.Equal(t, "new profile", model.inputModal.title)
	require.True(t, model.inputModal.input.Focused())
}

// TestCreateProfileDuplicateNameValidatesInModal covers the validate
// closure's case-sensitive match against the currently-loaded m.profiles:
// typing an existing profile's name and pressing enter keeps the modal open
// with the "profile already exists" error, and never dispatches.
func TestCreateProfileDuplicateNameValidatesInModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	require.NotEmpty(t, model.profiles)
	existing := model.profiles[0].Name // "survival", the canned active profile

	updated, _ := model.Update(keyRunes("c"))
	model = updated.(Model)
	model = typeString(t, model, existing)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	require.Nil(t, cmd, "a validation error must not dispatch anything")
	require.NotNil(t, model.inputModal, "modal must stay open on a validation error")
	require.Equal(t, "profile already exists", model.inputModal.errMsg)
	require.Empty(t, rec.CreateProfileCalls)
}

// TestCreateProfilePathTraversalNameValidatesInModal covers the validate
// closure's client-side mirror of the config layer's validateProfileName
// guard: names containing path separators or ".." would become file paths
// under the profiles directory, so typing one and pressing enter keeps the
// modal open with an inline error, and never dispatches - rather than only
// failing after submit via ActionOutcome.
func TestCreateProfilePathTraversalNameValidatesInModal(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"../evil", "foo/bar", `foo\bar`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{}
			model := modelWithActions(t, rec)
			model.screen = ScreenProfiles

			updated, _ := model.Update(keyRunes("c"))
			model = updated.(Model)
			model = typeString(t, model, name)
			updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = updated.(Model)

			require.Nil(t, cmd, "a validation error must not dispatch anything")
			require.NotNil(t, model.inputModal, "modal must stay open on a validation error")
			require.Equal(t, `name must not contain path separators or ".."`, model.inputModal.errMsg)
			require.Empty(t, rec.CreateProfileCalls)
		})
	}
}

// TestCreateProfileSubmitRunsActionAndRefreshes drives the full round trip:
// 'c' opens the modal, typing a new name and pressing enter dispatches a
// deferred profileCreateSubmittedMsg (the closure-over-stale-Model problem
// policyChosenMsg's doc comment describes - a pendingInput.submit closure
// can only return a Cmd, not a mutated Model), which Update() routes to
// resolveProfileCreate against the LIVE model - runs CreateProfile and
// confirms immediately (no second confirm gate), and the resulting
// actionDoneMsg updates the status line and triggers a data refresh.
func TestCreateProfileSubmitRunsActionAndRefreshes(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{CreateProfileOutcome: ActionOutcome{Message: "Created profile: survival"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	// The prototype's own canned Profiles list already contains "survival"
	// (the active profile) - cleared here so submitting that same name below
	// exercises the SUCCESS path, not the duplicate-name validation covered
	// separately by TestCreateProfileDuplicateNameValidatesInModal.
	model.profiles = nil

	updated, _ := model.Update(keyRunes("c"))
	model = updated.(Model)
	model = typeString(t, model, "survival")
	updated, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, model.inputModal, "successful submit clears the modal")
	require.NotNil(t, submitCmd)
	require.Empty(t, rec.CreateProfileCalls, "nothing must mutate before the deferred cmd runs")

	msg := submitCmd()
	require.IsType(t, profileCreateSubmittedMsg{}, msg)
	require.Equal(t, "survival", msg.(profileCreateSubmittedMsg).name)

	updated, actionCmd := model.Update(msg)
	model = updated.(Model)
	require.True(t, model.action.running, "resolving the submission must mark the action running")
	require.NotNil(t, actionCmd)

	doneMsg := runActionCmd(t, actionCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []string{"survival"}, rec.CreateProfileCalls)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, "Created profile: survival", model.action.status)
	require.False(t, model.action.statusIsError)

	loadedMsg := refreshCmd()
	require.IsType(t, dataLoadedMsg{}, loadedMsg)
}

// TestCreateProfileResolveDroppedWhileBusy mirrors
// TestPolicyChoiceSecondPickInFlightIsDropped's shape for the profile-create
// resolver: the window between a submit clearing the input modal (running
// still false) and resolveProfileCreate actually running is real, so a
// second 'c'/submit in that window must be dropped by resolveProfileCreate's
// own single-flight guard once the first resolution has set running=true.
func TestCreateProfileResolveDroppedWhileBusy(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{CreateProfileOutcome: ActionOutcome{Message: "Created profile: alpha"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.profiles = nil

	// First submit: 'c' -> modal -> "alpha" -> enter; the deferred msg exists
	// but has NOT resolved yet.
	updated, _ := model.Update(keyRunes("c"))
	model = updated.(Model)
	model = typeString(t, model, "alpha")
	updated, submitCmd1 := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg1 := submitCmd1()

	// Second submit in the window: the modal re-opens (running is still
	// false) and a second deferred msg is produced.
	updated, _ = model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, model.inputModal, "the window is real: a second modal can open before the first submit resolves")
	model = typeString(t, model, "beta")
	updated, submitCmd2 := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg2 := submitCmd2()

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
	require.Nil(t, actionCmd2, "the dropped second submit must not dispatch anything")
	require.Equal(t, genAfterFirst, model.action.gen, "the dropped submit must not bump gen")
	require.True(t, model.action.running, "the FIRST action stays in flight")

	doneMsg := runActionCmd(t, actionCmd1)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []string{"alpha"}, rec.CreateProfileCalls, "exactly one CreateProfile dispatch for two back-to-back submits")
}

// --- Profile delete ('d' on Profiles) ---

// TestDeleteProfileActiveRefusedOnStatusLine covers deleteSelectedProfile's
// active-row guard: selecting the active profile and pressing 'd' never
// opens a confirmation modal - it's refused synchronously on the status
// line.
func TestDeleteProfileActiveRefusedOnStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0 // "survival", the canned active profile
	require.True(t, model.profiles[0].Active)

	updated, cmd := model.Update(keyRunes("d"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending, "no modal for the active profile")
	require.Equal(t, "cannot delete the active profile", model.action.status)
	require.True(t, model.action.statusIsError)
	require.Empty(t, rec.DeleteProfileCalls)
}

// TestDeleteProfileConfirmFlow covers the standard confirm-modal path for a
// non-active row: 'd' opens a modal naming the profile, 'y' confirms,
// DeleteProfile is called with that name, and the resulting actionDoneMsg
// updates the status line and triggers a data refresh.
func TestDeleteProfileConfirmFlow(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{DeleteProfileOutcome: ActionOutcome{Message: "Deleted profile: vanilla-plus"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1 // "vanilla-plus", not active
	require.Equal(t, "vanilla-plus", model.profiles[1].Name)
	require.False(t, model.profiles[1].Active)

	updated, cmd := model.Update(keyRunes("d"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Contains(t, model.action.pending.title, "vanilla-plus")
	require.Empty(t, rec.DeleteProfileCalls, "nothing must mutate before confirm")

	updated, confirmCmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.True(t, model.action.running)
	require.NotNil(t, confirmCmd)

	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []string{"vanilla-plus"}, rec.DeleteProfileCalls)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, "Deleted profile: vanilla-plus", model.action.status)
	require.False(t, model.action.statusIsError)

	loadedMsg := refreshCmd()
	require.IsType(t, dataLoadedMsg{}, loadedMsg)
}

// --- Screen/focus guards, shared by both keys ---

// TestProfileKeysIgnoredOffProfilesScreen proves 'c'/'d' only fire on the
// Profiles screen, mirroring TestPolicyKeyIgnoredOffInstalledMods.
func TestProfileKeysIgnoredOffProfilesScreen(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.inputModal)

	updated, cmd = model.Update(keyRunes("d"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

// TestProfileKeysSwallowedByFocusedSearchInput proves 'c'/'d' type into the
// search box instead of triggering create/delete while ScreenSearch is
// focused, mirroring TestPolicyKeySwallowedByFocusedInput.
func TestProfileKeysSwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "c")
	updated = updateWithRunes(t, updated, "d")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "c")
	require.Contains(t, updated.search.input.Value(), "d")
	require.Nil(t, updated.inputModal)
	require.Nil(t, updated.action.pending)
}
