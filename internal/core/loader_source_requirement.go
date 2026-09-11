// Package core: this file is the loader precondition asked of the SOURCE
// (#409, docs/plans/2026-09-10-thunderstore-design.md §3.4).
//
// bepinex_layout.go's requireDeclaredLoader infers "this is a BepInEx mod"
// from an ARCHIVE's shape, which is the earliest an import or a download
// can know it. A source whose catalogue says so outright - a Thunderstore
// package declaring a dependency on a BepInExPack - can be asked BEFORE
// anything is downloaded, which is where a user wants the refusal: in front
// of a 40 MB transfer rather than behind it.
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
// mod loader the game does not declare.
//
// Three deliberate non-failures:
//
//   - A source implementing no source.LoaderRequirer is untouched. Every
//     source but Thunderstore is one today.
//   - A source that cannot ANSWER (its index is unreachable, say) does not
//     block the install. "Could not tell" is not "needs a loader", and
//     refusing over a question lmm could not ask would make an unreachable
//     source look like a misconfigured game. The archive-shape rule at
//     ingest is the second line of defence, and it sees the real bytes.
//   - A game that declares the loader installs normally - and the
//     BepInExPack dependency itself is never resolved as a mod, because the
//     source drops it from GetDependencies outright (design §3.4).
func (s *Service) requireSourceDeclaredLoader(ctx context.Context, sourceID string, game *domain.Game, mod *domain.Mod) error {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return nil
	}
	requirer, ok := src.(source.LoaderRequirer)
	if !ok {
		return nil
	}
	kind, version, required, err := requirer.LoaderRequirement(ctx, mod)
	if err != nil || !required || kind == "" {
		return nil
	}
	if game.DeclaresLoader(kind) {
		return nil
	}
	return newLoaderRequirement(game, mod.Name, kind, version,
		"the package declares a dependency on the "+loaderDisplayName(kind)+" framework")
}
