package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// TestPickerPlainNDoesNotCancel locks in the Blur-not-CancelAction matching
// choice (see updatePickerKey's doc comment): CancelAction's bound keys
// include a plain "n", which a picker's option labels may legitimately
// start with, so pressing it must neither cancel the picker nor choose
// anything.
func TestPickerPlainNDoesNotCancel(t *testing.T) {
	t.Parallel()

	var chosen []int
	model := promptTestPicker(pickerTestModel(t), &chosen)

	model = updateWithRunes(t, model, "n")

	require.NotNil(t, model.picker, "plain n must not cancel the picker")
	require.Empty(t, chosen, "plain n must not choose anything")
}

// TestPickerHeightCappedWithScrollWindow pins the exact-height render
// invariant for pickers taller than the panel: the rendered modal never
// exceeds availableContentHeight() (a small terminal forces the 8-line
// content floor here), and moving the selection below the visible window
// scrolls it into view rather than clipping it away.
func TestPickerHeightCappedWithScrollWindow(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 12) // forces the 8-line content floor
	options := make([]pickerOption, 20)
	for i := range options {
		options[i] = pickerOption{Label: fmt.Sprintf("alpha-%02d", i+1)}
	}
	model = model.promptPicker(pendingPicker{
		title:   "Pick one",
		options: options,
		choose:  func(int) tea.Cmd { return nil },
	})
	require.NotNil(t, model.picker)

	view := model.pickerView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
		"picker must never render taller than the content budget")
	require.Contains(t, view, "alpha-01", "window starts at the selection")
	require.NotContains(t, view, "alpha-20", "options past the window are clipped")
	require.Contains(t, view, "more", "clipped rows are named by an indicator line")

	for range len(options) - 1 {
		model = updateWithRunes(t, model, "j")
	}
	require.Equal(t, len(options)-1, model.picker.selected)

	view = model.pickerView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
		"scrolled picker must never render taller than the content budget")
	require.Contains(t, view, "alpha-20", "moving below the window scrolls the selection into view")
	require.NotContains(t, view, "alpha-01", "options scrolled above the window are clipped")
}
