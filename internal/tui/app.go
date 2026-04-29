package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
	"github.com/DonovanMods/linux-mod-manager/internal/tui/theme"
)

const defaultContentWidth = 76

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
	Theme     string
	Prototype bool
}

// Model is the root Bubble Tea model for the lmm TUI.
type Model struct {
	theme    theme.Theme
	layout   Layout
	data     prototype.Data
	screen   Screen
	selected map[Screen]int
	showHelp bool
	width    int
	height   int
}

// NewPrototypeModel creates a side-effect-free TUI model backed by fake data.
func NewPrototypeModel(options Options) (Model, error) {
	t, err := theme.ByName(options.Theme)
	if err != nil {
		return Model{}, err
	}

	return Model{
		theme:  t,
		layout: layoutForTheme(t.Name),
		data:   prototype.Load(),
		screen: ScreenDashboard,
		selected: map[Screen]int{
			ScreenDashboard:     0,
			ScreenInstalledMods: 0,
			ScreenSearch:        0,
			ScreenProfiles:      0,
		},
	}, nil
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "tab", "right", "l":
		m.screen = screenAt((m.screenIndex() + 1) % len(screens))
		return m, nil
	case "shift+tab", "left", "h":
		m.screen = screenAt((m.screenIndex() - 1 + len(screens)) % len(screens))
		return m, nil
	case "1":
		m.screen = ScreenDashboard
		return m, nil
	case "2":
		m.screen = ScreenInstalledMods
		return m, nil
	case "3", "/":
		m.screen = ScreenSearch
		return m, nil
	case "4":
		m.screen = ScreenProfiles
		return m, nil
	case "up", "k":
		m.moveSelection(-1)
		return m, nil
	case "down", "j":
		m.moveSelection(1)
		return m, nil
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
		return len(m.data.InstalledMods)
	case ScreenSearch:
		return len(m.data.SearchResults)
	case ScreenProfiles:
		return len(m.data.Profiles)
	default:
		return 4
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
	gap := 1
	panelWidth := max((width-gap)/2, 24)

	party := strings.Join([]string{
		m.theme.PanelTitle.Render("PARTY"),
		fmt.Sprintf("Game:    %s", m.data.Game.Name),
		fmt.Sprintf("Profile: %s", m.data.Profile.Name),
		fmt.Sprintf("Mods:    %d installed / %d enabled", m.data.Stats.Installed, m.data.Stats.Enabled),
	}, "\n")

	quest := strings.Join([]string{
		m.theme.PanelTitle.Render("QUEST LOG"),
		fmt.Sprintf("%s updates available", statusValue(m.data.Stats.Updates, m.theme.Warning)),
		fmt.Sprintf("%s file conflict", statusValue(m.data.Stats.Conflicts, m.theme.Danger)),
		"Last deploy: 2h ago",
	}, "\n")

	menu := strings.Join([]string{
		m.theme.PanelTitle.Render("COMMANDS"),
		m.row(0, "Installed Mods"),
		m.row(1, "Search Archives"),
		m.row(2, "Profiles"),
		m.row(3, "Consult Conflict Oracle"),
	}, "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panel(panelWidth).Render(party),
		" ",
		m.panel(panelWidth).Render(quest),
	) + "\n" + m.panel(width).Render(menu)
}

func (m Model) terminalDashboardView() string {
	rows := []string{
		m.theme.PanelTitle.Render("BOOT SEQUENCE // MOD GUILD TERMINAL"),
		fmt.Sprintf("> GAME     %s", m.data.Game.Name),
		fmt.Sprintf("> PROFILE  %s", m.data.Profile.Name),
		fmt.Sprintf("> MODS     %d INSTALLED / %d ENABLED", m.data.Stats.Installed, m.data.Stats.Enabled),
		fmt.Sprintf("> ALERTS   %s UPDATES // %s CONFLICT", statusValue(m.data.Stats.Updates, m.theme.Warning), statusValue(m.data.Stats.Conflicts, m.theme.Danger)),
		"",
		m.row(0, "RUN SPELLBOOK SCAN"),
		m.row(1, "QUERY ARCHIVE INDEX"),
		m.row(2, "LOAD PROFILE ROSTER"),
		m.row(3, "ASK CONFLICT ORACLE"),
	}
	return m.panel(m.availableWidth()).Render(strings.Join(rows, "\n"))
}

func (m Model) commanderDashboardView() string {
	width := m.availableWidth()
	gap := 1
	leftWidth := max((width-gap)/2, 24)
	rightWidth := max(width-gap-leftWidth, 24)

	left := strings.Join([]string{
		m.theme.PanelTitle.Render("ACTIVE PROFILE"),
		m.data.Profile.Name,
		"",
		fmt.Sprintf("Game     %s", m.data.Game.Name),
		fmt.Sprintf("Enabled  %d", m.data.Stats.Enabled),
		fmt.Sprintf("Updates  %d", m.data.Stats.Updates),
	}, "\n")
	right := strings.Join([]string{
		m.theme.PanelTitle.Render("OPERATIONS"),
		m.row(0, "Installed Mods"),
		m.row(1, "Search Archives"),
		m.row(2, "Profiles"),
		m.row(3, "Conflict Oracle"),
	}, "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.panel(leftWidth).Render(left),
		" ",
		m.panel(rightWidth).Render(right),
	)
}

func (m Model) crtDashboardView() string {
	rows := []string{
		m.theme.PanelTitle.Render("CRT STATUS STACK"),
		fmt.Sprintf("▓ %-10s %s", "GAME", m.data.Game.Name),
		fmt.Sprintf("▓ %-10s %s", "PROFILE", m.data.Profile.Name),
		fmt.Sprintf("▓ %-10s %d/%d", "MODS", m.data.Stats.Enabled, m.data.Stats.Installed),
		fmt.Sprintf("▓ %-10s %d updates, %d conflict", "SIGNAL", m.data.Stats.Updates, m.data.Stats.Conflicts),
		"",
		m.row(0, "Installed Mods"),
		m.row(1, "Search Archives"),
		m.row(2, "Profiles"),
		m.row(3, "Consult Conflict Oracle"),
	}
	return m.panel(m.availableWidth()).Render(strings.Join(rows, "\n"))
}

func (m Model) modsView() string {
	rows := []string{m.theme.PanelTitle.Render("SPELLBOOK: INSTALLED MODS")}
	rows = append(rows, "[E] Enable  [D] Disable  [U] Update  [/] Search")
	for i, mod := range m.data.InstalledMods {
		rows = append(rows, m.modRow(i, mod))
	}
	return m.panel(m.availableWidth()).Render(strings.Join(rows, "\n"))
}

func (m Model) searchView() string {
	rows := []string{m.theme.PanelTitle.Render("ARCHIVE SEARCH")}
	rows = append(rows, "Query: survival mods_")
	for i, mod := range m.data.SearchResults {
		rows = append(rows, m.modRow(i, mod))
	}
	return m.panel(m.availableWidth()).Render(strings.Join(rows, "\n"))
}

func (m Model) profilesView() string {
	rows := []string{m.theme.PanelTitle.Render("PROFILE ROSTER")}
	for i, profile := range m.data.Profiles {
		active := " "
		if profile.Active {
			active = "*"
		}
		line := fmt.Sprintf("%s %-22s %3d mods", active, profile.Name, profile.ModCount)
		rows = append(rows, m.row(i, line))
	}
	return m.panel(min(m.availableWidth(), 54)).Render(strings.Join(rows, "\n"))
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

func (m Model) modRow(index int, mod prototype.Mod) string {
	line := fmt.Sprintf("%-28s %-11s %-16s %7s", mod.Name, mod.Status, mod.Author, mod.Version)
	return m.row(index, line)
}

func (m Model) panel(width int) lipgloss.Style {
	return m.theme.Panel.Width(max(width-m.theme.Panel.GetHorizontalFrameSize(), 1))
}

func (m Model) availableWidth() int {
	if m.width == 0 {
		return defaultContentWidth
	}
	return max(m.width-m.theme.App.GetHorizontalFrameSize(), 40)
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

func statusValue(value int, color lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(fmt.Sprintf("%d", value))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
