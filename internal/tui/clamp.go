package tui

import "fmt"

// clampLines returns lines unchanged when they fit budget; otherwise the
// first budget-1 lines plus a muted "+N more" tail, so the result can never
// exceed budget lines. This is the tail-collapse idiom helpView (app.go)
// and actionModalView (actions.go) established, extracted so every screen
// view can honor its height budget the same way: lipgloss's Height() pads
// short content but never clips tall content, so any view that renders more
// lines than its panel's budget silently grows past the terminal (#42).
// Contract: when lines already fits budget, the returned slice IS lines
// (the same backing array, not a copy) — callers must not mutate the
// result in place unless they also own the input slice.
func (m Model) clampLines(lines []string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	if len(lines) <= budget {
		return lines
	}
	keep := budget - 1
	clamped := make([]string, 0, budget)
	clamped = append(clamped, lines[:keep]...)
	return append(clamped, m.theme.MutedText.Render(fmt.Sprintf("+%d more", len(lines)-keep)))
}

// windowedRows renders an n-row selectable list clipped to budget total
// lines using pickerWindow's scroll-follow-selection windowing, so the
// selected row is always visible and highlighted no matter how far past
// the fold it is navigated — unlike a first-N slice, where the highlight
// simply vanishes off-screen (#42). render(i) receives the ABSOLUTE row
// index, so m.row(i, ...)'s selected-index comparison keeps working
// unchanged. Clipped edges are named with "↑/↓ N more" indicator lines,
// except at budgets below 3 where the indicators themselves would overflow
// the budget: there the window shrinks to exactly budget rows centered on
// the selection instead — still honest, just unlabeled.
func (m Model) windowedRows(n, selected, budget int, render func(i int) string) []string {
	if budget <= 0 || n <= 0 {
		return nil
	}
	if n <= budget {
		rows := make([]string, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, render(i))
		}
		return rows
	}
	if budget < 3 {
		// pickerWindow unconditionally reserves two of its budget rows for
		// the "↑/↓ N more" indicators, which this branch does not emit, so
		// asking it for budget+2 yields the same centered, clamped window
		// of exactly budget rows without duplicating its math. One wrinkle:
		// when n <= budget+2 its everything-fits fast path hands back the
		// whole list, which would overflow here (n > budget in this
		// branch), so re-center on the selection in that case.
		start, windowSize := pickerWindow(n, selected, budget+2)
		if windowSize > budget {
			windowSize = budget
			start = min(max(selected-(windowSize-1)/2, 0), n-windowSize)
		}
		rows := make([]string, 0, windowSize)
		for i := start; i < start+windowSize; i++ {
			rows = append(rows, render(i))
		}
		return rows
	}
	start, windowSize := pickerWindow(n, selected, budget)
	rows := make([]string, 0, windowSize+2)
	if start > 0 {
		rows = append(rows, m.theme.MutedText.Render(fmt.Sprintf("↑ %d more", start)))
	}
	for i := start; i < start+windowSize; i++ {
		rows = append(rows, render(i))
	}
	if below := n - (start + windowSize); below > 0 {
		rows = append(rows, m.theme.MutedText.Render(fmt.Sprintf("↓ %d more", below)))
	}
	return rows
}

// truncateLines truncates every line to width in place, mutating and
// returning lines. Extracted from the ~6 near-identical per-line loops the
// dashboards/sourcesView/searchSinglePanel each hand-rolled: lipgloss's
// Width() re-wraps any line longer than a panel's content width into extra
// physical rows, which is invisible to clampLines' logical-line count, so
// every view that places dynamic content inside a Width()-constrained panel
// must truncate line-by-line first (ANSI-safe, display-width aware — see
// truncate's own doc comment) (#42).
func (m Model) truncateLines(lines []string, width int) []string {
	for i, line := range lines {
		lines[i] = truncate(line, width)
	}
	return lines
}
