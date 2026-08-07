package tui

import tea "github.com/charmbracelet/bubbletea"

// contextContent is a pluggable full-screen content view hosted by
// ScreenHealth (#224). #86's mod-details view will be the second
// implementation. One-deep stack by design (YAGNI): push replaces any
// pushed content; esc pops back to the pushing screen; with nothing
// pushed, ScreenHealth renders the health home view.
type contextContent interface {
	Title() string
	// Lines renders the body for the given content box; the host owns
	// chrome (panel, title, nav) and clamping.
	Lines(width, height int) []string
	// HandleKey lets pushed content consume a key before the outer
	// switch; handled=false falls through to global handling.
	HandleKey(msg tea.KeyMsg) (next contextContent, cmd tea.Cmd, handled bool)
	HelpGroup() helpGroup
}

// pushContext replaces the current pushed content (one-deep by design - see
// contextContent's own doc comment) with c, remembering from as the screen
// esc should pop back to, and jumps the session to ScreenHealth so the
// pushed content actually renders. Pointer receiver (unlike the rest of
// Model's value-receiver methods) so callers on an addressable Model value
// (every updateKey handler) can call it directly without threading a
// returned Model back through - mirrors popContext's own shape below.
func (m *Model) pushContext(c contextContent, from Screen) {
	m.contextContent = c
	m.contextReturn = from
	m.screen = ScreenHealth
}

// popContext clears the pushed content and returns the session to
// contextReturn (the screen that pushed it), reporting that screen back to
// the caller. A no-op (returns the CURRENT screen unchanged) when nothing is
// pushed - callers don't need to guard the call themselves.
func (m *Model) popContext() Screen {
	if m.contextContent == nil {
		return m.screen
	}
	target := m.contextReturn
	m.contextContent = nil
	m.contextReturn = ScreenDashboard
	m.screen = target
	return target
}
