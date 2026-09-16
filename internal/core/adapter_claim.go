// Package core: this file holds the FOREIGN-ARCHIVE refusal (#359, #413) -
// core's half of the loader precondition, once #353's seam took the BepInEx
// rules out of this package.
//
// The refusal itself is unchanged and is the one #359 shipped: an archive
// that is unmistakably a mod for some OTHER kind of game - a BepInEx plugin
// imported into a game with no BepInEx - deploys an assembly nothing will
// ever load and reports success. What changed is who recognises it. Core
// used to run the BepInEx layout rules over every game's archives and then
// ask whether that game was "gated"; it now resolves the game's own adapter
// (which for such a game is the identity, and has no opinion) and asks the
// registry whether any OTHER adapter claims the archive.
//
// That inversion is what makes the rule general. The BepInEx knowledge sits
// in internal/adapter/bepinex where it belongs, the vocabulary a refusal
// speaks in comes from the claim rather than from a constant here, and the
// day a second loader adapter arrives it is refused for the right reason
// with no change to this file.
package core

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// requireAdapterClaim refuses an archive that another registered adapter
// recognises as unmistakably its own, for a game whose adapter is not that
// one.
//
// members are the archive's file members - the sanitised, archive-relative
// listing an import plan renders and an ingest is about to commit. The
// game's own adapter is EXCLUDED from the scan (Registry.ClaimArchive): it
// has already had its full say through NormalizeArchive, and a claim from
// it would be a game refusing its own mods.
//
// Two outcomes, and they are different problems for the user:
//
//	an adapter's own refusal (adapter.ErrNotAMod, wrapped by core into a
//	message naming the remedy) - the archive is a framework or a base game
//	rather than a mod for one, so no amount of configuring their game makes
//	it installable;
//
//	a CLAIM - the archive is a mod, for a kind of game theirs is not set up
//	as, which is LoaderRequiredError and its setup steps.
//
// A claim carrying no loader requirement refuses nothing: an adapter may
// recognise an archive without that implying the user has anything to
// install, and inventing a requirement it did not state would be core
// guessing on an adapter's behalf.
func (s *Service) requireAdapterClaim(game *domain.Game, modName string, members []string) error {
	a, err := s.AdapterFor(game)
	if err != nil {
		return err
	}
	claimant, claim, err := s.adapterRegistry().ClaimArchive(a.ID(), slashMembers(members))
	if err != nil {
		return err
	}
	if claimant == nil || claim.Requires == "" {
		return nil
	}
	// A game that already DECLARES the loader the claim asks for has
	// nothing to be told. That is a real state rather than a contradiction:
	// the user may have pointed the game at a different adapter on purpose
	// (design decision 11 - `loader:` describes the installation, the
	// adapter decides what lmm does about it), and refusing their import
	// over a requirement their own configuration already states would be
	// lmm arguing with itself.
	if game.DeclaresLoader(claim.Requires) {
		return nil
	}
	return newLoaderRequirement(game, modName, claim.Requires, "", claim.Evidence)
}
