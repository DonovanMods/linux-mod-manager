package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap documents the prototype TUI keyboard contract.
type KeyMap struct {
	Quit          key.Binding
	Help          key.Binding
	NextScreen    key.Binding
	PrevScreen    key.Binding
	Up            key.Binding
	Down          key.Binding
	Search        key.Binding
	Dashboard     key.Binding
	InstalledMods key.Binding
	Profiles      key.Binding
	Select        key.Binding
}

// DefaultKeyMap returns the shared key bindings shown in help and used by tests.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		NextScreen: key.NewBinding(
			key.WithKeys("tab", "right", "l"),
			key.WithHelp("tab/l", "next screen"),
		),
		PrevScreen: key.NewBinding(
			key.WithKeys("shift+tab", "left", "h"),
			key.WithHelp("shift+tab/h", "previous screen"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Search: key.NewBinding(
			key.WithKeys("/", "3"),
			key.WithHelp("/", "search"),
		),
		Dashboard: key.NewBinding(
			key.WithKeys("1"),
			key.WithHelp("1", "dashboard"),
		),
		InstalledMods: key.NewBinding(
			key.WithKeys("2"),
			key.WithHelp("2", "installed mods"),
		),
		Profiles: key.NewBinding(
			key.WithKeys("4"),
			key.WithHelp("4", "profiles"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open"),
		),
	}
}
