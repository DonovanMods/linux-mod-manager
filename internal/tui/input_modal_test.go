package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// inputModalTestModel builds a fully-loaded, sized prototype Model with no
// pending action/picker/input modal - the common starting point for every
// test below.
func inputModalTestModel(t *testing.T) Model {
	t.Helper()
	return sizedPrototypeModel(t, "wizardry", 100, 30)
}

// promptTestInput attaches an input modal to model via promptInput, using
// validate as the validation func and recording every submit(value) call in
// *submitted (appended, so a test can assert both "was it called" and "with
// what value").
func promptTestInput(model Model, validate func(value string) string, submitted *[]string) Model {
	input := newInputModalTextInput("profile name", 64, model.availableWidth(), model.theme.Panel.GetHorizontalFrameSize())
	return model.promptInput(pendingInput{
		title:    "Create Profile",
		input:    input,
		validate: validate,
		submit: func(value string) tea.Cmd {
			*submitted = append(*submitted, value)
			return nil
		},
	})
}

// typeString sends model one KeyMsg per rune of s, matching how the rest of
// the package's tests drive textinput.Model (see search_test.go).
func typeString(t *testing.T, model Model, s string) Model {
	t.Helper()
	for _, r := range s {
		model = updateWithRunes(t, model, string(r))
	}
	return model
}

func alwaysValid(string) string { return "" }

// promptTestInputRequiring is promptTestInput plus a caller-supplied
// requiredMsg (see pendingInput's doc comment) - covers export/import's
// "path required" in place of the zero-value default "name required".
func promptTestInputRequiring(model Model, requiredMsg string, validate func(value string) string, submitted *[]string) Model {
	input := newInputModalTextInput("path", 256, model.availableWidth(), model.theme.Panel.GetHorizontalFrameSize())
	return model.promptInput(pendingInput{
		title:       "Import Profile",
		input:       input,
		requiredMsg: requiredMsg,
		validate:    validate,
		submit: func(value string) tea.Cmd {
			*submitted = append(*submitted, value)
			return nil
		},
	})
}

func TestInputModalTypeAndSubmit(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInput(inputModalTestModel(t), alwaysValid, &submitted)
	require.NotNil(t, model.inputModal)
	require.True(t, model.inputModal.input.Focused(), "promptInput must focus the input")

	model = typeString(t, model, "survival")
	model = updateWithKeyType(t, model, tea.KeyEnter)

	require.Equal(t, []string{"survival"}, submitted)
	require.Nil(t, model.inputModal)
}

// TestInputModalValidationErrorKeepsModalOpen covers a non-empty value that
// fails caller validation (e.g. a name collision): the error is shown
// in-modal, the modal stays open, and submit is never called.
func TestInputModalValidationErrorKeepsModalOpen(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInput(inputModalTestModel(t), func(string) string {
		return "name already exists"
	}, &submitted)

	model = typeString(t, model, "duplicate")
	model = updateWithKeyType(t, model, tea.KeyEnter)

	require.Empty(t, submitted, "submit must not be called on a validation error")
	require.NotNil(t, model.inputModal, "modal must stay open on a validation error")
	require.Contains(t, model.View(), "name already exists")
}

func TestInputModalEscCancels(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInput(inputModalTestModel(t), alwaysValid, &submitted)

	model = typeString(t, model, "abc")
	model = updateWithKeyType(t, model, tea.KeyEsc)

	require.Empty(t, submitted)
	require.Nil(t, model.inputModal)
}

func TestInputModalBlockedWhileActionRunning(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := inputModalTestModel(t)
	model.action.running = true

	model = promptTestInput(model, alwaysValid, &submitted)

	require.Nil(t, model.inputModal, "promptInput must be a no-op while an action is running")
}

// TestInputModalQKeyTypesInsteadOfQuitting locks in the modal's departure
// from the Quit binding's usual "q" behavior (see updateInputModalKey's doc
// comment): "q" must be typeable like any other character, and only ctrl+c
// actually quits while the modal is open.
func TestInputModalQKeyTypesInsteadOfQuitting(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInput(inputModalTestModel(t), alwaysValid, &submitted)

	model = updateWithRunes(t, model, "q")

	require.NotNil(t, model.inputModal, "plain q must not quit the modal")
	require.Contains(t, model.inputModal.input.Value(), "q")

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	require.Equal(t, tea.Quit(), cmd(), "ctrl+c must still quit while the modal is open")
}

// TestInputModalEmptySubmitShowsNameRequired covers submitting with no
// value typed at all: validate is never even consulted (empty is checked
// first - see updateInputModalKey's doc comment), and the modal stays open.
func TestInputModalEmptySubmitShowsNameRequired(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInput(inputModalTestModel(t), alwaysValid, &submitted)

	model = updateWithKeyType(t, model, tea.KeyEnter)

	require.Empty(t, submitted)
	require.NotNil(t, model.inputModal)
	require.Contains(t, model.View(), "name required")
}

// TestInputModalEmptySubmitShowsCustomRequiredMsg covers pendingInput's
// requiredMsg field (added for export/import's path fields, where "name
// required" is the wrong noun): a caller that sets it gets ITS copy on an
// empty submit instead of the "name required" default -
// TestInputModalEmptySubmitShowsNameRequired above proves the zero value
// still falls back to "name required" for callers (createProfilePrompt) that
// never set it.
func TestInputModalEmptySubmitShowsCustomRequiredMsg(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInputRequiring(inputModalTestModel(t), "path required", alwaysValid, &submitted)

	model = updateWithKeyType(t, model, tea.KeyEnter)

	require.Empty(t, submitted)
	require.NotNil(t, model.inputModal)
	require.Contains(t, model.View(), "path required")
	require.NotContains(t, model.View(), "name required")
}
