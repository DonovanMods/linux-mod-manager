package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// pickerOption is one selectable row in a pendingPicker: Label is the
// primary text, Note is optional dimmed detail rendered to its right (e.g.
// a policy's current value, or a profile's mod count).
type pickerOption struct{ Label, Note string }

// pendingPicker is a caller-built description of a list-choice modal
// awaiting a selection, mirroring pendingAction's role for the confirm
// modal (see actions.go). choose is invoked exactly once, when the user
// selects a row (Select/enter or a digit quick-select) - never on cancel.
type pendingPicker struct {
	title    string
	options  []pickerOption
	selected int
	choose   func(idx int) tea.Cmd
}

// promptPicker shows p as the picker modal: this is the only method that
// sets Model.picker, and it never calls choose itself - only
// updatePickerKey does, on selection. Guarded like promptAction/buildAction
// (single-flight): while an action is running or a confirmation modal is
// already pending, or a picker is already up, the request is a no-op.
func (m Model) promptPicker(p pendingPicker) Model {
	if m.action.running || m.action.pending != nil || m.picker != nil {
		return m
	}
	m.picker = &p
	return m
}

// updatePickerKey handles every key while the picker modal is shown:
// Up/Down move the selection (clamped to the option list); a digit 1-9
// immediately selects and chooses that option (when in range); Select
// (enter) chooses the currently-selected row; Blur (esc) - matched
// directly rather than via CancelAction, whose bound keys include a plain
// "n" that a picker's option labels may legitimately start with -
// cancels without choosing; quit keys still quit (via startQuit); every
// other key is swallowed so nothing behind the modal can react to it.
func (m Model) updatePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.startQuit()
	case key.Matches(msg, m.keys.Up):
		if p.selected > 0 {
			p.selected--
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if p.selected < len(p.options)-1 {
			p.selected++
		}
		return m, nil
	case key.Matches(msg, m.keys.Select):
		return m.choosePickerOption(p.selected)
	case key.Matches(msg, m.keys.Blur):
		m.picker = nil
		return m, nil
	default:
		if idx, ok := digitQuickSelect(msg, len(p.options)); ok {
			return m.choosePickerOption(idx)
		}
		return m, nil
	}
}

// choosePickerOption clears the picker and invokes its choose callback with
// idx - the single point both Select and a digit quick-select route
// through, so "choose exactly once, then close" stays true regardless of
// which key triggered it.
func (m Model) choosePickerOption(idx int) (tea.Model, tea.Cmd) {
	p := m.picker
	m.picker = nil
	return m, p.choose(idx)
}

// digitQuickSelect reports whether msg is a single digit "1"-"9" naming a
// valid index (0-based) into an option list of length n, for updatePickerKey's
// quick-select branch.
func digitQuickSelect(msg tea.KeyMsg, n int) (int, bool) {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return 0, false
	}
	r := msg.Runes[0]
	if r < '1' || r > '9' {
		return 0, false
	}
	idx := int(r - '1')
	if idx >= n {
		return 0, false
	}
	return idx, true
}

// pickerView renders the pending picker as a bordered panel that REPLACES
// the screen content, mirroring actionModalView's approach (see that
// method's doc comment for why: it preserves the exact-height render
// invariant every screen holds without an overlay needing its own height
// bookkeeping).
func (m Model) pickerView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)

	p := m.picker
	lines := []string{truncate(m.theme.PanelTitle.Render(p.title), panelContentWidth)}

	for i, opt := range p.options {
		marker := "  "
		if i == p.selected {
			marker = "> "
		}
		row := marker + opt.Label
		if opt.Note != "" {
			row += "  " + m.theme.MutedText.Render(opt.Note)
		}
		row = truncate(row, panelContentWidth)
		if i == p.selected {
			row = m.theme.Selected.Render(row)
		}
		lines = append(lines, row)
	}

	lines = append(lines, "", m.theme.MutedText.Render("↑/↓ move · enter choose · esc cancel"))

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
