// Package bepinex: this file holds the FILE ROUTER (#358 (b)) - the one
// rule that says a deployable file is not ordinary mod content.
//
// It moved here from internal/core's isBepInExConfigMember in U3 (#413).
// Core used to test the path inline in Installer.Install's per-file loop and
// call a bespoke seedBepInExConfig; it now asks the game's adapter, gets
// adapter.RouteCopyOnce back, and runs the copy-once write it already owned
// for profile overrides. Same semantics, one mechanism.
package bepinex

import (
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// configPrefix is the one directory under BepInEx/ whose contents are the
// USER's after the first deploy.
const configPrefix = dirName + "/config/"

// RouteFile reports how one cache-entry-relative file of a BepInEx mod is
// deployed.
//
// Everything is adapter.RouteLink - the linker's, exactly as before - except
// BepInEx/config/**, which is adapter.RouteCopyOnce.
//
// BepInEx generates those files on first run and users hand-edit them
// afterwards; a mod that ships one is seeding a DEFAULT. Deploying it as a
// symlink like every other member would make the user's edit either fail (a
// read-only cache) or silently write back INTO the cache, where the next
// re-download overwrites it and every other profile sharing the entry
// inherits it. A hardlink does the same through a different door. So they
// take profile-config-override semantics instead: a real file, copied on
// first deploy, never overwriting what is already there, and never entered
// into deployed_files - which is exactly what adapter.RouteCopyOnce means.
//
// The test is the PATH, and the path is already canonical: NormalizeArchive
// folds BepInEx's own directory spellings (canonicalRoot), so a shape-B
// archive rooted at `Config/` reaches the cache as `BepInEx/config/` and is
// matched here. rel arrives slash-separated by contract (adapter.FileRouter),
// so there is no separator to convert.
func (*Adapter) RouteFile(_ *domain.Game, rel string) adapter.FileRoute {
	if strings.HasPrefix(rel, configPrefix) {
		return adapter.RouteCopyOnce
	}
	return adapter.RouteLink
}
