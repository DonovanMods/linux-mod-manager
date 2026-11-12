package bethesda

import (
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Sources for the game-root table (read for facts only):
//
//	Script extenders: each one's readme says its .dll and .exe files go
//	in the game directory, not in Data - skse64_readme.txt and
//	skse_readme.txt (skse.silverlock.org: "Delete the .dll and .exe files
//	starting with skse64_"), f4se_readme.txt and sfse_readme.txt
//	(github.com/ianpatt/f4se, /sfse: "files starting with sfse_"),
//	f4sevr_readme.txt (github.com/Neanka/f4sevr), and SKSEVR's build
//	targets sksevr_loader / sksevr_1_4_15 (github.com/Odie/sksevr-mirror).
//	Vortex's script-extender installer gives such a mod the "dinput" type.
//	ENB and dinput8: Vortex extensions/modtype-enb (a mod with
//	enbseries.ini, deployed whole to the game directory) and
//	extensions/modtype-dinput (the same for dinput8.dll); d3d11.dll and
//	enbseries/ are the ENB files the collections design names (§4).
//	Mod types: Vortex's default type deploys to the game's queryModPath,
//	"Data" for these games (extensions/games/game-skyrimse).
//
// Left out because no readable source confirmed them: d3dcompiler_46e.dll
// and enblocal.ini as ENB files on their own (enbdev.com refuses scripted
// readers); an ENB preset's enbseries.ini still carries them to the root.

// extenderPrefixes are the script extenders' file-name prefixes; a top-level
// .dll or .exe that starts with one belongs at the game root.
var extenderPrefixes = []string{"skse64_", "skse_", "sksevr_", "f4se_", "f4sevr_", "sfse_"}

// wholeModMarkers are top-level files that make the WHOLE mod game-root
// content, as Vortex's "enb" and "dinput" mod types do.
var wholeModMarkers = []string{"enbseries.ini", "dinput8.dll"}

// rootFiles are top-level files that belong at the game root on their own.
var rootFiles = []string{"d3d11.dll"}

// rootDirs are top-level folders whose whole subtree belongs at the game
// root.
var rootDirs = []string{"enbseries"}

// isRootFile reports whether a top-level file name (no slash) is game-root
// content on its own.
func isRootFile(name string) bool {
	lower := strings.ToLower(name)
	if containsFold(rootFiles, lower) || containsFold(wholeModMarkers, lower) {
		return true
	}
	ext := path.Ext(lower)
	if ext != ".dll" && ext != ".exe" {
		return false
	}
	for _, p := range extenderPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// isRootDir reports whether a top-level folder name is game-root content.
func isRootDir(name string) bool { return containsFold(rootDirs, name) }

// containsFold reports whether list holds s, ignoring case.
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// RouteRoots routes one mod's files: the whole mod to the game root when it
// carries a whole-mod marker at its top level, otherwise each top-level
// script-extender binary, d3d11.dll and the enbseries/ folder to the root,
// and everything else to mod_path. Only the TOP level of the cache entry is
// ever the game root: an SKSE plugin DLL in SKSE/Plugins/ is Data content.
//
// Core does not honour the answer yet (#629, see adapter.RootRouter).
func (*Adapter) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for _, f := range files {
		if !strings.Contains(f, "/") && containsFold(wholeModMarkers, f) {
			for i := range out {
				out[i] = adapter.RootInstallPath
			}
			return out
		}
	}
	for i, f := range files {
		top, _, nested := strings.Cut(f, "/")
		if (!nested && isRootFile(top)) || (nested && isRootDir(top)) {
			out[i] = adapter.RootInstallPath
		}
	}
	return out
}

// RootForModType maps a Vortex mod type onto the root its whole archive is
// relative to: the default type ("") is Data/, "dinput" (which Vortex's
// script-extender installer sets) and "enb" are the game directory.
func (*Adapter) RootForModType(_ *domain.Game, modType string) (adapter.DeployRoot, bool) {
	switch modType {
	case "":
		return adapter.RootModPath, true
	case "dinput", "enb":
		return adapter.RootInstallPath, true
	default:
		return adapter.RootModPath, false
	}
}
