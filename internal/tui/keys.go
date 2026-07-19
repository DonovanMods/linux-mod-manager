package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap documents the TUI keyboard contract.
type KeyMap struct {
	Quit          key.Binding
	Help          key.Binding
	NextScreen    key.Binding
	PrevScreen    key.Binding
	Up            key.Binding
	Down          key.Binding
	Search        key.Binding
	SearchScreen  key.Binding
	Dashboard     key.Binding
	InstalledMods key.Binding
	Profiles      key.Binding
	Sources       key.Binding
	Select        key.Binding
	Submit        key.Binding
	Blur          key.Binding
	NextPage      key.Binding
	PrevPage      key.Binding
	CycleSource   key.Binding
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
			key.WithKeys("/"),
			key.WithHelp("/", "search"),
		),
		SearchScreen: key.NewBinding(
			key.WithKeys("3"),
			key.WithHelp("3", "search screen"),
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
		Sources: key.NewBinding(
			key.WithKeys("5"),
			key.WithHelp("5", "sources"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "search"),
		),
		Blur: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel input"),
		),
		NextPage: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "next page"),
		),
		PrevPage: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "prev page"),
		),
		CycleSource: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "cycle source"),
		),
	}
}
