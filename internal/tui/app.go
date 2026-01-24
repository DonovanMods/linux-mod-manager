package tui

import (
	"fmt"

	"lmm/internal/core"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ViewType represents different screens in the TUI
type ViewType int

const (
	ViewGameSelect ViewType = iota
	ViewModBrowser
	ViewInstalledMods
	ViewProfiles
	ViewSettings
)

// NavigateMsg is sent to change views
type NavigateMsg struct {
	View ViewType
}

// ErrorMsg is sent when an error occurs
type ErrorMsg struct {
	Err error
}

// App is the main TUI application model
type App struct {
	service     *core.Service
	currentView ViewType
	width       int
	height      int
	err         error

	// Sub-models for each view
	gameSelect    tea.Model
	modBrowser    tea.Model
	installedMods tea.Model
	profiles      tea.Model
	settings      tea.Model
}

// NewApp creates a new TUI application
func NewApp(service *core.Service) App {
	return App{
		service:     service,
		currentView: ViewGameSelect,
		width:       80,
		height:      24,
	}
}

// CurrentView returns the current view type
func (a App) CurrentView() ViewType {
	return a.currentView
}

// Init implements tea.Model
func (a App) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return a.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case NavigateMsg:
		a.currentView = msg.View
		return a, nil

	case ErrorMsg:
		a.err = msg.Err
		return a, nil
	}

	// Delegate to current view's model
	return a.updateCurrentView(msg)
}

func (a App) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global keybindings
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit

	case "?":
		// TODO: Show help
		return a, nil

	case "1":
		a.currentView = ViewGameSelect
		return a, nil

	case "2":
		a.currentView = ViewModBrowser
		return a, nil

	case "3":
		a.currentView = ViewInstalledMods
		return a, nil

	case "4":
		a.currentView = ViewProfiles
		return a, nil

	case "5":
		a.currentView = ViewSettings
		return a, nil
	}

	// Delegate to current view
	return a.updateCurrentView(msg)
}

func (a App) updateCurrentView(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch a.currentView {
	case ViewGameSelect:
		if a.gameSelect != nil {
			a.gameSelect, cmd = a.gameSelect.Update(msg)
		}
	case ViewModBrowser:
		if a.modBrowser != nil {
			a.modBrowser, cmd = a.modBrowser.Update(msg)
		}
	case ViewInstalledMods:
		if a.installedMods != nil {
			a.installedMods, cmd = a.installedMods.Update(msg)
		}
	case ViewProfiles:
		if a.profiles != nil {
			a.profiles, cmd = a.profiles.Update(msg)
		}
	case ViewSettings:
		if a.settings != nil {
			a.settings, cmd = a.settings.Update(msg)
		}
	}

	return a, cmd
}

// View implements tea.Model
func (a App) View() string {
	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		MarginBottom(1)

	tabStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	activeTabStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	// Header
	header := titleStyle.Render("lmm - Linux Mod Manager")

	// Tab bar
	tabs := []string{"[1]Games", "[2]Browse", "[3]Installed", "[4]Profiles", "[5]Settings"}
	tabBar := ""
	for i, tab := range tabs {
		if ViewType(i) == a.currentView {
			tabBar += activeTabStyle.Render(tab) + "  "
		} else {
			tabBar += tabStyle.Render(tab) + "  "
		}
	}

	// Content
	content := a.renderCurrentView()

	// Error display
	if a.err != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		content = errStyle.Render(fmt.Sprintf("Error: %v", a.err))
	}

	// Footer
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)
	footer := footerStyle.Render("q: quit  ?: help")

	return fmt.Sprintf("%s\n%s\n\n%s\n\n%s", header, tabBar, content, footer)
}

func (a App) renderCurrentView() string {
	switch a.currentView {
	case ViewGameSelect:
		if a.gameSelect != nil {
			return a.gameSelect.View()
		}
		return a.renderGameSelectPlaceholder()

	case ViewModBrowser:
		if a.modBrowser != nil {
			return a.modBrowser.View()
		}
		return "Mod Browser\n\nSelect a game first to browse mods."

	case ViewInstalledMods:
		if a.installedMods != nil {
			return a.installedMods.View()
		}
		return "Installed Mods\n\nNo mods installed yet."

	case ViewProfiles:
		if a.profiles != nil {
			return a.profiles.View()
		}
		return "Profiles\n\nNo profiles configured."

	case ViewSettings:
		if a.settings != nil {
			return a.settings.View()
		}
		return "Settings\n\nConfiguration options will appear here."

	default:
		return "Unknown view"
	}
}

func (a App) renderGameSelectPlaceholder() string {
	if a.service == nil {
		return "Game Selection\n\nNo games configured. Add a game with:\n  lmm config add-game <id> --name \"Game Name\" --path /path/to/game"
	}

	games := a.service.ListGames()
	if len(games) == 0 {
		return "Game Selection\n\nNo games configured. Add a game with:\n  lmm config add-game <id> --name \"Game Name\" --path /path/to/game"
	}

	result := "Game Selection\n\n"
	for i, game := range games {
		result += fmt.Sprintf("  %d. %s (%s)\n", i+1, game.Name, game.ID)
	}
	return result
}

// Run starts the TUI application
func Run(service *core.Service) error {
	app := NewApp(service)
	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
