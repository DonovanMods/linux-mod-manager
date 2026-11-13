// Package bethesda is the game adapter for Bethesda's Creation Engine games
// (#606, design docs/plans/2026-11-10-collections-design.md §5): Skyrim
// Special Edition/Anniversary Edition, Skyrim VR, Fallout 4, Fallout 4 VR,
// Starfield and Skyrim (LE), selected by `adapter: bethesda` in games.yaml.
//
// It answers four questions, every one of them from tables:
//
//	PluginOrderer     where the game's active-plugin list lives, how it is
//	                  spelled, which plugins load implicitly, which header
//	                  bits mean master/light/medium, and the active limits
//	                  (games.go)
//	NormalizeArchive  an archive rooted at the game directory - one with a
//	                  Data/ folder, possibly inside a version-named wrapper,
//	                  like SKSE64's own - is laid out relative to mod_path
//	                  (Data/), with its game-root files left at the top
//	                  (layout.go)
//	RootRouter        which of a mod's files belong at the game's
//	                  install_path rather than its mod_path: a script
//	                  extender's loader and DLLs, an ENB (route.go). Core
//	                  deploys those there (#629); see adapter.RootRouter
//	Verifier          a note when the adapter cannot tell which of its games
//	                  a game is (verify.go)
//	PrefixConsumer    yes for every game it recognises: Plugins.txt lives in
//	                  the game's Wine/Proton prefix (#605)
//
// Facts, not code: libloadorder, esplugin, libloot and Vortex are GPL, and
// lmm is MIT. Their documentation and behaviour were READ for the facts in
// these tables (file names, flag bits, implicit plugin lists, limits); no
// code was copied. Each table cites its source, and docs/adapters.md lists
// them all. A value that could not be verified is left out rather than
// guessed.
//
// Like every adapter it imports only internal/adapter and internal/domain
// (internal/adapter/boundary_test.go), and touches no disk.
package bethesda

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
)

// ID is this adapter's games.yaml value and registry key.
const ID = "bethesda"

// Adapter is the Bethesda Creation Engine game adapter. It holds no state.
type Adapter struct{}

// New returns the Bethesda adapter.
func New() *Adapter { return &Adapter{} }

// ID returns the adapter's games.yaml value ("bethesda").
func (*Adapter) ID() string { return ID }

// Label returns the display name a frontend shows.
func (*Adapter) Label() string { return "Bethesda (Creation Engine plugins)" }

// Compile-time proof of the capabilities the package documents.
var (
	_ adapter.GameAdapter    = (*Adapter)(nil)
	_ adapter.PluginOrderer  = (*Adapter)(nil)
	_ adapter.RootRouter     = (*Adapter)(nil)
	_ adapter.Verifier       = (*Adapter)(nil)
	_ adapter.PrefixConsumer = (*Adapter)(nil)
)
