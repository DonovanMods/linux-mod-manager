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

	// progress is the latest ActionProgress tick observed for the
	// CURRENTLY running action (zero value otherwise - see
	// hasVisibleStatus/statusLine, which only ever consult it while
	// running). progressCh is that action's pump channel - created by
	// buildAction, closed by its confirm closure once do() returns, and
	// read by the listener cmd waitForActionProgress re-issues on every
	// fresh actionProgressMsg (see Model.Update's case in app.go). Both are
	// per-action: a new buildAction call always installs a fresh pair, and
	// single-flight guarantees the previous action's Done/Failed message
	// (which clears progress - see app.go) has already been processed
	// before that can happen.
	progress   ActionProgress
	progressCh chan ActionProgress
}

// ActionProgress is one streamed progress tick from an in-flight
// ActionProvider mutation (ApplyInstall/ApplyUpdate/ApplyProfileSwitch -
// see actions_provider.go). Line is a ready-to-display, provider-composed
// status string kept short enough for the one-row status line (e.g.
// `Installing SkyUI: skyui_5_1.7z 42%`); Percent is the 0-100 completion
// when known, or -1 when the phase has no meaningful percentage
// (indeterminate - e.g. "extracting").
type ActionProgress struct {
	Line    string
	Percent float64
}

// actionProgressMsg carries one ActionProgress tick, tagged with the
// generation established when the action was built (see buildAction) so a
// tick from a superseded action is discarded exactly like actionDoneMsg/
// actionFailedMsg.
type actionProgressMsg struct {
	gen      int
	progress ActionProgress
}

// sendActionProgress delivers p to ch without ever blocking the caller -
// always the flow goroutine running inside a provider's Apply* method, via
// the send-adapter buildAction passes it. ch is a single-slot (buffer size
// 1) mailbox: a plain non-blocking send when empty; when already full (a
// previous tick hasn't been collected yet by the listener - see
// waitForActionProgress), a non-blocking drain of that stale tick followed
// by a non-blocking send of p.
//
// Net effect: the listener only ever observes the MOST RECENT tick the flow
// goroutine produced - intermediate ticks are coalesced away under a slow
// consumer, never queued, and the flow goroutine itself never waits on the
// UI. This is deliberately single-slot rather than a small (e.g. ~8-entry)
// buffer: a larger buffer would still need this exact drain-then-send
// discipline to guarantee "last value wins" under a slow consumer - a plain
// buffered send that just drops-when-full instead keeps the OLDEST N ticks
// and silently discards everything sent after the buffer first fills,
// which is the wrong failure mode (see
// TestActionProgressPumpNeverBlocksFlowAndCoalescesForSlowConsumer, which
// pins exactly this behavior).
func sendActionProgress(ch chan ActionProgress, p ActionProgress) {
	select {
	case ch <- p:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- p:
	default:
		// Lost a vanishingly unlikely race against a concurrent receive
		// that drained the slot between our drain and our send attempt.
		// Harmless: the listener still got A recent tick, and the next
		// send (or the eventual close(ch) - see buildAction) delivers
		// whatever is truly latest, so nothing is ever stuck.
	}
}

// waitForActionProgress is the pump's listener cmd, bridging ch (see
// sendActionProgress) into a Bubble Tea message: one actionProgressMsg per
// receive, tagged with gen so Update can discard a tick from a superseded
// action. A closed channel (the action has ended - buildAction's confirm
// closure closes ch right after do() returns) is terminal: this returns a
// nil tea.Msg, which Bubble Tea never dispatches to Update, so nothing
// re-issues the listener and the goroutine this cmd runs in simply exits.
// Only Update's own actionProgressMsg case (app.go) keeps the loop alive,
// by re-issuing this same cmd after every fresh tick.
func waitForActionProgress(ch chan ActionProgress, gen int) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return actionProgressMsg{gen: gen, progress: p}
	}
}

// actionDoneMsg carries a completed action's outcome, tagged with the
// generation established when the action was built (see buildAction) so a
// superseded result can be discarded.
type actionDoneMsg struct {
	gen     int
	kind    actionKind
	outcome ActionOutcome
	// switchedTo is set only by the profile-switch action (actionSwitch, via
	// buildAction's switchedTo parameter - see resolvePlanResult in
	// mutations.go), naming the profile the switch just applied. Every other
	// action leaves it "" (the zero value), which app.go's actionDoneMsg
	// handler treats as "nothing to rebind" - see rebindProfile.
	switchedTo string
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
//
// switchedTo is carried verbatim into the eventual actionDoneMsg (see that
// type's doc comment): every caller except resolvePlanResult's actionSwitch
// build passes "" here, since only a profile switch has a session-rebind
// consequence for app.go's actionDoneMsg handler to act on.
//
// Progress pump (Phase 5b Task 4): do's second parameter is a send-adapter
// buildAction always wires up, regardless of whether the underlying
// ActionProvider method actually reports progress - a caller whose action
// has none (EnableMod/DisableMod/UninstallMod/DeployProfile) simply never
// calls it. Confirming the resulting pendingAction (updatePendingActionKey)
// runs BOTH do itself and a listener cmd (waitForActionProgress) via
// tea.Batch: the listener bridges ch into actionProgressMsg values Update
// handles (app.go) by storing the latest tick and re-issuing the listener,
// for as long as ch stays open. ch is closed here, by the same goroutine
// that calls do, immediately after do returns - so the LAST tick do ever
// sent is still delivered (Go channels deliver buffered values before
// signaling closed), and the listener naturally stops re-issuing once it
// observes the close (see waitForActionProgress).
func (m Model) buildAction(kind actionKind, title string, detail []string, switchedTo string, do func(context.Context, func(ActionProgress)) (ActionOutcome, error)) (Model, pendingAction) {
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

	ch := make(chan ActionProgress, 1)
	m.action.progressCh = ch

	pa := pendingAction{
		kind:   kind,
		title:  title,
		detail: detail,
		confirm: func() tea.Cmd {
			actionCmd := func() tea.Msg {
				outcome, err := do(ctx, func(p ActionProgress) { sendActionProgress(ch, p) })
				close(ch)
				if err != nil {
					return actionFailedMsg{gen: gen, kind: kind, err: err}
				}
				return actionDoneMsg{gen: gen, kind: kind, outcome: outcome, switchedTo: switchedTo}
			}
			return tea.Batch(actionCmd, waitForActionProgress(ch, gen))
		},
	}
	return m, pa
}

// profileRebinder is implemented by DataProvider/ActionProvider values that
// carry a per-session active-profile binding (currently *coreProvider only
// - see service_core.go's SetProfile), letting a successful TUI-driven
// profile switch rebind the session without reconstructing the provider.
// It's deliberately NOT part of the DataProvider/ActionProvider interfaces
// themselves (both stay frozen at their documented read-only/write-only
// contracts) - rebindProfile below reaches it via an optional type
// assertion instead. prototypeProvider does not implement this: its own
// ApplyProfileSwitch already flips the canned data's Active profile
// in-place (see actions_provider.go), so a second flip here would
// double-apply; simply not implementing the interface makes rebindProfile a
// no-op for it, with no special-casing required.
type profileRebinder interface {
	SetProfile(name string)
}

// rebindProfile rebinds every provider/actions instance that supports it
// (see profileRebinder) to name. cmd/lmm/tui.go currently wires m.provider
// and m.actions from two SEPARATE *coreProvider instances - one per
// NewCoreProvider/NewCoreActions call (see their doc comments in
// service_core.go) - so both are rebound independently here rather than
// assuming they're the same pointer; this stays correct even if a future
// wiring change shares one instance between both fields, since rebinding
// the same pointer twice is a harmless no-op.
func (m Model) rebindProfile(name string) {
	if rb, ok := m.provider.(profileRebinder); ok {
		rb.SetProfile(name)
	}
	if rb, ok := m.actions.(profileRebinder); ok {
		rb.SetProfile(name)
	}
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

// hasVisibleStatus reports whether statusLine() would render anything: the
// latest in-flight progress tick while an action is running (see
// ActionProgress), or the last action's outcome/error text otherwise.
// contentChromeHeight (app.go) uses this to decide whether to reserve the
// status row, keeping the height budget and the actual render in lockstep -
// the same "" ⇒ nothing rendered contract statusLine has always had, now
// extended to cover the progress line too.
func (m Model) hasVisibleStatus() bool {
	if m.action.running && m.action.progress.Line != "" {
		return true
	}
	return m.action.status != ""
}

// statusLine renders the action status line truncated to the terminal's
// content width — "" when hasVisibleStatus reports nothing to show, matching
// contentChromeHeight's height-budget accounting: nothing to show renders
// (and occupies) nothing, not a blank row. While an action is running AND
// has reported at least one progress tick, that tick's Line takes priority
// over the stored outcome/error status (rule 8's "the status line renders
// the latest progress line while running" contract - actionDoneMsg/
// actionFailedMsg clear progress, see app.go, so this reverts to the
// outcome/error text the instant the action settles). Truncation happens
// here, at render time, against the CURRENT availableWidth(), not when the
// status/progress was set (rule 5): statusLine is called fresh on every
// View(), so a resize re-truncates the same stored text.
func (m Model) statusLine() string {
	if !m.hasVisibleStatus() {
		return ""
	}
	if m.action.running && m.action.progress.Line != "" {
		return truncate(m.theme.MutedText.Render(m.action.progress.Line), m.availableWidth())
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
