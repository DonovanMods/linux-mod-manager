// Package modtypes holds the game adapters whose one rule is a table: where
// the mod types a NexusMods collection states deploy (#635). A Vortex
// collection records each mod's type (ModReference.mod_type, #607), and a
// type the game's adapter does not know is refused - by the converter's
// gate and by every deploy - rather than deployed where the game never
// reads it. These adapters are how a game without other adapter rules gets
// its types known:
//
//	witcher3        The Witcher 3: Wild Hunt
//	baldurs-gate-3  Baldur's Gate 3
//
// The Witcher 3's adapter is otherwise the identity: archives are left as
// packaged, and an untyped mod deploys under the mod_path, as the generic
// adapter deploys it. Baldur's Gate 3's also sends a .pak mod into the
// game's Mods folder inside its Wine/Proton prefix (#636, bg3.go). A game gets one from the Steam catalog's `adapter:` when
// detection adds it, or from the user (`lmm game edit --adapter`).
//
// Facts, not code: Vortex's game extensions are GPL, and lmm is MIT. They
// were READ for which directory each type deploys to; no code was copied.
// Each row cites its source, docs/adapters.md lists them all, and a type
// that could not be expressed or verified is left out (and so stays
// refused) rather than guessed. Like every adapter this package imports
// only internal/adapter and internal/domain, and touches no disk.
package modtypes

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Sources (read for facts only), for this file and bg3.go:
//
//	[VXG]  github.com/Nexus-Mods/vortex-games at 18bbee9.
//	[VX]   github.com/Nexus-Mods/Vortex at 88dd2f6.
//
// No table lists the default type (""): core never asks for it, since an
// untyped mod routes file by file (RouteRoots, which for The Witcher 3
// sends every file to the mod_path). What Vortex does with "" is recorded below only to
// say whether the catalog's mod_path matches it.
//
// The Witcher 3 ([VXG] game-witcher3):
//
//	""          queryModPath 'Mods' (index.ts:141): the game's Mods/
//	            folder, which is the catalog's mod_path. Not listed.
//	witcher3tl  registered with getTLPath (index.ts:168), the game's
//	            discovery path (util.ts:66-72): the game directory.
//	dinput      [VX] extensions/modtype-dinput/src/index.ts:55-63, 91:
//	            the game directory, for every game but two others.
//
// Left out: witcher3dlc deploys under the game's DLC/ folder (getDLCPath,
// util.ts:58-64), which none of lmm's deploy roots is;
// witcher3menumodroot relies on Vortex merging the menu XML files named
// like the game's own (mergers.ts:29-106, common.ts:136-138), where lmm
// would replace the game's file. Both stay refused.
//
// Baldur's Gate 3's table, and its .pak rule (#636), are in bg3.go.

// ID values are the games.yaml spellings and registry keys.
const (
	// Witcher3ID selects the Witcher 3 adapter.
	Witcher3ID = "witcher3"
	// BaldursGate3ID selects the Baldur's Gate 3 adapter.
	BaldursGate3ID = "baldurs-gate-3"
)

// Adapter is one game's mod-type table, with no other rules. It holds no
// other state. The Witcher 3's adapter is one; Baldur's Gate 3's has rules
// of its own (BG3).
type Adapter struct {
	id    string
	label string
	types map[string]adapter.DeployRoot
}

// Compile-time proof of the capabilities the package documents.
var (
	_ adapter.GameAdapter   = (*Adapter)(nil)
	_ adapter.RootRouter    = (*Adapter)(nil)
	_ adapter.LayoutNeutral = (*Adapter)(nil)
)

// Witcher3 returns The Witcher 3's adapter.
func Witcher3() *Adapter {
	return &Adapter{id: Witcher3ID, label: "The Witcher 3 (Vortex mod types)", types: map[string]adapter.DeployRoot{
		"witcher3tl": adapter.RootInstallPath,
		"dinput":     adapter.RootInstallPath,
	}}
}

// All returns every adapter this package provides, for the composition
// root to register.
func All() []adapter.GameAdapter { return []adapter.GameAdapter{Witcher3(), BaldursGate3()} }

// ID returns the adapter's games.yaml value.
func (a *Adapter) ID() string { return a.id }

// Label returns the display name a frontend shows.
func (a *Adapter) Label() string { return a.label }

// NormalizeArchive is the identity: archives are left as packaged.
func (*Adapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// LayoutNeutral marks the adapter as adapter.LayoutNeutral: an untyped mod
// is laid out and deployed exactly as the identity does it (NormalizeArchive
// and RouteRoots below; TestNoPerFileRules).
func (*Adapter) LayoutNeutral() {}

// RouteRoots has no per-file rules: every file of an untyped mod deploys
// under the mod_path.
func (*Adapter) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	return make([]adapter.DeployRoot, len(files))
}

// RootForModType answers from the game's table; a type it does not list is
// unknown.
func (a *Adapter) RootForModType(_ *domain.Game, modType string) (adapter.DeployRoot, bool) {
	r, ok := a.types[modType]
	return r, ok
}
