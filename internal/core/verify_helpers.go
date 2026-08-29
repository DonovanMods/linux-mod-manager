package core

import (
	"os"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// HasRetainedCompileSource reports whether any of fileIDs has a retained compile
// source (cache.RetainedSourceName) on disk for sourceID/modID/version AND
// every recorded manifest at that cache entry carries ZERO members - the
// signal that the entry is a DeployCompile validate+retain-ONLY entry
// (#197 I4's ".exmodz" case), which deploys zero files of its own by design
// and must not be flagged as a FILE COUNT MISMATCH.
//
// #221 fix: a convert-eligible pak's cache entry ALSO carries a retained
// source (Task 9's widened ingest), but - unlike an exmodz - it stages a
// REAL deployable member (the raw pak copy) until the first successful
// merge flips it. Blanket-suppressing on retained-source presence alone (the
// pre-#221 behavior) silently hid a genuinely broken pak entry (its member
// content gone missing while the retained source survived) behind the same
// carve-out meant only for the zero-member exmodz case. Reading the file's
// manifests and requiring every one to record zero members narrows the
// carve-out back to its original intent.
func HasRetainedCompileSource(gameCache *cache.Cache, gameID, sourceID, modID, version string, fileIDs []string) bool {
	retained := false
	for _, fileID := range fileIDs {
		retainedPath := gameCache.GetFilePath(gameID, sourceID, modID, version, cache.RetainedSourceName(fileID))
		if _, err := os.Stat(retainedPath); err == nil {
			retained = true
			break
		}
	}
	if !retained {
		return false
	}
	manifests, err := gameCache.FileManifests(gameID, sourceID, modID, version)
	if err != nil {
		// Unreadable manifests: don't suppress on the strength of a check
		// that itself failed (epic98 audit Finding 4's precedent) - the
		// file-count check below runs normally instead of trusting a
		// carve-out we couldn't actually verify.
		return false
	}
	for _, m := range manifests {
		if len(m.Members) > 0 {
			return false // a real deployable member - not a retain-only entry
		}
	}
	return true
}

// unwrapJoined splits an errors.Join-produced error into its individual
// parts (Go's join error implements Unwrap() []error) so each per-item
// convergence failure (convergeDeployedFiles' joined error) can be reported
// as its own warning row instead of one opaque multi-item blob. A plain,
// non-joined error is returned as a single-element slice.
//
// #224 Task 6: moved from cmd/lmm/verify.go's unwrapJoinedErrors (doc
// comment carried over) so the verify engine's own convergence pass can use
// it - the CLI's copy stays in place until Task 7 deletes it in favor of
// calling the engine directly.
func unwrapJoined(err error) []error {
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		return u.Unwrap()
	}
	return []error{err}
}

// SourceMappedMod returns a copy of mod with GameID translated through the
// game's per-source ID mapping (game.SourceIDs) - the same rule
// Service.GetMod already applies (internal/core/service.go) before calling
// into a source. Installed rows persist the LMM game ID (see
// setupDoVerifyVersionTest and every InstalledMod fixture: GameID:
// game.ID), but Service.GetModFiles does NOT translate it itself - unlike
// GetMod, it forwards straight to the source. Sources like NexusMods
// address games by their own domain (e.g. "skyrimspecialedition"), so any
// direct svc.GetModFiles call driven off an installed row's mod.Mod (as
// opposed to one already sourced from the source itself, e.g. a fresh
// search result) needs this translation first or it silently queries the
// wrong game. An empty mapping value means "this source applies to any
// game" (e.g. directory sources: `donovan-mods: ""`) and must not blank
// out the LMM ID - matches Service.GetMod's `ok && id != ""` guard exactly.
func SourceMappedMod(game *domain.Game, mod *domain.Mod) *domain.Mod {
	mapped := *mod
	if id, ok := game.SourceIDs[mod.SourceID]; ok && id != "" {
		mapped.GameID = id
	}
	return &mapped
}
