// Package core provides business logic orchestration for lmm.
// deployable.go holds the deploy-direction file resolver (#210). Removal-direction
// operations (Uninstall, the old side of a replace) deliberately do NOT use it -
// cleanup must remove anything that might ever have been linked, so they keep the
// full ListFiles union. That asymmetry is what lets one deploy cycle self-heal an
// entry polluted by a stale, unclaimed file: the union side unlinks it, this
// resolver never re-links it.
package core

import (
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// deployableFiles returns the version-dir-relative files that deploy-direction
// operations may link for this cache entry, in ListFiles order.
//
// When EVERY completion marker in the entry carries a recorded member
// manifest, the result is the union of recorded members intersected with the
// files actually on disk - content claimed by no manifest (e.g. a stale
// pre-#197 compiled per-mod pak carried forward by staging seeding, #210) is
// excluded. A claimed-but-missing member is silently dropped here; verify
// owns missing-file detection and repair.
//
// When ANY marker is bare (Recorded=false - "provenance unknown, never
// none"), or the entry has no markers at all, the full ListFiles union is
// returned unchanged: pre-manifest entries, import-populated entries, and
// pure pre-#197 entries keep their historical deploy behavior exactly.
//
// Errors from ListFiles and FileManifests are returned unwrapped; the caller
// adds context with a single "resolving deployable files" wrapper.
func deployableFiles(gameCache *cache.Cache, gameID, sourceID, modID, version string) ([]string, error) {
	files, err := gameCache.ListFiles(gameID, sourceID, modID, version)
	if err != nil {
		return nil, err
	}
	manifests, err := gameCache.FileManifests(gameID, sourceID, modID, version)
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return files, nil
	}
	claimed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			return files, nil
		}
		for _, member := range m.Members {
			claimed[member] = true
		}
	}
	deployable := make([]string, 0, len(files))
	for _, f := range files {
		if claimed[f] {
			deployable = append(deployable, f)
		}
	}
	return deployable, nil
}
