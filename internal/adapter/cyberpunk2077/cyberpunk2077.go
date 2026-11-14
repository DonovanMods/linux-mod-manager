// Package cyberpunk2077 is Cyberpunk 2077's game adapter (#620). Its one
// rule is a table: the game's archive load order, which it reads from
// archive/pc/mod/modlist.txt (adapter.FileLoadOrderer). Everything else is
// the identity: archives are left as packaged, and every file deploys
// under the mod_path, which the Steam catalog sets to the install root.
// The game gets it from the Steam catalog's `adapter:` when detection adds
// it, or from the user (`lmm game edit --adapter cyberpunk2077`).
//
// Facts, not code: Vortex's Cyberpunk 2077 extension is GPL, and lmm is
// MIT. It was READ for how the game's archive order is spelled and where it
// is written; no code was copied. docs/adapters.md lists the sources.
//
//	[RED]  wiki.redmodding.org, "Archive files Load Order"
//	       (for-mod-users/users-modding-cyberpunk-2077/load-order): "If a
//	       file called modlist.txt is found inside archive/pc/mod then the
//	       game will load archives according to the order inside that
//	       file", one archive file name per line, extension included;
//	       without it the game loads archives in binary alphabetical order,
//	       and the archive that changes a file first wins.
//	[CPX]  github.com/E1337Kat/cyberpunk2077_ext_redux at 9aacde8f9e
//	       (2026-11-09), the Vortex extension: src/load_order.types.ts:31
//	       names the order ARCHIVE_LOAD_ORDER_ID = "cyberpunk2077-archive";
//	       src/load_order.archive.ts:58 names the file ("The game reads
//	       this from archive\pc\mod: listed archives load first, in order,
//	       then the rest"), :151-165 writes it as the archive names joined
//	       with WINDOWS_LINE_ENDING ("\r\n", src/load_order.ts:141) and no
//	       trailing line ending, and :269 registers conflictWinner "first";
//	       src/installers.layouts.ts:95,102 give the extension ".archive"
//	       and the directory archive/pc/mod/.
//
// The REDmod order (Vortex's "cyberpunk2077-redmod") is not here: no
// surveyed collection has an entry in one, so its format is unverified and
// a collection that needs it stays refused.
//
// Like every adapter this package imports only internal/adapter and
// internal/domain, and touches no disk.
package cyberpunk2077

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ID is the adapter's games.yaml spelling and registry key.
const ID = "cyberpunk2077"

// ArchiveOrderID is the archive load order's id, Vortex's spelling [CPX].
const ArchiveOrderID = "cyberpunk2077-archive"

// Adapter is Cyberpunk 2077's adapter. It holds no state.
type Adapter struct{}

// Compile-time proof of the capabilities the package documents.
var (
	_ adapter.GameAdapter     = Adapter{}
	_ adapter.LayoutNeutral   = Adapter{}
	_ adapter.FileLoadOrderer = Adapter{}
)

// New returns the adapter.
func New() Adapter { return Adapter{} }

// ID returns the adapter's games.yaml value.
func (Adapter) ID() string { return ID }

// Label returns the display name a frontend shows.
func (Adapter) Label() string { return "Cyberpunk 2077 (archive load order)" }

// NormalizeArchive is the identity: archives are left as packaged.
func (Adapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// LayoutNeutral marks the adapter as adapter.LayoutNeutral: it lays out
// and deploys every mod exactly as the identity does.
func (Adapter) LayoutNeutral() {}

// FileLoadOrders returns the archive load order [RED] [CPX].
func (Adapter) FileLoadOrders(*domain.Game) []adapter.FileLoadOrderSpec {
	return []adapter.FileLoadOrderSpec{ArchiveOrder()}
}

// ArchiveOrder is the archive load order's spec.
func ArchiveOrder() adapter.FileLoadOrderSpec {
	return adapter.FileLoadOrderSpec{
		ID:         ArchiveOrderID,
		Label:      "Archives",
		Dir:        adapter.RootedPath{Root: adapter.SpecRootGame, Path: "archive/pc/mod"},
		Extensions: []string{".archive"},
		ListFile:   "modlist.txt",
		Format:     adapter.FileListLines,
		LineEnding: "\r\n",
		FirstWins:  true,
	}
}
