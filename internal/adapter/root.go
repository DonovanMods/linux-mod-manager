package adapter

import "github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

// DeployRoot names which of a game's two directories a deployed file is
// relative to. It is the "Root" the game-adapter design left room for
// beside FileRoute (docs/plans/2026-09-10-game-adapter-design.md §2): a
// separate answer from a separate capability, so FileRoute and every
// adapter that answers it are unchanged. It is domain.DeployRoot, the
// vocabulary core records a deployed file's root in (#629).
type DeployRoot = domain.DeployRoot

const (
	// RootModPath is the zero value: the file deploys under the game's
	// mod_path, as every file of every game always has.
	RootModPath = domain.RootModPath
	// RootInstallPath is a file that belongs at the game's install_path,
	// relative to it: a script extender's loader and DLLs, an ENB's
	// d3d11.dll, which the game only loads from beside its executable.
	RootInstallPath = domain.RootInstallPath
)

// RootRouter is the optional capability of an adapter whose game loads
// some mod content from its install directory rather than from its
// mod_path (#606: a Bethesda game's mod_path is Data/, but a script
// extender's loader and an ENB live beside the executable).
//
// Core honours it (#629): a file routed to RootInstallPath deploys under the
// game's install_path, is recorded there (deployed_files.root), and every
// removal, converge, verify, purge and conflict report finds it there. It
// is written only beneath the install_path, and a stock file it replaces
// is preserved first and put back when lmm's file goes
// (internal/core/deploy_root.go). For a game whose mod_path IS its
// install_path the answer changes nothing: there is one directory.
//
// Like every capability it is pure: it answers from file names and the
// game's configuration, never from the disk.
type RootRouter interface {
	// RouteRoots answers for every file of ONE mod's cache entry at once,
	// because some rules are about the mod as a whole (an ENB preset is
	// recognised by one file, and then all of it belongs at the game
	// root). files are cache-entry-relative and slash-separated, like
	// NormalizeRequest.Members; the answer has one DeployRoot per file, in
	// the same order.
	RouteRoots(g *domain.Game, files []string) []DeployRoot

	// RootForModType answers for a mod whose kind was stated rather than
	// detected: a Vortex mod type, as a NexusMods collection records it
	// ("dinput" is Vortex's type for a mod deployed to the game root). It
	// reports false for a type the adapter does not know, which the caller
	// treats as a type it cannot honour, never as "mod_path".
	RootForModType(g *domain.Game, modType string) (DeployRoot, bool)
}

// RouteRoots returns the deploy root of each of one mod's files: the
// adapter's answer, or RootModPath for every file when it has no
// RootRouter. An answer of the wrong length is an adapter bug, and is
// treated as no answer rather than handed on for a caller to index past.
func RouteRoots(a GameAdapter, g *domain.Game, files []string) []DeployRoot {
	out := make([]DeployRoot, len(files))
	rr, ok := a.(RootRouter)
	if !ok {
		return out
	}
	got := rr.RouteRoots(g, files)
	if len(got) != len(files) {
		return out
	}
	copy(out, got)
	return out
}

// RootForModType returns the deploy root the adapter gives a stated mod
// type, and whether it knows the type. An adapter without a RootRouter
// knows none.
func RootForModType(a GameAdapter, g *domain.Game, modType string) (DeployRoot, bool) {
	rr, ok := a.(RootRouter)
	if !ok {
		return RootModPath, false
	}
	return rr.RootForModType(g, modType)
}
