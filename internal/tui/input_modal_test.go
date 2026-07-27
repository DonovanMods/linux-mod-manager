package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// TestPromptInputGuardArms tables promptInput's single-flight guard
// (input_modal.go): "action.running || action.pending != nil || picker !=
// nil || inputModal != nil" - only the action.running arm
// (TestInputModalBlockedWhileActionRunning above) had a dedicated test
// before this; the other three arms were only ever exercised incidentally
// by other tests, never asserted against directly.
func TestPromptInputGuardArms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		setup func(m *Model)
	}{
		{"action running", func(m *Model) { m.action.running = true }},
		{"action pending", func(m *Model) { m.action.pending = &pendingAction{title: "Some action"} }},
		{"picker already up", func(m *Model) {
			m.picker = &pendingPicker{title: "Pick one", options: []pickerOption{{Label: "a"}}}
		}},
		{"input modal already up", func(m *Model) {
			existing := newInputModalTextInput("existing", 64, m.availableWidth(), m.theme.Panel.GetHorizontalFrameSize())
			m.inputModal = &pendingInput{title: "already open", input: existing}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var submitted []string
			model := inputModalTestModel(t)
			tc.setup(&model)
			wasAlreadyUp := model.inputModal

			model = promptTestInput(model, alwaysValid, &submitted)

			require.Same(t, wasAlreadyUp, model.inputModal,
				"promptInput must be a no-op under this guard arm - it must neither clear an already-open modal nor replace it with a new one")
		})
	}
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

// TestInputModalEditKeystrokeClearsStaleError covers Task 6's #68 fix: an
// empty submit leaves errMsg ("name required") showing in-modal, and
// forwarding a normal edit keystroke to p.input.Update (updateInputModalKey's
// default branch) used to leave that stale error on screen even after the
// user started fixing it - implying an error that no longer applies to
// whatever's now in the field. Any further edit keystroke must clear it
// immediately, before the next validate/submit ever runs.
func TestInputModalEditKeystrokeClearsStaleError(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := promptTestInput(inputModalTestModel(t), alwaysValid, &submitted)

	model = updateWithKeyType(t, model, tea.KeyEnter) // empty submit
	require.Equal(t, "name required", model.inputModal.errMsg, "sanity: the stale error must be showing first")

	model = updateWithRunes(t, model, "a")

	require.Empty(t, model.inputModal.errMsg, "an edit keystroke must clear the stale error")
	require.NotContains(t, model.View(), "name required")
}

// TestInputModalNilValidateSkipsValidation covers input_modal.go's #68 nil-
// validate footgun: submitInputModal used to call p.validate(value)
// unconditionally on any non-empty value, so a pendingInput built without
// setting validate (the zero value - a nil func) panicked on the very first
// real submit. No existing caller happened to trip this (createProfilePrompt/
// importProfilePrompt/exportProfilePrompt all set validate, several to a
// no-op "always valid" closure purely to opt out) but a latent panic one
// missed field away beats a convention every caller must remember - nil now
// means the same thing explicitly: "no validation", and the value passes
// straight through to submit once the required-value check clears it.
func TestInputModalNilValidateSkipsValidation(t *testing.T) {
	t.Parallel()

	var submitted []string
	model := inputModalTestModel(t)
	input := newInputModalTextInput("profile name", 64, model.availableWidth(), model.theme.Panel.GetHorizontalFrameSize())
	model = model.promptInput(pendingInput{
		title: "Create Profile",
		input: input,
		// validate deliberately left nil - this is the case under test.
		submit: func(value string) tea.Cmd {
			submitted = append(submitted, value)
			return nil
		},
	})
	require.NotNil(t, model.inputModal)

	model = typeString(t, model, "survival")

	require.NotPanics(t, func() {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(Model)
	})

	require.Equal(t, []string{"survival"}, submitted)
	require.Nil(t, model.inputModal)
}

// TestInputModalOverlongHintTruncatesToWidth covers Task 6's #68 fix:
// inputModalView's hint line was rendered without the truncate() call its
// sibling lines (title, input, errMsg) already use. lipgloss word-wraps
// (rather than horizontally overflows) a line wider than the panel's set
// Width, so an overlong hint (importProfilePrompt/exportProfilePrompt can
// set an arbitrary caller string here) silently grew the modal past its
// fixed-height budget instead of clipping to one line like every other line
// does - this is the same "exact-height invariant" the rest of the package's
// modal views hold (see e.g. TestActionModalHeightInvariantAt80x24AndWidthFloor40
// in actions_test.go). A small terminal makes the overflow unmissable: a
// 300-char hint word-wraps into several lines at this width, which used to
// push screenView() well past availableContentHeight.
func TestInputModalOverlongHintTruncatesToWidth(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 40, 10)
	input := newInputModalTextInput("path", 256, model.availableWidth(), model.theme.Panel.GetHorizontalFrameSize())
	model = model.promptInput(pendingInput{
		title:    "Import Profile",
		input:    input,
		hint:     strings.Repeat("x", 300),
		validate: alwaysValid,
		submit:   func(string) tea.Cmd { return nil },
	})
	require.NotNil(t, model.inputModal)

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(), "exact-height invariant")
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), model.availableWidth(), "no rendered line exceeds terminal width")
	}
}
