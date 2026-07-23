package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// pickerTestModel builds a fully-loaded, sized prototype Model with no
// pending action/picker - the common starting point for every test below.
func pickerTestModel(t *testing.T) Model {
	t.Helper()
	return sizedPrototypeModel(t, "wizardry", 100, 30)
}

// promptTestPicker attaches a 3-option picker to model via promptPicker,
// recording every choose(idx) call in *chosen (appended, so a test can
// assert both "was it called" and "with what index").
func promptTestPicker(model Model, chosen *[]int) Model {
	return model.promptPicker(pendingPicker{
		title: "Pick one",
		options: []pickerOption{
			{Label: "First", Note: "one"},
			{Label: "Second", Note: "two"},
			{Label: "Third", Note: "three"},
		},
		choose: func(idx int) tea.Cmd {
			*chosen = append(*chosen, idx)
			return nil
		},
	})
}

func TestPickerNavigateAndChoose(t *testing.T) {
	t.Parallel()

	var chosen []int
	model := promptTestPicker(pickerTestModel(t), &chosen)
	require.NotNil(t, model.picker)

	model = updateWithRunes(t, model, "j")
	model = updateWithRunes(t, model, "j")
	model = updateWithKeyType(t, model, tea.KeyEnter)

	require.Equal(t, []int{2}, chosen)
	require.Nil(t, model.picker)
}

func TestPickerDigitQuickSelect(t *testing.T) {
	t.Parallel()

	var chosen []int
	model := promptTestPicker(pickerTestModel(t), &chosen)

	model = updateWithRunes(t, model, "2")

	require.Equal(t, []int{1}, chosen)
	require.Nil(t, model.picker)
}

func TestPickerEscCancels(t *testing.T) {
	t.Parallel()

	var chosen []int
	model := promptTestPicker(pickerTestModel(t), &chosen)

	model = updateWithKeyType(t, model, tea.KeyEsc)

	require.Empty(t, chosen)
	require.Nil(t, model.picker)
}

func TestPickerBlockedWhileActionPending(t *testing.T) {
	t.Parallel()

	var chosen []int
	model := pickerTestModel(t)
	model.action.pending = &pendingAction{title: "Some action"}

	model = promptTestPicker(model, &chosen)

	require.Nil(t, model.picker)
}
