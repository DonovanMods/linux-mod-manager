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
//
// The lookup is the cached steam.InstallAppIndex, one per Service:
// GameStatus asks it for every Steam game on every read, which is the web
// UI's hot path, and an uncached answer scans every app manifest.
//
// It also registers core's battlenet launch method (#545), which the
// launch registry runs after the built-in command and steam methods, and
// the machine's GameProbe (#587) - how lmm tells a game it launched, or
// any other process under the game's install path, is still running.
//
// And it lists the machine's Steam libraries for core's prefix resolution
// (#605): a game's Proton prefix is Steam's compatdata for its app id, in
// any library - the same libraryfolders.vdf this layer already reads.
func RegisterLaunchers(svc *core.Service) {
	idx := &steam.InstallAppIndex{}
	svc.SetLauncher(core.NewLauncher(func(installPath string) (core.SteamApp, bool) {
		app, ok := idx.Lookup(installPath)
		return core.SteamApp{AppID: app.AppID, Flatpak: app.Flatpak}, ok
	}))
	svc.RegisterLaunchMethod(core.BattleNetLaunchMethod())
	svc.SetGameProbe(core.NewGameProbe())
	svc.SetSteamLibraries(steam.AllLibraryPaths)
}
