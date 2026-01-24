package views

import (
	"fmt"

	"lmm/internal/domain"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SearchResultsMsg contains search results
type SearchResultsMsg struct {
	Mods []domain.Mod
}

// SearchErrorMsg indicates a search error
type SearchErrorMsg struct {
	Err error
}

// InstallModMsg is sent when user wants to install a mod
type InstallModMsg struct {
	Mod domain.Mod
}

// ModBrowser is the mod browsing/search view
type ModBrowser struct {
	game          *domain.Game
	searchInput   textinput.Model
	searchFocused bool
	results       []domain.Mod
	selected      int
	loading       bool
	err           error
	width         int
	height        int
}

// NewModBrowser creates a new mod browser view
func NewModBrowser(game *domain.Game) ModBrowser {
	ti := textinput.New()
	ti.Placeholder = "Search mods..."
	ti.Focus()
	ti.CharLimit = 100
	ti.Width = 40

	return ModBrowser{
		game:          game,
		searchInput:   ti,
		searchFocused: true,
		results:       nil,
		selected:      0,
		width:         80,
		height:        24,
	}
}

// SearchQuery returns the current search query
func (m ModBrowser) SearchQuery() string {
	return m.searchInput.Value()
}

// IsSearchFocused returns whether the search input is focused
func (m ModBrowser) IsSearchFocused() bool {
	return m.searchFocused
}

// ResultCount returns the number of search results
func (m ModBrowser) ResultCount() int {
	return len(m.results)
}

// Selected returns the currently selected result index
func (m ModBrowser) Selected() int {
	return m.selected
}

// SelectedMod returns the currently selected mod
func (m ModBrowser) SelectedMod() *domain.Mod {
	if len(m.results) == 0 || m.selected >= len(m.results) {
		return nil
	}
	return &m.results[m.selected]
}

// Init implements tea.Model
func (m ModBrowser) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model
func (m ModBrowser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case SearchResultsMsg:
		m.results = msg.Mods
		m.loading = false
		m.selected = 0
		return m, nil

	case SearchErrorMsg:
		m.err = msg.Err
		m.loading = false
		return m, nil
	}

	// Update text input if focused
	if m.searchFocused {
		m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m ModBrowser) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle search input when focused
	if m.searchFocused {
		switch msg.Type {
		case tea.KeyEsc:
			m.searchFocused = false
			m.searchInput.Blur()
			return m, nil

		case tea.KeyEnter:
			// Trigger search (would be handled by parent)
			m.loading = true
			return m, nil

		default:
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(msg)
			return m, cmd
		}
	}

	// Handle result navigation when not in search
	switch msg.String() {
	case "/":
		m.searchFocused = true
		m.searchInput.Focus()
		return m, nil

	case "up", "k":
		if len(m.results) > 0 {
			m.selected--
			if m.selected < 0 {
				m.selected = len(m.results) - 1
			}
		}
		return m, nil

	case "down", "j":
		if len(m.results) > 0 {
			m.selected++
			if m.selected >= len(m.results) {
				m.selected = 0
			}
		}
		return m, nil

	case "enter", " ":
		mod := m.SelectedMod()
		if mod != nil {
			return m, func() tea.Msg {
				return InstallModMsg{Mod: *mod}
			}
		}
		return m, nil

	case "i":
		// Info/details for selected mod
		return m, nil
	}

	return m, nil
}

// View implements tea.Model
func (m ModBrowser) View() string {
	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("69")).
		MarginBottom(1)

	gameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	itemStyle := lipgloss.NewStyle().
		PaddingLeft(2)

	selectedStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		Foreground(lipgloss.Color("205")).
		Bold(true)

	detailStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		PaddingLeft(4)

	loadingStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214"))

	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("196"))

	// Title and game info
	gameName := "No game selected"
	if m.game != nil {
		gameName = m.game.Name
	}

	output := titleStyle.Render("Browse Mods") + "\n"
	output += gameStyle.Render(fmt.Sprintf("Game: %s", gameName)) + "\n\n"

	// Search input
	searchLabel := "Search: "
	if m.searchFocused {
		searchLabel = "Search (esc to exit): "
	}
	output += searchLabel + m.searchInput.View() + "\n\n"

	// Loading indicator
	if m.loading {
		output += loadingStyle.Render("Searching...") + "\n"
		return output
	}

	// Error display
	if m.err != nil {
		output += errorStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n"
		return output
	}

	// Results
	if len(m.results) == 0 {
		if m.SearchQuery() != "" {
			output += itemStyle.Render("No mods found.") + "\n"
		} else {
			output += itemStyle.Render("Enter a search term and press Enter.") + "\n"
		}
	} else {
		output += fmt.Sprintf("Found %d mods:\n\n", len(m.results))

		for i, mod := range m.results {
			cursor := "  "
			style := itemStyle

			if i == m.selected {
				cursor = "▸ "
				style = selectedStyle
			}

			line := fmt.Sprintf("%s%s", cursor, mod.Name)
			output += style.Render(line) + "\n"

			// Show details for selected mod
			if i == m.selected {
				if mod.Author != "" {
					output += detailStyle.Render(fmt.Sprintf("by %s", mod.Author)) + "\n"
				}
				if mod.Summary != "" {
					output += detailStyle.Render(mod.Summary) + "\n"
				}
				output += detailStyle.Render(fmt.Sprintf("Downloads: %d  Endorsements: %d", mod.Downloads, mod.Endorsements)) + "\n"
				output += "\n"
			}
		}
	}

	// Help
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)

	if m.searchFocused {
		output += helpStyle.Render("enter: search  esc: exit search")
	} else {
		output += helpStyle.Render("/: search  ↑/↓: navigate  enter: install  i: details")
	}

	return output
}
