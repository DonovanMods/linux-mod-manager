package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// contextContent is a pluggable full-screen content view that any screen can
// push over itself (#224, generalized in #86). One-deep by design (YAGNI):
// push replaces any pushed content, esc pops, and navigating to another
// screen pops implicitly. The pushing screen stays the current screen
// throughout - the nav bar keeps highlighting it - so a mod details view
// opened from Installed Mods reads as "Installed Mods, showing a mod",
// not as a jump to some other screen.
type contextContent interface {
	Title() string
	// Lines renders the body for the given content box; the host owns
	// chrome (panel, title, nav) and clamping.
	Lines(width, height int) []string
	// HandleKey lets pushed content consume a key before the outer
	// switch; handled=false means the host swallows it (see updateKey) -
	// it does NOT fall through to the screen underneath.
	HandleKey(msg tea.KeyMsg) (next contextContent, cmd tea.Cmd, handled bool)
	HelpGroup() helpGroup
}

// pushContext replaces the current pushed content (one-deep by design) with
// c. The session's screen is deliberately NOT changed: screenView renders
// pushed content ahead of the per-screen switch, so the content appears over
// whatever screen the user is on and esc simply returns them to it. Pointer
// receiver so updateKey handlers can call it directly.
func (m *Model) pushContext(c contextContent) {
	m.contextContent = c
}

// popContext clears the pushed content, revealing the screen underneath. A
// no-op when nothing is pushed, so callers need no guard.
func (m *Model) popContext() {
	m.contextContent = nil
}

// contextView renders pushed content full-screen: the host owns the panel,
// the title line, truncation, and clamping, so content views stay small.
// Moved here from healthScreenView in #86, since the host is no longer
// Health's private machinery.
func (m Model) contextView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	contentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	lines := []string{m.theme.PanelTitle.Render(m.contextContent.Title())}
	lines = append(lines, m.contextContent.Lines(contentWidth, max(contentBudget-1, 1))...)
	lines = m.truncateLines(lines, contentWidth)
	lines = m.clampLines(lines, contentBudget)

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
