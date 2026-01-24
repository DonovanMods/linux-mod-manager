package views

import (
	"fmt"

	"lmm/internal/domain"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SwitchProfileMsg is sent to switch to a profile
type SwitchProfileMsg struct {
	Profile *domain.Profile
}

// DeleteProfileMsg is sent to delete a profile
type DeleteProfileMsg struct {
	Profile *domain.Profile
}

// SetDefaultProfileMsg is sent to set default profile
type SetDefaultProfileMsg struct {
	Profile *domain.Profile
}

// ExportProfileMsg is sent to export a profile
type ExportProfileMsg struct {
	Profile *domain.Profile
}

// CreateProfileMsg is sent when a new profile is created
type CreateProfileMsg struct {
	Name string
}

// Profiles is the profile management view
type Profiles struct {
	game          *domain.Game
	profiles      []*domain.Profile
	activeProfile string
	selected      int
	creating      bool
	nameInput     textinput.Model
	width         int
	height        int
}

// NewProfiles creates a new profiles view
func NewProfiles(game *domain.Game, profiles []*domain.Profile, activeProfile string) Profiles {
	ti := textinput.New()
	ti.Placeholder = "Profile name..."
	ti.CharLimit = 50
	ti.Width = 30

	return Profiles{
		game:          game,
		profiles:      profiles,
		activeProfile: activeProfile,
		selected:      0,
		creating:      false,
		nameInput:     ti,
		width:         80,
		height:        24,
	}
}

// Selected returns the currently selected index
func (p Profiles) Selected() int {
	return p.selected
}

// ProfileCount returns the number of profiles
func (p Profiles) ProfileCount() int {
	return len(p.profiles)
}

// IsCreating returns whether we're in create mode
func (p Profiles) IsCreating() bool {
	return p.creating
}

// SelectedProfile returns the currently selected profile
func (p Profiles) SelectedProfile() *domain.Profile {
	if len(p.profiles) == 0 || p.selected >= len(p.profiles) {
		return nil
	}
	return p.profiles[p.selected]
}

// Init implements tea.Model
func (p Profiles) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (p Profiles) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if p.creating {
			return p.handleCreateMode(msg)
		}
		return p.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, nil
	}

	return p, nil
}

func (p Profiles) handleCreateMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		p.creating = false
		p.nameInput.Reset()
		p.nameInput.Blur()
		return p, nil

	case tea.KeyEnter:
		name := p.nameInput.Value()
		if name != "" {
			p.creating = false
			p.nameInput.Reset()
			p.nameInput.Blur()
			return p, func() tea.Msg {
				return CreateProfileMsg{Name: name}
			}
		}
		return p, nil

	default:
		var cmd tea.Cmd
		p.nameInput, cmd = p.nameInput.Update(msg)
		return p, cmd
	}
}

func (p Profiles) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if len(p.profiles) > 0 {
			p.selected--
			if p.selected < 0 {
				p.selected = len(p.profiles) - 1
			}
		}
		return p, nil

	case "down", "j":
		if len(p.profiles) > 0 {
			p.selected++
			if p.selected >= len(p.profiles) {
				p.selected = 0
			}
		}
		return p, nil

	case "enter", " ":
		profile := p.SelectedProfile()
		if profile != nil {
			return p, func() tea.Msg {
				return SwitchProfileMsg{Profile: profile}
			}
		}
		return p, nil

	case "n":
		p.creating = true
		p.nameInput.Focus()
		return p, textinput.Blink

	case "d", "delete":
		profile := p.SelectedProfile()
		if profile != nil && profile.Name != p.activeProfile {
			return p, func() tea.Msg {
				return DeleteProfileMsg{Profile: profile}
			}
		}
		return p, nil

	case "D": // Set as default (shift+d)
		profile := p.SelectedProfile()
		if profile != nil {
			return p, func() tea.Msg {
				return SetDefaultProfileMsg{Profile: profile}
			}
		}
		return p, nil

	case "e":
		profile := p.SelectedProfile()
		if profile != nil {
			return p, func() tea.Msg {
				return ExportProfileMsg{Profile: profile}
			}
		}
		return p, nil

	case "home", "g":
		p.selected = 0
		return p, nil

	case "end", "G":
		if len(p.profiles) > 0 {
			p.selected = len(p.profiles) - 1
		}
		return p, nil
	}

	return p, nil
}

// View implements tea.Model
func (p Profiles) View() string {
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

	activeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("82"))

	defaultStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214"))

	detailStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		PaddingLeft(4)

	// Title
	output := titleStyle.Render("Profiles") + "\n"

	// Game info
	gameName := "No game selected"
	if p.game != nil {
		gameName = p.game.Name
	}
	output += infoStyle.Render(fmt.Sprintf("Game: %s", gameName)) + "\n\n"

	// Create mode
	if p.creating {
		output += "New profile name: " + p.nameInput.View() + "\n\n"
		output += infoStyle.Render("enter: create  esc: cancel")
		return output
	}

	// Empty state
	if len(p.profiles) == 0 {
		output += itemStyle.Render("No profiles configured.") + "\n\n"
		output += infoStyle.Render("Press 'n' to create a new profile.") + "\n"
		return output
	}

	// Profile list
	for i, profile := range p.profiles {
		cursor := "  "
		style := itemStyle

		if i == p.selected {
			cursor = "▸ "
			style = selectedStyle
		}

		// Build status indicators
		status := ""
		if profile.Name == p.activeProfile {
			status += activeStyle.Render(" [active]")
		}
		if profile.IsDefault {
			status += defaultStyle.Render(" [default]")
		}

		line := fmt.Sprintf("%s%s%s", cursor, profile.Name, status)
		output += style.Render(line) + "\n"

		// Show details for selected profile
		if i == p.selected {
			modCount := len(profile.Mods)
			output += detailStyle.Render(fmt.Sprintf("Mods: %d", modCount)) + "\n"
			if profile.LinkMethod != 0 {
				output += detailStyle.Render(fmt.Sprintf("Link method: %s", profile.LinkMethod.String())) + "\n"
			}
			output += "\n"
		}
	}

	// Help
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)
	output += helpStyle.Render("enter: switch  n: new  d: delete  D: set default  e: export")

	return output
}
