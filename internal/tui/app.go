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
// entries that have no screen yet.
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
	// health backs the Health screen's home content (#224 Task 9) - the
	// LOCAL-tier verify result from DataProvider.Health, fetched alongside
	// mods/profiles/conflicts in loadData (Task 10's wiring; this task's
	// healthHomeView renders whatever a caller has set here directly - see
	// contextview.go's doc comments for the screen this backs).
	health HealthView
	// healthAt is when health was produced (m.now() at receipt) - nil means
	// no scan has run yet, rendered as "no scan yet" rather than falling
	// through lastDeployLabel's own "never" wording (see healthScanLabel).
	healthAt *time.Time
	// healthErr is the last scan failure's message, "" when the most recent
	// scan (or the initial local-tier load) succeeded. Set by Task 10's
	// loadData wiring and the 'c'/'F' handlers Tasks 11/12 add.
	healthErr string
	// contextContent is a pushed full-screen view any screen can host (see
	// contextview.go, generalized in #86): nil means the current screen
	// renders normally; non-nil means contextView() renders it instead, over
	// whatever screen pushed it, and esc pops back via Model.popContext.
	contextContent contextContent
	search         searchModel
	action         actionModel
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
	// health and healthErr carry loadData's DataProvider.Health result
	// (#224 Task 10): healthErr == "" means health is the fresh view to
	// store (Update stamps healthAt alongside it); a non-empty healthErr
	// means the scan failed and health is the zero value - Update leaves
	// the Model's existing m.health untouched in that case (a transient
	// scan hiccup doesn't erase the last known-good findings) and instead
	// records the message on healthErr and the status line.
	health    HealthView
	healthErr string
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
		summary: Summary{Updates: -1, Conflicts: -1, HealthIssues: -1, HealthWarnings: -1},
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
			ScreenHealth:        0,
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

// dashboardMenu's two variants used to carry SEPARATE "Verify Integrity" and
// "Consult Conflict Oracle"/"ASK CONFLICT ORACLE" entries, back when
// ScreenHealth and ScreenConflicts were two different screens. #224 Task 15
// folded conflict reporting into ScreenHealth's own table and retired
// ScreenConflicts outright, which would have left both entries pointing at
// the identical target - two menu rows for one destination reads as broken,
// not thorough, so the Oracle entry is dropped rather than kept as a
// redundant duplicate (the cleaner of the two options the fold allowed).
func (m Model) dashboardMenu() []menuItem {
	if m.layout == LayoutMonochromeTerminal {
		return []menuItem{
			{label: "RUN SPELLBOOK SCAN", target: ScreenInstalledMods, hasTarget: true},
			{label: "QUERY ARCHIVE INDEX", target: ScreenSearch, hasTarget: true},
			{label: "LOAD PROFILE ROSTER", target: ScreenProfiles, hasTarget: true},
			{label: "SCRY SOURCE REGISTRY", target: ScreenSources, hasTarget: true},
			{label: "VERIFY INTEGRITY", target: ScreenHealth, hasTarget: true},
		}
	}
	return []menuItem{
		{label: "Installed Mods", target: ScreenInstalledMods, hasTarget: true},
		{label: "Search Archives", target: ScreenSearch, hasTarget: true},
		{label: "Profiles", target: ScreenProfiles, hasTarget: true},
		{label: "Sources", target: ScreenSources, hasTarget: true},
		// #224 Task 10: jumps straight to the Health screen (ScreenHealth) -
		// a "go look at a standing signal" jump, not search-style explicit
		// intent, so this uses the same plain gotoScreen path
		// (openSelectedMenuEntry only special-cases ScreenSearch). Task 15's
		// conflicts fold means this single entry now covers both verify
		// findings and file conflicts.
		{label: "Verify Integrity", target: ScreenHealth, hasTarget: true},
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
	// Navigating anywhere pops pushed content (#86: any screen can host it,
	// so this is no longer Health-specific). gotoScreen is the single choke
	// point every nav route funnels through, so one clear here covers them
	// all - including pressing the digit for the screen you are already on.
	if m.contextContent != nil {
		// #86 Task 7 review finding: cancel any in-flight fetch the pushed
		// content owns BEFORE dropping it - see cancelPushedContentFetch's
		// own doc comment for why an abandoned fetch left running/enter dead
		// otherwise.
		m.cancelPushedContentFetch()
		m.contextContent = nil
	}
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

	// Health rides the same ordinary refresh (#224 Task 10), fetched AFTER
	// Conflicts above - but wrapped in its own error capture rather than
	// Overview/Profiles/Conflicts' early-return pattern: a failed verify
	// scan is not a reason to fail the WHOLE dashboard load. On success,
	// Summary.HealthIssues/HealthWarnings take the view's real counts;
	// on failure they stay at the "-1 unknown" sentinel and the message
	// rides dataLoadedMsg.healthErr instead - Update (below) is what turns
	// that into the status-line error and healthErr display.
	summary.HealthIssues, summary.HealthWarnings = -1, -1
	health, healthErr := m.provider.Health(m.ctx)
	var healthErrMsg string
	if healthErr != nil {
		healthErrMsg = singleLine(healthErr.Error())
	} else {
		summary.HealthIssues, summary.HealthWarnings = health.Issues, health.Warnings
	}

	return dataLoadedMsg{gen: m.loadGen, summary: summary, mods: mods, profiles: profiles, conflicts: conflicts, health: health, healthErr: healthErrMsg}
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
		// Health (#224 Task 10): a successful scan replaces m.health and
		// stamps healthAt at receipt time (m.now(), matching healthAt's own
		// doc comment); a failed one leaves the last known-good m.health/
		// healthAt exactly as they were - only healthErr and the status
		// line change - see dataLoadedMsg.health's doc comment for why.
		//
		// #224 Task 12 cross-task ruling: this refresh is exactly what
		// resolveFixHealthCheckResult's own m.loadData return triggers right
		// after a batch fix stores its Full-tier result into m.health/
		// healthAt - so the very next dataLoadedMsg to land REPLACES that
		// just-stored Full view with a fresh LOCAL scan (this DataProvider.
		// Health call is always Local-tier - Task 10's own dashboard-scan-is-
		// Local seam, unchanged by this task). This decay is BY DESIGN, not a
		// bug to fix later: "latest scan wins" is the one rule this screen
		// has held since Task 10, and a completed mutation - fix included -
		// invalidates whatever scan preceded it exactly like every other
		// action's post-mutation refresh invalidates stale list/summary data.
		// A caller that wanted the Full fix result to stick would need its
		// own carve-out here; none exists, deliberately.
		if msg.healthErr == "" {
			m.health = msg.health
			now := m.now()
			m.healthAt = &now
		}
		m.healthErr = msg.healthErr
		if msg.healthErr != "" {
			// Spec error posture: "health shows ?, error on the status
			// line" - setIdleStatus (not an unconditional overwrite, unlike
			// actionFailedMsg's own status write) so a status line already
			// genuinely owned by something else (a running action, a just-
			// settled outcome/error) is never stomped by a background scan
			// hiccup landing in the same refresh cycle.
			m.setIdleStatus(msg.healthErr, true)
		}
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
		openedResultsOverlay := false
		if len(msg.outcome.ResultLines) > 0 && m.overlay == nil {
			m.overlay = &infoOverlay{title: "update results", lines: msg.outcome.ResultLines}
			openedResultsOverlay = true
		}
		// #253: a multi-warning outcome auto-opens the same overlay listing
		// every warning in full. The threshold is deliberately the SAME "> 1"
		// formatOutcomeStatus collapses at, so the overlay opens exactly when
		// the status line has degraded to a bare "(N warnings)" count and the
		// text would otherwise be unrecoverable - on a merged-pak game those
		// warnings are the ONLY report of a cross-mod asset conflict anywhere
		// in the app. A single warning still renders inline after the em dash
		// and opens nothing: that is the guard against throwing a modal at a
		// lone benign note (mergeDiagnostics folds Notes in alongside real
		// warnings), so no benign/severe classification is needed. The
		// warnings defer ONLY to the ResultLines overlay this same handler
		// just opened (the update batch's per-item record keeps priority) -
		// NOT to any overlay that happens to be up: a read-only overlay CAN
		// be open when an action settles (promptOverlay deliberately doesn't
		// gate on m.action.running - the Files overlay is the reachable
		// case), and deferring to it would silently re-lose the warnings
		// (Copilot PR #258 finding), so a stale overlay is replaced instead.
		if len(msg.outcome.Warnings) > 1 && !openedResultsOverlay {
			m.overlay = &infoOverlay{title: "warnings", lines: msg.outcome.Warnings}
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
	case fullHealthCheckResultMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveFullHealthCheckResult(msg)
	case fullHealthCheckFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveFullHealthCheckFailure(msg)
	case fixHealthCheckResultMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveFixHealthCheckResult(msg)
	case fixHealthCheckFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveFixHealthCheckFailure(msg)
	case modDetailsFetchedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveModDetailsFetched(msg)
	case modDetailsFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveModDetailsFailed(msg)
	case policyChosenMsg:
		return m.resolvePolicyChoice(msg)
	case versionsFetchedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveVersionsFetched(msg)
	case versionsFetchFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		return m.resolveVersionsFetchFailed(msg)
	case lockChosenMsg:
		return m.resolveLockChosen(msg)
	case unlockChosenMsg:
		return m.resolveUnlockChosen(msg)
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

// isScreenDigit reports whether msg is one of the nav digits ("1".."6"), so
// the pushed-content swallow rule can let navigation through.
func isScreenDigit(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	return msg.Runes[0] >= '1' && msg.Runes[0] <= rune('0'+len(screens))
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

	// Pushed content gets first refusal on every key. Anything it declines is
	// SWALLOWED rather than falling through to the screen underneath (#86) -
	// otherwise arrows would move a selection the user can't see and e/x/u
	// would mutate the row behind the view. Same rule updateOverlayKey
	// already applies for the info overlay. The exits below are the keys that
	// must keep working over any full-screen content: leave, navigate, quit,
	// help.
	if m.contextContent != nil {
		if next, cmd, handled := m.contextContent.HandleKey(msg); handled {
			m.contextContent = next
			return m, cmd
		}
		switch {
		case key.Matches(msg, m.keys.Blur):
			// #86 Task 7 review finding: cancel any in-flight fetch the
			// pushed content owns BEFORE popping - see
			// cancelPushedContentFetch's own doc comment.
			m.cancelPushedContentFetch()
			m.popContext()
			return m, nil
		case m.isQuitKey(msg):
			// fall through to the outer switch's quit handling
		case key.Matches(msg, m.keys.Help),
			key.Matches(msg, m.keys.NextScreen), key.Matches(msg, m.keys.PrevScreen),
			isScreenDigit(msg):
			// fall through: navigation and help stay available
		default:
			// LOAD-BEARING: this default case is what swallows Search's own
			// focus key ("/") while content is pushed - it is not in any of
			// the exit cases above. That is what makes "a focused search
			// input and pushed content cannot co-occur" an actual invariant
			// rather than a coincidence: without this, "/" over pushed
			// content would fall through to the outer switch, focus the
			// search input underneath a view the user can still see full of
			// mod details, and updateKey's focused-input branch (below)
			// would then start eating every keystroke as search input
			// instead of this view's own HandleKey - reintroducing exactly
			// the "acting on the row/screen underneath a pushed view" bug
			// class this whole swallow rule exists to retire (#86 review -
			// recorded here so a later cleanup pass doesn't "simplify" this
			// default away).
			return m, nil // swallowed
		}
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
	case key.Matches(msg, m.keys.HealthScreen):
		return m.gotoScreen(ScreenHealth)
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
		return m.afterSearchSelectionMove()
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
		return m.afterSearchSelectionMove()
	case key.Matches(msg, m.keys.Select):
		// Select ("enter") is context-dependent: Profiles switches to the
		// selected profile, Installed Mods and Search open the selected mod's
		// details (#86), and everywhere else it opens a dashboard menu entry.
		switch m.screen {
		case ScreenProfiles:
			return m.switchSelectedProfile()
		case ScreenInstalledMods, ScreenSearch:
			return m.openSelectedModDetails()
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
	case key.Matches(msg, m.keys.Lock):
		return m.editSelectedModLock()
	case key.Matches(msg, m.keys.ConvertToggle):
		return m.toggleSelectedModConvert()
	// FullCheck and CreateProfile deliberately share the physical key "c"
	// (#224 Task 11 - see keys.go's FullCheck doc comment): FullCheck's own
	// case condition carries its screen/context guard INLINE, unlike every
	// other binding's bare key.Matches, specifically so it can sit ahead of
	// CreateProfile's unconditional one in this switch without stealing "c"
	// on ScreenProfiles - a Go "switch true" takes the FIRST case whose
	// condition holds, so on ScreenHealth with nothing pushed this case wins
	// outright, and everywhere else the condition is false and control falls
	// through to CreateProfile's case - CreateProfile's own internal screen
	// guard (createProfilePrompt, mutations.go) still no-ops it on any screen
	// but Profiles.
	//
	// The m.contextContent == nil half of this guard is now DEAD CODE, kept
	// as defense-in-depth: #86's pushed-content swallow rule (updateKey,
	// above - "Pushed content gets first refusal on every key") means "c"
	// never reaches this outer switch at all while content is pushed on ANY
	// screen, including Health - the swallow happens earlier, before
	// updateKey's per-screen switch is ever entered. This inline check used
	// to be load-bearing (pre-#86, pushed content on Health fell through to
	// this exact switch); it is retained rather than removed because it
	// costs nothing and would matter again if the swallow rule were ever
	// relaxed.
	case key.Matches(msg, m.keys.FullCheck) && m.screen == ScreenHealth && m.contextContent == nil:
		return m.runFullHealthCheck()
	// FixHealth ("F", Task 12) has no other screen claiming the same key
	// (unlike FullCheck's "c"/CreateProfile collision above), so it needs no
	// inline compound guard here - the screen and pushed-context checks live
	// inside fixHealthPrompt itself, matching every other non-colliding
	// binding's handler (e.g. Lock/ConvertToggle above).
	case key.Matches(msg, m.keys.FixHealth):
		return m.fixHealthPrompt()
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
//
// Smoke round 2, finding 1: the "enter: switch" clause used to be
// hardcoded for every screen, but Select ("enter") is context-dependent
// (updateKey's own doc comment on its Select case) - on Installed Mods and
// Search it now opens mod details (#86), not "switch" (that's Profiles'
// meaning only), so a screen-independent string was actively wrong there.
// footerSelectHint() isolates just that one clause per screen; every other
// hint in the line stays a single shared string, matching this function's
// "keep the shared hints shared" contract. The clause sits in the same
// position it always has (immediately before "q: quit"), so its length
// change doesn't reorder or newly threaten any OTHER hint's survival under
// truncation - only "q: quit" sits after it, and every variant below still
// fits well inside the 160-column design target (see footerSelectHint's
// doc comment for the longest case).
func (m Model) footerLine() string {
	hint := fmt.Sprintf(
		"?: help  tab/h/l: screens  ↑↓/j/k: move  /: search  i: install  e: enable/disable · x: uninstall · D: deploy · u: check updates  %s  q: quit",
		m.footerSelectHint(),
	)
	return truncate(m.theme.Help.Render(hint), m.availableWidth())
}

// footerSelectHint returns the footer's "enter: ..." clause for the
// CURRENT screen, mirroring updateKey's own Select dispatch (app.go) and
// helpGroups' hand-written per-screen rows for the same key: Profiles
// switches to the highlighted profile; Installed Mods and Search open the
// selected mod's details (#86); everywhere else (Dashboard, Sources,
// Health) falls through to openSelectedMenuEntry, which only does
// something on Dashboard - "open" is kept as the generic fallback there
// rather than a screen-specific phrase, since Health/Sources have no
// menu-entry meaning for it at all. Longest case is "enter: view details"
// (18 chars) vs the old always-"enter: switch" (13 chars); the full footer
// line with it is still well under the 160-column design target.
func (m Model) footerSelectHint() string {
	switch m.screen {
	case ScreenProfiles:
		return "enter: switch"
	case ScreenInstalledMods, ScreenSearch:
		return "enter: view details"
	default:
		return "enter: open"
	}
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
	case ScreenHealth:
		// #224 Task 15: findings then conflict rows, in that order - see
		// healthTableRows/healthDetailPane's own doc comments for why the
		// two are concatenated rather than kept as separate selectable
		// lists.
		return len(m.health.Findings) + len(m.conflicts)
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

	// Pushed content renders over whatever screen pushed it (#86). Placed
	// after the picker/inputModal returns above so modals still outrank it,
	// matching updateKey's own precedence.
	if m.contextContent != nil {
		return m.contextView()
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
	case ScreenHealth:
		return m.healthHomeView()
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
		m.healthDashboardLine(),
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
		fmt.Sprintf("> %s", m.healthDashboardLine()),
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
		m.healthDashboardLine(),
		fmt.Sprintf("Deploy   %s", lastDeployLabel(m.now(), m.summary.LastDeploy)),
	}, contentBudget)
	rightLines := m.clampLines(
		append([]string{m.theme.PanelTitle.Render("OPERATIONS")}, m.dashboardMenuRows()...),
		contentBudget)

	// These half-width panels are narrow enough at small terminal widths that
	// an overlong row ("> Verify Integrity" at width 40) would
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
		fmt.Sprintf("▓ %s", m.healthDashboardLine()),
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
	// Toggle hint in the search screen's "(s cycles)" style: muted, inline,
	// naming the key. Fixed-size and appended AFTER truncating the title to
	// the remaining width, so a long game name can't push the line past the
	// panel width and re-wrap into an extra row (height-budget lesson, #42).
	hint := "  (a shows all)"
	// "  " matches m.row()'s 2-column selection-marker prefix ("> "/"  ") so
	// the header lines up with the data columns below it instead of starting
	// two columns to their left. The IN USE column exists only in the
	// all-sources view — the scoped default's rows are all trivially in
	// use, so the column would be redundant there (mirrors cmd/lmm/
	// source.go's --all-only "IN USE" column).
	headerLine := "  " + fmt.Sprintf("%-20s %-12s %-6s %s", "ID", "TYPE", "AUTH", "CAPABILITIES")
	if m.sourcesShowAll {
		title = "SOURCE REGISTRY — ALL SOURCES"
		hint = "  (a shows game)"
		headerLine = "  " + fmt.Sprintf("%-20s %-12s %-6s %-7s %s", "ID", "TYPE", "AUTH", "IN USE", "CAPABILITIES")
	}
	headerLine = truncate(headerLine, panelContentWidth)
	titleLine := m.theme.PanelTitle.Render(truncate(title, max(panelContentWidth-len(hint), 1))) +
		m.theme.MutedText.Render(hint)
	rows := []string{
		titleLine,
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

// conflictStatusLabel is the Health table's STATUS column value for a
// conflict row (#224 Task 15's conflicts fold): "STALE CONFLICT" when the
// DB's recorded owner disagrees with the current load-order winner (a
// redeploy would change who wins - see ConflictItem's own doc comment),
// plain "CONFLICT" otherwise.
func conflictStatusLabel(c ConflictItem) string {
	if c.Stale {
		return "STALE CONFLICT"
	}
	return "CONFLICT"
}

// conflictNoteText is the Health table's NOTE column value for a conflict
// row: names the current owner and how to change it (reorder) for an
// in-sync conflict, or the stale-specific "redeploy to apply" remedy for a
// stale one - the stale wording is byte-identical to the pre-fold Conflicts
// screen's own detail-pane hint (conflictDetailHint's stale branch) since
// it's already exactly what this column needs; the in-sync wording differs
// (this column has no separate OWNER field the way the old two-pane list
// did, so it names the owner inline).
func conflictNoteText(c ConflictItem) string {
	if c.Stale {
		return fmt.Sprintf("load order says %s should win — deploy (D) to apply", c.Winner)
	}
	return fmt.Sprintf("owned by %s — reorder (J/K on Installed) to change the winner", c.Owner)
}

// conflictDetailHint is the Health detail strip's hint line for a selected
// conflict row - byte-identical to the pre-fold Conflicts screen's own
// conflictsDetailPane hint copy (task-3-brief.md's wording), preserved
// verbatim by #224 Task 15's fold rather than replaced by conflictNoteText's
// differently-worded in-sync branch (the two serve different UI: this is the
// detail strip's own hint line, conflictNoteText is the table's compact NOTE
// column).
func conflictDetailHint(c ConflictItem) string {
	if c.Stale {
		return fmt.Sprintf("load order says %s should win — deploy (D) to apply", c.Winner)
	}
	return "reorder mods (J/K on installed) to change the winner"
}

// conflictRowStyle returns the Health table's STATUS-column tint for a
// conflict row: DangerText when stale (the deployed file no longer matches
// the load-order winner - a real, active problem), WarningText otherwise
// (an ordinary conflict is expected, lower-severity information, not
// necessarily wrong) - mirroring healthStatusStyle's own "text-color only,
// pad-then-style" convention for the finding rows above it in the same
// table.
func (m Model) conflictRowStyle(c ConflictItem) lipgloss.Style {
	if c.Stale {
		return m.theme.DangerText
	}
	return m.theme.WarningText
}

// healthHomeView renders the Health screen's home content: a full-width
// columnar table, one row per finding (STATUS | MOD | FILE | VERSION |
// NOTE), followed by a compact detail strip for the selected row -
// mirroring modsView/modRow's full-width table conventions (proportional
// clamped column widths, truncate() on every field, m.row()'s selection
// highlight, m.windowedRows' scroll-follow-selection) rather than the
// two-pane list/detail shape this screen used before (#224 layout rework,
// user request): verify findings are tabular data, and a side detail box
// mostly empty except for 2-4 short lines wasted half the screen's width
// that the Installed Mods screen puts to use as real columns.
//
// 2026-08-07 smoke feedback (#224): Findings now carries EVERY row the
// engine reported, quiet-ok included (HealthView's own doc comment), so the
// empty-state branch below only fires for a genuinely empty profile (0
// mods checked) rather than "everything happened to be quiet-ok" - the
// pre-fix behavior the user objected to.
//
// #224 Task 15: the empty-state branch now additionally requires m.conflicts
// to be empty too - a profile with zero verify findings but an outstanding
// file conflict still has real content to show, so the table renders in
// that case even though Findings alone is empty. See healthTableRows/
// healthDetailPane for how conflict rows are folded into the same table/
// selection index space as findings.
func (m Model) healthHomeView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()

	if len(m.health.Findings) == 0 && len(m.conflicts) == 0 {
		// Tier-aware (#224 Copilot round 2): after a successful FULL check
		// the "run a full check (c)" hint is misleading - the user just ran
		// one - so only the local-tier empty state offers it.
		empty := "no findings (local) — run a full check (c)"
		if m.health.Full {
			empty = "no findings (full)"
		}
		return m.panelWithHeight(width, height).Render(strings.Join([]string{
			m.theme.PanelTitle.Render("HEALTH"),
			m.theme.MutedText.Render(m.healthScanLine()),
			m.theme.MutedText.Render(empty),
		}, "\n"))
	}

	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)
	innerWidth := max(width-m.theme.Panel.GetHorizontalFrameSize(), 1)

	header := []string{
		m.theme.PanelTitle.Render("HEALTH"),
		m.theme.MutedText.Render(truncate(m.healthScanLine(), innerWidth)),
	}

	// The detail strip is a fixed-cap 4 lines (healthDetailPane's own doc
	// comment) - computed first so the table's budget below is whatever's
	// left after header + one blank separator + the strip, matching
	// modsView's own budget-then-windowedRows shape (app.go).
	const detailCap = 4
	detail := m.healthDetailPane(innerWidth, detailCap)
	detailLines := strings.Split(detail, "\n")

	tableBudget := max(contentBudget-len(header)-1-len(detailLines), 0)
	table := m.healthTableRows(width, tableBudget)

	rows := make([]string, 0, len(header)+len(table)+1+len(detailLines))
	rows = append(rows, header...)
	rows = append(rows, table...)
	rows = append(rows, "")
	rows = append(rows, detailLines...)
	rows = m.truncateLines(rows, innerWidth)
	rows = m.clampLines(rows, contentBudget)

	return m.panelWithHeight(width, height).Render(strings.Join(rows, "\n"))
}

// healthScanLine renders the Health header's full "last scan: ..." body:
// healthScanLabel's tier/age/checked-count text, plus (#224 Task 15) a
// trailing ", M conflict(s)" whenever m.conflicts is non-empty - keeping the
// header honest now that the table below includes conflict rows alongside
// verify findings. Shared by healthHomeView's empty-state and populated
// branches so the two can never describe the scan differently.
func (m Model) healthScanLine() string {
	line := fmt.Sprintf("last scan: %s", healthScanLabel(m.now(), m.healthAt, m.health.Full, m.health.Checked, len(m.health.Findings) > 0))
	if len(m.conflicts) > 0 {
		line += fmt.Sprintf(", %d conflict(s)", len(m.conflicts))
	}
	return line
}

// healthFindingSubject returns the best available human-readable identifier
// for a HealthFinding, in order: ModName, then ModID, then FileID. Some
// findings genuinely carry no mod at all - e.g. a stale_deployment row for a
// dangling cache link (#224 Copilot round 1) - so callers that assumed
// ModName was always populated rendered a blank/stray label. The two health
// surfaces that show a finding's SUBJECT (as opposed to a table's own
// separate MOD column, which never falls back to FileID - see
// healthFindingModLabel) - healthDetailPane's "Mod:" line and
// healthFixResultLine's overlay row in mutations.go - share this one
// fallback so their behavior can't drift apart.
func healthFindingSubject(f HealthFinding) string {
	if f.ModName != "" {
		return f.ModName
	}
	if f.ModID != "" {
		return f.ModID
	}
	return f.FileID
}

// healthFindingModLabel is the Health table's MOD column value: ModName,
// then ModID, blank otherwise - deliberately NOT falling all the way back to
// FileID the way healthFindingSubject does, since FileID has its own
// dedicated FILE column here (#224 layout rework) and repeating it in MOD
// too would be redundant now that the two are separate fields instead of
// one combined "subject (file)" list-row label.
func healthFindingModLabel(f HealthFinding) string {
	if f.ModName != "" {
		return f.ModName
	}
	return f.ModID
}

// healthFindingVersionText is the Health table's VERSION column value: for
// version_mismatch, "recorded→effective" (VerifyFinding.Recorded/Effective,
// #224 follow-up plumbing - see core.VerifyFinding's own doc comment); for
// missing, the recorded version (VerifyFinding.Version) when the engine
// supplied one, else blank; every other status has no version-pass data at
// all, so it's always blank.
func healthFindingVersionText(f HealthFinding) string {
	switch f.Status {
	case "version_mismatch":
		return fmt.Sprintf("%s→%s", f.Recorded, f.Effective)
	case "missing":
		return f.Version
	default:
		return ""
	}
}

// healthVersionCell is the Health table's VERSION column value: version, or
// an em dash placeholder when a row carries no version-pass data at all
// (healthFindingVersionText's default branch, or a conflict row, which never
// has one). 2026-08-07 smoke feedback round 3: a blank VERSION cell was
// invisible whitespace even after healthColumnWidths started padding every
// column to the full content width (3ee970c) - the row's VISIBLE text still
// stopped at FILE, so the table still read as narrow. The placeholder gives
// the column a visible anchor the way modRow's version column always has
// one, mirroring the common table convention for "no value here".
func healthVersionCell(version string) string {
	if version == "" {
		return "—"
	}
	return version
}

// healthNoteCell is the Health table's NOTE column value for a finding row:
// the finding's own Note when set - this covers e.g. an "ok" row carrying a
// lock-pending advisory (TestHealthHomeViewRendersFindingsAndHeaderAge's
// Bear Mount fixture), which must keep its real note rather than the
// fallback below - otherwise a short per-status default derived from
// healthRemedy, so the cell is never blank. "ok" gets its own short "no
// action needed" rather than healthRemedy's fuller "OK — no action needed":
// the STATUS column already reads OK, so repeating it here would be
// redundant (unlike the detail pane's remedy line, which has no STATUS
// column next to it). Every other status reuses healthRemedy's existing
// sentence verbatim - it's still subject to the same truncate() cutoff as
// every other NOTE value once healthTableRows renders it, so a long remedy
// clips exactly as any other overlong NOTE would.
//
// 2026-08-07 smoke feedback round 3: a Note-less row's NOTE cell used to
// render as blank padding even after healthColumnWidths started consuming
// the full content width (3ee970c) - the row's VISIBLE text stopped well
// short of the panel's right edge, so a healthy profile's table still read
// as narrow. This closes that gap the same way healthVersionCell does for
// VERSION.
func healthNoteCell(f HealthFinding) string {
	if f.Note != "" {
		return f.Note
	}
	if f.Status == "ok" {
		return "no action needed"
	}
	return healthRemedy(f)
}

// healthColumnWidths computes the Health table's STATUS|MOD|FILE|VERSION|
// NOTE column widths from the panel's available content width, mirroring
// modRow/modsView's full-width convention: the whole panel's content width
// is always consumed, never just a capped sum (#224 smoke feedback fix #3 -
// the earlier version capped modW/fileW at small literal maximums, e.g. an
// 18-rune fileW cap, so a wide terminal's extra width could only ever grow
// NOTE while MOD/FILE stayed pinned narrow; the row never "stretched to
// fill" the way Installed Mods' rows do).
//
// MOD is the SOLE surplus absorber, exactly like modRow's own uncapped
// NAME column (nameWidth = avail minus every other column) - not a
// proportional split. #224 smoke feedback fix #5 (fourth width iteration,
// diagnosed from a real 210-col screenshot): the previous version gave
// NOTE 3 of 5 parts of the surplus and capped MOD's share along with it -
// the inverse of modRow's model. Because NOTE is left-aligned prose that is
// usually much shorter than its generous share, that surplus rendered as
// ~100 columns of invisible trailing padding mid-row, while real mod names
// (e.g. "Donovan's Larger Resource Stacks") truncated for want of the width
// NOTE was silently holding. STATUS and VERSION stay intrinsic, like
// modRow's own status/author/version columns: their values come from
// short, bounded vocabularies ("STALE CONFLICT", "12.3.4→12.4.0"), so a
// proportional-but-capped share is already generous. FILE and NOTE are
// each bounded near their own typical content (min(literalCap, avail/N))
// rather than left to grow - a file path or diagnostic note that overruns
// its bound truncates in the table, but the detail strip below
// (healthDetailPane) always renders the FULL text, so nothing is lost, only
// deferred to where a reader who wants it already looks.
//
// Unlike modRow - which always renders all 5 of its columns and lets NAME
// shrink toward its own floor - a narrow terminal here instead drops whole
// columns, NOTE first then VERSION: a Health row's primary value is
// STATUS/MOD/FILE, and a column squeezed down to 1-2 legible runes is worse
// than not showing it at all (#224 layout rework).
func (m Model) healthColumnWidths(width int) (statusW, modW, fileW, versionW, noteW int, showVersion, showNote bool) {
	const prefixWidth = 2 // m.row()'s "> "/"  " selection marker
	const minStatus = 8
	const minMod = 10
	const minFile = 8
	const minVersion = 7
	const minNote = 10

	avail := max(width-m.theme.Panel.GetHorizontalFrameSize()-prefixWidth, 1)

	statusW = min(22, max(avail/6, minStatus))
	versionW = min(11, max(avail/10, minVersion))
	fileW = min(24, max(avail/8, minFile))
	noteW = min(32, max(avail/6, minNote))

	showVersion, showNote = true, true

	// 5 columns -> 4 separating gaps.
	remaining := avail - statusW - fileW - versionW - noteW - 4
	if remaining < minMod {
		showNote, noteW = false, 0
		// 4 columns -> 3 separating gaps.
		remaining = avail - statusW - fileW - versionW - 3
		if remaining < minMod {
			showVersion, versionW = false, 0
			// 3 columns -> 2 separating gaps.
			remaining = avail - statusW - fileW - 2
		}
	}
	modW = max(remaining, minMod)
	return
}

// healthStatusStyle returns the STATUS column's tint (healthStatusClass's
// three buckets): text-color only, since a columnar STATUS field has no
// room for the old two-pane list's separate leading "[glyph]" marker
// (healthGlyph, removed by #224's layout rework) - the class distinction now
// lives entirely in color, matching modRow/searchResultsPane's own
// "pad-then-style" convention for a themed column value. "fine" renders
// untinted - theme.Theme has no dedicated success style, only Warning/
// DangerText (see Theme's own field list).
func (m Model) healthStatusStyle(status string) lipgloss.Style {
	switch healthStatusClass(status) {
	case "danger":
		return m.theme.DangerText
	case "warning":
		return m.theme.WarningText
	default:
		return lipgloss.NewStyle()
	}
}

// healthTotalRows is the Health table's/selection's full row count: verify
// findings, then (#224 Task 15) file conflicts appended after them - the
// same order healthTableRows renders rows in and healthDetailPane indexes
// into, so m.selected[ScreenHealth] walks findings first, conflicts second,
// with no gap or reordering between the two.
func (m Model) healthTotalRows() int {
	return len(m.health.Findings) + len(m.conflicts)
}

// healthTableRows renders the Health table's selectable STATUS|MOD|FILE|
// VERSION|NOTE rows: a finding row's STATUS is uppercase and tinted
// (healthStatusStyle), MOD via healthFindingModLabel, FILE the raw FileID
// (may be blank), VERSION via healthFindingVersionText (column dropped
// entirely on a narrow terminal - see healthColumnWidths), NOTE truncated to
// whatever width remains (dropped before VERSION on an even narrower
// terminal). Windowed/scroll-follow-selection like modsView's own list
// (m.windowedRows) - the full-width columnar replacement for the old
// two-pane healthListPane.
//
// #224 Task 15: after every finding row, one row per m.conflicts follows -
// STATUS "CONFLICT" (warning tint) or "STALE CONFLICT" (danger tint,
// conflictStatusLabel/conflictNoteText's own doc comments), MOD the
// load-order winner (not the owner - the winner is what a reader most wants
// to know at a glance, mirroring the pre-fold Conflicts screen's own WINNER
// column), FILE the contested path, VERSION the em dash placeholder
// (healthVersionCell - conflicts carry no version data, same as any other
// versionless row), NOTE conflictNoteText's compact remedy. The combined index
// space (healthTotalRows) means m.windowedRows scrolls/follows selection
// across BOTH kinds of row exactly as if they were one list, because they
// are one list from this function's perspective.
func (m Model) healthTableRows(width, budget int) []string {
	statusW, modW, fileW, versionW, noteW, showVersion, showNote := m.healthColumnWidths(width)
	numFindings := len(m.health.Findings)
	rowFor := func(i int) string {
		var status, mod, file, version, note string
		var style lipgloss.Style
		if i < numFindings {
			f := m.health.Findings[i]
			style = m.healthStatusStyle(f.Status)
			status = healthStatusLabel(f.Status)
			mod = healthFindingModLabel(f)
			file = f.FileID
			version = healthFindingVersionText(f)
			note = healthNoteCell(f)
		} else {
			c := m.conflicts[i-numFindings]
			style = m.conflictRowStyle(c)
			status = conflictStatusLabel(c)
			mod = c.Winner
			file = c.Path
			note = conflictNoteText(c)
		}
		parts := []string{
			style.Render(fmt.Sprintf("%-*s", statusW, truncate(status, statusW))),
			fmt.Sprintf("%-*s", modW, truncate(mod, modW)),
			fmt.Sprintf("%-*s", fileW, truncate(file, fileW)),
		}
		if showVersion {
			parts = append(parts, fmt.Sprintf("%-*s", versionW, truncate(healthVersionCell(version), versionW)))
		}
		if showNote {
			// Padded like every other column (#224 smoke feedback fix #3),
			// not left ragged - a short note used to leave the row short of
			// the panel's full width, so the selection highlight (m.row's
			// background) fell short of the right edge instead of spanning
			// full width the way an Installed Mods row always does.
			//
			// Left-aligned ("%-*s"), NOT right-aligned like modRow's own
			// last column (version, "%*s"): modRow's version is a short,
			// bounded value where right-anchoring gives a clean numeric
			// edge, but NOTE is prose (healthNoteCell's remedy sentences) -
			// right-aligning would ragged its LEFT edge instead and fight
			// readability, so left alignment is the correct anchor here.
			parts = append(parts, fmt.Sprintf("%-*s", noteW, truncate(note, noteW)))
		}
		return m.row(i, strings.Join(parts, " "))
	}
	return m.windowedRows(m.healthTotalRows(), m.selected[ScreenHealth], budget, rowFor)
}

// healthDetailPane renders a compact 3-4 line detail strip for the selected
// row: a finding's "Mod:    <subject> — <STATUS>" line (healthFindingSubject's
// full ModName/ModID/FileID fallback, unlike the table's own MOD column -
// so a mod-less finding's subject still reads as its FileID here, exactly
// as it did in the pre-rework two-pane detail pane), an optional
// "File:   <FileID>" line, an optional "Note:   <Note>" line shown in
// FULL (not clipped to the table's narrower NOTE column - only to width,
// same Width()-constrained-panel safety truncation every line here gets,
// #42), and the per-status remedy line (healthRemedy) - replacing the old
// two-pane's side DETAIL pane (#224 layout rework). width is the caller's
// already-computed CONTENT width (healthHomeView's innerWidth) - unlike the
// pre-rework version, this is no longer rendered inside its own bordered
// panel, so it does not subtract the panel's frame size itself.
//
// #224 Task 15: an index landing past the findings, in the conflicts range
// (healthTotalRows), delegates to healthConflictDetailPane instead - the
// pre-fold Conflicts screen's own detail pane logic (File/Owner/Winner/Also
// in/hint), preserved so a selected conflict's full data (including the
// alternates list no other surface shows) is still reachable now that it's
// folded into this same table.
func (m Model) healthDetailPane(width, maxLines int) string {
	idx := m.selected[ScreenHealth]
	numFindings := len(m.health.Findings)
	if idx < 0 || idx >= m.healthTotalRows() {
		return m.theme.MutedText.Render("No selection.")
	}
	if idx >= numFindings {
		return m.healthConflictDetailPane(m.conflicts[idx-numFindings], width, maxLines)
	}
	f := m.health.Findings[idx]

	innerWidth := max(width, 1)
	lines := []string{
		truncate(fmt.Sprintf("Mod:    %s — %s", healthFindingSubject(f), healthStatusLabel(f.Status)), innerWidth),
	}
	if f.FileID != "" {
		lines = append(lines, truncate(fmt.Sprintf("File:   %s", f.FileID), innerWidth))
	}
	if f.Note != "" {
		lines = append(lines, truncate(fmt.Sprintf("Note:   %s", f.Note), innerWidth))
	}
	if remedy := healthRemedy(f); remedy != "" {
		lines = append(lines, truncate(m.theme.MutedText.Render(remedy), innerWidth))
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

// healthConflictDetailPane renders the detail strip for a selected conflict
// row (#224 Task 15): the load-order winner and status (matching the
// finding branch's own "Mod: <subject> — <STATUS>" shape above, for visual
// consistency between the two kinds of row this one table now shows), the
// contested file, the current deployed owner (with any other providing mods
// - AlsoIn - appended to the same line rather than a fifth line of its own),
// and the stale-aware hint (conflictDetailHint) - the pre-fold Conflicts
// screen's own conflictsDetailPane content (task-3-brief.md's wording for
// the hint, preserved verbatim), condensed from its original 4-6 lines down
// to fit the detail strip's shared 4-line cap (healthHomeView's detailCap) -
// a dedicated "Winner:" line is dropped since the Mod line above already
// names the winner.
func (m Model) healthConflictDetailPane(c ConflictItem, width, maxLines int) string {
	innerWidth := max(width, 1)
	ownerLine := fmt.Sprintf("Owner:  %s", c.Owner)
	if len(c.AlsoIn) > 0 {
		ownerLine += "  Also in: " + strings.Join(c.AlsoIn, ", ")
	}
	lines := []string{
		truncate(fmt.Sprintf("Mod:    %s — %s", c.Winner, conflictStatusLabel(c)), innerWidth),
		truncate(fmt.Sprintf("File:   %s", c.Path), innerWidth),
		truncate(ownerLine, innerWidth),
		truncate(m.theme.MutedText.Render(conflictDetailHint(c)), innerWidth),
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

// healthStatusClass buckets a HealthFinding.Status into the three tint
// classes the table/detail strip use (healthStatusStyle): "danger" for the
// two statuses that mean a file/version is actually wrong, "fine" for a
// healthy (lock-pending "ok") row OR a resolved fixed_* row (a successful
// --fix repair - "fixed_" prefix covers both fixed_stale_deployment and
// fixed_needs_reingest, #224 Copilot round 2: these used to fall into
// "warning" and so tinted/glyphed like an outstanding problem in the
// post-fix list, even though they mean the opposite), and "warning" for
// everything else - the broad "needs attention but isn't broken" middle
// ground (needs_reingest, no_checksum, version_unverifiable, stale_compile,
// conversion_failed, stale_deployment, file_count_mismatch, skipped, ...).
func healthStatusClass(status string) string {
	switch {
	case status == "missing" || status == "version_mismatch":
		return "danger"
	case status == "ok" || strings.HasPrefix(status, "fixed_"):
		return "fine"
	default:
		return "warning"
	}
}

// healthStatusLabel renders a Status value ("version_mismatch") as display
// text ("VERSION MISMATCH"), matching the CLI's own uppercase wording for
// the same statuses (cmd/lmm/verify.go).
func healthStatusLabel(status string) string {
	return strings.ToUpper(strings.ReplaceAll(status, "_", " "))
}

// healthRemedy returns the detail strip's per-status remedy line, reusing
// the CLI's own phrasings (cmd/lmm/verify.go's renderVerifyFinding/
// renderVerifySkipped) wherever HealthFinding carries enough to reconstruct
// them - ExpectedCount (a CLI-only VerifyEvent extra with no HealthFinding
// counterpart) is dropped rather than guessed at; Recorded/Effective/
// Version (#224 follow-up plumbing) ARE on HealthFinding now but the table's
// own VERSION column (healthFindingVersionText) already shows them, so this
// remedy copy doesn't repeat them. "(F)" names Task 12's fix binding.
//
// 2026-08-07 smoke feedback (#224): a quiet-ok row (Status "ok", no Note)
// used to return "" here - it never reached the detail pane before, since
// healthView dropped it outright. Now that it's a selectable row, "" would
// render as a blank line where every other status has SOME remedy copy, so
// it gets its own plain "nothing to do" line in the same voice.
func healthRemedy(f HealthFinding) string {
	switch f.Status {
	case "missing":
		return "file missing from cache — run a fix (F) to redownload"
	case "no_checksum":
		return "no checksum recorded — run a fix (F) to backfill"
	case "needs_reingest":
		return "run a fix (F) to re-ingest"
	case "version_unverifiable":
		return "recorded file ID(s) no longer found upstream; reinstall the mod or run 'lmm update' to adopt the current version"
	case "version_mismatch":
		return "source reports a different version than recorded; run 'lmm update' or a fix (F)"
	case "stale_compile":
		return "run 'lmm update --all' to fix"
	case "conversion_failed":
		return fmt.Sprintf("deploying raw; fix the mod or run 'lmm mod convert %s off' to silence", f.ModID)
	case "stale_deployment":
		return "run a fix (F) to remove"
	case "fixed_stale_deployment", "fixed_needs_reingest":
		return "resolved"
	case "file_count_mismatch":
		return "expected content from a download but the cache has 0 files"
	case "ok":
		if f.Note != "" {
			return fmt.Sprintf("%s — run 'lmm profile apply'", f.Note)
		}
		return "OK — no action needed"
	case "skipped":
		return "could not check — see note above"
	default:
		return ""
	}
}

// healthScanLabel renders the Health screen's header line body: "no scan
// yet" when at is nil (no scan has run this session), otherwise "<tier>,
// <age> — N checked" reusing lastDeployLabel's own relative-age computation
// (its nil branch is unreachable here since at != nil is already checked) -
// "local"/"full" names the verify tier the reported findings came from
// (HealthView.Full).
//
// 2026-08-07 smoke feedback (#224): the "N checked" suffix is new - it
// names how many rows the verify engine considered (HealthView.Checked),
// giving the header some content even when every row is quiet-ok. The
// suffix is omitted only when checked is 0 AND hasFindings is false - a
// genuinely empty profile - so a real scan's header always says what it
// looked at.
func healthScanLabel(now time.Time, at *time.Time, full bool, checked int, hasFindings bool) string {
	if at == nil {
		return "no scan yet"
	}
	tier := "local"
	if full {
		tier = "full"
	}
	label := fmt.Sprintf("%s, %s", tier, lastDeployLabel(now, at))
	if checked == 0 && !hasFindings {
		return label
	}
	return fmt.Sprintf("%s — %d checked", label, checked)
}

// helpGroup is one labeled section of the help panel: a screen name (or
// "global") plus the key entries that apply to it. Group labels are
// lowercase per Task 9's copy convention (see helpGroups).
type helpGroup struct {
	name    string
	entries []helpItem
}

// helpItem is one row of a helpGroup plus its collapse priority: keep marks
// a screen's headline action so helpView's "+N more" cap drops unmarked
// rows first (#234) instead of tail-dropping purely by position - which
// used to swallow the search group's LAST entry (enter: open mod details)
// at a normal 100x30 size. The marker only takes effect while the row's
// group is promoted (see helpView); an unpromoted group's headline can't
// force itself into another screen's help window.
type helpItem struct {
	text string
	keep bool
}

// kept returns a copy of the item marked as its group's headline action.
func (i helpItem) kept() helpItem {
	i.keep = true
	return i
}

// helpRow formats a single help-panel row: left-aligned key (16 chars),
// space, description.
func helpRow(key, desc string) helpItem {
	return helpItem{text: fmt.Sprintf("%-16s %s", key, desc)}
}

// helpEntry formats one keybinding as a help-panel row, reusing the
// binding's own key.WithHelp key/description from keys.go rather than
// restating it - the single source of truth for "what does this key do"
// stays in DefaultKeyMap.
func helpEntry(kb key.Binding) helpItem {
	h := kb.Help()
	return helpRow(h.Key, h.Desc)
}

// helpGroups builds the full, ordered set of help groups: "global" always
// first, then every screen group that has entries, in a fixed order
// (dashboard, installed mods, search, profiles, sources, health), with
// the CURRENT screen's group promoted to immediately follow global. Each
// screen's list
// mirrors updateKey's dispatch guards in mutations.go (e.g. Files/Policy/
// Purge all gate on ScreenInstalledMods too, alongside Deploy/CheckUpdates).
func (m Model) helpGroups() []helpGroup {
	global := helpGroup{
		name: "global",
		entries: []helpItem{
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
		entries: []helpItem{
			// Select ("enter") is context-dependent (see updateKey): on
			// the Dashboard it opens the selected menu entry
			// (openSelectedMenuEntry), so the description is written out
			// here rather than reusing keys.go's generic "open" - the same
			// ad-hoc shape as the profiles group's "switch profile" below.
			helpRow(m.keys.Select.Help().Key, "open menu entry").kept(),
			helpEntry(m.keys.Deploy),
			helpEntry(m.keys.CheckUpdates),
			helpEntry(m.keys.Purge),
		},
	}

	installedMods := helpGroup{
		name: "installed mods",
		entries: []helpItem{
			// Select is #86's enter-opens-details binding. Hand-written
			// (not helpEntry) for the same reason the dashboard's and
			// profiles' own rows above are: keys.go's Select.Help() is the
			// generic "enter"/"open" shared across every screen it applies
			// to, and never says WHAT it opens - smoke round 2's finding 2.
			helpRow(m.keys.Select.Help().Key, "open mod details").kept(),
			helpEntry(m.keys.ToggleEnable),
			helpEntry(m.keys.Uninstall),
			helpEntry(m.keys.Deploy),
			helpEntry(m.keys.CheckUpdates),
			helpEntry(m.keys.Files),
			helpEntry(m.keys.Policy),
			// Lock is Task 7's lock/unlock version-picker key (#97, see
			// mutations.go's editSelectedModLock) - listed beside Policy since
			// both open an item-scoped picker with no separate confirm modal.
			helpEntry(m.keys.Lock),
			// ConvertToggle is #221's pak-to-exmod conversion toggle (see
			// mutations.go's toggleSelectedModConvert) - listed beside Policy/
			// Lock since it's a third item-scoped, no-confirm-modal mutation.
			helpEntry(m.keys.ConvertToggle),
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
		entries: []helpItem{
			helpEntry(m.keys.Search),
			// Submit and Select share the same physical key ("enter") but
			// fire in mutually exclusive states (updateKey's focused-input
			// branch handles Submit before the outer switch that handles
			// Select is ever reached - see that branch's own key.Matches
			// case): Submit only while the query input is focused, Select
			// only once it's blurred with a result row selected. Smoke
			// round 2's finding 2: listing both via helpEntry rendered
			// "enter / search" next to "enter / open" with nothing saying
			// which state either applies to, so both are hand-written here
			// instead, each naming its own state explicitly. Both are the
			// screen's headline actions - the two mutually exclusive things
			// enter does here - so both are kept() ahead of the collapse
			// (#234): Submit's row happens to sit early enough to survive
			// positionally at 100x30 today, but that's an accident of entry
			// count, not a guarantee.
			helpRow(m.keys.Submit.Help().Key, "search (query input focused)").kept(),
			helpEntry(m.keys.Blur),
			helpEntry(m.keys.NextPage),
			helpEntry(m.keys.PrevPage),
			helpEntry(m.keys.CycleSource),
			helpEntry(m.keys.Install),
			// Select is #86's enter-opens-details binding - fires with the
			// input blurred and a result selected, mirroring Install's own
			// scoping (see openSelectedModDetails). kept() is #234's actual
			// fix: this row is the group's LAST entry, and the positional
			// tail-collapse used to swallow it at a normal 100x30 size.
			helpRow(m.keys.Select.Help().Key, "open mod details (input blurred, result selected)").kept(),
		},
	}

	profiles := helpGroup{
		name: "profiles",
		entries: []helpItem{
			// Select ("enter") is context-dependent (see updateKey): on
			// Profiles it switches, not "open" like keys.go's generic
			// Select.Help() says elsewhere - so this one entry is written
			// out rather than reusing helpEntry, matching the actual
			// behavior on this screen.
			helpRow(m.keys.Select.Help().Key, "switch profile").kept(),
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

	// sources is Task 4's Sources-screen group (#75): just the scope toggle
	// - Up/Down are left to the footer's generic hint like every screen
	// other than health below.
	sources := helpGroup{
		name: "sources",
		entries: []helpItem{
			helpEntry(m.keys.ToggleAllSources),
		},
	}

	// health is Task 9's Health-screen group (#224): unlike every other
	// screen group above, it lists its OWN jump-to-screen key (HealthScreen,
	// "6") rather than leaving that to the global group's generic "1-6"
	// entry - without it the group would start life empty and read as
	// broken rather than "grows later". FullCheck ("c", Task 11) and
	// FixHealth ("F", Task 12) fill it out.
	//
	// #224 Task 15's conflicts fold folded the retired standalone Conflicts
	// screen's own help group in here too: Up/Down - documented here unlike
	// most OTHER screens (left to the footer's generic "↑↓/j/k: move" hint)
	// because selecting a row IS this screen's core interaction, for both
	// findings and conflict rows now - it's what reveals the detail strip's
	// remedy/stale hint copy, not just a cosmetic highlight - and Deploy,
	// since the stale-conflict remedy names "deploy (D) to apply" and this
	// key must actually fire from here (deployActiveProfile's own screen
	// guard, mutations.go) for that remedy to be actionable in place.
	health := helpGroup{
		name: "health",
		entries: []helpItem{
			helpEntry(m.keys.HealthScreen),
			helpEntry(m.keys.Up),
			helpEntry(m.keys.Down),
			helpEntry(m.keys.FullCheck),
			helpEntry(m.keys.FixHealth),
			helpEntry(m.keys.Deploy),
		},
	}

	fixed := []helpGroup{dashboard, installedMods, search, profiles, sources, health}
	screenGroupName := map[Screen]string{
		ScreenDashboard:     dashboard.name,
		ScreenInstalledMods: installedMods.name,
		ScreenSearch:        search.name,
		ScreenProfiles:      profiles.name,
		ScreenSources:       sources.name,
		ScreenHealth:        health.name,
	}
	// #224 Task 9 fix round 1, Finding 2, generalized in #86: while any
	// screen has pushed content (contextview.go), the promoted group is the
	// CONTENT's own HelpGroup() - the ambient screen's static bindings don't
	// describe whatever the pushed content actually shows, so consulting it
	// here (rather than leaving HelpGroup() an orphaned interface member) is
	// the whole point of that method existing. The pushing screen's own
	// static group stays in fixed, unpromoted, further down the list.
	if m.contextContent != nil {
		return append([]helpGroup{global, m.contextContent.HelpGroup()}, fixed...)
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
		// Bumped 40->50 in Task 4 (see the history above this line for that
		// jump's own accounting), then 50->65 for #86 Task 7: the two new
		// Select entries (installed mods + search groups, documenting
		// enter-opens-details) pushed the full uncapped group list to 57
		// lines, one past 50. TestHelpViewListsPerScreenGroups depends on
		// this staying "generous" enough to render every group's content,
		// per this method's own doc comment. 65 leaves headroom above
		// today's 57 for the next few tasks' bindings, rather than needing a
		// re-bump for every single addition.
		return 65
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

	// The keep marker is honored only for the promoted group - index 1,
	// immediately after "global", per helpGroups' ordering contract (the
	// current screen's group, or pushed content's HelpGroup()). Any other
	// group's headline describes a screen the user is NOT on, and letting
	// it survive would force header-less rows from distant groups into the
	// few visible lines at the current screen's expense (#234).
	var body []helpItem
	for i, g := range m.helpGroups() {
		if i > 0 {
			body = append(body, helpItem{})
		}
		body = append(body, helpItem{text: truncate(m.theme.PanelTitle.Render(g.name), panelContentWidth)})
		for _, e := range g.entries {
			body = append(body, helpItem{text: truncate(e.text, panelContentWidth), keep: e.keep && i == 1})
		}
	}

	lines := []string{truncate(m.theme.PanelTitle.Render("HELP"), panelContentWidth), ""}
	budget := m.helpBodyBudget()
	if len(body) > budget {
		shown := max(budget-1, 0)
		// Drop by priority, not position (#234): walk from the tail
		// dropping unkept rows first, so a kept headline row survives
		// wherever it sits in its group; only when fewer than `shown` rows
		// are unkept does a second pass reach the kept ones. Survivors
		// render in their original order, so the "+N more" tail can stand
		// in for rows dropped from the middle, not just below it.
		drop := len(body) - shown
		dropped := make([]bool, len(body))
		for i := len(body) - 1; i >= 0 && drop > 0; i-- {
			if !body[i].keep {
				dropped[i] = true
				drop--
			}
		}
		for i := len(body) - 1; i >= 0 && drop > 0; i-- {
			if !dropped[i] {
				dropped[i] = true
				drop--
			}
		}
		for i, item := range body {
			if !dropped[i] {
				lines = append(lines, item.text)
			}
		}
		lines = append(lines, truncate(m.theme.MutedText.Render(fmt.Sprintf("+%d more", len(body)-shown)), panelContentWidth))
	} else {
		for _, item := range body {
			lines = append(lines, item.text)
		}
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

// modFlags renders the per-mod flag column: "lck", "pin", or "raw"
// (left-aligned in the first 3 columns) and "*" (the last column) for a mod
// actually updated THIS session - not merely checked. "lck" (#97) outranks
// "pin" in the 3-char slot - a locked+pinned mod shows "lck" ("lock wins the
// slot and the UI names the lock"); the mod's pin state is untouched and
// stays visible in the P picker and mod actions, it just doesn't get its own
// glyph here when a lock is also set. "raw" (#221) is the lowest-precedence
// flag: a mod on a merge-compile game (ModItem.CompileGame) that actually
// HAS a pak merge source (ModItem.HasPakSource) and whose prebuilt pak
// deploys unconverted shows "raw" ONLY when neither lck nor pin already
// claimed the slot - it names a state (this mod's prebuilt .pak ships
// unconverted into the merge) rather than a user-set marker like the other
// two, so it defers to both. An exmodz-only mod (HasPakSource false) never
// shows "raw" regardless of the conversion flags - it has no pak to leave
// unconverted, so the flag would be meaningless (#221 round-4 fix). Deploy
// truth needs BOTH the per-mod flag (ModItem.ConvertPaks) AND the active
// game's own flag (ModItem.GameConvertPaks) to be on before a pak actually
// converts - mirroring the README's "either one is enough to keep a pak
// raw" ([Pak conversion (Icarus)](README.md#pak-conversion-icarus)) - so
// among pak-source mods "raw" shows whenever EITHER is off, not just the
// per-mod one. The wire string "pin" (not the
// CLI's "pinned") matches ModItem.UpdatePolicy's documented values - see
// service_core.go's policyToString for why the two interfaces differ. The
// fixed "%-3s %s" shape always fills exactly flagsWidth (5) columns -
// "lck *", "pin *", "pin  ", "raw  ", "    *", or "     " - so the flag and
// the marker can appear independently, together, or not at all without ever
// shifting the author/version columns that follow (mirrors the pin-only
// column's own fixed-width reasoning above modRow).
func (m Model) modFlags(mod ModItem) string {
	flag := ""
	switch {
	case mod.Locked:
		flag = "lck" // lock wins the slot ("the UI names the lock"); pin state stays visible in the P picker and mod actions
	case mod.UpdatePolicy == "pin":
		flag = "pin"
	case mod.CompileGame && mod.HasPakSource && !(mod.GameConvertPaks && mod.ConvertPaks):
		flag = "raw"
	}
	marker := " "
	if m.wasUpdatedThisSession(mod) {
		marker = "*"
	}
	return fmt.Sprintf("%-3s %s", flag, marker)
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

// healthDashboardLine renders the dashboard's Health signal row (#224 Task
// 10): "?" (mirroring countLabel's own "-1 = unknown" convention) before
// the first LOCAL-tier scan lands, "OK" once a scan finds nothing, or its
// issue/warning counts otherwise. Every non-"?" case names the verify tier
// the counts came from - "(local)" for the ordinary dashboard refresh
// (loadData's DataProvider.Health call), "(full)" once the Health screen's
// own explicit full/network check (ActionProvider.RunHealthCheck, Tasks
// 11/12) has updated the same m.health/m.summary state (m.health.Full).
// Shared verbatim by every dashboardView layout below rather than each one
// adapting its own casing/prefix convention - the phrasing is pinned by
// spec, not layout-specific flavor text.
func (m Model) healthDashboardLine() string {
	issues, warnings := m.summary.HealthIssues, m.summary.HealthWarnings
	if issues < 0 || warnings < 0 {
		return "Health: ?"
	}
	tier := "local"
	if m.health.Full {
		tier = "full"
	}
	if issues == 0 && warnings == 0 {
		return fmt.Sprintf("Health: OK (%s)", tier)
	}
	return fmt.Sprintf("Health: %d issue(s), %d warning(s) (%s)", issues, warnings, tier)
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
