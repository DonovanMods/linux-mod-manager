package views

import (
	"fmt"

	"lmm/internal/domain"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ToggleModMsg is sent to enable/disable a mod
type ToggleModMsg struct {
	Mod domain.InstalledMod
}

// UninstallModMsg is sent to uninstall a mod
type UninstallModMsg struct {
	Mod domain.InstalledMod
}

// ReorderModMsg is sent to change load order
type ReorderModMsg struct {
	FromIndex int
	ToIndex   int
}

// Installed is the installed mods view
type Installed struct {
	game     *domain.Game
	profile  *domain.Profile
	mods     []domain.InstalledMod
	selected int
	width    int
	height   int
}

// NewInstalled creates a new installed mods view
func NewInstalled(game *domain.Game, profile *domain.Profile, mods []domain.InstalledMod) Installed {
	return Installed{
		game:     game,
		profile:  profile,
		mods:     mods,
		selected: 0,
		width:    80,
		height:   24,
	}
}

// Selected returns the currently selected index
func (m Installed) Selected() int {
	return m.selected
}

// ModCount returns the number of installed mods
func (m Installed) ModCount() int {
	return len(m.mods)
}

// SelectedMod returns the currently selected mod
func (m Installed) SelectedMod() *domain.InstalledMod {
	if len(m.mods) == 0 || m.selected >= len(m.mods) {
		return nil
	}
	return &m.mods[m.selected]
}

// Init implements tea.Model
func (m Installed) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m Installed) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	}

	return m, nil
}

func (m Installed) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.mods) == 0 {
		return m, nil
	}

	switch msg.String() {
	case "up", "k":
		m.selected--
		if m.selected < 0 {
			m.selected = len(m.mods) - 1
		}
		return m, nil

	case "down", "j":
		m.selected++
		if m.selected >= len(m.mods) {
			m.selected = 0
		}
		return m, nil

	case " ": // Toggle enabled
		mod := m.SelectedMod()
		if mod != nil {
			return m, func() tea.Msg {
				return ToggleModMsg{Mod: *mod}
			}
		}
		return m, nil

	case "d", "delete": // Uninstall
		mod := m.SelectedMod()
		if mod != nil {
			return m, func() tea.Msg {
				return UninstallModMsg{Mod: *mod}
			}
		}
		return m, nil

	case "K": // Move up in load order (shift+k)
		if m.selected > 0 {
			return m, func() tea.Msg {
				return ReorderModMsg{
					FromIndex: m.selected,
					ToIndex:   m.selected - 1,
				}
			}
		}
		return m, nil

	case "J": // Move down in load order (shift+j)
		if m.selected < len(m.mods)-1 {
			return m, func() tea.Msg {
				return ReorderModMsg{
					FromIndex: m.selected,
					ToIndex:   m.selected + 1,
				}
			}
		}
		return m, nil

	case "home", "g":
		m.selected = 0
		return m, nil

	case "end", "G":
		m.selected = len(m.mods) - 1
		return m, nil
	}

	return m, nil
}

// View implements tea.Model
func (m Installed) View() string {
	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("69")).
		MarginBottom(1)

	infoStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	itemStyle := lipgloss.NewStyle().
		PaddingLeft(2)

	selectedStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		Foreground(lipgloss.Color("205")).
		Bold(true)

	disabledStyle := lipgloss.NewStyle().
		PaddingLeft(2).
		Foreground(lipgloss.Color("241"))

	detailStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		PaddingLeft(4)

	// Title
	output := titleStyle.Render("Installed Mods") + "\n"

	// Game and profile info
	gameName := "No game"
	profileName := "No profile"
	if m.game != nil {
		gameName = m.game.Name
	}
	if m.profile != nil {
		profileName = m.profile.Name
	}
	output += infoStyle.Render(fmt.Sprintf("Game: %s  Profile: %s", gameName, profileName)) + "\n\n"

	// Empty state
	if len(m.mods) == 0 {
		output += itemStyle.Render("No mods installed in this profile.") + "\n\n"
		output += infoStyle.Render("Browse mods with [2] or search with 'lmm search <query>'") + "\n"
		return output
	}

	// Mod list header
	output += infoStyle.Render(fmt.Sprintf("Load Order (%d mods):", len(m.mods))) + "\n\n"

	// Mod list
	for i, mod := range m.mods {
		cursor := "  "
		style := itemStyle

		if i == m.selected {
			cursor = "▸ "
			style = selectedStyle
		} else if !mod.Enabled {
			style = disabledStyle
		}

		// Status indicator
		status := "[✓]"
		if !mod.Enabled {
			status = "[ ]"
		}

		line := fmt.Sprintf("%s%s %s v%s", cursor, status, mod.Name, mod.Version)
		output += style.Render(line) + "\n"

		// Show details for selected mod
		if i == m.selected {
			if mod.Author != "" {
				output += detailStyle.Render(fmt.Sprintf("by %s", mod.Author)) + "\n"
			}
			output += detailStyle.Render(fmt.Sprintf("Source: %s  ID: %s", mod.SourceID, mod.ID)) + "\n"
			output += detailStyle.Render(fmt.Sprintf("Installed: %s", mod.InstalledAt.Format("2006-01-02"))) + "\n"

			// Update policy
			policyStr := "notify"
			switch mod.UpdatePolicy {
			case domain.UpdateAuto:
				policyStr = "auto"
			case domain.UpdatePinned:
				policyStr = "pinned"
			}
			output += detailStyle.Render(fmt.Sprintf("Updates: %s", policyStr)) + "\n"
			output += "\n"
		}
	}

	// Help
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)
	output += helpStyle.Render("↑/↓: navigate  space: toggle  d: uninstall  K/J: reorder")

	return output
}
