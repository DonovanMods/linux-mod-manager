package views

import (
	"fmt"

	"lmm/internal/domain"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SettingsData holds the current settings values
type SettingsData struct {
	LinkMethod  domain.LinkMethod
	Keybindings string
}

// SettingsChangedMsg is sent when settings are modified
type SettingsChangedMsg struct {
	Settings SettingsData
}

// settingItem represents a single setting
type settingItem struct {
	name        string
	description string
	options     []string
	current     int
}

// Settings is the settings view
type Settings struct {
	settings SettingsData
	items    []settingItem
	selected int
	width    int
	height   int
}

// NewSettings creates a new settings view
func NewSettings(settings SettingsData) Settings {
	// Convert current settings to item indices
	linkMethodIdx := int(settings.LinkMethod)

	keybindingsIdx := 0
	if settings.Keybindings == "standard" {
		keybindingsIdx = 1
	}

	items := []settingItem{
		{
			name:        "Link Method",
			description: "How mods are deployed to game directories",
			options:     []string{"symlink", "hardlink", "copy"},
			current:     linkMethodIdx,
		},
		{
			name:        "Keybindings",
			description: "Keyboard navigation style",
			options:     []string{"vim", "standard"},
			current:     keybindingsIdx,
		},
	}

	return Settings{
		settings: settings,
		items:    items,
		selected: 0,
		width:    80,
		height:   24,
	}
}

// Selected returns the currently selected setting index
func (s Settings) Selected() int {
	return s.selected
}

// CurrentSettings returns the current settings values
func (s Settings) CurrentSettings() SettingsData {
	return s.settings
}

// Init implements tea.Model
func (s Settings) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (s Settings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return s.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		s.width = msg.Width
		s.height = msg.Height
		return s, nil
	}

	return s, nil
}

func (s Settings) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		s.selected--
		if s.selected < 0 {
			s.selected = len(s.items) - 1
		}
		return s, nil

	case "down", "j":
		s.selected++
		if s.selected >= len(s.items) {
			s.selected = 0
		}
		return s, nil

	case "enter", " ", "right", "l":
		return s.cycleForward()

	case "left", "h":
		return s.cycleBackward()
	}

	return s, nil
}

func (s Settings) cycleForward() (tea.Model, tea.Cmd) {
	item := &s.items[s.selected]
	item.current = (item.current + 1) % len(item.options)
	s.applySettings()
	return s, s.emitChange()
}

func (s Settings) cycleBackward() (tea.Model, tea.Cmd) {
	item := &s.items[s.selected]
	item.current--
	if item.current < 0 {
		item.current = len(item.options) - 1
	}
	s.applySettings()
	return s, s.emitChange()
}

func (s *Settings) applySettings() {
	// Link method
	s.settings.LinkMethod = domain.LinkMethod(s.items[0].current)

	// Keybindings
	s.settings.Keybindings = s.items[1].options[s.items[1].current]
}

func (s Settings) emitChange() tea.Cmd {
	return func() tea.Msg {
		return SettingsChangedMsg{Settings: s.settings}
	}
}

// View implements tea.Model
func (s Settings) View() string {
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

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		PaddingLeft(4)

	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("82"))

	optionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	selectedOptionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true)

	// Title
	output := titleStyle.Render("Settings") + "\n\n"

	// Settings list
	for i, item := range s.items {
		cursor := "  "
		style := itemStyle

		if i == s.selected {
			cursor = "▸ "
			style = selectedStyle
		}

		// Setting name and current value
		currentValue := item.options[item.current]
		line := fmt.Sprintf("%s%s: %s", cursor, item.name, valueStyle.Render(currentValue))
		output += style.Render(line) + "\n"

		// Description
		output += descStyle.Render(item.description) + "\n"

		// Options (show for selected item)
		if i == s.selected {
			optionsLine := "    Options: "
			for j, opt := range item.options {
				if j == item.current {
					optionsLine += selectedOptionStyle.Render("[" + opt + "]")
				} else {
					optionsLine += optionStyle.Render(" " + opt + " ")
				}
			}
			output += optionsLine + "\n"
		}

		output += "\n"
	}

	// Help
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1)
	output += helpStyle.Render("↑/↓: navigate  ←/→ or enter: change value")

	return output
}
