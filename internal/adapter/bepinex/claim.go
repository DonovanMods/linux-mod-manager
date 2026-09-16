// Package bepinex: this file holds the ARCHIVE CLAIM (#359, widened by
// #424) - the one rule this adapter answers about a game it is NOT the
// adapter for.
//
// It is the loader precondition, moved out of internal/core's
// requireDeclaredLoader in U3 (#413). The refusal it makes has not changed:
// an archive that is unmistakably a BepInEx mod, imported into a game with
// no BepInEx, deploys an assembly nothing will ever load and reports
// success. What changed is who decides. Core used to run the BepInEx rules
// over every game's archives and then ask whether the game was "gated";
// now core resolves the game's own adapter (which for such a game is the
// identity, and has no opinion) and asks the registry whether any OTHER
// adapter claims the archive - so the BepInEx knowledge is here and core's
// half is one rule that works for whatever adapter comes next.
package bepinex

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ClaimArchive reports whether members are unmistakably a BepInEx mod, for
// a game this is not the adapter for.
//
// UNMISTAKABLE is a narrower bar than NormalizeArchive's, and deliberately
// so: it is exactly the two shapes that name the directory `BepInEx`
// (shape A, and shape C's single wrapper around one), plus the MIXED root
// that holds `BepInEx/` beside a sibling - a layout lmm will not place, but
// still one no other game's mod plausibly has (#424 re-review, finding A).
// The ambiguous shapes are not claimed at all: `plugins/` at an archive
// root and `Mods/<Mod>/<Mod>.dll` are ordinary names, and claiming one
// would tell a 7 Days to Die user to install a loader they do not need.
// normalise's gated=false path is that bar, expressed once.
//
// A framework pack is an ERROR rather than a claim, because the user's
// problem is different: the archive is BepInEx itself, so no amount of
// configuring their game makes it installable as a mod. adapter.ErrNotAMod
// carries that, and core surfaces it ahead of the requirement.
//
// The game root is deliberately "" - there is no game to consult, and the
// one rule that would use it (shape F's game-owned refusal) is not reachable
// from an ungated classification anyway. So is the mod name: no claimed
// shape names a directory after the mod.
func (*Adapter) ClaimArchive(members []string) (adapter.Claim, error) {
	l, err := normalise(members, "", false, "")
	if err != nil {
		return adapter.Claim{}, err
	}
	switch {
	case l.applies:
		return adapter.Claim{Evidence: l.Shape.String(), Requires: domain.LoaderKindBepInEx}, nil
	case l.loaderRoot:
		// A mixed root is shapeNone as a LAYOUT - it deploys verbatim, with
		// a warning, for a game that has the loader - but the evidence a
		// user is owed is still "your archive has a BepInEx directory in
		// it", which is what shape A is called.
		return adapter.Claim{Evidence: shapeRooted.String(), Requires: domain.LoaderKindBepInEx}, nil
	}
	return adapter.Claim{}, nil
}
