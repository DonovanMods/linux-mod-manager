// Package core provides business logic orchestration for lmm.
// deployable.go holds the deploy-direction file resolver (#210). Removal-direction
// operations (Uninstall, the old side of a replace) deliberately do NOT use it -
// cleanup must remove anything that might ever have been linked, so they keep the
// full ListFiles union. That asymmetry is what lets one deploy cycle self-heal an
// entry polluted by a stale, unclaimed file: the union side unlinks it, this
// resolver never re-links it.
package core

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// deployableFiles returns the version-dir-relative files that deploy-direction
// operations may link for this cache entry, in ListFiles order.
//
// The narrowing gate is a three-way rule (#210, amended after Task 3's #144
// regression):
//
//  1. EVERY completion marker in the entry must carry a recorded member
//     manifest (Recorded=true). ANY bare marker (Recorded=false -
//     "provenance unknown, never none"), or no markers at all, falls back to
//     the full ListFiles union unchanged: pre-manifest entries and
//     pure pre-#197 entries keep their historical deploy behavior exactly.
//  2. Even with full attribution, the entry must hold at least one retained
//     compile source (cache.HasRetainedSource) - the validate+retain compile
//     model's signature, present in every real #210 entry (a merged-pak
//     deploy that keeps its .exmodz for offline recompile), and the same
//     signal verify's retained-source carve-out trusts. Without it,
//     unattributed content on disk cannot be told apart from an
//     unmanifested contributor (#144, e.g. an entry `lmm import` populated
//     directly) rather than a stale pre-#197 leftover, so the full union is
//     the only safe answer.
//
// Only when BOTH hold does the result narrow to the union of recorded
// members intersected with the files actually on disk - content claimed by
// no manifest (e.g. a stale pre-#197 compiled per-mod pak carried forward by
// staging seeding) is excluded. A claimed-but-missing member is silently
// dropped here; verify owns missing-file detection and repair. When
// attribution is complete and every listed file is claimed, the narrowed
// result equals the union anyway, so this gate only changes behavior for the
// contested "recorded manifests plus unattributed content" shape.
//
// After the narrowing, the game's ADAPTER gets the last word per file
// (#353): a member it routes RouteCopyOnce or RouteSkip is not the linker's
// to deploy. Every adapter U1 ships implements no FileRouter, so every file
// routes RouteLink and this stage is the identity - which is why the
// existing deploy goldens do not move.
//
// Errors from ListFiles, FileManifests, and HasRetainedSource are returned
// unwrapped; each caller adds a single contextual wrapper of its own choosing
// (e.g. Installer.Install's "resolving deployable files", conflicts.go's
// pre-existing "listing cache files for %s") - this resolver does not
// prescribe the wording.
func deployableFiles(gameCache *cache.Cache, a adapter.GameAdapter, game *domain.Game, sourceID, modID, version string) ([]string, error) {
	files, err := gameCache.ListFiles(game.ID, sourceID, modID, version)
	if err != nil {
		return nil, err
	}
	return deployableFilesFromListing(gameCache, a, game, sourceID, modID, version, files)
}

// deployableFilesFromListing is deployableFiles' narrowing logic for a
// caller that has already listed the cache entry (Task 24 review, Minor
// #5): planDeploy needs both the deploy-direction (this) and the
// removal-direction (the raw listing itself) result for the same mod, and
// ListFiles-ing one cache entry twice to build one plan row is wasteful.
func deployableFilesFromListing(gameCache *cache.Cache, a adapter.GameAdapter, game *domain.Game, sourceID, modID, version string, files []string) ([]string, error) {
	gameID := game.ID
	manifests, err := gameCache.FileManifests(gameID, sourceID, modID, version)
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return routeDeployables(a, game, files), nil
	}
	claimed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			return routeDeployables(a, game, files), nil
		}
		for _, member := range m.Members {
			claimed[member] = true
		}
	}
	versionDir := gameCache.ModPath(gameID, sourceID, modID, version)
	hasRetainedSource, err := cache.HasRetainedSource(versionDir)
	if err != nil {
		return nil, err
	}
	if !hasRetainedSource {
		return routeDeployables(a, game, files), nil
	}
	deployable := make([]string, 0, len(files))
	for _, f := range files {
		if claimed[f] {
			deployable = append(deployable, f)
		}
	}
	return routeDeployables(a, game, deployable), nil
}
