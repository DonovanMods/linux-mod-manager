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
// or submit, only to read and close. lines are plain display rows;
// truncation happens at render time against the current width (see
// overlayView), not here, matching every other modal's convention.
type infoOverlay struct {
	title string
	lines []string
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
// toggle's open and close halves; quit keys still quit, via
// isQuitKey (actions.go) - unlike updateInputModalKey, which matches only
// "ctrl+c" so a plain "q" stays typeable in its text field, the overlay has
// no text entry at all, so a plain "q" quitting (isQuitKey's ordinary,
// non-focused-search-input behavior) matches every other list screen; every
// other key is swallowed so nothing behind the overlay can react to it.
// Invariant this relies on: the overlay is only ever opened from Installed
// Mods (Task 4's showDeployedFiles, mutations.go, guards on m.screen ==
// ScreenInstalledMods) - the search input only ever focuses on ScreenSearch
// (gotoScreenFocused) - so it can never be focused while the overlay is up,
// meaning a plain "q" here always quits reliably, never types into a field.
func (m Model) updateOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.isQuitKey(msg):
		return m.startQuit()
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

// overlayView renders the pending info overlay as a bordered panel that
// REPLACES the screen content, mirroring pickerView/inputModalView's
// approach (see actionModalView's doc comment for why: it preserves the
// exact-height render invariant every screen holds without an overlay
// needing its own height bookkeeping). Unlike pickerView's scroll-follow-
// selection option list, the overlay is read-only and, deliberately
// (YAGNI - file lists are typically short), not scrollable: when o.lines
// overflows the panel's budget, it is capped exactly like actionModalView's
// detail lines - the first N-1 lines that fit, plus a dimmed "+N more" tail
// line naming the overflow.
func (m Model) overlayView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	panelContentHeight := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	o := m.overlay
	lines := []string{truncate(m.theme.PanelTitle.Render(o.title), panelContentWidth)}

	// Fixed lines: title, blank separator, hint (3 total) - the same
	// accounting pickerView/actionModalView use. Whatever vertical room
	// remains is the budget for o.lines.
	const fixedLines = 3
	budget := max(panelContentHeight-fixedLines, 1)

	if len(o.lines) > budget {
		shown := max(budget-1, 0)
		more := len(o.lines) - shown
		for _, line := range o.lines[:shown] {
			lines = append(lines, truncate(line, panelContentWidth))
		}
		lines = append(lines, m.theme.MutedText.Render(fmt.Sprintf("+%d more", more)))
	} else {
		for _, line := range o.lines {
			lines = append(lines, truncate(line, panelContentWidth))
		}
	}

	lines = append(lines, "", m.theme.MutedText.Render("esc close"))

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
