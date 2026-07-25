package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// Task 10 (Phase 6b): profile export ('E' on Profiles) -
// mutations.go's exportProfilePrompt/resolveExportSubmitted, wired onto
// ActionProvider.ExportProfile. See task-10-brief.md for the exact flow this
// implements. TestExportWritesFile (coreProvider + t.TempDir()) and
// TestPrototypeExportSucceedsWithoutWriting live alongside this task's other
// provider-level tests (service_core_test.go / actions_provider_test.go
// respectively) - this file covers only the TUI-level wiring, mirroring
// import_test.go's own split.

// TestExportKeyOpensPathInputPrefilled covers exportProfilePrompt's core
// contract: pressing 'E' on Profiles, with a profile row selected, opens the
// input modal titled "export profile — path to save" with the input
// PREFILLED "<gameID>-<name>.yaml" (modelWithActions' prototype game is
// "skyrim-se" - see prototype/data.go; index 0 is "survival" - see
// TestDeleteProfileKeyOnActiveProfileRefusesSynchronously's identical
// selection precedent).
func TestExportKeyOpensPathInputPrefilled(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0 // "survival"

	updated, cmd := model.Update(keyRunes("E"))
	model = updated.(Model)
	require.Nil(t, cmd, "opening the input modal is synchronous - no cmd yet")
	require.NotNil(t, model.inputModal)
	require.Equal(t, "export profile — path to save", model.inputModal.title)
	require.True(t, model.inputModal.input.Focused())
	require.Equal(t, "skyrim-se-survival.yaml", model.inputModal.input.Value())
}

// TestExportEmptySubmitShowsPathRequired covers exportProfilePrompt's
// requiredMsg (Minor #5, input_modal.go): the input starts prefilled (see
// TestExportKeyOpensPathInputPrefilled above), so this clears it first
// (mirrors TestExportSuccessStatusLine's own SetValue precedent for editing
// the path) to reach the empty-submit case - which must say "path required",
// not the generic "name required".
func TestExportEmptySubmitShowsPathRequired(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0 // "survival"

	updated, _ := model.Update(keyRunes("E"))
	model = updated.(Model)
	model.inputModal.input.SetValue("")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	require.Nil(t, cmd, "an empty submit must not dispatch anything")
	require.NotNil(t, model.inputModal, "modal must stay open on an empty submit")
	require.Equal(t, "path required", model.inputModal.errMsg)
	require.Empty(t, rec.ExportCalls)
}

// TestExportSuccessStatusLine drives the full 'E' -> prefilled path -> enter
// round trip against a recordingActions fake configured to succeed, proving
// ExportProfile is called with the selected profile's name and the (possibly
// edited) path, SYNCHRONOUSLY (no confirmation modal, no async cmd - mirrors
// moveSelectedMod/showDeployedFiles' own documented sync exception), and the
// outcome's message lands on the status line with statusIsError false.
func TestExportSuccessStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		ExportOutcome: ActionOutcome{Message: `exported "survival" to survival.yaml`},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0 // "survival"

	updated, _ := model.Update(keyRunes("E"))
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, model.inputModal, "successful submit clears the input modal")
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, exportPathSubmittedMsg{}, msg)

	updated, resolveCmd := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, resolveCmd, "the export write is a local, SYNCHRONOUS call - no further cmd")

	require.Len(t, rec.ExportCalls, 1)
	require.Equal(t, "survival", rec.ExportCalls[0].Name)
	require.Equal(t, "skyrim-se-survival.yaml", rec.ExportCalls[0].Path)

	require.Equal(t, `exported "survival" to survival.yaml`, model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestExportRefusesOverwrite drives the SAME full round trip against a REAL
// coreProvider/coreActions pair (not a recording fake - mirrors
// TestSwitchConfirmSurfacesInstallFailureWarningInStatusLine's own "prove it
// end to end against the real provider" precedent) targeting a path that
// already has a file at it: the overwrite refusal (coreProvider's O_EXCL
// guard) must surface on the status line as an error, and the pre-existing
// file must be left completely untouched.
func TestExportRefusesOverwrite(t *testing.T) {
	t.Parallel()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "test-game", Name: "Test Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	provider := NewCoreProvider(svc, game, "default")
	actions := NewCoreActions(svc, game, "default")

	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: actions})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0

	path := filepath.Join(t.TempDir(), "export.yaml")
	require.NoError(t, os.WriteFile(path, []byte("pre-existing content"), 0o644))

	updated, _ := model.Update(keyRunes("E"))
	model = updated.(Model)
	require.NotNil(t, model.inputModal)
	// Overwrite the prefilled value with the pre-existing path directly -
	// simpler than a keystroke-by-keystroke backspace+retype, and every bit
	// as valid: submitInputModal only ever reads p.input.Value() (see its own
	// doc comment), never how it got there.
	model.inputModal.input.SetValue(path)
	model.inputModal.input.CursorEnd()

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, model.inputModal, "a valid (non-empty) path always clears the input modal - the refusal surfaces later")
	require.NotNil(t, cmd)

	msg := cmd()
	updated, resolveCmd := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, resolveCmd)

	require.Equal(t, "file exists: "+path, model.action.status)
	require.True(t, model.action.statusIsError)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "pre-existing content", string(got), "an overwrite refusal must leave the pre-existing file untouched")
}

// TestExportKeySwallowedByFocusedSearchInput proves 'E' types into the
// search box instead of triggering export while ScreenSearch is focused -
// mirrors TestImportKeySwallowedByFocusedSearchInput's identical shape.
func TestExportKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "E")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "E")
	require.Nil(t, updated.inputModal)
}
