package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// clampLines is the shared "+N more" tail-collapse primitive (the idiom
// helpView and actionModalView each hand-roll today). These tests pin the
// budget contract every screen view will rely on.
func TestClampLines(t *testing.T) {
	t.Parallel()
	m := sizedPrototypeModel(t, "wizardry", 80, 24)
	lines := []string{"a", "b", "c", "d", "e"}

	require.Equal(t, lines, m.clampLines(lines, 5), "exact fit is untouched")
	require.Equal(t, lines, m.clampLines(lines, 9), "surplus budget is untouched")
	require.Nil(t, m.clampLines(lines, 0), "zero budget renders nothing")

	got := m.clampLines(lines, 3)
	require.Len(t, got, 3)
	require.Equal(t, []string{"a", "b"}, got[:2])
	require.Contains(t, got[2], "+3 more")

	got = m.clampLines(lines, 1)
	require.Len(t, got, 1)
	require.Contains(t, got[0], "+5 more", "budget 1 is only the indicator")
}

// windowedRows must keep the selected row visible at any budget and never
// exceed the budget — including budgets below pickerWindow's practical
// floor of 3, where indicator rows would themselves cause overflow.
func TestWindowedRows(t *testing.T) {
	t.Parallel()
	m := sizedPrototypeModel(t, "wizardry", 80, 24)
	render := func(i int) string { return fmt.Sprintf("row-%d", i) }

	t.Run("all fit, no indicators", func(t *testing.T) {
		got := m.windowedRows(4, 2, 6, render)
		require.Equal(t, []string{"row-0", "row-1", "row-2", "row-3"}, got)
	})

	t.Run("selection past the fold stays visible", func(t *testing.T) {
		got := m.windowedRows(20, 19, 5, render)
		require.LessOrEqual(t, len(got), 5)
		require.Contains(t, strings.Join(got, "\n"), "row-19")
		require.Contains(t, got[0], "more", "clipped rows above are named")
	})

	t.Run("selection at the top", func(t *testing.T) {
		got := m.windowedRows(20, 0, 5, render)
		require.LessOrEqual(t, len(got), 5)
		require.Contains(t, got[0], "row-0")
		require.Contains(t, got[len(got)-1], "more", "clipped rows below are named")
	})

	t.Run("tiny budgets never overflow", func(t *testing.T) {
		for budget := 1; budget <= 2; budget++ {
			for selected := 0; selected < 6; selected++ {
				got := m.windowedRows(6, selected, budget, render)
				require.LessOrEqual(t, len(got), budget, "budget %d selected %d", budget, selected)
				require.Contains(t, strings.Join(got, "\n"), fmt.Sprintf("row-%d", selected))
			}
		}
	})

	t.Run("zero budget renders nothing", func(t *testing.T) {
		require.Nil(t, m.windowedRows(6, 3, 0, render))
	})
}
