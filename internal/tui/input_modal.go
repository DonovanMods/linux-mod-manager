package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// pendingInput is a caller-built description of a text-entry modal awaiting
// a value, mirroring pendingPicker's role for the list-choice modal (see
// picker.go) and pendingAction's for the confirm modal (see actions.go).
// validate is consulted only for a non-empty trimmed value (the empty case
// is handled directly by submitInputModal's own required-field check - see
// requiredMsg below - so callers need not special-case it); "" means the
// value is acceptable,
// any other string is shown in-modal as the error and keeps the modal open.
// submit is invoked exactly once, when the value passes both checks - never
// on cancel, and never while an error is showing.
type pendingInput struct {
	title  string
	input  textinput.Model
	errMsg string
	// hint overrides inputModalView's default "enter create · esc cancel"
	// footer - "" (the zero value) keeps that default, which every
	// pendingInput used until Task 9's importProfilePrompt introduced a
	// second, differently-worded submit ("enter import", not "enter
	// create"). Existing callers (createProfilePrompt) are unaffected by
	// simply never setting this.
	hint string
	// requiredMsg overrides submitInputModal's default "name required" error
	// for an empty submit - "" (the zero value) keeps that default, which is
	// right for a name field (createProfilePrompt, unaffected by simply
	// never setting this) but wrong for a path field: importProfilePrompt/
	// exportProfilePrompt set this to "path required" instead.
	requiredMsg string
	validate    func(value string) string // "" = ok, else error copy shown in-modal
	submit      func(value string) tea.Cmd
}

// newInputModalTextInput builds a textinput.Model configured for use inside
// a pendingInput, following the same construction pattern newSearchModel
// uses (search.go): charLimit (shorter than search's 120 for a short value
// like a profile name - see createProfilePrompt's own 64; a filesystem path
// needs considerably more room - see importProfilePrompt's own 256, Task 9)
// and Width derived from the current available width via
// searchInputWidthFor, so a value near the viewport width scrolls
// horizontally instead of word-wrapping inside the width-set modal panel.
func newInputModalTextInput(placeholder string, charLimit, availableWidth, panelHorizontalFrameSize int) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.CharLimit = charLimit
	input.Width = searchInputWidthFor(availableWidth, panelHorizontalFrameSize)
	return input
}

// promptInput shows p as the text-input modal: this is the only method that
// sets Model.inputModal, and it never calls submit itself - only
// updateInputModalKey does, on a successful validated submission. Guarded
// like promptPicker/promptAction (single-flight): while an action is
// running or pending, a picker is up, or an input modal is already up, the
// request is a no-op. The input is focused immediately, matching
// gotoScreenFocused's search-input focus (app.go) - a modal is only ever
// shown because the user asked for it, so it should be ready to type into
// without an extra keypress.
func (m Model) promptInput(p pendingInput) Model {
	if m.action.running || m.action.pending != nil || m.picker != nil || m.inputModal != nil {
		return m
	}
	p.input.Focus()
	m.inputModal = &p
	return m
}

// updateInputModalKey handles every key while the input modal is shown, and
// is the focused-input law applied to a modal: Select (enter) attempts a
// submission - trim the value, p.requiredMsg (or "name required", its
// default) if empty, else validate(value) (a non-empty result becomes errMsg
// and the modal stays open, exactly like an empty value), and on success
// clear the modal and return submit(value);
// Blur (esc) clears the modal without calling submit; ctrl+c quits (matched
// directly by string, NOT via m.keys.Quit, whose bound keys also include a
// plain "q" that must be typeable here - see the doc comment on isQuitKey's
// focused-search-input case in app.go, which this mirrors); every other key -
// including "q" - forwards to p.input.Update(msg), so the modal is a focused
// input in every sense: nothing typed here can leak through to a binding
// behind it.
func (m Model) updateInputModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.inputModal

	switch {
	case msg.String() == "ctrl+c":
		return m.startQuit()
	case key.Matches(msg, m.keys.Blur):
		m.inputModal = nil
		return m, nil
	case key.Matches(msg, m.keys.Select):
		return m.submitInputModal(p)
	default:
		// #68: a stale errMsg (from a previous failed submit) used to keep
		// showing here even after the user started editing the value it no
		// longer describes. Any edit keystroke clears it immediately, before
		// the input itself even sees the rune - the next Select re-derives a
		// fresh errMsg (or none) from validate/requiredMsg against whatever's
		// actually in the field now.
		p.errMsg = ""
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return m, cmd
	}
}

// submitInputModal runs p's validation chain against its current input
// value and either keeps the modal open with an errMsg (empty value, or a
// non-empty validate result) or clears the modal and dispatches p.submit -
// the single point updateInputModalKey's Select branch routes through, so
// "validate, then submit exactly once" stays true regardless of which key
// triggered it (mirroring choosePickerOption's role for the picker modal).
func (m Model) submitInputModal(p *pendingInput) (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(p.input.Value())
	if value == "" {
		p.errMsg = p.requiredMsg
		if p.errMsg == "" {
			p.errMsg = "name required"
		}
		return m, nil
	}
	if errMsg := p.validate(value); errMsg != "" {
		p.errMsg = errMsg
		return m, nil
	}
	m.inputModal = nil
	return m, p.submit(value)
}

// inputModalView renders the pending input as a bordered panel that
// REPLACES the screen content, mirroring pickerView/actionModalView's
// approach (see actionModalView's doc comment for why). Unlike those two,
// this modal's line count never varies with caller-supplied data (no
// option/detail list to budget for): title, input, an optional errMsg, a
// blank separator, and the hint is at most 5 lines, always. That fits with
// room to spare even at the smallest terminal this ever renders at:
// availableContentHeight's floor is 8, and panelWithHeight subtracts the
// panel's vertical border size (2, top+bottom) from whatever height it's
// given, leaving a content floor of 6 - so no scroll/clip handling is
// needed the way pickerView's option list requires.
func (m Model) inputModalView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)

	p := m.inputModal
	lines := []string{
		truncate(m.theme.PanelTitle.Render(p.title), panelContentWidth),
		truncate(p.input.View(), panelContentWidth),
	}
	if p.errMsg != "" {
		lines = append(lines, truncate(m.theme.DangerText.Render(p.errMsg), panelContentWidth))
	}
	hint := p.hint
	if hint == "" {
		hint = "enter create · esc cancel"
	}
	// #68: unlike title/input/errMsg above, this line was never truncated -
	// an overlong caller-supplied hint (importProfilePrompt/
	// exportProfilePrompt set an arbitrary string here) word-wraps inside the
	// fixed-Width panel instead of overflowing horizontally, silently growing
	// the modal past its documented fixed-height budget. truncate() to the
	// same panelContentWidth its siblings already use keeps this modal
	// exactly as tall as every other modal, regardless of hint length.
	lines = append(lines, "", truncate(m.theme.MutedText.Render(hint), panelContentWidth))

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
