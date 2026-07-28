package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/tui/theme"
)

const defaultContentWidth = 76

// menuItem is one dashboard menu entry. hasTarget is false for flavor-only
// entries (like the Conflict Oracle) that have no screen yet.
type menuItem struct {
	label     string
	target    Screen
	hasTarget bool
}

// Layout describes the major panel arrangement for a theme.
type Layout string

const (
	LayoutPartySheet         Layout = "party-sheet"
	LayoutMonochromeTerminal Layout = "monochrome-terminal"
	LayoutCommander          Layout = "commander"
	LayoutCrtStack           Layout = "crt-stack"
)

// Options configures the TUI app.
type Options struct {
	Theme    string
	Provider DataProvider
	// Actions is the write-side ActionProvider seam (see actions_provider.go).
	// Optional: a nil Actions means no mutation can be confirmed through
	// promptAction/buildAction, which is fine for tests that only exercise
	// the read-only DataProvider surface.
	Actions ActionProvider
	// Ctx seeds Model.ctx; see that field for why the context is stored
	// rather than threaded as a parameter.
	Ctx context.Context
	// NoColor mirrors the root --no-color flag and NO_COLOR env var (#91):
	// when true, NewModel pins lipgloss's process-global color profile to
	// termenv.Ascii before constructing the theme, so every style renders
	// plain text. Empirically, lipgloss resolves a Style's color profile at
	// Render() time by dereferencing the shared *Renderer the Style
	// captured at construction (see Style.Render/Renderer.ColorProfile) -
	// the pin therefore also takes effect for styles built earlier in the
	// same process - but pinning before theme construction keeps the
	// ordering obviously correct rather than relying on that render-time
	// detail.
	NoColor bool
	// GameName is the active game's display name, threaded into
	// newSearchModel (#58 item 5's wording-parity fix) so the TUI's
	// no-sources-configured diagnostic and all-sources honesty notice can
	// name the game the same way the CLI's own diagnostics do. Optional: an
	// empty GameName (every test double that doesn't set it) falls back to
	// a generic "this game" - see noSourcesConfiguredErr. NewPrototypeModel
	// ignores this field entirely and derives its own from the canned
	// active game, mirroring how it already discards a caller-supplied
	// Provider/Actions.
	GameName string
}

// Model is the root Bubble Tea model for the lmm TUI.
type Model struct {
	theme    theme.Theme
	layout   Layout
	keys     KeyMap
	provider DataProvider
	actions  ActionProvider
	// ctx deviates from "don't store contexts in structs": Bubble Tea's
	// Init/Update/View take no context parameter, so commands (e.g.
	// startSearch) close over m.ctx to reach it from goroutines.
	ctx context.Context

	// now is the injectable clock seam for lastDeployLabel (#106a): every
	// dashboard layout renders "Last deploy" by calling lastDeployLabel(m.
	// now(), m.summary.LastDeploy) at View() time rather than caching a
	// precomputed label at load time, so the relative age keeps advancing
	// between refreshes without a fake extra refresh cycle. Defaults to
	// time.Now in NewModel; tests override it with a fixed func to pin
	// exact "Nh ago" text deterministically (see dashboard view tests).
	now func() time.Time

	state   loadState
	loadErr error
	// loadGen tags every loadData dispatch (dataLoadedMsg/loadFailedMsg
	// carry the gen current when the cmd was built - loadData's value
	// receiver captures it), mirroring searchModel.gen's staleness
	// discipline: Update discards a load message whose gen no longer
	// matches. Bumped ONLY where an in-flight load is intentionally
	// invalidated - resolveGameSwitch's session reset (mutations.go) and
	// actionDoneMsg's profile-switch rebind (both rebind the providers a
	// still-running load may be reading from, so its eventual result
	// describes a binding that no longer exists). Ordinary post-action
	// refreshes deliberately do NOT bump it: concurrent same-binding
	// refreshes all describe current data, and last-wins (the pre-gen
	// behavior) remains correct for them.
	loadGen int

	summary  Summary
	mods     []ModItem
	profiles []ProfileItem
	sources  []SourceInfo
	// sourcesShowAll is the Sources screen's 'a'-toggled view state (Task 4,
	// #75): false (the zero value, and default on every fresh load/game
	// switch) is the game-scoped default view; true is the full-registry
	// view with the InUse marker. m.sources itself always holds whichever
	// view is CURRENTLY selected - toggleSourcesAll (mutations.go) flips
	// this and re-fetches m.sources from the provider in lockstep, so the
	// two never disagree about what's on screen.
	sourcesShowAll bool
	// conflicts backs the Conflicts screen (Task 3) - fetched alongside
	// mods/profiles in the same loadData refresh cycle (see dataLoadedMsg),
	// not gated behind an explicit user action the way Updates/CheckUpdates
	// is: conflict detection is a cheap, pure DB/cache read (core.Service.
	// GetProfileConflicts), so it belongs in the ordinary load rather than a
	// Phase 5b-style on-demand check.
	conflicts []ConflictItem
	search    searchModel
	action    actionModel
	// picker is the pending list-choice modal (see picker.go), if any.
	// Sibling to action.pending: promptPicker/updatePickerKey/pickerView
	// mirror promptAction/updatePendingActionKey/actionModalView's structure.
	picker *pendingPicker
	// inputModal is the pending text-entry modal (see input_modal.go), if
	// any. Another sibling of action.pending/picker: promptInput/
	// updateInputModalKey/inputModalView mirror the same structure.
	inputModal *pendingInput
	// overlay is the pending read-only info panel (see overlay.go), if any.
	// Another sibling of action.pending/picker/inputModal: promptOverlay/
	// updateOverlayKey/overlayView mirror the same structure, simplified
	// (no choose/submit - see infoOverlay's doc comment).
	overlay *infoOverlay

	// pendingUpdates is the retained CheckUpdates result behind the
	// apply-updates confirmation modal (Task 7's changelog viewer - see
	// resolveCheckUpdatesResult, mutations.go, the ONLY place that ever
	// sets it, alongside action.pending when that modal opens): non-nil
	// ONLY while action.pending is that SAME update batch, and it is what
	// updatePendingActionKey's 'v' case (actions.go) consults to know both
	// THAT the pending action is the update batch (the discriminator - no
	// other pendingAction kind ever sets this) and WHICH updates/changelogs
	// to show. Invariant: cleared at BOTH points action.pending itself is
	// cleared - updatePendingActionKey's ConfirmAction branch (the fix-wave
	// Critical: confirm-only clearing was missed at first, leaking the
	// stale batch into later, unrelated modals - see
	// TestUpdateModalConfirmClearsRetainedUpdatesView) and its CancelAction
	// branch - plus resolveGameSwitch's defense-in-depth modal reset, so it
	// can never outlive the modal it describes.
	pendingUpdates *UpdatesView

	// lastUpdates retains the most recent CheckUpdates result for list-scoped
	// changelog viewing (fix-wave-2 smoke finding #1): 'v' on
	// ScreenInstalledMods, OUTSIDE any modal (viewSelectedModChangelog,
	// mutations.go), consults this rather than m.pendingUpdates, which lives
	// and dies with the apply-updates confirmation modal (see that field's
	// own doc comment). Once the modal closes - confirm OR cancel - or a
	// check found zero updates and never opened a modal at all,
	// pendingUpdates is nil while lastUpdates still holds the same
	// CheckUpdates result the user just saw, so changelogs stay reachable
	// from the list itself.
	//
	// Set in resolveCheckUpdatesResult (mutations.go) for BOTH the
	// zero-updates and modal-opening paths - unlike pendingUpdates, which
	// only the modal-opening path ever sets. Deliberately NOT cleared on the
	// modal's own confirm/cancel (updatePendingActionKey, actions.go): that
	// would defeat the whole point of a list-scoped viewer that outlives the
	// modal. Cleared only in resolveGameSwitch, alongside pendingUpdates' own
	// defense-in-depth reset, since a different game's updates must never
	// leak into this one's Installed Mods list.
	lastUpdates *UpdatesView

	screen   Screen
	selected map[Screen]int
	showHelp bool
	width    int
	height   int

	// orderChanged is Task 4's post-reorder deploy hint flag: moveSelectedMod
	// (mutations.go) sets this true after every successful J/K move, and
	// statusLine (actions.go) falls back to rendering "order changed —
	// deploy (D) to apply" whenever it's true and there's nothing more
	// specific (m.action.status) to show - unlike m.action.status, this is
	// NOT cleared by rule 8's "any keypress clears the status" (app.go's
	// updateKey), so it survives ordinary navigation until a deploy or
	// profile-switch action actually resolves successfully (cleared in the
	// actionDoneMsg case below) or a game switch resets the session
	// (resolveGameSwitch, mutations.go).
	orderChanged bool
}

// loadState tracks where the Model is in its async data-load lifecycle.
type loadState int

const (
	stateLoading loadState = iota
	stateReady
	stateFailed
)

// dataLoadedMsg carries data successfully loaded through a DataProvider,
// tagged with the load generation current when the fetch was dispatched
// (see Model.loadGen) so a superseded load - one still in flight when a
// game switch reset the session - is discarded exactly like a stale
// searchResultMsg (the mechanism this mirrors).
type dataLoadedMsg struct {
	gen       int
	summary   Summary
	mods      []ModItem
	profiles  []ProfileItem
	conflicts []ConflictItem
}

// loadFailedMsg carries an error from a failed DataProvider load, tagged
// like dataLoadedMsg.
type loadFailedMsg struct {
	gen int
	err error
}

// NewModel creates the TUI model backed by the given DataProvider.
func NewModel(options Options) (Model, error) {
	if options.Provider == nil {
		return Model{}, fmt.Errorf("TUI options: provider is required")
	}
	if options.NoColor {
		// Pinning termenv.Ascii disables lipgloss's usual escape-sequence
		// output process-wide, so both this Model's own styles AND anything
		// else in the process that renders through lipgloss's default
		// renderer go plain. Acceptable for the TUI binary (its only
		// caller): the CLI's own colorGreen/Red/Yellow helpers (cmd/lmm/
		// root.go) build ANSI escapes by hand rather than through lipgloss,
		// so they are unaffected by this process-global state.
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	t, err := theme.ByName(options.Theme)
	if err != nil {
		return Model{}, err
	}

	if options.Ctx == nil {
		options.Ctx = context.Background()
	}

	return Model{
		theme:    t,
		layout:   layoutForTheme(t.Name),
		keys:     DefaultKeyMap(),
		provider: options.Provider,
		actions:  options.Actions,
		ctx:      options.Ctx,
		now:      time.Now,
		state:    stateLoading,
		screen:   ScreenDashboard,
		// Updates starts at its own "-1 unknown" sentinel (Summary's own
		// doc comment), not the Go zero value 0, so the dataLoadedMsg
		// preserve check below (`m.summary.Updates >= 0`) can't mistake
		// "no load has happened yet" for "a check already found zero
		// updates" on the very first load.
		summary: Summary{Updates: -1, Conflicts: -1},
		search:  newSearchModel(options.Provider, t.Panel.GetHorizontalFrameSize(), options.GameName),
		// sources is seeded synchronously (like search's source list above)
		// rather than through loadData/dataLoadedMsg: SourceInfos is a
		// read-only view of already-registered sources, not an I/O call that
		// can fail, so it needs no async load state or error path. false
		// (sourcesShowAll's own zero value) - the game-scoped default view
		// (Task 4, #75).
		sources: options.Provider.SourceInfos(false),
		selected: map[Screen]int{
			ScreenDashboard:     0,
			ScreenInstalledMods: 0,
			ScreenSearch:        0,
			ScreenProfiles:      0,
			ScreenSources:       0,
			ScreenConflicts:     0,
		},
	}, nil
}

// NewPrototypeModel creates a side-effect-free TUI model backed by fake data.
// Provider and Actions are wired from the SAME prototypeProvider instance
// (see NewPrototypeProvider's doc comment), so actions confirmed through
// the returned Model are visible in its own subsequent reads — whatever the
// caller passed in either field is discarded. options.GameName is likewise
// discarded and derived from the canned active game (see prototypeProvider.
// activeGame) instead, mirroring Provider/Actions above (#58 item 5) - a
// caller has no real game to name in demo mode.
func NewPrototypeModel(options Options) (Model, error) {
	provider := newPrototypeProviderConcrete()
	options.Provider = provider
	options.Actions = provider
	options.GameName = provider.activeGame().Name
	return NewModel(options)
}

func (m Model) dashboardMenu() []menuItem {
	if m.layout == LayoutMonochromeTerminal {
		return []menuItem{
			{label: "RUN SPELLBOOK SCAN", target: ScreenInstalledMods, hasTarget: true},
			{label: "QUERY ARCHIVE INDEX", target: ScreenSearch, hasTarget: true},
			{label: "LOAD PROFILE ROSTER", target: ScreenProfiles, hasTarget: true},
			{label: "SCRY SOURCE REGISTRY", target: ScreenSources, hasTarget: true},
			// Targetless until Phase 6b shipped ScreenConflicts; the wiring
			// lagged behind the screen and Enter silently no-opped (user
			// smoke find, PR #113).
			{label: "ASK CONFLICT ORACLE", target: ScreenConflicts, hasTarget: true},
		}
	}
	return []menuItem{
		{label: "Installed Mods", target: ScreenInstalledMods, hasTarget: true},
		{label: "Search Archives", target: ScreenSearch, hasTarget: true},
		{label: "Profiles", target: ScreenProfiles, hasTarget: true},
		{label: "Sources", target: ScreenSources, hasTarget: true},
		{label: "Consult Conflict Oracle", target: ScreenConflicts, hasTarget: true},
	}
}

func (m Model) dashboardMenuRows() []string {
	items := m.dashboardMenu()
	rows := make([]string, 0, len(items))
	for i, item := range items {
		rows = append(rows, m.row(i, item.label))
	}
	return rows
}

// openSelectedMenuEntry jumps to the selected dashboard-menu item's target
// screen. Choosing "Search Archives" by name is an EXPLICIT request to
// search — the same category as the "/" and SearchScreen ("3") bindings —
// so that one target routes through gotoScreenFocused; every other menu
// target (Installed Mods, Profiles, Sources) is a plain screen jump and
// keeps using gotoScreen. See gotoScreen's doc comment for why passive
// jumps must never focus.
func (m Model) openSelectedMenuEntry() (Model, tea.Cmd) {
	if m.screen != ScreenDashboard {
		return m, nil
	}
	items := m.dashboardMenu()
	selected := m.selected[ScreenDashboard]
	if selected >= len(items) || !items[selected].hasTarget {
		return m, nil
	}
	target := items[selected].target
	if target == ScreenSearch {
		return m.gotoScreenFocused(target)
	}
	return m.gotoScreen(target)
}

// gotoScreen switches to the target screen without touching the search
// input's focus state. This is the entry path for screen-cycling
// (NextScreen/PrevScreen) and the direct screen-jump bindings (Dashboard,
// InstalledMods, Profiles, Sources) — none of these are an explicit request
// to search, so landing on ScreenSearch through them must NOT focus the
// input. A focused input swallows every keystroke (see updateKey's
// focused-input branch), so auto-focusing here trapped a user cycling
// through screens with tab/shift-tab/left/right/h/l on Search until they
// pressed Esc (smoke-test Finding 1). See gotoScreenFocused for the bindings
// that DO focus.
func (m Model) gotoScreen(screen Screen) (Model, tea.Cmd) {
	m.screen = screen
	return m, nil
}

// gotoScreenFocused switches to ScreenSearch and focuses the input
// immediately. Reserved for EXPLICIT "go search" intent: the Search ("/")
// and SearchScreen ("3") bindings, and selecting "Search Archives" from the
// dashboard menu (openSelectedMenuEntry) — picking "search" by name is
// intent, not passive cycling. Esc (the Blur binding) is the only way back
// out of focus; once blurred, screen-level keys (s, n/p, navigation) reach
// updateKey's outer switch again.
func (m Model) gotoScreenFocused(screen Screen) (Model, tea.Cmd) {
	m, _ = m.gotoScreen(screen)
	m.search.input.Focus()
	return m, textinput.Blink
}

func (m Model) Init() tea.Cmd {
	return m.loadData
}

// loadData fetches all dashboard data through the configured DataProvider.
// It runs as a Bubble Tea command, off the update loop. The value receiver
// captures m.loadGen as of dispatch time, so the message this eventually
// produces is tagged with the generation the load was issued under (see
// Model.loadGen) - a caller that bumps loadGen and THEN returns m.loadData
// (resolveGameSwitch) stamps the fresh gen, while a load already in flight
// keeps its old one and is discarded on arrival.
func (m Model) loadData() tea.Msg {
	summary, mods, err := m.provider.Overview(m.ctx)
	if err != nil {
		return loadFailedMsg{gen: m.loadGen, err: err}
	}
	profiles, err := m.provider.Profiles(m.ctx)
	if err != nil {
		return loadFailedMsg{gen: m.loadGen, err: err}
	}
	conflicts, err := m.provider.Conflicts(m.ctx)
	if err != nil {
		return loadFailedMsg{gen: m.loadGen, err: err}
	}
	// The dashboard's conflict count is derived from THIS fetch, not
	// whatever Overview itself reported (real coreProvider.Overview always
	// reports the -1 sentinel there - see its own doc comment - and the
	// prototype's canned Stats.Conflicts is just flavor text): unlike
	// Updates, which genuinely needs an explicit user-triggered check
	// (Phase 5b), conflict detection is a plain, cheap read available on
	// every ordinary refresh, so Summary.Conflicts always reflects the real,
	// current count once this returns - never the "?" unknown sentinel.
	summary.Conflicts = len(conflicts)

	return dataLoadedMsg{gen: m.loadGen, summary: summary, mods: mods, profiles: profiles, conflicts: conflicts}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dataLoadedMsg:
		// Stale gen: a load dispatched before a game/profile switch reset
		// the session (see Model.loadGen) - discarded whole, exactly like a
		// stale searchResultMsg below.
		if msg.gen != m.loadGen {
			return m, nil
		}
		m.state = stateReady
		summary := msg.summary
		// Updates is the model's own in-memory count once a check has
		// actually run (see resolveCheckUpdatesResult's doc comment) -
		// coreProvider.Overview has no persistent count of its own and
		// always reports the -1/"?" sentinel, so wholesale-overwriting
		// m.summary with every incoming summary reverted a just-checked
		// count to "?" on the very next UNRELATED refresh (enable/
		// disable/deploy/switch/install all funnel through here). Preserve
		// the session's known count across that case; a genuinely fresh,
		// non-sentinel count from the provider still always wins. The one
		// case that SHOULD go stale - applying updates, which changes how
		// many are left - re-sentinels explicitly in actionDoneMsg below
		// rather than here, since a plain refresh has no way to tell "stale
		// because updates were just applied" apart from "stale because
		// nothing changed".
		if summary.Updates < 0 && m.summary.Updates >= 0 {
			summary.Updates = m.summary.Updates
		}
		m.summary = summary
		m.mods = msg.mods
		m.profiles = msg.profiles
		m.conflicts = msg.conflicts
		m.clampSelections()
		return m, nil
	case actionDoneMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		m.action.running = false
		if m.action.cancel != nil {
			m.action.cancel()
			m.action.cancel = nil
		}
		// Task 6 item d: a quit-triggered drain (see startQuit) resolves the
		// instant the action it was waiting on settles - see resolveDrainedQuit's
		// own doc comment for why (shared with actionFailedMsg below and the six
		// plan/check messages in mutations.go).
		if m.action.draining {
			return m.resolveDrainedQuit()
		}
		m.action.status = formatOutcomeStatus(msg.outcome)
		m.action.statusIsError = false
		m.action.progress = ActionProgress{}
		// Fix-wave-2 smoke finding #2: a completed batch whose outcome
		// carries ResultLines (today, only the update-apply batch -
		// applyUpdatesSequentially, mutations.go) gets a durable per-item
		// record beyond the status line's aggregate count summary above -
		// a scrollable info overlay titled "update results" listing each
		// mod's own "✓ .../✗ ..." line. Placed AFTER the status-line
		// assignment (the count summary is kept, not replaced) and guarded
		// on m.overlay == nil, defensively, exactly like resolveChangelogPicked
		// guards against clobbering an existing overlay - the modal/overlay
		// machinery is idle whenever an action resolves, by construction, so
		// this should never actually refuse, but costs nothing to check.
		// The quit-drain path above already returned before this point, so
		// draining never reaches here either.
		if len(msg.outcome.ResultLines) > 0 && m.overlay == nil {
			m.overlay = &infoOverlay{title: "update results", lines: msg.outcome.ResultLines}
		}
		// A fresh switch's target must rebind the session's active-profile
		// providers BEFORE the refresh below reads them (see rebindProfile
		// and profileRebinder in actions.go) - otherwise Profiles() keeps
		// starring the OLD profile forever, and subsequent mutations keep
		// targeting it too (finding C1).
		if msg.switchedTo != "" {
			m.rebindProfile(msg.switchedTo)
			// Task 6 item b's second belt (the 5a review's UX-correctness
			// recommendation, beyond coreProvider's own p.profile race
			// guard): any in-flight search was built against the OLD
			// profile's installed-mod marks (installedModKeys), which are
			// stale the instant the switch lands - cancel it and bump gen
			// so a late result (or a late auth/search failure) is discarded
			// by the ordinary stale-gen checks (searchResultMsg/
			// searchFailedMsg's cases above) instead of rendering
			// now-wrong "installed" markers. Mirrors CycleSource's own
			// cancel+bump+reset-to-idle (updateKey, below).
			if m.search.cancel != nil {
				m.search.cancel()
				m.search.cancel = nil
			}
			m.search.gen++
			m.search.state = searchIdle
			// refilling/refreshing (#111 Tier 3 fix round 5 - task
			// re-review audit): state going to searchIdle already blocks
			// any NEW refill/refresh from dispatching, and the ONLY path
			// back to searchReady is a fresh submit (startSearch ->
			// beginNewSession, which resets both anyway) - but resetting
			// here too keeps the invariant "state != searchReady implies
			// neither flag is stuck true" holding everywhere a session gets
			// invalidated out-of-band, not just at the two dispatch sites
			// that already reset it as a side effect of their own gen bump.
			m.search.refilling = false
			m.search.refreshing = false
			// Same invalidation for any in-flight DATA load (see
			// Model.loadGen): it was dispatched against the OLD profile's
			// binding, so its eventual rows/summary go stale the instant
			// the rebind above lands - and, if it completed after the
			// fresh refresh dispatched below, would silently overwrite the
			// NEW profile's data. Bumped BEFORE the cmds slice below is
			// built, so the refresh's own m.loadData captures the fresh
			// gen (loadData's value receiver - see its doc comment).
			m.loadGen++
		}
		// A successful deploy or profile switch is exactly what Task 4's
		// post-reorder hint (m.orderChanged, see its own doc comment) is
		// waiting for - either one means the reordered load order has now
		// actually been applied, so the reminder to deploy no longer
		// applies. A FAILED deploy/switch (actionFailedMsg below) does NOT
		// clear it: the order is still unapplied in that case, so the hint
		// should keep reminding the user.
		if msg.kind == actionDeploy || msg.kind == actionSwitch {
			m.orderChanged = false
		}
		// A completed update-apply batch invalidates the just-checked
		// Updates count: applying updates changes how many are left, and
		// Phase 5b has no way to compute the new real number without
		// re-running CheckUpdates (resolveCheckUpdatesResult's own doc
		// comment on the "no DataProvider change" tradeoff). Re-sentinel it
		// back to "?" here, in the action's own done-path, rather than
		// leave a now-stale count on screen or lean on the dataLoadedMsg
		// preserve behavior above (which has no way to distinguish "stale
		// because updates were just applied" from "stale because nothing
		// relevant changed") to eventually correct it.
		if msg.kind == actionUpdate {
			m.summary.Updates = -1
		}
		// A completed install additionally re-runs the current search query
		// (if any) so the just-installed result's "installed" marker updates
		// immediately - see refreshSearchAfterInstall's doc comment for why
		// the refresh above (Overview/Profiles only, via m.loadData) doesn't
		// already cover this. tea.Batch collapses to the single m.loadData
		// cmd, UNWRAPPED, for every other action kind (compactCmds - see the
		// bubbletea package's own Batch doc comment), so this changes
		// nothing about the refresh cmd every other action already returns.
		cmds := []tea.Cmd{m.loadData}
		if msg.kind == actionInstall {
			var searchCmd tea.Cmd
			m, searchCmd = m.refreshSearchAfterInstall()
			cmds = append(cmds, searchCmd)
		}
		// Task 9: a successful import whose outcome named a profile (a
		// same-game import - see ActionOutcome.ImportedProfile's own doc
		// comment) offers a follow-up "switch to it now?" confirmation,
		// dispatched as a deferred message (see importAppliedMsg's own doc
		// comment for why this isn't opened inline, right here, instead).
		if msg.kind == actionImport && msg.outcome.ImportedProfile != "" {
			name := msg.outcome.ImportedProfile
			cmds = append(cmds, func() tea.Msg { return importAppliedMsg{name: name} })
		}
		return m, tea.Batch(cmds...)
	case actionFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		m.action.running = false
		if m.action.cancel != nil {
			m.action.cancel()
			m.action.cancel = nil
		}
		// Task 6 item d: mirrors actionDoneMsg's drain resolution above - a
		// failure settles the drain exactly like a success does (see
		// resolveDrainedQuit's doc comment).
		if m.action.draining {
			return m.resolveDrainedQuit()
		}
		m.action.status = singleLine(msg.err.Error())
		m.action.statusIsError = true
		m.action.progress = ActionProgress{}
		return m, m.loadData
	case actionProgressMsg:
		// Stale gen (a tick from a superseded action, e.g. one whose
		// context was already cancelled by a newer buildAction call) is
		// discarded entirely: no state touched, no re-issue. A fresh tick
		// becomes the currently-displayed progress (see statusLine) and
		// re-issues the listener so the next tick (or the terminal
		// closed-channel signal - see waitForActionProgress) keeps arriving.
		if msg.gen != m.action.gen {
			return m, nil
		}
		m.action.progress = msg.progress
		return m, waitForActionProgress(m.action.progressCh, msg.gen)
	case actionDrainTimeoutMsg:
		// Task 6 item d: forces the quit a drain (see startQuit) was
		// waiting on if the action never settled within actionDrainTimeout.
		// A stale gen (a LATER drain, or an already-resolved one whose
		// actionDoneMsg/actionFailedMsg case already cleared draining) is a
		// no-op - never force-quits a drain this timeout doesn't belong to.
		if msg.gen != m.action.gen || !m.action.draining {
			return m, nil
		}
		m.action.draining = false
		return m, tea.Quit
	case planResultMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolvePlanResult(msg)
	case planFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolvePlanFailure(msg)
	case installPlanResultMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveInstallPlanResult(msg)
	case installPlanFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveInstallPlanFailure(msg)
	case checkUpdatesResultMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveCheckUpdatesResult(msg)
	case checkUpdatesFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveCheckUpdatesFailure(msg)
	case policyChosenMsg:
		return m.resolvePolicyChoice(msg)
	case profileCreateSubmittedMsg:
		return m.resolveProfileCreate(msg)
	case gameChosenMsg:
		return m.resolveGameSwitch(msg)
	case changelogPickedMsg:
		return m.resolveChangelogPicked(msg)
	case importDataReadMsg:
		return m.resolveImportDataRead(msg)
	case importAppliedMsg:
		return m.resolveImportApplied(msg)
	case importSwitchConfirmedMsg:
		return m.resolveImportSwitchConfirmed(msg)
	case exportPathSubmittedMsg:
		return m.resolveExportSubmitted(msg)
	case loadFailedMsg:
		// Stale gen: mirrors dataLoadedMsg's discard above - a superseded
		// load's failure must not flip the fresh session into stateFailed.
		if msg.gen != m.loadGen {
			return m, nil
		}
		m.state = stateFailed
		m.loadErr = msg.err
		return m, nil
	case searchResultMsg:
		if msg.gen != m.search.gen {
			return m, nil
		}
		m.search.state = searchReady
		switch msg.kind {
		case searchResultSubmit:
			m.search.applyRoundResult(msg.page, true, roundExhausted(msg.page))
			m.search.fetchRound = 1
			m.selected[ScreenSearch] = 0
		case searchResultRefill:
			m.search.refilling = false
			newRows := len(msg.page.Results)
			// newRows == 0 is a churn backstop, not exhaustion detection
			// (roundExhausted handles that): a provider that reports
			// non-exhausted yet returns zero rows would otherwise be
			// re-polled every time the low-water mark trips. Reachable
			// only via provider inconsistency; genuine failures land in
			// searchFailedMsg below, which stays retryable.
			m.search.applyRoundResult(msg.page, false, newRows == 0 || roundExhausted(msg.page))
			if newRows > 0 {
				m.search.fetchRound++
			}
		case searchResultRefresh:
			m.search.refreshing = false
			m.search.applyRoundResult(msg.page, true, msg.page.Exhausted)
			m.search.fetchRound = msg.rounds
			if m.selected[ScreenSearch] >= len(m.search.buffer) {
				m.selected[ScreenSearch] = max(len(m.search.buffer)-1, 0)
			}
		}
		return m, nil
	case searchFailedMsg:
		if msg.gen != m.search.gen {
			return m, nil
		}
		switch msg.kind {
		case searchResultRefill:
			// A refill failure must not destroy an already-useful buffer,
			// NOR permanently give up (#111 Tier 3 fix round 4 - task
			// review finding: this used to set providerExhausted
			// unconditionally, making the footer falsely claim "all N
			// shown" with no way to retry). providerExhausted means the
			// PROVIDER said there's nothing left - a failed ATTEMPT is not
			// that. fetchRound is untouched here (only ever advanced on a
			// SUCCESSFUL searchResultRefill, see the case above), so the
			// next low-water-triggering movement
			// (afterSearchSelectionMove -> maybeRefillSearch) naturally
			// retries the SAME round. That retry can only ever be fired by
			// a real keypress - maybeRefillSearch is never called from a
			// timer or loop, only from Up/Down/NextPage's handlers - so
			// this can't spin into a tight automatic retry loop; a user
			// holding Down will keep retrying on every press, which is the
			// intended "just keep scrolling and it'll pick back up"
			// behavior, not a bug.
			m.search.refilling = false
			m.setIdleStatus("couldn't load more — will retry", false)
			return m, nil
		case searchResultRefresh:
			// A refresh failure must leave the PRE-refresh buffer and
			// searchReady state exactly as they were (#111 Tier 3 fix
			// round 4): refreshSearchAfterInstall's beginRefresh
			// (search.go) never touches either up front - see its own doc
			// comment - so there is nothing to roll back here; just clear
			// refreshing (fix round 5) so a subsequent scroll can refill
			// again, and surface a muted notice.
			m.search.refreshing = false
			m.setIdleStatus("couldn't refresh results", false)
			return m, nil
		}
		// The sentinel source ("" == all sources) has no single source name to
		// report; routing it here would render "Authentication required for ."
		// and a broken "lmm auth login " hint. Fall through to searchFailed,
		// whose rendering already names each failing source (see
		// core.Service.SearchAllSources' joined per-source errors).
		if msg.source != "" && errors.Is(msg.err, domain.ErrAuthRequired) {
			m.search.state = searchAuthRequired
			m.search.authSource = msg.source
			return m, nil
		}
		m.search.state = searchFailed
		m.search.err = msg.err
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.search.input.Width = searchInputWidthFor(m.availableWidth(), m.theme.Panel.GetHorizontalFrameSize())
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	default:
		return m, nil
	}
}

// updateKey routes a keypress to whichever modal (if any) is currently
// showing, then falls through to the outer per-screen switch below.
//
// Picker/inputModal/overlay are checked BEFORE action.pending (Task 7): the
// changelog viewer (updatePendingActionKey's 'v' case, actions.go) can open
// an overlay or picker WHILE action.pending is still set - the apply-updates
// modal waits underneath, reappearing the instant the overlay/picker closes,
// since action.pending itself is never touched while either is up (see
// openChangelogFromUpdateModal's own doc comment) - the first time any two of
// these four ever coexist. Every OTHER combination remains mutually
// exclusive exactly as before: each promptX constructor's own guard
// (promptOverlay/promptPicker/promptInput/promptAction) still refuses
// outright whenever a DIFFERENT one of the four is already up, so this
// reordering changes nothing for any pre-existing single-modal case - it
// only matters for the new stacked one. screenView (below) mirrors this same
// order for the identical reason.
func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.picker != nil {
		return m.updatePickerKey(msg)
	}

	if m.inputModal != nil {
		return m.updateInputModalKey(msg)
	}

	if m.overlay != nil {
		return m.updateOverlayKey(msg)
	}

	if m.action.pending != nil {
		return m.updatePendingActionKey(msg)
	}

	// Rule 8: any keypress that isn't a modal response (handled above,
	// before this point is ever reached) and isn't quit clears the status
	// line. isQuitKey (not the bare Quit binding) is used so a "q" that's
	// actually being typed into the focused search input still clears it.
	// m.action.running gates this too: it covers both a running mutation and
	// an in-flight plan fetch (planProfileSwitch in mutations.go sets it
	// alongside "Planning switch…" before any pendingAction confirmation
	// exists), so a navigation keypress mid-flight must not wipe the only
	// sign that work is in progress. actionDoneMsg/actionFailedMsg clear
	// running when the action settles, restoring normal clearing below.
	if !m.isQuitKey(msg) && !m.action.running {
		m.action.status = ""
		m.action.statusIsError = false
	}

	if m.screen == ScreenSearch && m.search.input.Focused() {
		switch {
		case m.isQuitKey(msg): // only ctrl+c while focused — see isQuitKey
			return m.startQuit()
		case key.Matches(msg, m.keys.Blur):
			m.search.input.Blur()
			return m, nil
		case key.Matches(msg, m.keys.Submit):
			m.search.input.Blur()
			return m.startSearch(m.search.input.Value())
		default:
			var cmd tea.Cmd
			m.search.input, cmd = m.search.input.Update(msg)
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.startQuit()
	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, m.keys.NextScreen):
		return m.gotoScreen(screenAt((m.screenIndex() + 1) % len(screens)))
	case key.Matches(msg, m.keys.PrevScreen):
		return m.gotoScreen(screenAt((m.screenIndex() - 1 + len(screens)) % len(screens)))
	case key.Matches(msg, m.keys.Dashboard):
		return m.gotoScreen(ScreenDashboard)
	case key.Matches(msg, m.keys.InstalledMods):
		return m.gotoScreen(ScreenInstalledMods)
	case key.Matches(msg, m.keys.Search), key.Matches(msg, m.keys.SearchScreen):
		return m.gotoScreenFocused(ScreenSearch)
	case key.Matches(msg, m.keys.NextPage):
		// #111 Tier 3: n is now a "jump a paneful" selection accelerator,
		// not a page turn - infinite scroll has no pages. Moving forward
		// can walk into refill range, so it runs through
		// afterSearchSelectionMove like Up/Down does.
		if m.screen == ScreenSearch && m.search.state == searchReady {
			m.moveSelection(m.search.fetchSize)
			return m.afterSearchSelectionMove()
		}
		return m, nil
	case key.Matches(msg, m.keys.PrevPage):
		// p jumps backward by the same distance - deliberately WITHOUT
		// calling afterSearchSelectionMove: every row a backward jump can
		// land on is already in the buffer, so this must never dispatch a
		// refill (see maybeRefillSearch's doc comment on the low-water
		// check this skips entirely).
		if m.screen == ScreenSearch && m.search.state == searchReady {
			m.moveSelection(-m.search.fetchSize)
		}
		return m, nil
	case key.Matches(msg, m.keys.CycleSource):
		if m.screen == ScreenSearch && len(m.search.sources) > 1 {
			m.search.sourceIdx = (m.search.sourceIdx + 1) % len(m.search.sources)
			// Cycling the target source must not leave the header, results,
			// and pagination disagreeing about which source they describe:
			// cancel any in-flight query for the old source, bump gen so a
			// late result/failure for it is discarded as stale, and drop
			// back to idle (keeping the typed query) so the user resubmits
			// explicitly against the new source.
			if m.search.cancel != nil {
				m.search.cancel()
				m.search.cancel = nil
			}
			m.search.gen++
			m.search.state = searchIdle
			// #111 Tier 3 fix round 5 - see the profile-switch reset above
			// (actionDoneMsg's switchedTo handling) for why these are reset
			// here too, not just left to the next submit.
			m.search.refilling = false
			m.search.refreshing = false
		}
		return m, nil
	case key.Matches(msg, m.keys.Profiles):
		return m.gotoScreen(ScreenProfiles)
	case key.Matches(msg, m.keys.Sources):
		return m.gotoScreen(ScreenSources)
	case key.Matches(msg, m.keys.ConflictsScreen):
		return m.gotoScreen(ScreenConflicts)
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
		return m.afterSearchSelectionMove()
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
		return m.afterSearchSelectionMove()
	case key.Matches(msg, m.keys.Select):
		// Select ("enter") is context-dependent: it opens a dashboard menu
		// entry everywhere except Profiles, where Task 7 repurposes it to
		// switch to the selected (non-active) profile - see mutations.go's
		// switchSelectedProfile.
		if m.screen == ScreenProfiles {
			return m.switchSelectedProfile()
		}
		return m.openSelectedMenuEntry()
	case key.Matches(msg, m.keys.ToggleEnable):
		return m.toggleSelectedModEnable()
	case key.Matches(msg, m.keys.Uninstall):
		return m.uninstallSelectedMod()
	case key.Matches(msg, m.keys.Deploy):
		return m.deployActiveProfile()
	case key.Matches(msg, m.keys.Install):
		return m.installSelectedSearchResult()
	case key.Matches(msg, m.keys.CheckUpdates):
		return m.checkForUpdates()
	case key.Matches(msg, m.keys.Files):
		return m.showDeployedFiles()
	case key.Matches(msg, m.keys.Policy):
		return m.editSelectedModPolicy()
	case key.Matches(msg, m.keys.CreateProfile):
		return m.createProfilePrompt()
	case key.Matches(msg, m.keys.DeleteProfile):
		return m.deleteSelectedProfile()
	case key.Matches(msg, m.keys.ImportProfile):
		return m.importProfilePrompt()
	case key.Matches(msg, m.keys.ExportProfile):
		return m.exportProfilePrompt()
	case key.Matches(msg, m.keys.ToggleAllSources):
		return m.toggleSourcesAll()
	case key.Matches(msg, m.keys.Purge):
		return m.purgeProfilePrompt()
	case key.Matches(msg, m.keys.GameSwitch):
		return m.openGameSwitcher()
	case key.Matches(msg, m.keys.MoveDown):
		return m.moveSelectedMod(1)
	case key.Matches(msg, m.keys.MoveUp):
		return m.moveSelectedMod(-1)
	case key.Matches(msg, m.keys.Rollback):
		return m.rollbackSelectedMod()
	case key.Matches(msg, m.keys.Changelog):
		// Fix-wave-2 smoke finding #1: 'v' on Installed Mods, OUTSIDE any
		// modal - the modal-scoped 'v' (updatePendingActionKey, actions.go)
		// only ever fires from the m.action.pending != nil branch ABOVE this
		// outer switch (see updateKey's own doc comment on dispatch order),
		// so the two never collide.
		return m.viewSelectedModChangelog()
	default:
		return m, nil
	}
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(m.theme.Title.Render("LMM // Linux Mod Manager"))
	b.WriteString("\n")
	// Hard-truncated to availableWidth(), matching every other fixed-height
	// line (footerLine/helpView) - Task 3's 6th screen entry ("[6]
	// Conflicts") pushed the joined nav bar past 80 columns for the first
	// time, and an untruncated overlong line here would otherwise trigger
	// lipgloss's automatic word-wrap and silently grow the view past its
	// fixed height budget (the same defect class truncate()'s other call
	// sites already guard against - see its own doc comment).
	b.WriteString(truncate(m.nav(), m.availableWidth()))
	b.WriteString("\n\n")

	b.WriteString(m.screenView())

	// Exactly one extra line when a status is set (see
	// contentChromeHeight's matching statusHeight accounting); none when
	// it's "".
	if status := m.statusLine(); status != "" {
		b.WriteString("\n")
		b.WriteString(status)
	}

	b.WriteString("\n\n")
	if m.showHelp {
		b.WriteString(m.helpView())
	} else {
		b.WriteString(m.footerLine())
	}

	app := m.theme.App
	if m.width > 0 {
		app = app.Width(m.width)
	}
	if m.height > 0 {
		app = app.Height(m.height)
	}

	return app.Render(b.String())
}

// footerLine renders the bottom key-hint line shown whenever the help
// overlay isn't. Finding 3 (smoke test): the old "e/x/D: mutate" hint named
// the keys but never said what they DO, so the mutation hints are spelled
// out explicitly instead, matching the "·"-separated sub-hint style
// searchFooterLine already uses. The whole line is hard-truncated
// (ANSI-safe truncate()) to availableWidth() so a narrower terminal
// degrades by dropping trailing hints rather than word-wrapping into an
// extra row - which would silently grow the view past contentChromeHeight's
// fixed footerHeight == 1 assumption. Per the smoke tester's follow-up
// guidance, 160 columns (not 80) is the normal case the full wording is
// designed for; narrower terminals are expected to lose some trailing hints
// to truncation rather than the wording being shortened to fit them.
func (m Model) footerLine() string {
	hint := "?: help  tab/h/l: screens  ↑↓/j/k: move  /: search  i: install  e: enable/disable · x: uninstall · D: deploy · u: check updates  enter: switch  q: quit"
	return truncate(m.theme.Help.Render(hint), m.availableWidth())
}

// CurrentScreen exposes the selected screen for tests.
func (m Model) CurrentScreen() Screen {
	return m.screen
}

// SelectedIndex exposes the selected row for tests.
func (m Model) SelectedIndex(screen Screen) int {
	return m.selected[screen]
}

// HelpVisible exposes help overlay state for tests.
func (m Model) HelpVisible() bool {
	return m.showHelp
}

// Layout exposes the active layout for tests and future visual selection UI.
func (m Model) Layout() Layout {
	return m.layout
}

func (m Model) screenIndex() int {
	for i, screen := range screens {
		if screen == m.screen {
			return i
		}
	}
	return 0
}

func (m Model) moveSelection(delta int) {
	max := m.itemCount(m.screen) - 1
	if max < 0 {
		return
	}

	next := m.selected[m.screen] + delta
	if next < 0 {
		next = 0
	}
	if next > max {
		next = max
	}
	m.selected[m.screen] = next
}

func (m Model) itemCount(screen Screen) int {
	switch screen {
	case ScreenInstalledMods:
		return len(m.mods)
	case ScreenSearch:
		return len(m.search.page.Results)
	case ScreenProfiles:
		return len(m.profiles)
	case ScreenSources:
		return len(m.sources)
	case ScreenConflicts:
		return len(m.conflicts)
	default:
		return len(m.dashboardMenu())
	}
}

// nav renders the top nav bar, picking the narrowest tier that still fits
// availableWidth() (measured with lipgloss.Width, ANSI-aware):
//
//  1. full: every screen as "[N] Label" ("•N• Label" for the current one).
//  2. current-label-only: non-current screens collapse to just their "[N]"
//     cell; the current screen keeps its full "•N• Label".
//  3. numbers-only: every screen collapses to its number cell.
//
// This exists because #108's sixth screen entry ("[6] Conflicts") pushed
// the full nav to ~87 cells, past an 80-column terminal's ~76-cell
// availableWidth() - before this fix nav() always returned the full-width
// line and left View()'s hard truncate (see its own doc comment; still the
// final safety net below) to cut it down, which chopped the tail label -
// and, when the current screen was last, its marker too - off entirely
// rather than degrading gracefully. Measuring and stepping down tiers
// means the common case (a mid-size terminal) still shows every label,
// and only pathologically narrow terminals fall all the way to bare
// numbers; panel titles still identify the current screen even then.
//
// Every tier keeps the •N• marker (#91 audit): nav() distinguished the
// current screen from the rest by color alone, which disappears under
// NO_COLOR/--no-color/non-color terminals where Selected and MutedText
// render byte-identical text. •N• replaces (never joins) the [N] cell -
// SAME WIDTH is load-bearing (PR #107 review): the first fix prefixed
// "• ", which grew the line and shifted the hard truncation so the
// rightmost label degraded at 80 cols (the committed goldens caught it as
// a dangling "•…"). The glyph swap itself - [N] versus •N• inside that one
// three-cell slot - is zero-width in every tier: it never changes a tier's
// measured width by itself.
//
// That is NOT the same as tier selection being current-screen-independent.
// Tiers 1 and 3 render every screen the same way regardless of which one is
// current, so their total width IS constant across screens - but tier 2
// renders a label for the current screen ONLY, so tier 2's total width is
// 29 + len(current screen's label) BY DESIGN, and varies with whoever is
// current. Near a tier boundary this matters: at ~40 cols, navigating onto
// "Installed Mods" (the longest label) can push tier 2 over budget and flip
// the nav to tier 3, then flip back to tier 2 the moment you leave it (see
// TestNavCompressesToNumbersOnlyAt40Columns). This is a deliberate
// tradeoff, not an oversight - nav() measures each candidate per render
// rather than pinning tier 2's budget to its worst-case label, because
// pinning would force tier 3 for ALL SIX screens at every width in that
// range, when five of them fit tier 2 comfortably. The flip is confined to
// a narrow band of pathological widths, and whichever tier renders, its
// measured width always honestly fits availableWidth() - that invariant
// (not "width never depends on current screen") is what tier selection
// actually guarantees.
func (m Model) nav() string {
	width := m.availableWidth()
	for _, tier := range []func() string{m.navFull, m.navCurrentLabelOnly, m.navNumbersOnly} {
		if candidate := tier(); lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	// Even tier 3 doesn't fit (availableWidth() floors at 40, tier 3 is
	// ~28 cells, so this shouldn't happen in practice) - fall back to the
	// narrowest tier and let View()'s hard truncate handle it.
	return m.navNumbersOnly()
}

// navFull renders every screen's number cell and label - tier 1, see nav()'s
// doc comment for the tier design and the •N• marker's same-width rationale.
func (m Model) navFull() string {
	items := make([]string, 0, len(screens))
	for i, screen := range screens {
		if screen == m.screen {
			items = append(items, m.theme.Selected.Render(fmt.Sprintf("•%d• %s", i+1, screen)))
		} else {
			items = append(items, m.theme.MutedText.Render(fmt.Sprintf("[%d] %s", i+1, screen)))
		}
	}
	return strings.Join(items, "  ")
}

// navCurrentLabelOnly renders every screen's number cell, but only the
// current screen's label - tier 2, see nav()'s doc comment.
func (m Model) navCurrentLabelOnly() string {
	items := make([]string, 0, len(screens))
	for i, screen := range screens {
		if screen == m.screen {
			items = append(items, m.theme.Selected.Render(fmt.Sprintf("•%d• %s", i+1, screen)))
		} else {
			items = append(items, m.theme.MutedText.Render(fmt.Sprintf("[%d]", i+1)))
		}
	}
	return strings.Join(items, "  ")
}

// navNumbersOnly renders every screen as a bare number cell, current
// included - tier 3, see nav()'s doc comment.
func (m Model) navNumbersOnly() string {
	items := make([]string, 0, len(screens))
	for i, screen := range screens {
		if screen == m.screen {
			items = append(items, m.theme.Selected.Render(fmt.Sprintf("•%d•", i+1)))
		} else {
			items = append(items, m.theme.MutedText.Render(fmt.Sprintf("[%d]", i+1)))
		}
	}
	return strings.Join(items, "  ")
}

// screenView renders whichever modal (if any) is currently showing, mirroring
// updateKey's own ordering exactly (see that method's doc comment for why
// picker/inputModal/overlay are checked before action.pending) so a
// changelog overlay/picker opened on top of the apply-updates modal renders
// on top too, not the modal waiting underneath it.
func (m Model) screenView() string {
	if m.picker != nil {
		return m.pickerView()
	}

	if m.inputModal != nil {
		return m.inputModalView()
	}

	if m.overlay != nil {
		return m.overlayView()
	}

	if m.action.pending != nil {
		return m.actionModalView()
	}

	switch m.state {
	case stateLoading:
		return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
			Render(m.theme.PanelTitle.Render("Consulting the archives..."))
	case stateFailed:
		lines := []string{
			m.theme.PanelTitle.Render("THE RITUAL FAILED"),
			m.theme.DangerText.Render(m.loadErr.Error()),
			"",
			m.theme.MutedText.Render("q: quit"),
		}
		// m.loadErr is arbitrary runtime data (a wrapped source/network error)
		// with no length bound; truncateLines (see clamp.go) keeps it from
		// lipgloss-auto-wrapping past the height budget (#42).
		panelContentWidth := max(m.availableWidth()-m.theme.Panel.GetHorizontalFrameSize(), 1)
		lines = m.truncateLines(lines, panelContentWidth)
		contentBudget := max(m.availableContentHeight()-m.theme.Panel.GetVerticalBorderSize(), 1)
		lines = m.clampLines(lines, contentBudget)
		return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
			Render(strings.Join(lines, "\n"))
	}

	switch m.screen {
	case ScreenDashboard:
		return m.dashboardView()
	case ScreenInstalledMods:
		return m.modsView()
	case ScreenSearch:
		return m.searchView()
	case ScreenProfiles:
		return m.profilesView()
	case ScreenSources:
		return m.sourcesView()
	case ScreenConflicts:
		return m.conflictsView()
	default:
		return m.dashboardView()
	}
}

func (m Model) dashboardView() string {
	switch m.layout {
	case LayoutMonochromeTerminal:
		return m.terminalDashboardView()
	case LayoutCommander:
		return m.commanderDashboardView()
	case LayoutCrtStack:
		return m.crtDashboardView()
	default:
		return m.partyDashboardView()
	}
}

// partyDashboardView clamps each panel's content to its height budget to
// prevent silently growing beyond the terminal (#42).
func (m Model) partyDashboardView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	gap := 1
	panelWidth := max((width-gap)/2, 1)
	splitHeight := height
	topHeight := splitHeight / 2
	menuHeight := splitHeight - topHeight

	topBudget := max(topHeight-m.theme.Panel.GetVerticalBorderSize(), 1)
	menuBudget := max(menuHeight-m.theme.Panel.GetVerticalBorderSize(), 1)

	partyLines := m.clampLines([]string{
		m.theme.PanelTitle.Render("PARTY"),
		fmt.Sprintf("Game:    %s", m.summary.GameName),
		fmt.Sprintf("Profile: %s", m.summary.ProfileName),
		fmt.Sprintf("Mods:    %d installed / %d enabled", m.summary.Installed, m.summary.Enabled),
	}, topBudget)

	questLines := m.clampLines([]string{
		m.theme.PanelTitle.Render("QUEST LOG"),
		fmt.Sprintf("%s updates available", m.theme.WarningText.Render(countLabel(m.summary.Updates))),
		fmt.Sprintf("%s file conflict", m.theme.DangerText.Render(countLabel(m.summary.Conflicts))),
		fmt.Sprintf("Last deploy: %s", lastDeployLabel(m.now(), m.summary.LastDeploy)),
	}, topBudget)

	menuLines := m.clampLines(
		append([]string{m.theme.PanelTitle.Render("COMMANDS")}, m.dashboardMenuRows()...),
		menuBudget)

	// A long game/profile name would otherwise lipgloss-auto-wrap inside the
	// half-width top panels; truncateLines (see clamp.go) prevents that (#42).
	topContentWidth := max(panelWidth-m.theme.Panel.GetHorizontalFrameSize(), 1)
	menuContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	partyLines = m.truncateLines(partyLines, topContentWidth)
	questLines = m.truncateLines(questLines, topContentWidth)
	menuLines = m.truncateLines(menuLines, menuContentWidth)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panelWithHeight(panelWidth, topHeight).Render(strings.Join(partyLines, "\n")),
		" ",
		m.panelWithHeight(panelWidth, topHeight).Render(strings.Join(questLines, "\n")),
	) + "\n" + m.panelWithHeight(width, menuHeight).Render(strings.Join(menuLines, "\n"))
}

// terminalDashboardView clamps content to the height budget to prevent
// silently growing beyond the terminal (#42).
func (m Model) terminalDashboardView() string {
	rows := []string{
		m.theme.PanelTitle.Render("BOOT SEQUENCE // MOD GUILD TERMINAL"),
		fmt.Sprintf("> GAME     %s", m.summary.GameName),
		fmt.Sprintf("> PROFILE  %s", m.summary.ProfileName),
		fmt.Sprintf("> MODS     %d INSTALLED / %d ENABLED", m.summary.Installed, m.summary.Enabled),
		fmt.Sprintf("> ALERTS   %s UPDATES // %s CONFLICT", m.theme.WarningText.Render(countLabel(m.summary.Updates)), m.theme.DangerText.Render(countLabel(m.summary.Conflicts))),
		fmt.Sprintf("> DEPLOY   %s", strings.ToUpper(lastDeployLabel(m.now(), m.summary.LastDeploy))),
		"",
	}
	rows = append(rows, m.dashboardMenuRows()...)
	budget := max(m.availableContentHeight()-m.theme.Panel.GetVerticalBorderSize(), 1)
	rows = m.clampLines(rows, budget)
	// A long game/profile name would otherwise lipgloss-auto-wrap into extra
	// physical lines; truncateLines (see clamp.go) prevents that (#42).
	contentWidth := max(m.availableWidth()-m.theme.Panel.GetHorizontalFrameSize(), 1)
	rows = m.truncateLines(rows, contentWidth)
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

// commanderDashboardView clamps each side's content to its height budget to
// prevent silently growing beyond the terminal (#42).
func (m Model) commanderDashboardView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	gap := 1
	leftWidth := max((width-gap)/2, 1)
	rightWidth := max(width-gap-leftWidth, 1)

	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)
	leftLines := m.clampLines([]string{
		m.theme.PanelTitle.Render("ACTIVE PROFILE"),
		m.summary.ProfileName,
		"",
		fmt.Sprintf("Game     %s", m.summary.GameName),
		fmt.Sprintf("Enabled  %d", m.summary.Enabled),
		fmt.Sprintf("Updates  %s", countLabel(m.summary.Updates)),
		fmt.Sprintf("Deploy   %s", lastDeployLabel(m.now(), m.summary.LastDeploy)),
	}, contentBudget)
	rightLines := m.clampLines(
		append([]string{m.theme.PanelTitle.Render("OPERATIONS")}, m.dashboardMenuRows()...),
		contentBudget)

	// These half-width panels are narrow enough at small terminal widths that
	// an overlong row ("> Consult Conflict Oracle" at width 40) would
	// lipgloss-auto-wrap; truncateLines (see clamp.go) prevents that (#42).
	leftContentWidth := max(leftWidth-m.theme.Panel.GetHorizontalFrameSize(), 1)
	rightContentWidth := max(rightWidth-m.theme.Panel.GetHorizontalFrameSize(), 1)
	leftLines = m.truncateLines(leftLines, leftContentWidth)
	rightLines = m.truncateLines(rightLines, rightContentWidth)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panelWithHeight(leftWidth, height).Render(strings.Join(leftLines, "\n")),
		" ",
		m.panelWithHeight(rightWidth, height).Render(strings.Join(rightLines, "\n")),
	)
}

// crtDashboardView clamps content to the height budget to prevent
// silently growing beyond the terminal (#42).
func (m Model) crtDashboardView() string {
	rows := []string{
		m.theme.PanelTitle.Render("CRT STATUS STACK"),
		fmt.Sprintf("▓ %-10s %s", "GAME", m.summary.GameName),
		fmt.Sprintf("▓ %-10s %s", "PROFILE", m.summary.ProfileName),
		fmt.Sprintf("▓ %-10s %d/%d", "MODS", m.summary.Enabled, m.summary.Installed),
		fmt.Sprintf("▓ %-10s %s updates, %s conflict", "SIGNAL", countLabel(m.summary.Updates), countLabel(m.summary.Conflicts)),
		fmt.Sprintf("▓ %-10s %s", "DEPLOY", lastDeployLabel(m.now(), m.summary.LastDeploy)),
		"",
	}
	rows = append(rows, m.dashboardMenuRows()...)
	budget := max(m.availableContentHeight()-m.theme.Panel.GetVerticalBorderSize(), 1)
	rows = m.clampLines(rows, budget)
	// A long game/profile name would otherwise lipgloss-auto-wrap into extra
	// physical lines; truncateLines (see clamp.go) prevents that (#42).
	contentWidth := max(m.availableWidth()-m.theme.Panel.GetHorizontalFrameSize(), 1)
	rows = m.truncateLines(rows, contentWidth)
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

// modsView renders the Installed Mods screen: selectable list of active-profile
// mods with windowed height (never exceeds budget) and scroll-follow-selection
// (selected row stays visible when navigation walks past the fold, #42).
func (m Model) modsView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	rows := []string{m.theme.PanelTitle.Render("SPELLBOOK: INSTALLED MODS"), "[/] Search"}
	if len(m.mods) == 0 {
		rows = append(rows, m.theme.MutedText.Render("No mods installed yet. 'lmm install <mod>' begins the quest."))
	}
	listBudget := max(contentBudget-len(rows), 0)
	rows = append(rows, m.windowedRows(len(m.mods), m.selected[ScreenInstalledMods], listBudget, func(i int) string {
		return m.modRow(i, width, m.mods[i])
	})...)
	return m.panelWithHeight(width, height).Render(strings.Join(rows, "\n"))
}

// searchHeaderLines renders the two lines shared by every search state: the
// panel title with the active source, and the query input itself. In
// searchReady, the source label reflects m.search.page.Source (the source
// the on-screen results actually came from) rather than m.search.source()
// (the target of the next search), so cycling sources mid-view can never
// make the header claim a source the results don't match. Every other state
// has no results yet, so source() (the next search's target) is correct.
func (m Model) searchHeaderLines() []string {
	title := m.theme.PanelTitle.Render("ARCHIVE SEARCH")
	source := m.search.source()
	if m.search.state == searchReady {
		source = m.search.page.Source
	}
	meta := m.theme.MutedText.Render(fmt.Sprintf("[source: %s  (s cycles)]", sourceLabel(source)))
	return []string{title + "  " + meta, m.search.input.View()}
}

// searchWarningLine renders m.search.page.Warnings — per-source failures
// surfaced by all-sources searches, see SearchPage.Warnings — as one status
// line truncated to width, or "" when there are none. Only meaningful in
// searchReady (the only state where page is guaranteed to describe the
// on-screen results; see searchHeaderLines's source-label comment for the
// same reasoning applied to the source label).
//
// width must match where the caller places the line: searchReadyView's
// header sits OUTSIDE any Width()-constrained panel, so it truncates to
// m.availableWidth(); the zero-results branch of searchView places it
// INSIDE searchSinglePanel, whose content width is narrower by the panel's
// horizontal frame size (border + padding, see searchInputWidthFor's
// equivalent math). Passing the wrong width lets a still-overlong line
// reach a narrower panel, where lipgloss re-wraps it into extra physical
// lines and grows the view past the fixed height budget.
func (m Model) searchWarningLine(width int) string {
	warnings := m.search.page.Warnings
	if len(warnings) == 0 {
		return ""
	}
	noun := "source"
	if len(warnings) != 1 {
		noun = "sources"
	}
	line := fmt.Sprintf("⚠ %d %s unavailable: %s", len(warnings), noun, strings.Join(warnings, "; "))
	return truncate(m.theme.WarningText.Render(line), width)
}

// searchSinglePanel wraps header+body lines in one full-bounds panel, used by
// every search state except the ready-with-results two-pane layout. Several
// of those states interpolate unbounded dynamic text (searchFailed's
// m.search.err.Error(), zero-results' user-supplied query), so lines are run
// through truncateLines (see clamp.go) before the row count is clamped to the
// panel's content budget (#42).
func (m Model) searchSinglePanel(lines []string) string {
	panelContentWidth := max(m.availableWidth()-m.theme.Panel.GetHorizontalFrameSize(), 1)
	lines = m.truncateLines(lines, panelContentWidth)
	contentBudget := max(m.availableContentHeight()-m.theme.Panel.GetVerticalBorderSize(), 1)
	lines = m.clampLines(lines, contentBudget)
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
		Render(strings.Join(lines, "\n"))
}

func (m Model) searchView() string {
	header := m.searchHeaderLines()

	switch m.search.state {
	case searchLoading:
		return m.searchSinglePanel(append(header, "Consulting the archive index..."))
	case searchAuthRequired:
		return m.searchSinglePanel(append(header,
			m.theme.DangerText.Render(fmt.Sprintf("Authentication required for %s.", m.search.authSource)),
			fmt.Sprintf("Run 'lmm auth login %s' in a shell, then search again.", m.search.authSource),
		))
	case searchFailed:
		return m.searchSinglePanel(append(header, m.theme.DangerText.Render(m.search.err.Error())))
	case searchReady:
		if len(m.search.page.Results) == 0 {
			// Placed inside searchSinglePanel below, so the warning must
			// truncate to the panel's content width, not the full terminal
			// width — see searchWarningLine's doc comment.
			panelContentWidth := max(m.availableWidth()-m.theme.Panel.GetHorizontalFrameSize(), 1)
			if warning := m.searchWarningLine(panelContentWidth); warning != "" {
				header = append(header, warning)
			}
			// #58 item 3: an all-sources search (Source == "") whose
			// AttemptedCount is 0 means NONE of the game's configured
			// sources support searching at all - a genuinely different
			// condition from a capable source finding zero matches, which
			// "No archives matched" would otherwise claim just as
			// confidently. Single-source searches never populate
			// AttemptedCount (it stays its 0 zero value), so this branch is
			// gated on the all-sources sentinel too, not AttemptedCount
			// alone.
			msg := fmt.Sprintf("No archives matched %q on %s.", m.search.page.Query, sourceLabel(m.search.page.Source))
			if m.search.page.Source == "" && m.search.page.AttemptedCount == 0 {
				msg = fmt.Sprintf("None of %s's sources support searching; install by ID instead.", displayGameName(m.search.gameName))
			}
			return m.searchSinglePanel(append(header, m.theme.MutedText.Render(msg)))
		}
		if warning := m.searchWarningLine(m.availableWidth()); warning != "" {
			header = append(header, warning)
		}
		return m.searchReadyView(header)
	default: // searchIdle
		// Only the EXPLICIT "go search" paths (gotoScreenFocused: "/", "3", and
		// the dashboard menu's "Search Archives" entry) focus the input on
		// entry; passive screen-cycling and the other direct jump keys leave it
		// unfocused, so the hint always needs to mention "/ focus" unless the
		// input happens to already be focused. While focused, 's' types into
		// the query (not a source-cycle shortcut), so exclude it from the
		// focused hint.
		hint := "enter search · esc unfocus"
		if !m.search.input.Focused() {
			hint = "/ focus · s source"
		}
		return m.searchSinglePanel(append(header, m.theme.MutedText.Render(hint)))
	}
}

// searchReadyView renders the two-pane results/detail layout, mirroring
// commanderDashboardView's width math so the panes plus a 1-column gap sum to
// exactly availableWidth(). Unlike the other search states, this view's
// header and footer lines sit outside any Width()-constrained panel style,
// so they are hard-capped to width here: lipgloss.Width of the whole view is
// the max width across its lines, and the panes line already sums to exactly
// width, but an unclamped header/footer line would push that max past width
// and wrap the bordered panes onto separate output lines at narrow sizes.
func (m Model) searchReadyView(header []string) string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	footer := m.searchFooterLine()

	paneHeight := max(height-len(header)-1, 1)
	gap := 1
	leftWidth := max((width-gap)/2, 1)
	rightWidth := max(width-gap-leftWidth, 1)

	// Panel content must never exceed paneContentHeight: lipgloss pads short
	// content to a set Height() but does not clip content taller than it, so
	// an unbounded row count or a long summary would silently grow the
	// rendered block past paneHeight and break the exact-height invariant.
	paneContentHeight := max(paneHeight-m.theme.Panel.GetVerticalBorderSize(), 1)

	panes := lipgloss.JoinHorizontal(lipgloss.Top,
		m.panelWithHeight(leftWidth, paneHeight).Render(m.searchResultsPane(leftWidth, paneContentHeight)),
		" ",
		m.panelWithHeight(rightWidth, paneHeight).Render(m.searchDetailPane(rightWidth, paneContentHeight)),
	)

	lines := make([]string, 0, len(header)+2)
	for _, line := range header {
		lines = append(lines, truncate(line, width))
	}
	lines = append(lines, panes, truncate(footer, width))
	return strings.Join(lines, "\n")
}

// searchResultsPane renders the selectable result rows: name / version /
// status, with "installed" statuses styled to pop. In all-sources mode
// (m.search.source() == "", i.e. the search that produced page targeted
// every configured source), a source column is added so results from
// different sources can be told apart; single-source mode's columns are
// unchanged. Column widths are derived from the pane's actual content width
// (rather than fixed constants) and the name column always absorbs
// whatever's left, so the columns can never sum past innerWidth. Overflowing
// values truncate instead of overflowing into lipgloss's automatic line
// wrap, which would silently break the exact-height layout invariant. Rows
// beyond maxLines scroll-follow the selection via m.windowedRows instead of
// a first-N slice — a first-N slice let the highlight vanish off-screen
// once selection walked past the fold, even though the detail pane kept
// tracking the invisible selection (#42).
func (m Model) searchResultsPane(width, maxLines int) string {
	const prefixWidth = 2 // m.row()'s "> "/"  " selection marker

	withSource := m.search.source() == ""
	gaps := 2 // separating spaces between columns (name|version|status)
	minAvail := 3
	if withSource {
		gaps = 3 // one more separator for the added source column
		minAvail = 4
	}

	innerWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	avail := max(innerWidth-prefixWidth-gaps, minAvail)
	statusWidth := min(max(avail/4, 1), 9) // "installed"/"available" are 9 runes
	versionWidth := min(max(avail/4, 1), 8)
	sourceWidth := 0
	if withSource {
		sourceWidth = min(max(avail/5, 1), 10)
	}
	nameWidth := max(avail-statusWidth-versionWidth-sourceWidth, 1)

	results := m.search.page.Results
	rowFor := func(i int) string {
		item := results[i]
		status := fmt.Sprintf("%-*s", statusWidth, truncate(item.Status, statusWidth))
		if item.Status == "installed" {
			status = m.theme.WarningText.Render(status)
		}
		var line string
		if withSource {
			line = fmt.Sprintf("%-*s %-*s %-*s %s",
				nameWidth, truncate(item.Name, nameWidth),
				versionWidth, truncate(item.Version, versionWidth),
				sourceWidth, truncate(item.Source, sourceWidth),
				status)
		} else {
			line = fmt.Sprintf("%-*s %-*s %s",
				nameWidth, truncate(item.Name, nameWidth),
				versionWidth, truncate(item.Version, versionWidth),
				status)
		}
		return m.row(i, line)
	}
	return strings.Join(m.windowedRows(len(results), m.selected[ScreenSearch], maxLines, rowFor), "\n")
}

// searchDetailPane renders the fields for the currently selected result.
// Unknown endorsements render "?" (countLabel convention: never fake data).
// Labels and free-text values are truncated to the pane's content width for
// the same reason searchResultsPane truncates: overflow would trigger an
// unpredictable automatic re-wrap inside the bordered panel. The 8 fixed
// field lines are themselves clamped to maxLines (via m.clampLines) before
// the summary is considered — on a floor-height terminal the fields alone
// can exceed the pane's budget, and the summary's own clipping only ever
// accounted for the fields fitting (#42). The summary is then clipped to
// whatever vertical budget remains after the (now-clamped) fixed fields so
// a long summary can never grow the pane past maxLines.
func (m Model) searchDetailPane(width, maxLines int) string {
	results := m.search.page.Results
	idx := m.selected[ScreenSearch]
	if idx < 0 || idx >= len(results) {
		return m.theme.MutedText.Render("No selection.")
	}
	item := results[idx]

	endorsements := "?"
	if item.HasEndorsements {
		endorsements = fmt.Sprintf("%d", item.Endorsements)
	}

	innerWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	labelWidth := min(13, max(innerWidth-1, 1)) // len("Endorsements ") == 13
	// Floored at 0, not 1: truncate already returns "" for width <= 0
	// (app.go's truncate), so a pathologically narrow pane (innerWidth 1,
	// labelWidth 1) simply renders no value instead of a value column wider
	// than what's left after the label (#42).
	valueWidth := max(innerWidth-labelWidth, 0)
	field := func(label, value string) string {
		return fmt.Sprintf("%-*s%s", labelWidth, truncate(label, labelWidth), truncate(value, valueWidth))
	}

	// Status gets the same WarningText treatment as searchResultsPane's
	// status column when installed — this was the one place the status was
	// spelled out in full and the one place it didn't pop (#42).
	statusValue := truncate(item.Status, valueWidth)
	if item.Status == "installed" {
		statusValue = m.theme.WarningText.Render(statusValue)
	}

	lines := []string{
		m.theme.PanelTitle.Render("DETAIL"),
		field("Name", item.Name),
		field("Author", item.Author),
		field("Version", item.Version),
		field("Source", item.Source),
		fmt.Sprintf("%-*s%s", labelWidth, truncate("Status", labelWidth), statusValue),
		field("Downloads", fmt.Sprintf("%d", item.Downloads)),
		field("Endorsements", endorsements),
	}
	lines = m.clampLines(lines, maxLines)

	if summaryBudget := maxLines - len(lines) - 1; summaryBudget > 0 && item.Summary != "" {
		lines = append(lines, "")
		summary := strings.Split(m.theme.MutedText.Width(innerWidth).Render(item.Summary), "\n")
		if len(summary) > summaryBudget {
			summary = summary[:summaryBudget]
			last := summaryBudget - 1
			summary[last] = strings.TrimRight(summary[last], " ") + m.theme.MutedText.Render("…")
		}
		lines = append(lines, summary...)
	}

	return strings.Join(lines, "\n")
}

// searchFooterLine renders the session's LOAD status (#111 Tier 3: infinite
// scroll replaced per-page pagination - see searchModel.buffer's own doc
// comment for why an aggregate "Page N/M" figure could never be made
// honest). Three forms, chosen by what's actually knowable:
//   - a single source that reports a TotalCount: "X of Y loaded" - Y is the
//     source's own reported total (not derived from how many rounds have
//     run), so this stays accurate before AND after providerExhausted
//     flips, unlike the other two forms below.
//   - otherwise (aggregate mode, or a single source with no reported
//     total), once providerExhausted: "all X shown" - X is definitively
//     everything there is.
//   - otherwise, still fetching: "X loaded · more available" - honest about
//     there being more without claiming to know how much (an aggregate
//     TotalCount is SUMMED across sources with independent per-round
//     cursors and cannot bound the eventual total the way a single
//     source's can - see roundExhausted's doc comment).
//
// X is len(page.Results) - the RENDERED view, not len(buffer) directly:
// applyRoundResult always keeps the two in lockstep for every real fetch, so
// this only matters for tests that hand-construct m.search.page without
// separately populating buffer (a pre-existing, still-common convention -
// see e.g. TestSearchViewRendersStates), which would otherwise report "0
// loaded" against results that plainly render.
//
// A trailing "· fetching…" appears whenever a refill is actually in flight
// (searchModel.refilling), regardless of which of the three forms above is
// showing - the transient signal that walking further down might briefly
// stall while a KeyMsg is bound.
func (m Model) searchFooterLine() string {
	s := m.search
	loaded := len(s.page.Results)

	var footer string
	switch {
	case s.page.Source != "" && s.page.TotalCount > 0:
		footer = fmt.Sprintf("%d of %d loaded", loaded, s.page.TotalCount)
	case s.providerExhausted:
		footer = fmt.Sprintf("all %d shown", loaded)
	default:
		footer = fmt.Sprintf("%d loaded · more available", loaded)
	}

	if s.refilling {
		footer += " · fetching…"
	}
	return footer
}

// profilesView renders the Profiles screen: selectable list of profiles with
// windowed height (never exceeds budget) and scroll-follow-selection (selected
// row stays visible when navigation walks past the fold, #42).
func (m Model) profilesView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	rows := []string{m.theme.PanelTitle.Render("PROFILE ROSTER")}
	listBudget := max(contentBudget-len(rows), 0)
	rows = append(rows, m.windowedRows(len(m.profiles), m.selected[ScreenProfiles], listBudget, func(i int) string {
		return m.profileRow(i, width, m.profiles[i])
	})...)
	return m.panelWithHeight(width, height).Render(strings.Join(rows, "\n"))
}

// profileRow renders one Profiles row: active marker / name / mod count.
// Same defect class modRow was fixed for (see modRow's doc comment): the
// name column used to be a fixed 22 runes with no truncation
// ("%s %-22s %3d mods"), so a longer name overflowed it unchecked and
// shifted the mod-count column out of alignment with shorter rows. The
// mod-count column gets a proportional (clamped) share of the panel's
// width and the name column absorbs whatever's left, so it grows with the
// panel instead of staying a small fixed number. truncate() (ANSI-safe,
// hard cutoff with an ellipsis) is applied to both fields so an overlong
// value can never push the mod-count column out of place.
func (m Model) profileRow(index, width int, profile ProfileItem) string {
	const prefixWidth = 2 // m.row()'s "> "/"  " selection marker
	const activeWidth = 1 // "*"/" " active marker
	const gaps = 2        // separating spaces: marker-name, name-count
	const minName = 8

	avail := max(width-m.theme.Panel.GetHorizontalFrameSize()-prefixWidth-activeWidth-gaps, minName)
	countWidth := min(9, max(avail/6, 4)) // "999 mods" is up to 8 runes
	nameWidth := max(avail-countWidth, minName)

	active := " "
	if profile.Active {
		active = "*"
	}
	count := fmt.Sprintf("%d mods", profile.ModCount)

	line := fmt.Sprintf("%s %-*s %-*s",
		active,
		nameWidth, truncate(profile.Name, nameWidth),
		countWidth, truncate(count, countWidth))
	return m.row(index, line)
}

// sourcesView renders the read-only source registry (Task 4, #75): by
// default, only the active game's configured+registered sources
// (m.sourcesShowAll == false); toggled with 'a' to every source registered
// with the DataProvider (built-in and user-defined), each marked IN USE
// when it belongs to the active game. One row each, in the single-pane list
// style profilesView uses. Unlike `lmm source list`, there is no
// error/status column — see SourceInfo's doc comment for why
// definition-load failures never reach this screen. The panel title names
// the current scope ("SOURCE REGISTRY — <game>" vs "— ALL SOURCES") so a
// toggle is visible even before reading any row. The list has windowed
// height (never exceeds budget) and scroll-follow-selection (selected row
// stays visible when navigation walks past the fold, #42).
func (m Model) sourcesView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	// Calculate the panel's content width, which is narrower than availableWidth()
	// by the panel's horizontal frame size (border + padding). Rows that render
	// INSIDE this panel must be truncated to this width to prevent lipgloss from
	// re-wrapping overlong lines and growing the view past its fixed height
	// budget; see the fix in commit 2c075e3 for the same issue in searchView's
	// zero-results warning.
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)

	title := "SOURCE REGISTRY — " + displayGameName(m.summary.GameName)
	// "  " matches m.row()'s 2-column selection-marker prefix ("> "/"  ") so
	// the header lines up with the data columns below it instead of starting
	// two columns to their left. The IN USE column exists only in the
	// all-sources view — the scoped default's rows are all trivially in
	// use, so the column would be redundant there (mirrors cmd/lmm/
	// source.go's --all-only "IN USE" column).
	headerLine := "  " + fmt.Sprintf("%-20s %-12s %-6s %s", "ID", "TYPE", "AUTH", "CAPABILITIES")
	if m.sourcesShowAll {
		title = "SOURCE REGISTRY — ALL SOURCES"
		headerLine = "  " + fmt.Sprintf("%-20s %-12s %-6s %-7s %s", "ID", "TYPE", "AUTH", "IN USE", "CAPABILITIES")
	}
	headerLine = truncate(headerLine, panelContentWidth)
	rows := []string{
		m.theme.PanelTitle.Render(title),
		m.theme.MutedText.Render(headerLine),
	}
	listBudget := max(contentBudget-len(rows), 0)
	rows = append(rows, m.windowedRows(len(m.sources), m.selected[ScreenSources], listBudget, func(i int) string {
		src := m.sources[i]
		var line string
		if m.sourcesShowAll {
			inUse := "no"
			if src.InUse {
				inUse = "yes"
			}
			line = fmt.Sprintf("%-20s %-12s %-6s %-7s %s", src.ID, src.Type, src.Auth, inUse, src.Capabilities)
		} else {
			line = fmt.Sprintf("%-20s %-12s %-6s %s", src.ID, src.Type, src.Auth, src.Capabilities)
		}
		return m.row(i, line)
	})...)
	// Truncate each row to the panel's content width AFTER m.row()'s per-row
	// call above, not before: the data fields are already width-bound by the
	// %-20s/%-12s/%-6s format verbs, but m.row() then prepends its own 2-column
	// "> "/"  " selection marker on top of that, which is what actually pushes
	// a row past panelContentWidth. Truncating pre-marker (as the render
	// closure once did) left that overhang in place; only this outer pass,
	// applied to the marker-included row, is load-bearing (#42).
	rows = m.truncateLines(rows, panelContentWidth)
	return m.panelWithHeight(width, height).Render(strings.Join(rows, "\n"))
}

// conflictsView renders the Conflicts screen (Task 3): every file conflict
// GetProfileConflicts found for the active profile, one row each, sorted by
// Path (the query's own contract - see core.GetProfileConflicts' doc
// comment) - m.conflicts is stored in that order, so no re-sort is needed
// here. Conflicts only surface for files the DB already knows were
// deployed (ownership comes from deployed_files - see ConflictItem's own
// doc comment), so a profile that has never been deployed reports none;
// the empty-state copy below deliberately does not promise pre-deploy
// detection.
func (m Model) conflictsView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()

	if len(m.conflicts) == 0 {
		return m.panelWithHeight(width, height).Render(strings.Join([]string{
			m.theme.PanelTitle.Render("CONFLICT ORACLE"),
			m.theme.MutedText.Render("No conflicts detected."),
		}, "\n"))
	}

	// Two-pane list/detail layout mirroring commanderDashboardView's width
	// math: leftWidth + gap + rightWidth sums to EXACTLY width, so
	// lipgloss.JoinHorizontal's result satisfies the "screenView uses
	// availableWidth() exactly" invariant every other screen already meets.
	gap := 1
	leftWidth := max((width-gap)/2, 1)
	rightWidth := max(width-gap-leftWidth, 1)
	paneContentHeight := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panelWithHeight(leftWidth, height).Render(m.conflictsListPane(leftWidth, paneContentHeight)),
		" ",
		m.panelWithHeight(rightWidth, height).Render(m.conflictsDetailPane(rightWidth, paneContentHeight)),
	)
}

// conflictsListPane renders the selectable FILE/OWNER/WINNER rows, marking a
// stale conflict (owner disagrees with the load-order winner - a redeploy
// would change who wins) with a leading "!" so it's visible without opening
// the detail pane. Column widths derive from the pane's actual content
// width (mirroring searchResultsPane/profileRow's own proportional-with-
// floor approach) so the columns can never sum past it; overflowing values
// truncate rather than reaching lipgloss's automatic line-wrap, which would
// silently break the exact-height layout invariant. The list has windowed
// height (never exceeds budget) and scroll-follow-selection (selected row
// stays visible when navigation walks past the fold, #42).
func (m Model) conflictsListPane(width, maxLines int) string {
	const prefixWidth = 2 // m.row()'s "> "/"  " selection marker
	const markerWidth = 2 // "! "/"  " stale marker
	const gaps = 2        // separating spaces between the 3 columns
	const minPath = 8

	innerWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	avail := max(innerWidth-prefixWidth-markerWidth-gaps, minPath)
	ownerWidth := min(14, max(avail/4, 1))
	winnerWidth := min(14, max(avail/4, 1))
	pathWidth := max(avail-ownerWidth-winnerWidth, minPath)

	headerLine := fmt.Sprintf("  %-*s %-*s %s",
		pathWidth, "FILE", ownerWidth, "OWNER", "WINNER")
	rows := []string{
		m.theme.PanelTitle.Render("CONFLICT ORACLE"),
		m.theme.MutedText.Render(truncate(headerLine, innerWidth)),
	}

	budget := max(maxLines-len(rows), 0)
	rowFor := func(i int) string {
		c := m.conflicts[i]
		marker := "  "
		if c.Stale {
			marker = m.theme.WarningText.Render("! ")
		}
		line := marker + fmt.Sprintf("%-*s %-*s %s",
			pathWidth, truncate(c.Path, pathWidth),
			ownerWidth, truncate(c.Owner, ownerWidth),
			truncate(c.Winner, winnerWidth))
		return m.row(i, line)
	}
	rows = append(rows, m.windowedRows(len(m.conflicts), m.selected[ScreenConflicts], budget, rowFor)...)
	return strings.Join(rows, "\n")
}

// conflictsDetailPane renders the fields for the currently selected
// conflict: path/owner/winner, every other providing mod (AlsoIn), and a
// hint line whose copy depends on Stale - task-3-brief.md's wording is used
// verbatim since it's the only place in the TUI that explains what a stale
// conflict means and how to resolve it.
func (m Model) conflictsDetailPane(width, maxLines int) string {
	idx := m.selected[ScreenConflicts]
	if idx < 0 || idx >= len(m.conflicts) {
		return m.theme.MutedText.Render("No selection.")
	}
	c := m.conflicts[idx]

	innerWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)
	lines := []string{
		m.theme.PanelTitle.Render("DETAIL"),
		truncate(fmt.Sprintf("File:   %s", c.Path), innerWidth),
		truncate(fmt.Sprintf("Owner:  %s", c.Owner), innerWidth),
		truncate(fmt.Sprintf("Winner: %s", c.Winner), innerWidth),
	}
	if len(c.AlsoIn) > 0 {
		lines = append(lines, truncate("Also in: "+strings.Join(c.AlsoIn, ", "), innerWidth))
	}

	hint := "reorder mods (J/K on installed) to change the winner"
	if c.Stale {
		hint = fmt.Sprintf("load order says %s should win — deploy (D) to apply", c.Winner)
	}
	lines = append(lines, "", truncate(m.theme.MutedText.Render(hint), innerWidth))

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

// helpGroup is one labeled section of the help panel: a screen name (or
// "global") plus the key entries that apply to it. Group labels are
// lowercase per Task 9's copy convention (see helpGroups).
type helpGroup struct {
	name    string
	entries []string
}

// helpRow formats a single help-panel row: left-aligned key (16 chars),
// space, description.
func helpRow(key, desc string) string {
	return fmt.Sprintf("%-16s %s", key, desc)
}

// helpEntry formats one keybinding as a help-panel row, reusing the
// binding's own key.WithHelp key/description from keys.go rather than
// restating it - the single source of truth for "what does this key do"
// stays in DefaultKeyMap.
func helpEntry(kb key.Binding) string {
	h := kb.Help()
	return helpRow(h.Key, h.Desc)
}

// helpGroups builds the full, ordered set of help groups: "global" always
// first, then every screen group that has entries, in a fixed order
// (dashboard, installed mods, search, profiles - sources has no bindings
// beyond plain navigation, so it's omitted entirely), with the CURRENT
// screen's group promoted to immediately follow global. Each screen's list
// mirrors updateKey's dispatch guards in mutations.go (e.g. Files/Policy/
// Purge all gate on ScreenInstalledMods too, alongside Deploy/CheckUpdates).
func (m Model) helpGroups() []helpGroup {
	global := helpGroup{
		name: "global",
		entries: []string{
			helpEntry(m.keys.Quit),
			helpEntry(m.keys.Help),
			helpEntry(m.keys.NextScreen),
			helpEntry(m.keys.PrevScreen),
			helpRow("1-6", "jump to a screen"),
			helpEntry(m.keys.GameSwitch),
		},
	}

	dashboard := helpGroup{
		name: "dashboard",
		entries: []string{
			// Select ("enter") is context-dependent (see updateKey): on
			// the Dashboard it opens the selected menu entry
			// (openSelectedMenuEntry), so the description is written out
			// here rather than reusing keys.go's generic "open" - the same
			// ad-hoc shape as the profiles group's "switch profile" below.
			helpRow(m.keys.Select.Help().Key, "open menu entry"),
			helpEntry(m.keys.Deploy),
			helpEntry(m.keys.CheckUpdates),
			helpEntry(m.keys.Purge),
		},
	}

	installedMods := helpGroup{
		name: "installed mods",
		entries: []string{
			helpEntry(m.keys.ToggleEnable),
			helpEntry(m.keys.Uninstall),
			helpEntry(m.keys.Deploy),
			helpEntry(m.keys.CheckUpdates),
			helpEntry(m.keys.Files),
			helpEntry(m.keys.Policy),
			helpEntry(m.keys.Purge),
			// MoveDown/MoveUp are Task 4's load-order reorder keys (see
			// mutations.go's moveSelectedMod).
			helpEntry(m.keys.MoveDown),
			helpEntry(m.keys.MoveUp),
			// Rollback is Task 6's rollback-behind-confirmation key (see
			// mutations.go's rollbackSelectedMod).
			helpEntry(m.keys.Rollback),
			// Changelog is fix-wave-2's list-scoped changelog-viewer key (see
			// mutations.go's viewSelectedModChangelog) - distinct from the
			// modal-scoped 'v' (updatePendingActionKey/
			// openChangelogFromUpdateModal, actions.go), which has no help
			// entry of its own since it only ever applies while the
			// apply-updates modal is already up (see keys.go's Changelog
			// doc comment).
			helpEntry(m.keys.Changelog),
		},
	}

	search := helpGroup{
		name: "search",
		entries: []string{
			helpEntry(m.keys.Search),
			helpEntry(m.keys.Submit),
			helpEntry(m.keys.Blur),
			helpEntry(m.keys.NextPage),
			helpEntry(m.keys.PrevPage),
			helpEntry(m.keys.CycleSource),
			helpEntry(m.keys.Install),
		},
	}

	profiles := helpGroup{
		name: "profiles",
		entries: []string{
			// Select ("enter") is context-dependent (see updateKey): on
			// Profiles it switches, not "open" like keys.go's generic
			// Select.Help() says elsewhere - so this one entry is written
			// out rather than reusing helpEntry, matching the actual
			// behavior on this screen.
			helpRow(m.keys.Select.Help().Key, "switch profile"),
			helpEntry(m.keys.CreateProfile),
			helpEntry(m.keys.DeleteProfile),
			// ImportProfile is Task 9's import binding (see mutations.go's
			// importProfilePrompt).
			helpEntry(m.keys.ImportProfile),
			// ExportProfile is Task 10's export binding (see mutations.go's
			// exportProfilePrompt).
			helpEntry(m.keys.ExportProfile),
		},
	}

	// conflicts lists Deploy (the review fix wave extended
	// deployActiveProfile's screen guard here, so the stale-conflict hint's
	// "deploy (D) to apply" names a key that actually fires - mirroring how
	// dashboard/installedMods above list the same binding) plus Up/Down -
	// documented here unlike every OTHER screen (where they're left to the
	// footer's generic "↑↓/j/k: move" hint) because selecting a row IS this
	// screen's core interaction: it's what reveals the detail pane's stale/
	// in-sync hint copy, not just a cosmetic highlight.
	conflicts := helpGroup{
		name: "conflicts",
		entries: []string{
			helpEntry(m.keys.Up),
			helpEntry(m.keys.Down),
			helpEntry(m.keys.Deploy),
		},
	}

	// sources is Task 4's Sources-screen group (#75): just the scope toggle
	// - Up/Down are left to the footer's generic hint like every screen
	// other than conflicts above.
	sources := helpGroup{
		name: "sources",
		entries: []string{
			helpEntry(m.keys.ToggleAllSources),
		},
	}

	fixed := []helpGroup{dashboard, installedMods, search, profiles, conflicts, sources}
	screenGroupName := map[Screen]string{
		ScreenDashboard:     dashboard.name,
		ScreenInstalledMods: installedMods.name,
		ScreenSearch:        search.name,
		ScreenProfiles:      profiles.name,
		ScreenConflicts:     conflicts.name,
		ScreenSources:       sources.name,
	}
	if name, ok := screenGroupName[m.screen]; ok {
		for i, g := range fixed {
			if g.name == name {
				fixed = append(append([]helpGroup{g}, fixed[:i]...), fixed[i+1:]...)
				break
			}
		}
	}

	return append([]helpGroup{global}, fixed...)
}

// helpBodyBudget bounds how many content rows the help panel's group list
// may use, so a long grouped list can't crowd screenView down past its own
// floor (matching availableContentHeight's own max(...,8)) - the same
// "+N more" cap style actionModalView uses (overlayView's former cap, until
// Task 7's fix wave made the overlay scroll instead), sized so the two
// floors agree exactly:
// when the list is capped, screenView gets precisely its floor of 8; when
// it isn't, screenView gets whatever room the (smaller) natural list left,
// same as before Task 9.
func (m Model) helpBodyBudget() int {
	if m.height == 0 {
		// Bumped 40->50 in Task 4: the full uncapped group list was already
		// at 41 lines after Task 3's conflicts group (silently one past the
		// old 40 default), and the installed-mods group's two new
		// MoveDown/MoveUp entries pushed it further past 40, to 43 -
		// TestHelpViewListsPerScreenGroups depends on this staying
		// "generous" enough to render every group's content, per this
		// method's own doc comment. 50 leaves headroom above today's 43 for
		// the next few tasks' bindings, rather than needing a re-bump for
		// every single addition.
		return 50
	}
	status := 0
	if m.hasVisibleStatus() {
		status = 1
	}
	const (
		titleNavSpacerHeight = 4 // matches contentChromeHeight's own constant
		screenViewFloor      = 8 // matches availableContentHeight's own floor
		fixedHelpLines       = 2 // "HELP" title + blank separator
	)
	total := m.height - m.theme.App.GetVerticalFrameSize() - titleNavSpacerHeight - status
	return max(total-screenViewFloor-m.theme.Panel.GetVerticalBorderSize()-fixedHelpLines, 1)
}

func (m Model) helpView() string {
	width := m.availableWidth()
	panelContentWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)

	var body []string
	for i, g := range m.helpGroups() {
		if i > 0 {
			body = append(body, "")
		}
		body = append(body, truncate(m.theme.PanelTitle.Render(g.name), panelContentWidth))
		for _, e := range g.entries {
			body = append(body, truncate(e, panelContentWidth))
		}
	}

	lines := []string{truncate(m.theme.PanelTitle.Render("HELP"), panelContentWidth), ""}
	budget := m.helpBodyBudget()
	if len(body) > budget {
		shown := max(budget-1, 0)
		more := len(body) - shown
		lines = append(lines, body[:shown]...)
		lines = append(lines, truncate(m.theme.MutedText.Render(fmt.Sprintf("+%d more", more)), panelContentWidth))
	} else {
		lines = append(lines, body...)
	}

	return m.panel(width).Render(strings.Join(lines, "\n"))
}

func (m Model) row(index int, label string) string {
	prefix := "  "
	if m.selected[m.screen] == index {
		prefix = "> "
		return m.theme.Selected.Render(prefix + label)
	}
	return prefix + label
}

// modRow renders one Installed Mods row: name / status / flags / author /
// version. The flags column is a fixed width so it is blank rather than absent
// for an unpinned mod - a variable-width flag would shift author and version
// between rows, the same alignment defect described below.
// Finding 2 (smoke test): the name column used to be a fixed 28 runes with
// no truncation, so a longer name overflowed it and shifted every
// subsequent column to the right, breaking row alignment. Status/author/
// version get proportional (clamped) shares of the panel's width - the same
// pattern searchResultsPane already uses - and the name column absorbs
// whatever's left, so it grows with the panel instead of staying a small
// fixed number. truncate() (ANSI-safe, hard cutoff with an ellipsis) is
// applied to every field so an overlong value can never push a later column
// out of place, matching searchResultsPane's reasoning for why overflow must
// never reach lipgloss's automatic line-wrap.
func (m Model) modRow(index, width int, mod ModItem) string {
	const prefixWidth = 2 // m.row()'s "> "/"  " selection marker
	const gaps = 4        // separating spaces between the 5 columns
	const minName = 8
	// Fixed, not proportional: the flags are the whole point of the column, so
	// they must not be the first thing a narrow terminal truncates. 5, not 3
	// ("pin"), so the pin flag and the "updated this session" marker (Task 2,
	// #77) can coexist without colliding: a pinned mod that was ALSO just
	// updated (a manual apply overrides the policy for one run) renders
	// "pin *" - see modFlags' doc comment for the exact layout.
	const flagsWidth = 5

	avail := max(width-m.theme.Panel.GetHorizontalFrameSize()-prefixWidth-gaps, minName)
	statusWidth := min(11, max(avail/6, 1)) // "disabled"/"deployed" are 8 runes
	authorWidth := min(16, max(avail/5, 1))
	versionWidth := min(7, max(avail/8, 1))
	nameWidth := max(avail-flagsWidth-statusWidth-authorWidth-versionWidth, minName)

	line := fmt.Sprintf("%-*s %-*s %-*s %-*s %*s",
		nameWidth, truncate(mod.Name, nameWidth),
		statusWidth, truncate(mod.Status, statusWidth),
		flagsWidth, m.modFlags(mod),
		authorWidth, truncate(mod.Author, authorWidth),
		versionWidth, truncate(mod.Version, versionWidth))
	return m.row(index, line)
}

// modFlags renders the per-mod flag column: "pin" (left-aligned in the
// first 3 columns) for a mod held back from update checks, and "*" (the
// last column) for a mod actually updated THIS session - not merely
// checked. The wire string "pin" (not the CLI's "pinned") matches
// ModItem.UpdatePolicy's documented values - see service_core.go's
// policyToString for why the two interfaces differ. The fixed
// "%-3s %s" shape always fills exactly flagsWidth (5) columns - "pin *",
// "pin  ", "    *", or "     " - so pin and the marker can appear
// independently, together, or not at all without ever shifting the
// author/version columns that follow (mirrors the pin-only column's own
// fixed-width reasoning above modRow).
func (m Model) modFlags(mod ModItem) string {
	pin := ""
	if mod.UpdatePolicy == "pin" {
		pin = "pin"
	}
	marker := " "
	if m.wasUpdatedThisSession(mod) {
		marker = "*"
	}
	return fmt.Sprintf("%-3s %s", pin, marker)
}

// wasUpdatedThisSession reports whether mod was actually brought current by
// an update applied this session, per task-2-brief.md's decided design: mod
// must match an entry in m.lastUpdates.Updates by Source+ID (the same key
// viewSelectedModChangelog uses, mutations.go) AND mod's CURRENT Version
// must already equal that entry's ToVersion. The version comparison is
// load-bearing, not redundant: m.lastUpdates is set on EVERY CheckUpdates
// call (resolveCheckUpdatesResult, mutations.go), including one whose
// confirm modal was cancelled or whose apply failed partway through - such
// a checked-but-unapplied entry still matches by Source+ID while the mod's
// Version remains the OLD one, so without this comparison the marker would
// falsely claim an update that never happened. Session lifetime rides
// m.lastUpdates' own lifetime (cleared only on game switch,
// resolveGameSwitch/mutations.go) - no new lifecycle code needed.
func (m Model) wasUpdatedThisSession(mod ModItem) bool {
	if m.lastUpdates == nil {
		return false
	}
	for _, u := range m.lastUpdates.Updates {
		if u.Source == mod.Source && u.ID == mod.ID {
			return mod.Version == u.ToVersion
		}
	}
	return false
}

func (m Model) panel(width int) lipgloss.Style {
	return m.theme.Panel.Width(max(width-m.theme.Panel.GetHorizontalBorderSize(), 1))
}

func (m Model) panelWithHeight(width, height int) lipgloss.Style {
	return m.panel(width).Height(max(height-m.theme.Panel.GetVerticalBorderSize(), 1))
}

func (m Model) availableWidth() int {
	if m.width == 0 {
		return defaultContentWidth
	}
	return max(m.width-m.theme.App.GetHorizontalFrameSize(), 40)
}

func (m Model) availableContentHeight() int {
	if m.height == 0 {
		return 12
	}

	return max(m.height-m.theme.App.GetVerticalFrameSize()-m.contentChromeHeight(), 8)
}

func (m Model) contentChromeHeight() int {
	footerHeight := 1
	if m.showHelp {
		footerHeight = lipgloss.Height(m.helpView())
	}

	// The action status line (rule 8) occupies exactly one row above the
	// footer, and only when hasVisibleStatus reports something to show —
	// see statusLine's matching "" ⇒ nothing rendered contract in View().
	statusHeight := 0
	if m.hasVisibleStatus() {
		statusHeight = 1
	}

	const titleNavAndSpacerHeight = 4 // title, nav, and the spacer lines around content.
	return titleNavAndSpacerHeight + footerHeight + statusHeight
}

// searchFetchSizeCap bounds Model.searchFetchSize's derived value from
// above (#111 Tier 1): a very tall terminal's visible budget is not, by
// itself, a reason to request 100+ results from a real source's API in one
// call - 50 is a deliberate ~5x headroom over SearchPageSize's historical
// fixed size, not "as many as fit on screen". Verified against the live
// NexusMods API (2026-07-27, final-review follow-up): a count-50 search
// request is honored verbatim - 50 rows returned, no server-side clamp, no
// error - so a full page at this cap keeps the short-page exhaustion
// heuristic truthful. A source that DID silently clamp below a requested
// page size would read as exhausted with results still remaining; if a
// future source behaves that way, cap per-source (a capability) rather
// than raising this constant - see #109's Tier-2 note.
// SearchPageSize (service.go)
// is the corresponding floor - see its own doc comment for why the same
// constant also doubles as every DataProvider.Search implementation's
// pageSize <= 0 fallback.
const searchFetchSizeCap = 50

// searchFetchSize derives how many results ONE query session should fetch
// (#111 Tier 1) from the terminal's actual visible budget, targeting the
// WORST-CASE render of the results pane rather than its best case. A user
// smoke finding on PR #114 (fetch 48, only 43 visible, "↓ 5 more") traced
// to the opposite: the original derivation assumed the smallest possible
// chrome (a bare 2-line header, no status line), then a warning line
// and/or the action status line materializing at RENDER time shrank the
// pane out from under an already-dispatched fetch - and the results pane's
// own "↓ N more" scroll indicator (windowedRows/pickerWindow, clamp.go)
// consumes a row of whatever budget IS available once that happens, so a
// too-generous fetch reads as an even bigger, more visible shortfall than
// the raw row deficit alone.
//
// Beyond the base header(2)+footer(1)+border(2) subtraction (unchanged -
// see below), two additional rows are reserved for the WORST case:
//   - statusReserve (1, unconditional): m.hasVisibleStatus() reflects only
//     what's true AT THE MOMENT this runs, but an action can start and post
//     a status line at any later point in the session, after this size was
//     already dispatched - there's no way to rule that out, so it's always
//     reserved regardless of the current value.
//   - warningReserve (1, only when m.search.source() == "" at submit - the
//     all-sources sentinel): searchView only ever appends a warning line to
//     an ALL-SOURCES page (SearchPage.Warnings is empty for single-source
//     searches - see its own doc comment), so a single-source session never
//     needs this; an all-sources session can't know in advance whether the
//     page it's about to fetch will warn, so it reserves defensively.
//
// This remains a LOWER bound in the other direction, not an exact fit: when
// neither reservation actually materializes (no status line ever appears,
// a single-source page, or an all-sources page that happens not to warn),
// the pane under-fills by up to 2 rows - accepted, since the alternative
// would require knowing the render-time-exact chrome before the very fetch
// that determines part of it. windowedRows (clamp.go) is the actual safety
// net that makes any shortfall harmless - it scroll-follows the selection
// through whatever the buffer holds regardless of exactly how many rows
// one round happened to return.
//
// Mirrors searchReadyView's own arithmetic; that function's
// paneContentHeight local computes:
//
//	paneContentHeight := max(paneHeight - Panel.GetVerticalBorderSize(), 1)
//	paneHeight         := max(availableContentHeight() - len(header) - 1, 1)
//
// i.e. availableContentHeight() minus len(header) minus the footer line
// (1) minus the panel's own top+bottom border, before the two worst-case
// reservations above. headerLineCount is hardcoded to 2 (searchHeaderLines'
// title + query-input lines, the guaranteed base) - the warning line is
// handled by warningReserve above, not by reading len(searchHeaderLines())
// live (which is unknowable before the fetch that would produce it).
//
// #111 Tier 3 (infinite scroll): this value now serves DOUBLE duty beyond
// sizing one provider round - it's also the distance
// maybeRefillSearch's low-water check and the n/p accelerators
// (afterSearchSelectionMove, search.go) jump the selection by. Called once
// per query session, exclusively from startSearch (the Enter/submit path):
// startSearch stores the result in m.search.fetchSize; every later round of
// that session - refillSearch's fetches, refreshSearchAfterInstall's
// rebuild - reuses it UNCHANGED, even across an intervening resize. A
// resize only changes what the NEXT startSearch call fetches.
func (m Model) searchFetchSize() int {
	const (
		headerLineCount = 2 // searchHeaderLines(): title + query input
		footerLineCount = 1 // searchFooterLine(): single status line
		statusReserve   = 1 // hasVisibleStatus can become true after submit
		warningReserve  = 1 // all-sources sessions only - see doc comment
	)
	budget := m.availableContentHeight() - headerLineCount - footerLineCount - m.theme.Panel.GetVerticalBorderSize() - statusReserve
	if m.search.source() == "" {
		budget -= warningReserve
	}
	return min(max(budget, SearchPageSize), searchFetchSizeCap)
}

// countLabel renders n, or "?" when n is negative (unknown, e.g. no update
// check has run yet).
func countLabel(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
}

// lastDeployLabel renders Summary.LastDeploy (#106a's dashboard "Last
// deploy" row) for display: nil (never deployed) is "never"; otherwise a
// coarse relative age - "just now" under a minute, then minutes, then
// hours, then days - up to 6 days, past which a plain date reads better
// than an ever-growing "N days ago". now is passed in explicitly (see
// Model.now's doc comment) rather than read via time.Now() here, keeping
// this a pure function that every case in TestLastDeployLabel can pin
// exactly without a fake clock or sleep.
func lastDeployLabel(now time.Time, t *time.Time) string {
	if t == nil {
		return "never"
	}
	age := now.Sub(*t)
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age/time.Hour))
	case age < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(age/(24*time.Hour)))
	default:
		// Local, not the stored zone: deployed_at is written by SQLite's
		// CURRENT_TIMESTAMP (UTC), so formatting in the stored zone can
		// show yesterday's/tomorrow's date for users away from UTC (a
		// late-night deploy). Matches the CLI's formatLastDeploy, which
		// also renders t.Local().
		return t.Local().Format("2006-01-02")
	}
}

// truncate returns s trimmed to at most width display columns, marking a cut
// with a trailing ellipsis. Used to keep fixed-width row/field values from
// overflowing a panel's content width, which would otherwise trigger
// lipgloss's automatic re-wrap and silently grow the rendered line count.
// ansi.Truncate is display-width aware (wide runes such as CJK count as two
// columns) and ANSI-escape safe, unlike a plain rune-count slice.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func layoutForTheme(name string) Layout {
	switch name {
	case "amber":
		return LayoutMonochromeTerminal
	case "dos":
		return LayoutCommander
	case "green":
		return LayoutCrtStack
	default:
		return LayoutPartySheet
	}
}
