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
	// ScreenHealth is #224 Task 9's context-view host: the health home
	// content (a full-width findings+conflicts table with a detail strip)
	// when nothing is pushed, or whatever contextContent Model.pushContext
	// last pushed (see contextview.go) - the landing zone a future
	// mod-details story (#86) will reuse as its second implementation. #224
	// Task 15 folded the former standalone ScreenConflicts (slot 6) into
	// this screen's table - see healthTableRows/healthDetailPane's own doc
	// comments - so ScreenHealth now occupies slot 6 (digit "6") instead of
	// 7, and there is no separate Conflicts screen anymore.
	ScreenHealth
)

var screens = []Screen{
	ScreenDashboard,
	ScreenInstalledMods,
	ScreenSearch,
	ScreenProfiles,
	ScreenSources,
	ScreenHealth,
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
	case ScreenHealth:
		return "Health"
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
