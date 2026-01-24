package views

import (
	"fmt"

	"lmm/internal/domain"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// GameSelectedMsg is sent when a game is selected
type GameSelectedMsg struct {
	Game *domain.Game
}

// GameSelect is the game selection view model
type GameSelect struct {
	games    []*domain.Game
	selected int
	width    int
	height   int
}

// NewGameSelect creates a new game selection view
func NewGameSelect(games []*domain.Game) GameSelect {
	return GameSelect{
		games:    games,
		selected: 0,
		width:    80,
		height:   24,
	}
}

// Selected returns the currently selected index
func (g GameSelect) Selected() int {
	return g.selected
}

// SelectedGame returns the currently selected game
func (g GameSelect) SelectedGame() *domain.Game {
	if len(g.games) == 0 || g.selected >= len(g.games) {
		return nil
	}
	return g.games[g.selected]
}

// Init implements tea.Model
func (g GameSelect) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (g GameSelect) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return g.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		g.width = msg.Width
		g.height = msg.Height
		return g, nil
	}

	return g, nil
}

func (g GameSelect) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(g.games) == 0 {
		return g, nil
	}

	switch msg.String() {
	case "up", "k":
		g.selected--
		if g.selected < 0 {
			g.selected = len(g.games) - 1
		}
		return g, nil

	case "down", "j":
		g.selected++
		if g.selected >= len(g.games) {
			g.selected = 0
		}
		return g, nil

	case "enter", " ":
		game := g.SelectedGame()
		if game != nil {
			return g, func() tea.Msg {
				return GameSelectedMsg{Game: game}
			}
		}
		return g, nil

	case "home", "g":
		g.selected = 0
		return g, nil

	case "end", "G":
		g.selected = len(g.games) - 1
		return g, nil
	}

	return g, nil
}

// View implements tea.Model
func (g GameSelect) View() string {
	if len(g.games) == 0 {
		return g.renderEmpty()
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("69")).
		MarginBottom(1)

	itemStyle := lipgloss.NewStyle().
		PaddingLeft(2)

	selectedStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		Foreground(lipgloss.Color("205")).
		Bold(true)

	detailStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		PaddingLeft(4)

	// Title
	output := titleStyle.Render("Select a Game") + "\n\n"

	// Game list
	for i, game := range g.games {
		cursor := "  "
		style := itemStyle

		if i == g.selected {
			cursor = "▸ "
			style = selectedStyle
		}

		line := fmt.Sprintf("%s%s", cursor, game.Name)
		output += style.Render(line) + "\n"

		// Show details for selected game
		if i == g.selected {
			output += detailStyle.Render(fmt.Sprintf("ID: %s", game.ID)) + "\n"
			if game.InstallPath != "" {
				output += detailStyle.Render(fmt.Sprintf("Path: %s", game.InstallPath)) + "\n"
			}
			if game.ModPath != "" {
				output += detailStyle.Render(fmt.Sprintf("Mods: %s", game.ModPath)) + "\n"
			}
			output += "\n"
		}
	}

	// Help
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)
	output += helpStyle.Render("↑/↓: navigate  enter: select  a: add game")

	return output
}

func (g GameSelect) renderEmpty() string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	return style.Render(`No games configured.

Add a game with:
  lmm config add-game <id> --name "Game Name" --path /path/to/game

Example:
  lmm config add-game skyrim-se \
    --name "Skyrim Special Edition" \
    --path "/home/user/.steam/steam/steamapps/common/Skyrim Special Edition" \
    --mod-path "/home/user/.steam/steam/steamapps/common/Skyrim Special Edition/Data"
`)
}
