package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// Layout describes the major panel arrangement for a prototype theme.
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
}

// Model is the root Bubble Tea model for the lmm TUI.
type Model struct {
	theme    theme.Theme
	layout   Layout
	keys     KeyMap
	provider DataProvider

	state   loadState
	loadErr error

	summary       Summary
	mods          []ModItem
	searchResults []ModItem
	profiles      []ProfileItem

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
	summary       Summary
	mods          []ModItem
	searchResults []ModItem
	profiles      []ProfileItem
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

	return Model{
		theme:    t,
		layout:   layoutForTheme(t.Name),
		keys:     DefaultKeyMap(),
		provider: options.Provider,
		state:    stateLoading,
		screen:   ScreenDashboard,
		selected: map[Screen]int{
			ScreenDashboard:     0,
			ScreenInstalledMods: 0,
			ScreenSearch:        0,
			ScreenProfiles:      0,
		},
	}, nil
}

// NewPrototypeModel creates a side-effect-free TUI model backed by fake data.
func NewPrototypeModel(options Options) (Model, error) {
	options.Provider = NewPrototypeProvider()
	return NewModel(options)
}

func (m Model) dashboardMenu() []menuItem {
	if m.layout == LayoutMonochromeTerminal {
		return []menuItem{
			{label: "RUN SPELLBOOK SCAN", target: ScreenInstalledMods, hasTarget: true},
			{label: "QUERY ARCHIVE INDEX", target: ScreenSearch, hasTarget: true},
			{label: "LOAD PROFILE ROSTER", target: ScreenProfiles, hasTarget: true},
			{label: "ASK CONFLICT ORACLE"},
		}
	}
	return []menuItem{
		{label: "Installed Mods", target: ScreenInstalledMods, hasTarget: true},
		{label: "Search Archives", target: ScreenSearch, hasTarget: true},
		{label: "Profiles", target: ScreenProfiles, hasTarget: true},
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

func (m Model) openSelectedMenuEntry() Model {
	if m.screen != ScreenDashboard {
		return m
	}
	items := m.dashboardMenu()
	selected := m.selected[ScreenDashboard]
	if selected >= len(items) || !items[selected].hasTarget {
		return m
	}
	m.screen = items[selected].target
	return m
}

func (m Model) Init() tea.Cmd {
	return m.loadData
}

// loadData fetches all dashboard data through the configured DataProvider.
// It runs as a Bubble Tea command, off the update loop.
func (m Model) loadData() tea.Msg {
	ctx := context.Background()

	summary, err := m.provider.Summary(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	mods, err := m.provider.InstalledMods(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	results, err := m.provider.SearchResults(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	profiles, err := m.provider.Profiles(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}

	return dataLoadedMsg{summary: summary, mods: mods, searchResults: results, profiles: profiles}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dataLoadedMsg:
		m.state = stateReady
		m.summary = msg.summary
		m.mods = msg.mods
		m.searchResults = msg.searchResults
		m.profiles = msg.profiles
		return m, nil
	case loadFailedMsg:
		m.state = stateFailed
		m.loadErr = msg.err
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	default:
		return m, nil
	}
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, m.keys.NextScreen):
		m.screen = screenAt((m.screenIndex() + 1) % len(screens))
		return m, nil
	case key.Matches(msg, m.keys.PrevScreen):
		m.screen = screenAt((m.screenIndex() - 1 + len(screens)) % len(screens))
		return m, nil
	case key.Matches(msg, m.keys.Dashboard):
		m.screen = ScreenDashboard
		return m, nil
	case key.Matches(msg, m.keys.InstalledMods):
		m.screen = ScreenInstalledMods
		return m, nil
	case key.Matches(msg, m.keys.Search):
		m.screen = ScreenSearch
		return m, nil
	case key.Matches(msg, m.keys.Profiles):
		m.screen = ScreenProfiles
		return m, nil
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
		return m, nil
	case key.Matches(msg, m.keys.Select):
		return m.openSelectedMenuEntry(), nil
	default:
		return m, nil
	}
}

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(m.theme.Title.Render("LMM // Linux Mod Manager // Prototype Terminal"))
	b.WriteString("\n")
	b.WriteString(m.nav())
	b.WriteString("\n\n")

	b.WriteString(m.screenView())

	b.WriteString("\n\n")
	if m.showHelp {
		b.WriteString(m.helpView())
	} else {
		b.WriteString(m.theme.Help.Render("?: help  tab/h/l: screens  ↑↓/j/k: move  /: search  q: quit"))
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
		return len(m.searchResults)
	case ScreenProfiles:
		return len(m.profiles)
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
		"Last deploy: 2h ago",
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
	rows = append(rows, "[E] Enable  [D] Disable  [U] Update  [/] Search")
	if len(m.mods) == 0 {
		rows = append(rows, m.theme.MutedText.Render("No mods installed yet. 'lmm install <mod>' begins the quest."))
	}
	for i, mod := range m.mods {
		rows = append(rows, m.modRow(i, mod))
	}
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
}

func (m Model) searchView() string {
	rows := []string{m.theme.PanelTitle.Render("ARCHIVE SEARCH")}
	rows = append(rows, "Query: survival mods_")
	if len(m.searchResults) == 0 {
		rows = append(rows, m.theme.MutedText.Render("The archive index opens in a later chapter. (Search arrives in Phase 4.)"))
	}
	for i, mod := range m.searchResults {
		rows = append(rows, m.modRow(i, mod))
	}
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
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

func (m Model) helpView() string {
	return m.panel(m.availableWidth()).Render(strings.Join([]string{
		m.theme.PanelTitle.Render("HELP"),
		"arrows / hjkl       move or switch screens",
		"tab / shift+tab     cycle top-level screens",
		"1-4                 jump to a screen",
		"/                   open search screen",
		"?                   toggle this help",
		"q / ctrl+c           quit",
		"",
		"Prototype mode uses static fake data only. No DB, API, install, update, or deploy actions run here.",
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

func (m Model) modRow(index int, mod ModItem) string {
	line := fmt.Sprintf("%-28s %-11s %-16s %7s", mod.Name, mod.Status, mod.Author, mod.Version)
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

	const titleNavAndSpacerHeight = 4 // title, nav, and the spacer lines around content.
	return titleNavAndSpacerHeight + footerHeight
}

// countLabel renders n, or "?" when n is negative (unknown, e.g. no update
// check has run yet).
func countLabel(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
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
