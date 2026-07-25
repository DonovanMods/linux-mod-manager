package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// infoOverlay is a caller-built description of a read-only info panel
// awaiting dismissal, mirroring pendingPicker/pendingInput's role for their
// own modals (see picker.go/input_modal.go) - simpler than either: there is
// no choose/submit callback, since there's nothing for the user to choose
// or submit, only to read, scroll, and close. lines are plain display rows;
// truncation happens at render time against the current width (see
// overlayView), not here, matching every other modal's convention.
type infoOverlay struct {
	title string
	lines []string
	// offset is the index of the first visible lines entry when the overlay
	// is taller than the panel (Task 7's fix wave: the changelog viewer
	// retains FULL untruncated changelog text - see UpdateItem.Changelog -
	// so overflow must be reachable by scrolling, not clipped forever
	// behind a static "+N more" tail the way the pre-scroll overlay did).
	// Adjusted by updateOverlayKey's Up/Down cases, clamped there against
	// overlayMaxOffset and again defensively at render time (overlayView);
	// always 0 for an overlay that fully fits.
	offset int
}

// promptOverlay shows o as the info overlay: this is the only method that
// sets Model.overlay. Guarded like promptPicker/promptInput (single-flight):
// while a confirmation modal, picker, input modal, or another overlay is
// already up, the request is a no-op. Unlike promptPicker/promptInput, this
// deliberately does NOT check m.action.running - the overlay is read-only,
// so it's safe to open even while a mutation is still in flight.
func (m Model) promptOverlay(o infoOverlay) Model {
	if m.action.pending != nil || m.picker != nil || m.inputModal != nil || m.overlay != nil {
		return m
	}
	m.overlay = &o
	return m
}

// updateOverlayKey handles every key while the info overlay is shown: esc
// (Blur) or the Files binding closes it - two close keys because Task 4
// also binds Files ("f") to OPEN the overlay from a list screen, and
// closing on the same key it opened with is the expected toggle feel; the
// close side matches m.keys.Files rather than a hard-coded "f" (Copilot PR
// #69 finding) so a custom KeyMap remapping Files can never desync the
// toggle's open and close halves; Up/Down (and their k/j aliases) scroll
// the visible window when the overlay's lines overflow the panel (Task 7's
// fix wave - see infoOverlay.offset), clamped at both ends and a no-op for
// an overlay that fully fits; quit keys still quit, via
// isQuitKey (actions.go) - unlike updateInputModalKey, which matches only
// "ctrl+c" so a plain "q" stays typeable in its text field, the overlay has
// no text entry at all, so a plain "q" quitting (isQuitKey's ordinary,
// non-focused-search-input behavior) matches every other list screen; every
// other key is swallowed so nothing behind the overlay can react to it.
// Invariant this relies on: the overlay only ever opens from Installed
// Mods' Files binding (Task 4's showDeployedFiles, mutations.go, guards on
// m.screen == ScreenInstalledMods) or on top of the apply-updates modal
// (Task 7's changelog viewer) - the search input only ever focuses on
// ScreenSearch (gotoScreenFocused) - so it can never be focused while the
// overlay is up, meaning a plain "q" here always quits reliably, never
// types into a field.
func (m Model) updateOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.isQuitKey(msg):
		return m.startQuit()
	case key.Matches(msg, m.keys.Up):
		if m.overlay.offset > 0 {
			m.overlay.offset--
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.overlay.offset < m.overlayMaxOffset() {
			m.overlay.offset++
		}
		return m, nil
	case key.Matches(msg, m.keys.Blur):
		m.overlay = nil
		return m, nil
	case key.Matches(msg, m.keys.Files):
		m.overlay = nil
		return m, nil
	default:
		return m, nil
	}
}

// overlayLineBudget returns the number of rows currently available for the
// overlay's lines (and any scroll-indicator rows), from the same accounting
// overlayView renders with: panel content height minus the 3 fixed lines
// (title, blank separator, hint) - shared by the scroll clamp
// (overlayMaxOffset) and the renderer so the two can never disagree about
// where the window ends.
func (m Model) overlayLineBudget() int {
	panelContentHeight := max(m.availableContentHeight()-m.theme.Panel.GetVerticalBorderSize(), 1)
	const fixedLines = 3
	return max(panelContentHeight-fixedLines, 1)
}

// overlayMaxOffset returns the largest valid infoOverlay.offset for the
// current overlay at the current panel size: 0 when everything fits (no
// scrolling), otherwise the offset that puts the LAST line at the bottom of
// the window - updateOverlayKey's Down case clamps against this so
// scrolling can never walk past the end.
func (m Model) overlayMaxOffset() int {
	n := len(m.overlay.lines)
	budget := m.overlayLineBudget()
	if n <= budget {
		return 0
	}
	return n - overlayWindowSize(budget)
}

// overlayWindowSize returns how many overlay lines render inside budget rows
// when the list overflows: two rows are reserved for the "↑/↓ N more"
// indicator lines - unconditionally, exactly like pickerWindow's own
// reservation (see that function's doc comment), so total rendered lines can
// never exceed budget regardless of which edge the window touches; a window
// at an edge simply renders one fewer line.
func overlayWindowSize(budget int) int {
	return max(budget-2, 1)
}

// overlayView renders the pending info overlay as a bordered panel that
// REPLACES the screen content, mirroring pickerView/inputModalView's
// approach (see actionModalView's doc comment for why: it preserves the
// exact-height render invariant every screen holds without an overlay
// needing its own height bookkeeping). When o.lines overflows the panel's
// budget, the overflow is clipped to a scrollable window positioned by
// o.offset (Task 7's fix wave - see infoOverlay.offset; previously a static
// "+N more" tail with no way to reach the clipped lines), with dimmed
// "↑ N more"/"↓ N more" indicator lines naming whatever is clipped
// above/below - pickerView's indicator style, driven by an explicit scroll
// offset rather than a followed selection.
func (m Model) overlayView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)

	o := m.overlay
	lines := []string{truncate(m.theme.PanelTitle.Render(o.title), panelContentWidth)}

	budget := m.overlayLineBudget()
	hint := "esc close"

	if len(o.lines) > budget {
		windowSize := overlayWindowSize(budget)
		// Defensive re-clamp: updateOverlayKey already clamps against
		// overlayMaxOffset, but a resize AFTER scrolling can shrink the
		// valid range out from under a stored offset.
		start := min(max(o.offset, 0), len(o.lines)-windowSize)

		if start > 0 {
			lines = append(lines, m.theme.MutedText.Render(fmt.Sprintf("↑ %d more", start)))
		}
		for _, line := range o.lines[start : start+windowSize] {
			lines = append(lines, truncate(line, panelContentWidth))
		}
		if below := len(o.lines) - (start + windowSize); below > 0 {
			lines = append(lines, m.theme.MutedText.Render(fmt.Sprintf("↓ %d more", below)))
		}
		hint = "↑/↓ scroll · esc close"
	} else {
		for _, line := range o.lines {
			lines = append(lines, truncate(line, panelContentWidth))
		}
	}

	lines = append(lines, "", m.theme.MutedText.Render(hint))

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
