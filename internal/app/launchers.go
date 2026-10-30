package app

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// RegisterLaunchers gives the Service its Launcher (#501) - exactly what
// Open registers, and the call Open makes. Like RegisterAdapters, it is
// exported for a frontend's tests that build a Service without Open.
//
// This layer exists in the launch path for one reason: finding a game's
// Steam app id from its install path reads Steam's own library files
// (steam.AppIDForInstallPath), and internal/core may not import a concrete
// source. Everything else about a launch is core's.
func RegisterLaunchers(svc *core.Service) {
	svc.SetLauncher(core.NewLauncher(steamAppForInstallPath))
}

// steamAppForInstallPath is steam.AppIDForInstallPath in core's terms.
func steamAppForInstallPath(installPath string) (core.SteamApp, bool) {
	app, ok := steam.AppIDForInstallPath(installPath)
	return core.SteamApp{AppID: app.AppID, Flatpak: app.Flatpak}, ok
}
