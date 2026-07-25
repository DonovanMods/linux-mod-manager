package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// Task 9 (Phase 6b): profile import ('I' on Profiles) - mutations.go's
// importProfilePrompt/resolveImportDataRead/resolveImportApplied/
// resolveImportSwitchConfirmed, wired onto ActionProvider.PlanImport/
// ApplyImport. See task-9-brief.md for the exact flow this implements.
// TestPrototypeImportAddsProfile (the brief's 6th required test) lives in
// actions_provider_test.go instead, alongside every other prototype-provider
// mutation test (TestPrototypeRollbackSwapsVersions et al.).

// writeTempProfileFile writes content to a fresh file under t.TempDir() and
// returns its path - the "readable path" fixture every test below except
// the unreadable-path one needs. Content is never actually parsed by
// anything in this file (PlanImport is a recordingActions fake here), so it
// need not be valid profile YAML.
func writeTempProfileFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// TestImportKeyOpensPathInput covers importProfilePrompt's core contract:
// pressing 'I' on Profiles opens the input modal titled "import profile —
// path to yaml", synchronously (no cmd yet - mirrors
// TestCreateProfileKeyOpensInputModal's identical shape).
func TestImportKeyOpensPathInput(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles

	updated, cmd := model.Update(keyRunes("I"))
	model = updated.(Model)
	require.Nil(t, cmd, "opening the input modal is synchronous - no cmd yet")
	require.NotNil(t, model.inputModal)
	require.Equal(t, "import profile — path to yaml", model.inputModal.title)
	require.True(t, model.inputModal.input.Focused())
}

// TestImportUnreadablePathErrorsInModal covers the validate closure's I/O
// check: typing a path os.ReadFile can't open and pressing enter keeps the
// modal open with the OS error as errMsg, and never dispatches anything -
// mirroring TestCreateProfileDuplicateNameValidatesInModal's "modal stays
// open on a validation error" shape.
func TestImportUnreadablePathErrorsInModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles

	bogusPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	updated, _ := model.Update(keyRunes("I"))
	model = updated.(Model)
	model = typeString(t, model, bogusPath)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	require.Nil(t, cmd, "a validation error must not dispatch anything")
	require.NotNil(t, model.inputModal, "modal must stay open on a read error")
	require.NotEmpty(t, model.inputModal.errMsg)
	require.Empty(t, rec.PlanImportCalls, "PlanImport must never be reached for an unreadable path")
	require.Empty(t, rec.ApplyImportCalls)
}

// TestImportPreviewModalFromPlan drives the full read->plan round trip:
// submitting a readable path dispatches a deferred importDataReadMsg, which
// Update() routes to resolveImportDataRead - PlanImport runs against the
// LIVE model's actions, and a successful plan opens the import preview
// modal with per-category counts+names, an "overwrites existing profile"
// warning when Exists, and a "different game: <id>" warning when the plan's
// GameID differs from the session's active game (modelWithActions' default
// prototype game is "skyrim-se" - see prototype/data.go).
func TestImportPreviewModalFromPlan(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		PlanImportViewOut: ImportPlanView{
			Name:          "raider-pack",
			GameID:        "fallout4",
			Installed:     []string{"nexusmods:already-installed v1.0"},
			NeedsDownload: []string{"nexusmods:needs-redownload v2.0"},
			Missing:       []string{"nexusmods:missing-mod v3.0"},
			Exists:        true,
		},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	path := writeTempProfileFile(t, "irrelevant")

	updated, _ := model.Update(keyRunes("I"))
	model = updated.(Model)
	model = typeString(t, model, path)
	updated, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, model.inputModal, "successful submit clears the input modal")
	require.NotNil(t, submitCmd)

	msg := submitCmd()
	require.IsType(t, importDataReadMsg{}, msg)

	updated, cmd := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd, "opening the preview modal is synchronous")
	require.Len(t, rec.PlanImportCalls, 1)
	require.Equal(t, []byte("irrelevant"), rec.PlanImportCalls[0])

	require.NotNil(t, model.action.pending)
	require.Equal(t, actionImport, model.action.pending.kind)
	require.Equal(t, `Import profile "raider-pack"?`, model.action.pending.title)

	detail := model.action.pending.detail
	require.Contains(t, detail, "overwrites existing profile")
	require.Contains(t, detail, "different game: fallout4")
	require.Contains(t, detail, "1 already installed:")
	require.Contains(t, detail, "  nexusmods:already-installed v1.0")
	require.Contains(t, detail, "1 need re-download:")
	require.Contains(t, detail, "  ↓ nexusmods:needs-redownload v2.0")
	require.Contains(t, detail, "1 need to be downloaded:")
	require.Contains(t, detail, "  ↓ nexusmods:missing-mod v3.0")
	require.Empty(t, rec.ApplyImportCalls, "nothing must mutate before confirm")
}

// TestImportConfirmAppliesWithProgress proves confirming the preview modal
// calls ActionProvider.ApplyImport with the SAME bytes PlanImport was given,
// streams progress through the standard pump, and triggers the standard
// post-action refresh - mirroring TestRollbackConfirmAppliesAndRefreshes'
// own shape.
func TestImportConfirmAppliesWithProgress(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		PlanImportViewOut:  ImportPlanView{Name: "raider-pack", GameID: "skyrim-se", Missing: []string{"nexusmods:missing-mod v1.0"}},
		ApplyImportOutcome: ActionOutcome{Message: `Imported profile "raider-pack"`},
		ApplyImportTicks:   []ActionProgress{{Line: "Importing: 50%", Percent: 50}},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	path := writeTempProfileFile(t, "profile-bytes")

	updated, _ := model.Update(keyRunes("I"))
	model = updated.(Model)
	model = typeString(t, model, path)
	updated, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := submitCmd()

	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.Nil(t, model.action.pending)
	require.NotNil(t, confirmCmd)

	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.ApplyImportCalls, 1)
	require.Equal(t, []byte("profile-bytes"), rec.ApplyImportCalls[0])

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd, "a completed import must trigger the standard refresh")
	require.Equal(t, `Imported profile "raider-pack"`, model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestImportOfferSwitchAfterApply covers the post-apply "switch to it now?"
// offer (task-9-brief.md): a successful import for the ACTIVE game offers
// it; confirming "yes" routes into switchToProfileNamed's own plan chain
// (never a duplicate ApplyProfileSwitch call); declining leaves nothing
// switched; and a cross-game import never offers at all.
func TestImportOfferSwitchAfterApply(t *testing.T) {
	t.Parallel()

	// applyImportAndDone drives 'I' -> path -> plan -> confirm -> the
	// resulting actionDoneMsg, returning the model AFTER actionDoneMsg's own
	// handling (which is what dispatches the offer, per app.go) plus
	// whichever cmd that handling returned.
	applyImportAndDone := func(t *testing.T, rec *recordingActions) (Model, tea.Cmd) {
		t.Helper()
		model := modelWithActions(t, rec)
		model.screen = ScreenProfiles
		path := writeTempProfileFile(t, "profile-bytes")

		updated, _ := model.Update(keyRunes("I"))
		model = updated.(Model)
		model = typeString(t, model, path)
		updated, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(Model)
		msg := submitCmd()

		updated, _ = model.Update(msg)
		model = updated.(Model)

		confirmed, confirmCmd := model.Update(keyRunes("y"))
		model = confirmed.(Model)
		doneMsg := runActionCmd(t, confirmCmd)

		updated, cmd := model.Update(doneMsg)
		return updated.(Model), cmd
	}

	t.Run("active game offers, yes routes into the switch plan chain", func(t *testing.T) {
		t.Parallel()

		rec := &recordingActions{
			PlanImportViewOut:  ImportPlanView{Name: "raider-pack", GameID: "skyrim-se"},
			ApplyImportOutcome: ActionOutcome{Message: "Imported", ImportedProfile: "raider-pack"},
			PlanView:           SwitchPlanView{From: "survival", To: "raider-pack"},
		}
		model, cmd := applyImportAndDone(t, rec)
		require.NotNil(t, cmd)

		batch, ok := cmd().(tea.BatchMsg)
		require.True(t, ok)
		var offerMsg tea.Msg
		for _, sub := range batch {
			if m, ok := sub().(importAppliedMsg); ok {
				offerMsg = m
			}
		}
		require.NotNil(t, offerMsg, "a same-game import must dispatch the deferred switch offer")
		require.Equal(t, importAppliedMsg{name: "raider-pack"}, offerMsg)

		updated, offerCmd := model.Update(offerMsg)
		model = updated.(Model)
		require.Nil(t, offerCmd, "opening the offer picker is synchronous")
		require.NotNil(t, model.picker)
		require.Equal(t, `switch to "raider-pack" now?`, model.picker.title)

		// "yes" (index 0): dispatches importSwitchConfirmedMsg.
		chosen, chooseCmd := model.choosePickerOption(0)
		model = chosen.(Model)
		require.NotNil(t, chooseCmd)
		confirmMsg := chooseCmd()
		require.Equal(t, importSwitchConfirmedMsg{name: "raider-pack"}, confirmMsg)

		updated, switchCmd := model.Update(confirmMsg)
		model = updated.(Model)
		require.NotNil(t, switchCmd, "yes must route into switchToProfileNamed's own plan fetch")
		require.True(t, model.action.running)
		require.Empty(t, rec.ApplyCalls, "yes only PLANS the switch here - it must not call ApplyProfileSwitch directly")

		planMsg := switchCmd()
		require.IsType(t, planResultMsg{}, planMsg)
		require.Equal(t, []string{"raider-pack"}, rec.PlanCalls)
	})

	t.Run("declined leaves nothing switched", func(t *testing.T) {
		t.Parallel()

		rec := &recordingActions{
			PlanImportViewOut:  ImportPlanView{Name: "raider-pack", GameID: "skyrim-se"},
			ApplyImportOutcome: ActionOutcome{Message: "Imported", ImportedProfile: "raider-pack"},
		}
		model, cmd := applyImportAndDone(t, rec)
		batch := cmd().(tea.BatchMsg)
		var offerMsg tea.Msg
		for _, sub := range batch {
			if m, ok := sub().(importAppliedMsg); ok {
				offerMsg = m
			}
		}
		require.NotNil(t, offerMsg)

		updated, _ := model.Update(offerMsg)
		model = updated.(Model)
		require.NotNil(t, model.picker)

		// "no" (index 1): choose returns a nil cmd, no further message.
		chosen, chooseCmd := model.choosePickerOption(1)
		model = chosen.(Model)
		require.Nil(t, chooseCmd)
		require.Nil(t, model.picker)
		require.Empty(t, rec.PlanCalls)
		require.Empty(t, rec.ApplyCalls)
	})

	t.Run("non-active-game import never offers", func(t *testing.T) {
		t.Parallel()

		rec := &recordingActions{
			PlanImportViewOut:  ImportPlanView{Name: "fo4-pack", GameID: "fallout4"},
			ApplyImportOutcome: ActionOutcome{Message: "Imported"}, // ImportedProfile left "" by coreProvider/prototype for a cross-game import
		}
		_, cmd := applyImportAndDone(t, rec)
		require.NotNil(t, cmd, "the standard refresh cmd must still be returned")

		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, sub := range batch {
				require.NotEqual(t, importAppliedMsg{name: "fo4-pack"}, sub(), "a cross-game import must never offer a switch")
			}
		}
	})
}

// TestImportKeySwallowedByFocusedSearchInput proves 'I' types into the
// search box instead of triggering import while ScreenSearch is focused -
// mirrors TestRollbackKeySwallowedByFocusedSearchInput's identical shape.
func TestImportKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "I")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "I")
	require.Nil(t, updated.inputModal)
}

// --- Extra coverage mirroring Purge/Rollback/CreateProfile's own siblings ---

func TestImportKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenInstalledMods, ScreenSearch, ScreenSources} {
		model := modelWithActions(t, &recordingActions{})
		model.screen = screen

		updated, cmd := model.Update(keyRunes("I"))
		model = updated.(Model)
		require.Nil(t, cmd)
		require.Nil(t, model.inputModal, "screen %v", screen)
	}
}

func TestImportKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.action.running = true

	updated, cmd := model.Update(keyRunes("I"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.inputModal)
}
