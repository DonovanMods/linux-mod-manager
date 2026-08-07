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
	// ScreenHealth renders the health home view (a full-width
	// findings+conflicts table with a detail strip) - one screen among six,
	// no longer special-cased as the context-view host: since #86 generalized
	// contextview.go, ANY screen can host Model.pushContext's pushed content
	// (see contextContent's own doc comment), rendering it over whatever
	// screen pushed it instead of jumping the session to Health. #224 Task 15
	// folded the former standalone ScreenConflicts (slot 6) into this
	// screen's table - see healthTableRows/healthDetailPane's own doc
	// comments - so ScreenHealth occupies slot 6 (digit "6") instead of 7,
	// and there is no separate Conflicts screen anymore.
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
