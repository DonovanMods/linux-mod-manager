package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// actionKind identifies which mutation a pendingAction/actionDoneMsg/
// actionFailedMsg represents, for callers (Task 7's keybindings) that branch
// on it — e.g. to route a completed switch's outcome differently from a
// completed uninstall.
type actionKind int

const (
	actionEnable actionKind = iota
	actionDisable
	actionUninstall
	actionDeploy
	actionSwitch
)

// pendingAction is a caller-built (Task 7) description of one mutation
// awaiting user confirmation. confirm is normally built by buildAction and
// is only ever invoked once, when the user confirms — see
// updatePendingActionKey.
type pendingAction struct {
	kind    actionKind
	title   string   // e.g. `Uninstall "SkyUI"?`
	detail  []string // affected game/profile/mods; Task 7 fills per action
	confirm func() tea.Cmd
}

// actionModel is the Model's mutation-machinery sub-state: the pending
// confirmation (if any), the single-flight guard, the staleness generation,
// the in-flight action's cancel func, and the last outcome/error rendered
// as the status line.
type actionModel struct {
	pending       *pendingAction
	running       bool
	gen           int
	cancel        context.CancelFunc
	status        string
	statusIsError bool
}

// actionDoneMsg carries a completed action's outcome, tagged with the
// generation established when the action was built (see buildAction) so a
// superseded result can be discarded.
type actionDoneMsg struct {
	gen     int
	kind    actionKind
	outcome ActionOutcome
}

// actionFailedMsg carries a failed action's error, tagged like
// actionDoneMsg.
type actionFailedMsg struct {
	gen  int
	kind actionKind
	err  error
}

// actionModalMaxDetailLines is the normal-case cap on how many detail lines
// the confirmation modal shows before collapsing the remainder into a
// "+N more" line (see actionModalView). actionModalView additionally derives
// a dynamic floor from the actual panel height, so an unusually short
// terminal can't grow the modal past its height budget even though this
// constant alone wouldn't prevent that.
const actionModalMaxDetailLines = 8

// buildAction constructs the pendingAction a caller (Task 7's keybinding
// handlers) passes to promptAction. It owns the two pieces of bookkeeping
// this task is responsible for:
//
//   - A cancelable context derived from m.ctx, stored as action.cancel so
//     quit (see quitCmd) or a later action can tear it down. Any cancel func
//     already there — e.g. a still-running action nobody's message has
//     arrived for — is cancelled first, matching actionModel.cancel's
//     "cancelled ... on new action" contract.
//   - The gen tag actionDoneMsg/actionFailedMsg carry, baked into the
//     confirm closure here (at build/show time) rather than inside
//     updatePendingActionKey's confirm branch. confirm is an opaque
//     func() tea.Cmd built once by this method, so build time is the only
//     point that can attach a consistent gen to whatever message the
//     eventual call produces. Nothing can bump action.gen between a
//     pendingAction being built and it being confirmed — promptAction's own
//     single-flight guard blocks any other action from starting first — so
//     the gen captured here is still current at confirm time: the
//     observable contract ("a stale gen is discarded entirely") holds
//     regardless of the exact instant the bump happens.
//
// Single-flight: buildAction applies the same guard promptAction does
// (running or already pending) and, if it fails, leaves m and its gen/cancel
// state completely untouched, returning a no-op pendingAction instead. This
// means a caller that builds an action before calling promptAction (the
// normal two-step usage) can never corrupt an in-flight action's bookkeeping
// even if it forgets to check the guard itself — promptAction's own refusal
// to show the no-op pendingAction is just belt-and-suspenders on top.
func (m Model) buildAction(kind actionKind, title string, detail []string, do func(context.Context) (ActionOutcome, error)) (Model, pendingAction) {
	if m.action.running || m.action.pending != nil {
		return m, pendingAction{kind: kind, title: title, detail: detail, confirm: func() tea.Cmd { return nil }}
	}

	if m.action.cancel != nil {
		m.action.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.action.cancel = cancel
	m.action.gen++
	gen := m.action.gen

	pa := pendingAction{
		kind:   kind,
		title:  title,
		detail: detail,
		confirm: func() tea.Cmd {
			return func() tea.Msg {
				outcome, err := do(ctx)
				if err != nil {
					return actionFailedMsg{gen: gen, kind: kind, err: err}
				}
				return actionDoneMsg{gen: gen, kind: kind, outcome: outcome}
			}
		},
	}
	return m, pa
}

// promptAction shows pa as the confirmation modal (rule 1): this is the only
// method that sets action.pending, and it never calls the ActionProvider
// itself — nothing mutates until confirm (see updatePendingActionKey).
// While an action is already running, or a confirmation is already up, the
// request is ignored (single-flight) rather than queued or replacing what's
// already shown.
func (m Model) promptAction(pa pendingAction) Model {
	if m.action.running || m.action.pending != nil {
		return m
	}
	m.action.pending = &pa
	return m
}

// updatePendingActionKey handles every key while a confirmation modal is
// shown: y/enter confirms (dispatch pa.confirm(), mark running, clear
// pending — see buildAction's doc comment for why gen/cancel are already
// established by the time this runs, rather than bumped here); n/esc
// cancels, leaving every other piece of action/search/screen state
// untouched and making no ActionProvider call; quit keys still quit (via
// quitCmd, which also tears down the modal's now-abandoned context); every
// other key is swallowed so nothing behind the modal can react to it.
func (m Model) updatePendingActionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, m.quitCmd()
	case key.Matches(msg, m.keys.ConfirmAction):
		pa := m.action.pending
		m.action.pending = nil
		m.action.running = true
		return m, pa.confirm()
	case key.Matches(msg, m.keys.CancelAction):
		m.action.pending = nil
		return m, nil
	default:
		return m, nil
	}
}

// quitCmd cancels any in-flight action and any in-flight search (the #42
// lifecycle carry-forward: search.cancel was previously never invoked on
// quit) before returning tea.Quit, so goroutines reading a context derived
// from either observe cancellation instead of leaking past program exit.
func (m Model) quitCmd() tea.Cmd {
	if m.action.cancel != nil {
		m.action.cancel()
	}
	if m.search.cancel != nil {
		m.search.cancel()
	}
	return tea.Quit
}

// isQuitKey reports whether msg actually triggers tea.Quit given the current
// focus context: ctrl+c always quits; the rest of Quit's bound keys (a bare
// "q") only quit outside the focused search input, where "q" is otherwise
// typed as a literal character (see updateKey's focused-input branch). Used
// to decide whether a keypress should clear the status line (rule 8: any
// keypress that isn't a modal response and isn't quit clears it) without
// wrongly skipping the clear on a "q" that's actually being typed into the
// search box.
func (m Model) isQuitKey(msg tea.KeyMsg) bool {
	if !key.Matches(msg, m.keys.Quit) {
		return false
	}
	if m.screen == ScreenSearch && m.search.input.Focused() {
		return msg.String() == "ctrl+c"
	}
	return true
}

// clampSelections clamps every screen's selected index to that screen's
// current list length. A post-action refresh (dataLoadedMsg) can shrink any
// list out from under whatever row was selected — most notably an uninstall
// shrinking the mods list — and without this, selected can walk off the end
// of the list (the Phase 4 #42 selection-drift note this must not
// reintroduce).
func (m Model) clampSelections() {
	for _, screen := range screens {
		max := m.itemCount(screen) - 1
		switch {
		case max < 0:
			m.selected[screen] = 0
		case m.selected[screen] > max:
			m.selected[screen] = max
		case m.selected[screen] < 0:
			m.selected[screen] = 0
		}
	}
}

// formatOutcomeStatus renders a completed ActionOutcome as the one-line
// status text (rule 4): the outcome's own Message, plus its single warning
// appended after an em dash, or an "(N warnings)" count suffix when there is
// more than one — chosen so the status line never grows past one line
// regardless of how many warnings a flow reports.
func formatOutcomeStatus(outcome ActionOutcome) string {
	switch len(outcome.Warnings) {
	case 0:
		return outcome.Message
	case 1:
		return outcome.Message + " — " + outcome.Warnings[0]
	default:
		return fmt.Sprintf("%s (%d warnings)", outcome.Message, len(outcome.Warnings))
	}
}

// singleLine collapses embedded newlines so a multi-line error (e.g. a
// joined multi-source failure, or a *domain.DeployError's multi-part text)
// can never grow the one-row status line past its budget.
func singleLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
}

// statusLine renders the action status line (last mutation's outcome or
// error) truncated to the terminal's content width — "" when no status is
// set, matching contentChromeHeight's height-budget accounting: an unset
// status renders (and occupies) nothing, not a blank row. Truncation
// happens here, at render time, against the CURRENT availableWidth(), not
// when the status was set (rule 5): statusLine is called fresh on every
// View(), so a resize re-truncates the same stored text.
func (m Model) statusLine() string {
	if m.action.status == "" {
		return ""
	}
	style := m.theme.MutedText
	if m.action.statusIsError {
		style = m.theme.DangerText
	}
	return truncate(style.Render(m.action.status), m.availableWidth())
}

// actionModalView renders the pending confirmation as a bordered panel that
// REPLACES the screen content (chrome/footer stay unchanged) — the simplest
// way to preserve the exact-height render invariant every other screen
// already holds, since an overlay composite would need its own height
// bookkeeping layered on top of whatever screen it covers.
func (m Model) actionModalView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	panelContentHeight := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	pa := m.action.pending
	lines := []string{truncate(m.theme.PanelTitle.Render(pa.title), panelContentWidth)}

	// Fixed lines: title, blank separator, hint (3 total). Whatever
	// vertical room remains, capped at actionModalMaxDetailLines, is
	// available for detail rows — minus one more if a "+N more" line is
	// needed to name the overflow. Deriving the cap from panelContentHeight
	// (not just the constant) keeps the exact-height invariant even at
	// terminal sizes shorter than this modal's normal case.
	const fixedLines = 3
	budget := min(actionModalMaxDetailLines, max(panelContentHeight-fixedLines, 0))

	detail := pa.detail
	if len(detail) > budget {
		shown := max(budget-1, 0)
		more := len(detail) - shown
		for _, d := range detail[:shown] {
			lines = append(lines, truncate(d, panelContentWidth))
		}
		lines = append(lines, m.theme.MutedText.Render(fmt.Sprintf("+%d more", more)))
	} else {
		for _, d := range detail {
			lines = append(lines, truncate(d, panelContentWidth))
		}
	}

	lines = append(lines, "", m.theme.MutedText.Render("y/enter confirm · n/esc cancel"))

	return m.panelWithHeight(width, height).Render(strings.Join(lines, "\n"))
}
