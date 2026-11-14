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
// Each adapter is otherwise the identity: archives are left as packaged,
// and an untyped mod deploys under the mod_path, as the generic adapter
// deploys it. A game gets one from the Steam catalog's `adapter:` when
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

// Sources (read for facts only):
//
//	[VXG]  github.com/Nexus-Mods/vortex-games at 18bbee9.
//	[VX]   github.com/Nexus-Mods/Vortex at 88dd2f6.
//
// The Witcher 3 ([VXG] game-witcher3):
//
//	""          queryModPath 'Mods' (index.ts:141): the game's Mods/
//	            folder, which is the catalog's mod_path.
//	witcher3tl  registered with getTLPath (index.ts:168), the game's
//	            discovery path (util.ts:66-72): the game directory.
//	dinput      [VX] extensions/modtype-dinput/src/index.ts:55-63, 91:
//	            the game directory, for every game but two others.
//
// Left out: witcher3dlc deploys under the game's DLC/ folder (getDLCPath,
// util.ts:58-64), which neither of lmm's two deploy roots is;
// witcher3menumodroot relies on Vortex merging the menu XML files named
// like the game's own (mergers.ts:29-106, common.ts:136-138), where lmm
// would replace the game's file. Both stay refused.
//
// Baldur's Gate 3 ([VXG] game-baldursgate3):
//
//	bg3-loose  registered with getGameDataPath (index.tsx:289-292), the
//	           game directory's Data/ (util.ts:17-25), which is the
//	           catalog's mod_path.
//
// The default type ("") is NOT listed for Baldur's Gate 3: Vortex deploys it
// to Documents/Larian Studios/Baldur's Gate 3/Mods (queryModPath modsPath,
// index.tsx:236, util.ts:31-33), which is not the catalog's mod_path, so the
// adapter does not claim to know it. Core never asks for "" (an untyped mod
// routes file by file), so nothing is refused for it.

// ID values are the games.yaml spellings and registry keys.
const (
	// Witcher3ID selects the Witcher 3 adapter.
	Witcher3ID = "witcher3"
	// BaldursGate3ID selects the Baldur's Gate 3 adapter.
	BaldursGate3ID = "baldurs-gate-3"
)

// Adapter is one game's mod-type table. It holds no other state.
type Adapter struct {
	id    string
	label string
	types map[string]adapter.DeployRoot
}

// Compile-time proof of the capabilities the package documents.
var (
	_ adapter.GameAdapter = (*Adapter)(nil)
	_ adapter.RootRouter  = (*Adapter)(nil)
)

// Witcher3 returns The Witcher 3's adapter.
func Witcher3() *Adapter {
	return &Adapter{id: Witcher3ID, label: "The Witcher 3 (Vortex mod types)", types: map[string]adapter.DeployRoot{
		"":           adapter.RootModPath,
		"witcher3tl": adapter.RootInstallPath,
		"dinput":     adapter.RootInstallPath,
	}}
}

// BaldursGate3 returns Baldur's Gate 3's adapter.
func BaldursGate3() *Adapter {
	return &Adapter{id: BaldursGate3ID, label: "Baldur's Gate 3 (Vortex mod types)", types: map[string]adapter.DeployRoot{
		"bg3-loose": adapter.RootModPath,
	}}
}

// All returns every adapter this package provides, for the composition
// root to register.
func All() []*Adapter { return []*Adapter{Witcher3(), BaldursGate3()} }

// ID returns the adapter's games.yaml value.
func (a *Adapter) ID() string { return a.id }

// Label returns the display name a frontend shows.
func (a *Adapter) Label() string { return a.label }

// NormalizeArchive is the identity: archives are left as packaged.
func (*Adapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

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
