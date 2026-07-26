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
//
// #68: the overflow indicator assertions below name the EXACT digit each
// "↑/↓ N more" line must carry, not just the substring "more" (the
// pre-existing check, which would have passed just as happily with the
// wrong count). The expected N is computed via pickerWindow itself - already
// covered as a pure function by TestPickerWindow below - so this test's job
// is narrower and different: proving pickerView actually threads
// pickerWindow's start/windowSize into its fmt.Sprintf calls correctly,
// not re-deriving the windowing math by hand (which would just duplicate
// pickerWindow's own doc comment in test form and drift the moment either
// changed).
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

	panelContentHeight := max(model.availableContentHeight()-model.theme.Panel.GetVerticalBorderSize(), 1)
	budget := max(panelContentHeight-3, 1) // 3 == pickerView's fixedLines (title, blank, hint)

	view := model.pickerView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
		"picker must never render taller than the content budget")
	require.Contains(t, view, "alpha-01", "window starts at the selection")
	require.NotContains(t, view, "alpha-20", "options past the window are clipped")

	startAtTop, sizeAtTop := pickerWindow(len(options), 0, budget)
	require.Zero(t, startAtTop, "sanity: selection 0 must window from the very top")
	require.Contains(t, view, fmt.Sprintf("↓ %d more", len(options)-(startAtTop+sizeAtTop)),
		"the below-window indicator must name the exact clipped count")

	for range len(options) - 1 {
		model = updateWithRunes(t, model, "j")
	}
	require.Equal(t, len(options)-1, model.picker.selected)

	view = model.pickerView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
		"scrolled picker must never render taller than the content budget")
	require.Contains(t, view, "alpha-20", "moving below the window scrolls the selection into view")
	require.NotContains(t, view, "alpha-01", "options scrolled above the window are clipped")

	startAtBottom, _ := pickerWindow(len(options), len(options)-1, budget)
	require.Contains(t, view, fmt.Sprintf("↑ %d more", startAtBottom),
		"the above-window indicator must name the exact clipped count")
}

// TestPickerWindow directly tables pickerWindow's (n, selected, budget) ->
// (start, windowSize) contract - see its own doc comment for the invariants
// this locks in: the everything-fits fast path, the reserved-two-rows
// windowing once n exceeds budget, scroll-follow-selection clamped to the
// list bounds at both edges, and the budget<3 edge clamp.go's windowedRows
// doc comment calls out (pickerWindow itself has no budget>=3 floor -
// windowedRows is the one caller that special-cases budget<3 by asking for
// budget+2 instead - so pickerWindow must still degrade sanely, not panic
// or misbehave, when called directly with a budget below that "in practice"
// floor).
func TestPickerWindow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		n, selected, budget  int
		wantStart, wantWSize int
	}{
		{"empty list", 0, 0, 10, 0, 0},
		{"fits with room to spare", 5, 2, 10, 0, 5},
		{"fits exactly at budget", 5, 2, 5, 0, 5},
		{"overflow, selection at first row", 20, 0, 10, 0, 8},
		{"overflow, selection at last row", 20, 19, 10, 12, 8},
		{"overflow, selection centered", 20, 10, 10, 7, 8},
		{"overflow, selection just past window start clamp", 20, 2, 10, 0, 8},
		{"overflow, selection just before window end clamp", 20, 16, 10, 12, 8},
		{"overflow, small excess over budget", 9, 4, 8, 2, 6},
		{"budget of exactly 3 - reserved rows leave one option row", 20, 5, 3, 5, 1},
		{"budget below 3 still clamps to one option row", 20, 5, 2, 5, 1},
		{"budget of 1 still clamps to one option row", 20, 0, 1, 0, 1},
		{"budget<3, selection clamped at the end", 20, 19, 3, 19, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			start, windowSize := pickerWindow(tc.n, tc.selected, tc.budget)
			require.Equal(t, tc.wantStart, start, "start")
			require.Equal(t, tc.wantWSize, windowSize, "windowSize")
			if tc.n > tc.budget {
				require.LessOrEqual(t, start+windowSize, tc.n, "window must never run past the list end")
			}
		})
	}
}
