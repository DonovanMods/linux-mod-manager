// Package bepinex is the game adapter for Unity games loading mods through
// BepInEx (#353 U3, #413).
//
// It is the second real adapter, and the one that motivated most of the
// seam: before this package the BepInEx rules lived in internal/core as a
// bool parameter threaded through every ingest (`loaderDeclared`), a path
// prefix tested inline by the installer, and a verify tier gated on an `if
// game.Loader != nil`. Each of those is a property of the GAME, which is
// what adapter.GameAdapter names, so each moved here unchanged but for
// losing the parameter that used to stand in for "is this a BepInEx game?".
//
// The gate is now STRUCTURAL, which is the whole point (design §2,
// "Composition with loader:"): this adapter is only ever asked about a game
// that HAS BepInEx - internal/core resolves it for a game declaring
// `loader: kind: bepinex`, or one with BepInEx's own preloader in its
// install directory (#424) - so the failure mode `loaderDeclared` guarded
// against, a 7 Days to Die archive rooted at `plugins/` silently moved
// under a `BepInEx/` directory the game has never heard of, is
// unrepresentable rather than merely checked.
//
// The one place that judgement still has to be made about a game this is
// NOT the adapter for is ClaimArchive (claim.go): importing a BepInEx
// plugin into a game with no BepInEx deploys an assembly nothing will ever
// load, and core asks this adapter about that archive precisely because the
// game's own adapter has no opinion. Everything else here is asked only of
// a BepInEx game.
//
// The capabilities implemented, and where each came from:
//
//	NormalizeArchive  the archive-root normaliser (#358), layout.go
//	FileRouter        BepInEx/config/** seeding (#358 (b)), route.go
//	ArchiveClaimer    the loader precondition (#359/#424), claim.go
//	Verifier          verify's loader REPORTING tier (#359), verify.go
//	Guide             the setup guidance (#359), guide.go - implemented, but
//	                  not rendered by any frontend yet (see adapter.GuidanceNote)
//
// See docs/plans/2026-09-09-bepinex-spike.md for the evidence behind every
// rule and docs/adapters.md for how the pieces fit together.
package bepinex

import "github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"

// ID is this adapter's games.yaml value and registry key.
//
// `bepinex`, not `unity-loader`, and that is a settled decision (design
// OQ3): it follows the spike's own ruling - resist inventing a general
// framework abstraction before there is a second framework - and costs
// nothing later, because MelonLoader arrives as its own adapter package
// sharing whatever the two turn out to actually share.
const ID = "bepinex"

// Adapter is the BepInEx game adapter. It holds no state: every rule here
// is a pure function of the archive's member list, the game's
// configuration, and (for the two rules that genuinely cannot be answered
// otherwise) the game's own install directory.
type Adapter struct{}

// New returns the BepInEx adapter. It takes no configuration for the same
// reason adapter.Generic does not: a rule that varied per construction
// would be a rule internal/core could not reason about.
func New() *Adapter { return &Adapter{} }

// ID returns the adapter's games.yaml value ("bepinex").
func (*Adapter) ID() string { return ID }

// Label returns the display name a frontend shows.
func (*Adapter) Label() string { return "BepInEx (Unity plugin loader)" }

// Compile-time proof that every capability this package documents is
// actually implemented. A method renamed out of an interface is otherwise
// invisible - core type-asserts, so it would simply stop being asked.
var (
	_ adapter.GameAdapter    = (*Adapter)(nil)
	_ adapter.FileRouter     = (*Adapter)(nil)
	_ adapter.ArchiveClaimer = (*Adapter)(nil)
	_ adapter.Verifier       = (*Adapter)(nil)
	_ adapter.Guide          = (*Adapter)(nil)
)
