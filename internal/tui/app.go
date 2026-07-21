package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

	state   loadState
	loadErr error

	summary  Summary
	mods     []ModItem
	profiles []ProfileItem
	sources  []SourceInfo
	search   searchModel
	action   actionModel

	screen   Screen
	selected map[Screen]int
	showHelp bool
	width    int
	height   int
}

// loadState tracks where the Model is in its async data-load lifecycle.
type loadState int

const (
	stateLoading loadState = iota
	stateReady
	stateFailed
)

// dataLoadedMsg carries data successfully loaded through a DataProvider.
type dataLoadedMsg struct {
	summary  Summary
	mods     []ModItem
	profiles []ProfileItem
}

// loadFailedMsg carries an error from a failed DataProvider load.
type loadFailedMsg struct{ err error }

// NewModel creates the TUI model backed by the given DataProvider.
func NewModel(options Options) (Model, error) {
	if options.Provider == nil {
		return Model{}, fmt.Errorf("TUI options: provider is required")
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
		state:    stateLoading,
		screen:   ScreenDashboard,
		search:   newSearchModel(options.Provider, t.Panel.GetHorizontalFrameSize()),
		// sources is seeded synchronously (like search's source list above)
		// rather than through loadData/dataLoadedMsg: SourceInfos is a
		// read-only view of already-registered sources, not an I/O call that
		// can fail, so it needs no async load state or error path.
		sources: options.Provider.SourceInfos(),
		selected: map[Screen]int{
			ScreenDashboard:     0,
			ScreenInstalledMods: 0,
			ScreenSearch:        0,
			ScreenProfiles:      0,
			ScreenSources:       0,
		},
	}, nil
}

// NewPrototypeModel creates a side-effect-free TUI model backed by fake data.
// Provider and Actions are wired from the SAME prototypeProvider instance
// (see NewPrototypeProvider's doc comment), so actions confirmed through
// the returned Model are visible in its own subsequent reads — whatever the
// caller passed in either field is discarded.
func NewPrototypeModel(options Options) (Model, error) {
	provider := NewPrototypeProvider()
	options.Provider = provider
	if actions, ok := provider.(ActionProvider); ok {
		options.Actions = actions
	}
	return NewModel(options)
}

func (m Model) dashboardMenu() []menuItem {
	if m.layout == LayoutMonochromeTerminal {
		return []menuItem{
			{label: "RUN SPELLBOOK SCAN", target: ScreenInstalledMods, hasTarget: true},
			{label: "QUERY ARCHIVE INDEX", target: ScreenSearch, hasTarget: true},
			{label: "LOAD PROFILE ROSTER", target: ScreenProfiles, hasTarget: true},
			{label: "SCRY SOURCE REGISTRY", target: ScreenSources, hasTarget: true},
			{label: "ASK CONFLICT ORACLE"},
		}
	}
	return []menuItem{
		{label: "Installed Mods", target: ScreenInstalledMods, hasTarget: true},
		{label: "Search Archives", target: ScreenSearch, hasTarget: true},
		{label: "Profiles", target: ScreenProfiles, hasTarget: true},
		{label: "Sources", target: ScreenSources, hasTarget: true},
		{label: "Consult Conflict Oracle"},
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

func (m Model) openSelectedMenuEntry() (Model, tea.Cmd) {
	if m.screen != ScreenDashboard {
		return m, nil
	}
	items := m.dashboardMenu()
	selected := m.selected[ScreenDashboard]
	if selected >= len(items) || !items[selected].hasTarget {
		return m, nil
	}
	return m.gotoScreen(items[selected].target)
}

// gotoScreen switches to the target screen without touching the search
// input's focus state. This is the entry path for screen-cycling
// (NextScreen/PrevScreen), the direct screen-jump bindings (Dashboard,
// InstalledMods, Profiles, Sources), and dashboard-menu selection
// (openSelectedMenuEntry) — none of these are an explicit request to search,
// so landing on ScreenSearch through them must NOT focus the input. A
// focused input swallows every keystroke (see updateKey's focused-input
// branch), so auto-focusing here trapped a user cycling through screens with
// tab/shift-tab/left/right/h/l on Search until they pressed Esc (smoke-test
// Finding 1). See gotoScreenFocused for the two bindings that DO focus.
func (m Model) gotoScreen(screen Screen) (Model, tea.Cmd) {
	m.screen = screen
	return m, nil
}

// gotoScreenFocused switches to ScreenSearch and focuses the input
// immediately. Reserved for the two EXPLICIT "go search" bindings — Search
// ("/") and SearchScreen ("3") — so pressing either is enough to start
// typing right away. Esc (the Blur binding) is the only way back out of
// focus; once blurred, screen-level keys (s, n/p, navigation) reach
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
// It runs as a Bubble Tea command, off the update loop.
func (m Model) loadData() tea.Msg {
	summary, mods, err := m.provider.Overview(m.ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	profiles, err := m.provider.Profiles(m.ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}

	return dataLoadedMsg{summary: summary, mods: mods, profiles: profiles}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dataLoadedMsg:
		m.state = stateReady
		m.summary = msg.summary
		m.mods = msg.mods
		m.profiles = msg.profiles
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
		m.action.status = formatOutcomeStatus(msg.outcome)
		m.action.statusIsError = false
		// A fresh switch's target must rebind the session's active-profile
		// providers BEFORE the refresh below reads them (see rebindProfile
		// and profileRebinder in actions.go) - otherwise Profiles() keeps
		// starring the OLD profile forever, and subsequent mutations keep
		// targeting it too (finding C1).
		if msg.switchedTo != "" {
			m.rebindProfile(msg.switchedTo)
		}
		return m, m.loadData
	case actionFailedMsg:
		if msg.gen != m.action.gen {
			return m, nil
		}
		m.action.running = false
		if m.action.cancel != nil {
			m.action.cancel()
			m.action.cancel = nil
		}
		m.action.status = singleLine(msg.err.Error())
		m.action.statusIsError = true
		return m, m.loadData
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
	case loadFailedMsg:
		m.state = stateFailed
		m.loadErr = msg.err
		return m, nil
	case searchResultMsg:
		if msg.gen != m.search.gen {
			return m, nil
		}
		m.search.state = searchReady
		m.search.page = msg.page
		m.selected[ScreenSearch] = 0
		return m, nil
	case searchFailedMsg:
		if msg.gen != m.search.gen {
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

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
			return m, m.quitCmd()
		case key.Matches(msg, m.keys.Blur):
			m.search.input.Blur()
			return m, nil
		case key.Matches(msg, m.keys.Submit):
			m.search.input.Blur()
			return m.startSearch(m.search.input.Value(), 0)
		default:
			var cmd tea.Cmd
			m.search.input, cmd = m.search.input.Update(msg)
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, m.quitCmd()
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
		if m.screen == ScreenSearch && m.search.state == searchReady && m.search.hasNextPage() {
			return m.startSearch(m.search.page.Query, m.search.page.Page+1)
		}
		return m, nil
	case key.Matches(msg, m.keys.PrevPage):
		if m.screen == ScreenSearch && m.search.state == searchReady && m.search.page.Page > 0 {
			return m.startSearch(m.search.page.Query, m.search.page.Page-1)
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
		}
		return m, nil
	case key.Matches(msg, m.keys.Profiles):
		return m.gotoScreen(ScreenProfiles)
	case key.Matches(msg, m.keys.Sources):
		return m.gotoScreen(ScreenSources)
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
		return m, nil
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
	default:
		return m, nil
	}
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(m.theme.Title.Render("LMM // Linux Mod Manager"))
	b.WriteString("\n")
	b.WriteString(m.nav())
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
	hint := "?: help  tab/h/l: screens  ↑↓/j/k: move  /: search  e: enable/disable · x: uninstall · D: deploy  enter: switch  q: quit"
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
	default:
		return len(m.dashboardMenu())
	}
}

func (m Model) nav() string {
	items := make([]string, 0, len(screens))
	for i, screen := range screens {
		label := fmt.Sprintf("[%d] %s", i+1, screen)
		if screen == m.screen {
			label = m.theme.Selected.Render(label)
		} else {
			label = m.theme.MutedText.Render(label)
		}
		items = append(items, label)
	}
	return strings.Join(items, "  ")
}

func (m Model) screenView() string {
	if m.action.pending != nil {
		return m.actionModalView()
	}

	switch m.state {
	case stateLoading:
		return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
			Render(m.theme.PanelTitle.Render("Consulting the archives..."))
	case stateFailed:
		return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
			Render(strings.Join([]string{
				m.theme.PanelTitle.Render("THE RITUAL FAILED"),
				m.theme.DangerText.Render(m.loadErr.Error()),
				"",
				m.theme.MutedText.Render("q: quit"),
			}, "\n"))
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

func (m Model) partyDashboardView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	gap := 1
	panelWidth := max((width-gap)/2, 1)
	splitHeight := height
	topHeight := splitHeight / 2
	menuHeight := splitHeight - topHeight

	party := strings.Join([]string{
		m.theme.PanelTitle.Render("PARTY"),
		fmt.Sprintf("Game:    %s", m.summary.GameName),
		fmt.Sprintf("Profile: %s", m.summary.ProfileName),
		fmt.Sprintf("Mods:    %d installed / %d enabled", m.summary.Installed, m.summary.Enabled),
	}, "\n")

	quest := strings.Join([]string{
		m.theme.PanelTitle.Render("QUEST LOG"),
		fmt.Sprintf("%s updates available", m.theme.WarningText.Render(countLabel(m.summary.Updates))),
		fmt.Sprintf("%s file conflict", m.theme.DangerText.Render(countLabel(m.summary.Conflicts))),
		"Last deploy: ?",
	}, "\n")

	menu := strings.Join(
		append([]string{m.theme.PanelTitle.Render("COMMANDS")}, m.dashboardMenuRows()...),
		"\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panelWithHeight(panelWidth, topHeight).Render(party),
		" ",
		m.panelWithHeight(panelWidth, topHeight).Render(quest),
	) + "\n" + m.panelWithHeight(width, menuHeight).Render(menu)
}

func (m Model) terminalDashboardView() string {
	rows := []string{
		m.theme.PanelTitle.Render("BOOT SEQUENCE // MOD GUILD TERMINAL"),
		fmt.Sprintf("> GAME     %s", m.summary.GameName),
		fmt.Sprintf("> PROFILE  %s", m.summary.ProfileName),
		fmt.Sprintf("> MODS     %d INSTALLED / %d ENABLED", m.summary.Installed, m.summary.Enabled),
		fmt.Sprintf("> ALERTS   %s UPDATES // %s CONFLICT", m.theme.WarningText.Render(countLabel(m.summary.Updates)), m.theme.DangerText.Render(countLabel(m.summary.Conflicts))),
		"",
	}
	rows = append(rows, m.dashboardMenuRows()...)
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

func (m Model) commanderDashboardView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	gap := 1
	leftWidth := max((width-gap)/2, 1)
	rightWidth := max(width-gap-leftWidth, 1)

	left := strings.Join([]string{
		m.theme.PanelTitle.Render("ACTIVE PROFILE"),
		m.summary.ProfileName,
		"",
		fmt.Sprintf("Game     %s", m.summary.GameName),
		fmt.Sprintf("Enabled  %d", m.summary.Enabled),
		fmt.Sprintf("Updates  %s", countLabel(m.summary.Updates)),
	}, "\n")
	right := strings.Join(
		append([]string{m.theme.PanelTitle.Render("OPERATIONS")}, m.dashboardMenuRows()...),
		"\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panelWithHeight(leftWidth, height).Render(left),
		" ",
		m.panelWithHeight(rightWidth, height).Render(right),
	)
}

func (m Model) crtDashboardView() string {
	rows := []string{
		m.theme.PanelTitle.Render("CRT STATUS STACK"),
		fmt.Sprintf("▓ %-10s %s", "GAME", m.summary.GameName),
		fmt.Sprintf("▓ %-10s %s", "PROFILE", m.summary.ProfileName),
		fmt.Sprintf("▓ %-10s %d/%d", "MODS", m.summary.Enabled, m.summary.Installed),
		fmt.Sprintf("▓ %-10s %s updates, %s conflict", "SIGNAL", countLabel(m.summary.Updates), countLabel(m.summary.Conflicts)),
		"",
	}
	rows = append(rows, m.dashboardMenuRows()...)
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

func (m Model) modsView() string {
	rows := []string{m.theme.PanelTitle.Render("SPELLBOOK: INSTALLED MODS")}
	rows = append(rows, "[/] Search")
	if len(m.mods) == 0 {
		rows = append(rows, m.theme.MutedText.Render("No mods installed yet. 'lmm install <mod>' begins the quest."))
	}
	for i, mod := range m.mods {
		rows = append(rows, m.modRow(i, m.availableWidth(), mod))
	}
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
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
// every search state except the ready-with-results two-pane layout.
func (m Model) searchSinglePanel(lines []string) string {
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
			return m.searchSinglePanel(append(header,
				m.theme.MutedText.Render(fmt.Sprintf("No archives matched %q on %s.", m.search.page.Query, sourceLabel(m.search.page.Source))),
			))
		}
		if warning := m.searchWarningLine(m.availableWidth()); warning != "" {
			header = append(header, warning)
		}
		return m.searchReadyView(header)
	default: // searchIdle
		// Only "/" and "3" (gotoScreenFocused) focus the input on entry; every
		// other path here (cycling, dashboard menu, jump keys) leaves it
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
// beyond maxLines are omitted for the same reason (a full page of results
// can outnumber the available rows on a short terminal).
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
	if len(results) > maxLines {
		results = results[:maxLines]
	}

	rows := make([]string, 0, len(results))
	for i, item := range results {
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
		rows = append(rows, m.row(i, line))
	}
	return strings.Join(rows, "\n")
}

// searchDetailPane renders the fields for the currently selected result.
// Unknown endorsements render "?" (countLabel convention: never fake data).
// Labels and free-text values are truncated to the pane's content width for
// the same reason searchResultsPane truncates: overflow would trigger an
// unpredictable automatic re-wrap inside the bordered panel. The summary is
// clipped to whatever vertical budget remains after the fixed fields so a
// long summary can never grow the pane past maxLines.
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
	valueWidth := max(innerWidth-labelWidth, 1)
	field := func(label, value string) string {
		return fmt.Sprintf("%-*s%s", labelWidth, truncate(label, labelWidth), truncate(value, valueWidth))
	}

	lines := []string{
		m.theme.PanelTitle.Render("DETAIL"),
		field("Name", item.Name),
		field("Author", item.Author),
		field("Version", item.Version),
		field("Source", item.Source),
		field("Status", item.Status),
		field("Downloads", fmt.Sprintf("%d", item.Downloads)),
		field("Endorsements", endorsements),
	}

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

// searchFooterLine renders pagination status. The total-pages figure only
// appears when the source reports a TotalCount; otherwise only the current
// page is shown. In both cases, the "n next"/"p prev" hints only render when
// the corresponding key would actually act, so a page-1 footer never shows a
// dead "p prev" hint.
func (m Model) searchFooterLine() string {
	page := m.search.page
	current := page.Page + 1

	footer := fmt.Sprintf("Page %d", current)
	if page.TotalCount > 0 && page.PageSize > 0 {
		totalPages := (page.TotalCount + page.PageSize - 1) / page.PageSize
		footer = fmt.Sprintf("Page %d/%d (%d results)", current, totalPages, page.TotalCount)
	}

	if m.search.hasNextPage() {
		footer += " · n next"
	}
	if page.Page > 0 {
		footer += " · p prev"
	}
	return footer
}

func (m Model) profilesView() string {
	rows := []string{m.theme.PanelTitle.Render("PROFILE ROSTER")}
	for i, profile := range m.profiles {
		active := " "
		if profile.Active {
			active = "*"
		}
		line := fmt.Sprintf("%s %-22s %3d mods", active, profile.Name, profile.ModCount)
		rows = append(rows, m.row(i, line))
	}
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

// sourcesView renders the read-only source registry: every source
// registered with the DataProvider (built-in and user-defined), one row
// each, in the single-pane list style profilesView uses. Unlike
// `lmm source list`, there is no error/status column — see SourceInfo's doc
// comment for why definition-load failures never reach this screen.
func (m Model) sourcesView() string {
	// Calculate the panel's content width, which is narrower than availableWidth()
	// by the panel's horizontal frame size (border + padding). Rows that render
	// INSIDE this panel must be truncated to this width to prevent lipgloss from
	// re-wrapping overlong lines and growing the view past its fixed height
	// budget; see the fix in commit 2c075e3 for the same issue in searchView's
	// zero-results warning.
	panelContentWidth := max(m.availableWidth()-m.theme.Panel.GetHorizontalFrameSize(), 1)

	// "  " matches m.row()'s 2-column selection-marker prefix ("> "/"  ") so
	// the header lines up with the data columns below it instead of starting
	// two columns to their left.
	headerLine := "  " + fmt.Sprintf("%-20s %-12s %-6s %s", "ID", "TYPE", "AUTH", "CAPABILITIES")
	headerLine = truncate(headerLine, panelContentWidth)
	rows := []string{
		m.theme.PanelTitle.Render("SOURCE REGISTRY"),
		m.theme.MutedText.Render(headerLine),
	}
	for i, src := range m.sources {
		line := fmt.Sprintf("%-20s %-12s %-6s %s", src.ID, src.Type, src.Auth, src.Capabilities)
		line = truncate(line, panelContentWidth)
		rows = append(rows, m.row(i, line))
	}
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

func (m Model) helpView() string {
	return m.panel(m.availableWidth()).Render(strings.Join([]string{
		m.theme.PanelTitle.Render("HELP"),
		"arrows / hjkl       move or switch screens",
		"tab / shift+tab     cycle top-level screens",
		"1-5                 jump to a screen (3 focuses search)",
		"/                   search from anywhere (jumps + focuses input)",
		"enter               open menu entry / search / switch profile",
		"esc                 unfocus search input",
		"n/p                 result pages",
		"s                   cycle source",
		"e/x/D               toggle enable/disable / uninstall selected mod / deploy active profile",
		"?                   toggle this help",
		"q / ctrl+c           quit",
		"",
		"Enable, disable, uninstall, deploy, and profile switch all confirm through a modal before anything changes.",
	}, "\n"))
}

func (m Model) row(index int, label string) string {
	prefix := "  "
	if m.selected[m.screen] == index {
		prefix = "> "
		return m.theme.Selected.Render(prefix + label)
	}
	return prefix + label
}

// modRow renders one Installed Mods row: name / status / author / version.
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
	const gaps = 3        // separating spaces between the 4 columns
	const minName = 8

	avail := max(width-m.theme.Panel.GetHorizontalFrameSize()-prefixWidth-gaps, minName)
	statusWidth := min(11, max(avail/6, 1)) // "disabled"/"deployed" are 8 runes
	authorWidth := min(16, max(avail/5, 1))
	versionWidth := min(7, max(avail/8, 1))
	nameWidth := max(avail-statusWidth-authorWidth-versionWidth, minName)

	line := fmt.Sprintf("%-*s %-*s %-*s %*s",
		nameWidth, truncate(mod.Name, nameWidth),
		statusWidth, truncate(mod.Status, statusWidth),
		authorWidth, truncate(mod.Author, authorWidth),
		versionWidth, truncate(mod.Version, versionWidth))
	return m.row(index, line)
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
	// footer, and only when set — see statusLine's matching "" ⇒ nothing
	// rendered contract in View().
	statusHeight := 0
	if m.action.status != "" {
		statusHeight = 1
	}

	const titleNavAndSpacerHeight = 4 // title, nav, and the spacer lines around content.
	return titleNavAndSpacerHeight + footerHeight + statusHeight
}

// countLabel renders n, or "?" when n is negative (unknown, e.g. no update
// check has run yet).
func countLabel(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
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
