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
	ConfirmAction key.Binding
	CancelAction  key.Binding
	// ToggleEnable, Uninstall, and Deploy are Phase 5a's Installed
	// Mods/Dashboard mutation bindings (see mutations.go). Profile switch
	// deliberately has no binding of its own here - it reuses Select
	// ("enter"), dispatched by screen in updateKey.
	ToggleEnable key.Binding
	Uninstall    key.Binding
	Deploy       key.Binding
	// Install is Phase 5b's install-from-search binding (see mutations.go's
	// installSelectedSearchResult): it only fires on ScreenSearch with the
	// input blurred and a result selected - a focused input swallows "i" as
	// a typed character (see updateKey's focused-input branch, which runs
	// before this ever reaches the outer switch).
	Install key.Binding
	// CheckUpdates is Phase 5b's check/apply-updates binding (see
	// mutations.go's checkForUpdates): fires on ScreenDashboard and
	// ScreenInstalledMods.
	CheckUpdates key.Binding
	// Files is Task 4's deployed-files-overlay binding (see mutations.go's
	// showDeployedFiles): fires on ScreenInstalledMods with a mod selected.
	// "f" is overloaded - overlay.go's updateOverlayKey also matches a plain
	// "f" to CLOSE the overlay once open, so this key doubles as an open/
	// close toggle.
	Files key.Binding
	// Policy is Task 5's update-policy picker binding (see mutations.go's
	// editSelectedModPolicy): fires on ScreenInstalledMods with a mod
	// selected, opening a notify/auto/pin picker whose selection dispatches
	// immediately (no separate confirm modal).
	Policy key.Binding
	// CreateProfile and DeleteProfile are Task 6's Profiles-screen bindings
	// (see mutations.go's createProfilePrompt/deleteSelectedProfile).
	// CreateProfile opens the "new profile" input modal, whose submit
	// dispatches immediately (no separate confirm modal - mirroring Policy's
	// own "the choice IS the confirmation" shape). DeleteProfile opens the
	// standard y/n confirmation modal for a non-active row, or refuses
	// synchronously on the status line for the active one.
	CreateProfile key.Binding
	DeleteProfile key.Binding
	// Purge is Task 7's Dashboard/Installed-Mods purge-behind-confirmation
	// binding (see mutations.go's purgeProfilePrompt): undeploys every mod
	// currently installed in the active profile, behind the standard y/n
	// confirmation modal - the TUI equivalent of `lmm purge`. Capital "X"
	// (distinct from lowercase "x"/Uninstall) since purge acts on the WHOLE
	// profile, not the selected mod.
	Purge key.Binding
	// GameSwitch is Task 8's in-TUI game switcher binding (see mutations.go's
	// openGameSwitcher): fires on ANY screen (unlike every other mutation
	// binding above, which is scoped to specific screens), opening a picker
	// of every configured game with the active one marked - picking one
	// dispatches immediately (no separate confirm modal, mirroring Policy's
	// own "the choice IS the confirmation" shape).
	GameSwitch key.Binding
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
		ConfirmAction: key.NewBinding(
			key.WithKeys("y", "enter"),
			key.WithHelp("y/enter", "confirm"),
		),
		CancelAction: key.NewBinding(
			key.WithKeys("n", "esc"),
			key.WithHelp("n/esc", "cancel"),
		),
		ToggleEnable: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "toggle enable/disable"),
		),
		Uninstall: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "uninstall"),
		),
		Deploy: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "deploy profile"),
		),
		Install: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "install selected result"),
		),
		CheckUpdates: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "check for updates"),
		),
		Files: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "files"),
		),
		Policy: key.NewBinding(
			key.WithKeys("P"),
			key.WithHelp("P", "policy"),
		),
		CreateProfile: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "new profile"),
		),
		DeleteProfile: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete profile"),
		),
		Purge: key.NewBinding(
			key.WithKeys("X"),
			key.WithHelp("X", "purge"),
		),
		GameSwitch: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "game"),
		),
	}
}
