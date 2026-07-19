package tui

import "fmt"

// Screen identifies a top-level TUI view.
type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenInstalledMods
	ScreenSearch
	ScreenProfiles
	ScreenSources
)

var screens = []Screen{
	ScreenDashboard,
	ScreenInstalledMods,
	ScreenSearch,
	ScreenProfiles,
	ScreenSources,
}

// String returns a human-readable screen name.
func (s Screen) String() string {
	switch s {
	case ScreenDashboard:
		return "Dashboard"
	case ScreenInstalledMods:
		return "Installed Mods"
	case ScreenSearch:
		return "Search"
	case ScreenProfiles:
		return "Profiles"
	case ScreenSources:
		return "Sources"
	default:
		return fmt.Sprintf("Screen(%d)", s)
	}
}

func screenAt(index int) Screen {
	if index < 0 {
		return screens[0]
	}
	if index >= len(screens) {
		return screens[len(screens)-1]
	}
	return screens[index]
}
