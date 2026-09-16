// Package core: this file is the loader precondition asked of the SOURCE
// (#409, docs/plans/2026-09-10-thunderstore-design.md §3.4).
//
// adapter_claim.go's requireAdapterClaim infers "this is a mod for a game
// kind yours is not" from an ARCHIVE's shape, which is the earliest an
// import or a download can know it. A source whose catalogue says so
// outright - a Thunderstore package declaring a dependency on a BepInExPack
// - can be asked BEFORE anything is downloaded, which is where a user wants
// the refusal: in front of a 40 MB transfer rather than behind it.
//
// The two halves share ONE refusal (newLoaderRequirement) on purpose. The
// user is in the same position whichever half noticed, and the setup steps
// are the whole value of the error; two copies of them would be two copies
// to drift.
package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// requireSourceDeclaredLoader refuses a mod whose own SOURCE says it needs a
// mod loader the game does not have.
//
// Three deliberate non-failures:
//
//   - A source implementing no source.LoaderRequirer is untouched. Every
//     source but Thunderstore is one today.
//   - A source that cannot ANSWER (its index is unreachable, say) does not
//     block the install. "Could not tell" is not "needs a loader", and
//     refusing over a question lmm could not ask would make an unreachable
//     source look like a misconfigured game. The adapter's archive claim at
//     ingest is the second line of defence, and it sees the real bytes.
//   - A game that HAS the loader installs normally - declared, or installed
//     where lmm can see it (hasLoader, #413 review F6), which is the same
//     test that resolves the bepinex adapter for it - and the BepInExPack
//     dependency itself is never resolved as a mod, because the source
//     drops it from GetDependencies outright (design §3.4).
func (s *Service) requireSourceDeclaredLoader(ctx context.Context, sourceID string, game *domain.Game, mod *domain.Mod) error {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return nil
	}
	requirer, ok := src.(source.LoaderRequirer)
	if !ok {
		return nil
	}
	kind, version, dependency, required, err := requirer.LoaderRequirement(ctx, mod)
	if err != nil || !required || kind == "" {
		return nil
	}
	if hasLoader(game, kind) {
		return nil
	}
	return newLoaderRequirement(game, mod.Name, kind, version, sourceLoaderEvidence(kind, dependency))
}

// sourceLoaderEvidence is what LoaderRequiredError.Layout carries when a
// SOURCE reported the requirement: the dependency string itself, which is
// the package a doubting user would go and look at, rather than a sentence
// naming no package at all (#409 review F5). A source with nothing
// quotable falls back to the generic phrasing.
func sourceLoaderEvidence(kind, dependency string) string {
	if dependency == "" {
		return "the package declares a dependency on the " + loaderDisplayName(kind) + " framework"
	}
	return "the package depends on " + dependency
}
