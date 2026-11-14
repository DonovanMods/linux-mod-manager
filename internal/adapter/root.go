package adapter

import (
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

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
	// RootPrefix is a file that belongs inside the game's Wine/Proton
	// prefix, under its Windows user directory (#636): Baldur's Gate 3
	// loads .pak mods only from AppData/Local/Larian Studios/Baldur's
	// Gate 3/Mods. An adapter that answers it must be a PrefixPlacer too,
	// which says where under the user directory each such file goes.
	RootPrefix = domain.RootPrefix
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
// RootRouter. An answer of the wrong length, a root this build does not
// know, or RootPrefix from an adapter that is no PrefixPlacer (it cannot
// say where in the prefix the file goes) is an adapter bug, and is treated
// as no answer rather than handed on for a caller to index past.
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
	for _, r := range got {
		if !usableRoot(a, r) {
			return out
		}
	}
	copy(out, got)
	return out
}

// usableRoot reports whether a may answer r: a root this build knows, and
// RootPrefix only from a PrefixPlacer.
func usableRoot(a GameAdapter, r DeployRoot) bool {
	switch r {
	case RootModPath, RootInstallPath:
		return true
	case RootPrefix:
		return PlacesInPrefix(a)
	}
	return false
}

// RootForModType returns the deploy root the adapter gives a stated mod
// type, and whether it knows the type. An adapter without a RootRouter
// knows none, and a root RouteRoots would refuse (usableRoot) makes the
// type unknown.
func RootForModType(a GameAdapter, g *domain.Game, modType string) (DeployRoot, bool) {
	rr, ok := a.(RootRouter)
	if !ok {
		return RootModPath, false
	}
	root, ok := rr.RootForModType(g, modType)
	if ok && !usableRoot(a, root) {
		return RootModPath, false
	}
	return root, ok
}

// PrefixPlacer is the optional capability of an adapter whose RootRouter
// sends files to RootPrefix (#636): it says where under the prefix's
// Windows user directory each such file goes. The deployed path is not the
// cache member's own: Baldur's Gate 3 loads a .pak only from directly
// inside its Mods folder, so "MyMod-1.2/MyMod.pak" deploys as
// "AppData/Local/Larian Studios/Baldur's Gate 3/Mods/MyMod.pak".
//
// Core resolves the prefix itself (domain.Game.PrefixRoot) and owns every
// write there; the adapter only names the path, pure and with no disk
// access. An adapter that is a PrefixPlacer should also be a
// PrefixConsumer, so an unresolved prefix is reported.
type PrefixPlacer interface {
	// PrefixPath is where member (cache-entry-relative, SLASH-separated,
	// as RouteRoots' files) deploys: a SLASH-separated path relative to
	// the prefix user directory.
	PrefixPath(g *domain.Game, member string) string
}

// PlacesInPrefix reports whether a is a PrefixPlacer.
func PlacesInPrefix(a GameAdapter) bool {
	_, ok := a.(PrefixPlacer)
	return ok
}

// PrefixPath returns where member deploys under the prefix user directory,
// canonicalised (path.Clean), and whether there is a usable answer: false
// for an adapter that is no PrefixPlacer, and for an answer that is empty,
// absolute, leaves the user directory, or holds a backslash (a Windows
// separator, which Linux would read as part of a name) - which core
// refuses rather than writing the file anywhere else.
func PrefixPath(a GameAdapter, g *domain.Game, member string) (string, bool) {
	pp, ok := a.(PrefixPlacer)
	if !ok {
		return "", false
	}
	p := path.Clean(pp.PrefixPath(g, member))
	if p == "." || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "\\") {
		return "", false
	}
	return p, true
}

// LayoutNeutral is the optional capability of an adapter that does nothing
// to an UNTYPED mod that the identity would not: its NormalizeArchive is the
// identity and, if it is a RootRouter, its RouteRoots sends every file to
// the mod_path. Whatever else it does is for mods whose type is stated
// (RootForModType) - the mod-type adapters of #635 - or for capabilities
// that do not touch where files go.
//
// Core reads it when a game switches adapters: a switch between two
// layout-neutral adapters re-lays no installed mod and moves none of its
// files, so nothing needs reinstalling and there is nothing to warn about.
// The claim is the adapter's, and its own package tests it.
type LayoutNeutral interface {
	// LayoutNeutral marks the capability; it has no behaviour.
	LayoutNeutral()
}

// IsLayoutNeutral reports whether a is LayoutNeutral. A nil adapter is not.
func IsLayoutNeutral(a GameAdapter) bool {
	_, ok := a.(LayoutNeutral)
	return ok
}
