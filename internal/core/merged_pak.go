package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// mergedPakModID/mergedPakVersion identify the merged pak as a synthetic,
// singleton "mod" per (game, profile) - domain.SourceMerged is the matching
// sourceID. This reuses Installer.Install/Uninstall and cache.Cache
// verbatim (#197 design decision 2) rather than a parallel deploy/tracking
// mechanism: zero schema changes, and the SAME deployed_files ownership
// (and #168-class residue risk) as every other deployed file. The merged
// artifact's on-disk FILENAME is the compile source's business, not core's
// (#256): mc.MergedArtifactName() supplies it wherever it's needed.
const (
	mergedPakModID = "merged-pak"
	// mergedPakVersion is fixed ("merged", not a real upstream version) -
	// there is exactly one merged pak per (game, profile) at any time, and
	// every regeneration REPLACES it outright (mirrors #166's directory-
	// source "replace, don't overlay" precedent) rather than versioning it.
	mergedPakVersion = "merged"
)

// MergedFingerprint captures everything a merged pak was built from (#197):
// the base pak's IndexHash plus an ORDERED list of every contributing
// exmodz file's identity and content checksum. Order matters - it's the
// profile's load order, which is also merge-application order - so two
// fingerprints with the same entries in a DIFFERENT order must compare
// unequal (a load-order change is a documented regeneration trigger).
type MergedFingerprint struct {
	BaseIndexHash string
	Mods          []MergedFingerprintEntry
}

// MergedFingerprintEntry identifies one contributing file within a
// MergedFingerprint.
type MergedFingerprintEntry struct {
	SourceID string
	ModID    string
	Version  string
	Checksum string // MD5 of the retained source bytes (md5File)
	Kind     string `json:",omitempty"` // source-defined convertible kind for retained paks; empty/native otherwise (#221, opaque to core since #256)

	// Outcome fields (#221): recorded AFTER the merge, ignored by input
	// equality - a failed conversion retries only when an INPUT changes
	// (pak bytes, base pak, membership), not on every sync.
	Converted  bool   `json:",omitempty"`
	FailReason string `json:",omitempty"`
}

// mergeSourceClassifier is the one sliver of source.MergeCompiler the
// package-level fingerprint helpers need: ClassifyMergeSource as a function
// value (a method value like mc.ClassifyMergeSource assigns directly).
// Kept narrow so the helpers stay pure and tests can exercise fingerprint
// semantics without constructing a full source (#256).
type mergeSourceClassifier func(id string) (kind string, convertible bool)

// ModHasPakMergeSource reports whether mod carries at least one
// convertible-kind (raw pak) merge-source fileID, as opposed to being
// native/exmodz-only (#221 round-4 fix). Classification over mod.FileIDs
// via the game's compile source (#256) - no cache lookups or
// retained-source disk checks (unlike enabledMergeSources, which
// additionally confirms ingest actually RETAINED something). Callers that
// only need "does pak-conversion state have any effect on this mod at all"
// want this cheaper check, not enabledMergeSources' full retained-file
// resolution. A game with no (or an ambiguous)
// merge-compiler source has no merge sources of any kind, so resolution
// failure is simply false, not an error. Deliberately NOT gated on
// game.DeployMode: `lmm mod convert` persists the per-mod flag on
// non-compile games too (with an advisory that it has no effect there,
// TestModConvertCommand_NonCompileGame), so classification must answer
// for any game whose compile source resolves - matching the pre-#256
// static behavior. Callers that only care about compile games gate on
// DeployMode themselves.
func (s *Service) ModHasPakMergeSource(game *domain.Game, mod *domain.InstalledMod) bool {
	if game == nil || mod == nil {
		return false
	}
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return false
	}
	for _, fileID := range mod.FileIDs {
		if _, convertible := mc.ClassifyMergeSource(fileID); convertible {
			return true
		}
	}
	return false
}

// fingerprintInputs strips outcome fields and normalizes Kind so equality
// judges inputs only. A legacy pre-#221 marker entry's empty Kind and the
// source's own default kind are the same input - classify("") returns that
// default (icarus: "exmodz"), per the ClassifyMergeSource contract.
func fingerprintInputs(f MergedFingerprint, classify mergeSourceClassifier) MergedFingerprint {
	out := MergedFingerprint{BaseIndexHash: f.BaseIndexHash, Mods: make([]MergedFingerprintEntry, len(f.Mods))}
	for i, m := range f.Mods {
		kind := m.Kind
		if kind == "" {
			kind, _ = classify(kind)
		}
		out.Mods[i] = MergedFingerprintEntry{SourceID: m.SourceID, ModID: m.ModID, Version: m.Version, Checksum: m.Checksum, Kind: kind}
	}
	return out
}

// failedRefsFromFingerprint extracts the "ModRef -> failure reason" map
// reconcilePakManifests needs, from an ALREADY-COMPUTED fingerprint (#221).
// Used by syncMergedPak's fast path, which trusts the STORED fingerprint's
// recorded outcomes rather than re-running MergeCompile just to relearn
// what it already reported on the run that produced fp - the merge inputs
// are unchanged (that's why the fast path was taken), so a mod's prior
// conversion success/failure still holds.
func failedRefsFromFingerprint(fp MergedFingerprint) map[string]string {
	failed := make(map[string]string, len(fp.Mods))
	for _, m := range fp.Mods {
		if !m.Converted {
			failed[m.SourceID+":"+m.ModID] = m.FailReason
		}
	}
	return failed
}

// marshalMergedFingerprint renders f deterministically: encoding/json
// marshals struct fields in declaration order (not sorted) and preserves
// slice order exactly, so the same MergedFingerprint value always produces
// byte-identical output - the property mergedFingerprintsEqual depends on.
//
// A nil Mods is normalized to an empty (non-nil) slice first: encoding/json
// marshals a nil slice as `null` but an empty slice as `[]` - two DIFFERENT
// byte sequences for what must count as the same "zero contributing mods"
// state (e.g. a freshly-built "current" fingerprint via `var mods []T`
// compared against a previously-stored marker written some other way).
// Caught by extraction-verification (a scratch test comparing the two
// literally failed before this normalization was added) - without it, a
// profile with zero enabled exmodz mods could spuriously flip between
// "stale"/"not stale" depending on which code path happened to build each
// side's slice.
func marshalMergedFingerprint(f MergedFingerprint) ([]byte, error) {
	if f.Mods == nil {
		f.Mods = []MergedFingerprintEntry{}
	}
	return json.Marshal(f)
}

// mergedFingerprintsEqual reports whether a and b describe the same merge
// inputs, by comparing their marshaled bytes - exactly what "compare
// against the stored marker" needs, since the marker itself IS the
// marshaled form.
func mergedFingerprintsEqual(a, b MergedFingerprint, classify mergeSourceClassifier) (bool, error) {
	aBytes, err := marshalMergedFingerprint(fingerprintInputs(a, classify))
	if err != nil {
		return false, err
	}
	bBytes, err := marshalMergedFingerprint(fingerprintInputs(b, classify))
	if err != nil {
		return false, err
	}
	return bytes.Equal(aBytes, bBytes), nil
}

// enabledMergeSources returns every enabled mod's retained merge-source files
// for game+profileName, in PROFILE LOAD ORDER (the merge-application order,
// #197 design) - the exact input MergeCompile needs. Only files that were
// actually retained (cache.RetainedSourceName present in the mod's cache
// entry) count: a mod's plain .pak files, or a mod whose ingest never got
// far enough to retain anything, contribute nothing. A mod's OWN FileIDs
// are walked (not the whole cache directory) because a download-compiled
// entry's retained-source name is keyed by a real DownloadableFile.ID,
// while an import-compiled entry's is keyed by its own archive filename
// (see Task 2/3's ingest branches) - FileIDs is the one list that already
// carries whichever identity applies, for either origin.
func (s *Service) enabledMergeSources(ctx context.Context, game *domain.Game, profileName string) ([]source.MergeSource, error) {
	mods, err := s.GetInstalledModsInProfileOrder(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile mods: %w", err)
	}

	gameCache := s.GetGameCache(game)
	// The compile source is resolved lazily, on the first retained file
	// found (#256): classification is its business now, but a profile with
	// nothing retained has nothing to classify, and must keep working -
	// exactly as it did pre-#256 - even for a game whose MergeCompiler
	// source isn't configured (syncMergedPak's uninstall-to-zero path runs
	// unconditionally from every mutation flow).
	var mc source.MergeCompiler
	var sources []source.MergeSource
	for _, mod := range mods {
		if !mod.Enabled {
			continue
		}
		for _, fileID := range mod.FileIDs {
			retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(fileID))
			if _, statErr := os.Stat(retainedPath); statErr != nil {
				continue // not a retained merge source (nothing ingested for this fileID - a legacy-ingest pak, or a non-convert-eligible one)
			}
			if mc == nil {
				var mcErr error
				if mc, mcErr = s.mergeCompilerForGame(game); mcErr != nil {
					return nil, mcErr
				}
			}
			kind, convertible := mc.ClassifyMergeSource(fileID)
			if convertible && (!game.ConvertPaks || !mod.ConvertPaks) {
				continue // opted out (game- or mod-level): stays raw-deployed (#221)
			}
			sources = append(sources, source.MergeSource{
				ModRef:     mod.SourceID + ":" + mod.ID,
				ModName:    mod.Name,
				SourcePath: retainedPath,
				Kind:       kind,
			})
		}
	}
	return sources, nil
}

// syncMergedPak regenerates game+profileName's merged pak if its recorded
// fingerprint no longer matches the CURRENT enabled-mod set/order/versions/
// base pak (#197). Cheap when nothing changed: the fast path is one
// directory read (enabledMergeSources), one base-artifact fingerprint read
// (mc.FingerprintBase - for Icarus a pak footer, never the full content), and N small MD5s
// (md5File over each retained .exmodz - real files here are small, see
// #175's own research on real base-table sizes), then a byte comparison.
// Safe to call unconditionally from ANY mutation flow regardless of game
// type - it no-ops immediately for a non-DeployCompile game.
//
// Zero enabled merge sources (exmodz or convert-eligible pak, #221)
// uninstalls any existing merged pak instead of generating an empty one
// (#197 design decision 2's "uninstall-to-zero"
// requirement) - Installer.Uninstall on the synthetic merged-pak mod is
// idempotent when there is nothing deployed (linker.Undeploy tolerates an
// already-absent path, matching every other uninstall in this codebase),
// so calling it unconditionally here is safe even when no pak was ever
// generated.
func (s *Service) syncMergedPak(ctx context.Context, game *domain.Game, profileName string) (warnings []string, err error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}

	current, sources, err := s.currentMergedFingerprint(ctx, game, profileName)
	if err != nil {
		return nil, err
	}

	gameCache := s.GetGameCache(game)
	syntheticMod := &domain.Mod{ID: mergedPakModID, SourceID: domain.SourceMerged, Version: mergedPakVersion, GameID: game.ID}

	installer, err := s.GetInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return nil, err
	}

	if len(sources) == 0 {
		if uerr := installer.Uninstall(ctx, game, syntheticMod, profileName); uerr != nil {
			return nil, fmt.Errorf("removing merged pak: %w", uerr)
		}
		if derr := gameCache.Delete(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion); derr != nil {
			return nil, fmt.Errorf("clearing merged pak cache entry: %w", derr)
		}
		// An all-opted-out (or all-disabled) profile must still flip any
		// previously-CONVERTED pak mod back to raw deploy (#221) - there is
		// no merged pak anymore to claim its content.
		reconWarnings, rerr := s.reconcilePakManifests(ctx, game, profileName, installer, nil)
		if rerr != nil {
			return nil, fmt.Errorf("reconciling pak manifests: %w", rerr)
		}
		return reconWarnings, nil
	}

	// Non-empty sources imply a resolvable compile source (enabledMergeSources
	// already consulted it to classify them), so resolving here - earlier
	// than pre-#256, which only needed the source on the slow path below -
	// cannot newly fail a flow that used to succeed.
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return nil, err
	}

	basePakPath, err := mc.ResolveBaseArtifact(game)
	if err != nil {
		return nil, err
	}

	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	deployedPath := filepath.Join(game.ModPath, mc.MergedArtifactName())
	if stored, ok := readMergedFingerprint(cachePath); ok {
		if eq, eqErr := mergedFingerprintsEqual(current, stored, mc.ClassifyMergeSource); eqErr == nil && eq {
			// #197 I5 fix: an unchanged fingerprint alone doesn't guarantee
			// the pak is actually deployed - a PRIOR call's Install could
			// have failed AFTER the fingerprint was already committed
			// (wedging detection forever, since nothing here would ever
			// notice), or a purge could have deliberately undeployed just
			// the file while keeping the cache entry (#197 I2). Confirm
			// the artifact is really on disk before trusting the fast
			// path; if it's missing, redeploy the EXISTING cache content
			// (self-healing) rather than re-merging - the inputs haven't
			// changed, so there is nothing new to compute.
			if _, statErr := os.Stat(deployedPath); statErr != nil {
				if err := installer.Install(ctx, game, syntheticMod, profileName); err != nil {
					return nil, fmt.Errorf("redeploying merged pak: %w", err)
				}
			}
			// #221 fix: reconcile unconditionally even on this fast path.
			// Inputs are unchanged, so stored's own outcome fields (which
			// mods converted vs failed) are still authoritative - no need to
			// re-run MergeCompile to learn what it already reported last
			// time. Skipping reconcile entirely here (as before this fix)
			// is exactly what let a PRIOR partial failure - e.g. a per-mod
			// Uninstall/Install that failed after reconcile had already
			// started, or was never reached because syncMergedPak errored
			// out earlier in a previous run - go permanently unretried: the
			// fingerprint becomes "current" as soon as it's committed
			// (before reconcile even runs, in the non-fast path below), so
			// every later call would keep hitting this same fast path and
			// never look at per-mod manifest state again. Reconcile's own
			// pre-checks are cheap (a stat plus a couple of manifest/file-
			// list reads per enabled pak mod) and no-op once truly
			// converged, so running it here is not a meaningful cost on the
			// common "nothing to do" path.
			reconWarnings, rerr := s.reconcilePakManifests(ctx, game, profileName, installer, failedRefsFromFingerprint(stored))
			if rerr != nil {
				return nil, fmt.Errorf("reconciling pak manifests: %w", rerr)
			}
			return reconWarnings, nil
		}
	}

	stagePath := cachePath + ".staging"
	if err := os.RemoveAll(stagePath); err != nil {
		return nil, fmt.Errorf("clearing merged pak staging: %w", err)
	}
	if err := os.MkdirAll(stagePath, 0755); err != nil {
		return nil, fmt.Errorf("preparing merged pak staging: %w", err)
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck

	outputPath := filepath.Join(stagePath, mc.MergedArtifactName())
	mergeWarnings, mergeFailed, err := mc.MergeCompile(ctx, basePakPath, sources, outputPath)
	if err != nil {
		return nil, fmt.Errorf("merging %d merge source(s): %w", len(sources), err)
	}
	warnings = mergeWarnings

	failedByRef := make(map[string]string, len(mergeFailed))
	for _, f := range mergeFailed {
		failedByRef[f.ModRef] = f.Reason
	}
	for i := range current.Mods {
		ref := current.Mods[i].SourceID + ":" + current.Mods[i].ModID
		if reason, bad := failedByRef[ref]; bad {
			current.Mods[i].Converted = false
			current.Mods[i].FailReason = reason
		}
	}

	fingerprintBytes, err := marshalMergedFingerprint(current)
	if err != nil {
		return warnings, fmt.Errorf("encoding merge fingerprint: %w", err)
	}
	if err := os.WriteFile(cache.MergeFingerprintPath(stagePath), fingerprintBytes, 0644); err != nil {
		return warnings, fmt.Errorf("writing merge fingerprint: %w", err)
	}

	if err := commitStagedCache(cachePath, stagePath); err != nil {
		return warnings, err
	}

	if err := installer.Install(ctx, game, syntheticMod, profileName); err != nil {
		return warnings, fmt.Errorf("deploying merged pak: %w", err)
	}

	reconWarnings, rerr := s.reconcilePakManifests(ctx, game, profileName, installer, failedByRef)
	if rerr != nil {
		return warnings, fmt.Errorf("reconciling pak manifests: %w", rerr)
	}
	warnings = append(warnings, reconWarnings...)
	return warnings, nil
}

// SyncMergedPak exposes syncMergedPak as the public entry point every
// mutation flow that can change a merged pak's inputs (enabled-mod set,
// load order, mod version, base pak) must call after the mutation is
// durable (#197 fix wave: rollback, purge, and both import paths were
// found missing this call). Safe to call unconditionally - it no-ops for a
// non-DeployCompile game and is a cheap fast-path when nothing changed.
func (s *Service) SyncMergedPak(ctx context.Context, game *domain.Game, profileName string) ([]string, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.syncMergedPak(ctx, game, profileName)
}

// reconcilePakManifests aligns every enabled pak mod's cache manifest with
// the merge outcome (#221): a mod whose pak CONVERTED has members=nil (the
// merged pak claims its content; the raw copy must not deploy - flipping it
// undeploys any raw link), while a failed or opted-out mod has its raw pak
// as the sole member (raw deploy, today's behavior). Flips are followed by
// the matching installer action so the game dir converges immediately: the
// first-install transient (raw deployed, then first sync converts) is
// healed here, not left for the next verify.
func (s *Service) reconcilePakManifests(ctx context.Context, game *domain.Game, profileName string, installer *Installer, failedByRef map[string]string) (warnings []string, err error) {
	mods, err := s.GetInstalledModsInProfileOrder(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile mods: %w", err)
	}
	gameCache := s.GetGameCache(game)
	// Lazily resolved, like enabledMergeSources (#256): only a mod with a
	// retained file has anything to classify, so a profile with nothing
	// retained never needs (and pre-#256 never consulted) the compile
	// source - the retained-stat therefore runs BEFORE classification,
	// flipping the pre-#256 order of two independent, side-effect-free
	// filters.
	var mc source.MergeCompiler
	for i := range mods {
		mod := &mods[i]
		if !mod.Enabled {
			continue
		}
		for _, fileID := range mod.FileIDs {
			versionDir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
			retained := filepath.Join(versionDir, cache.RetainedSourceName(fileID))
			if _, statErr := os.Stat(retained); statErr != nil {
				continue // nothing retained (legacy ingest): Task 11's needs_reingest covers it
			}
			if mc == nil {
				var mcErr error
				if mc, mcErr = s.mergeCompilerForGame(game); mcErr != nil {
					return warnings, mcErr
				}
			}
			if _, convertible := mc.ClassifyMergeSource(fileID); !convertible {
				continue
			}
			ref := mod.SourceID + ":" + mod.ID
			_, failed := failedByRef[ref]
			participating := game.ConvertPaks && mod.ConvertPaks && !failed

			manifests, merr := gameCache.FileManifests(game.ID, mod.SourceID, mod.ID, mod.Version)
			if merr != nil {
				return warnings, merr
			}
			// FileManifests keys its map by fileID directly (see
			// internal/storage/cache/cache.go's fileManifestsAt) - no FileID
			// field on FileManifest itself.
			existing := manifests[fileID]
			currentMembers, recorded := existing.Members, existing.Recorded

			if participating {
				if recorded && len(currentMembers) == 0 {
					continue // already converged
				}
				// #221 partial-failure fix: Uninstall FIRST, mark SECOND.
				// Installer.Uninstall targets cache.ListFiles' full on-disk
				// union, never the manifest, so its correctness does not
				// depend on the mark having already flipped - unlike the
				// raw-fallback branch below, reordering here is safe. It
				// also closes the window a mark-then-uninstall order would
				// leave open: if Uninstall fails, the mark is never
				// written, so this mod's manifest still reads "raw"
				// (non-empty currentMembers) and the "already converged"
				// check above stays false - the NEXT reconcile pass (any
				// later sync, including the fast path) retries. Uninstall
				// is idempotent (linker.Undeploy tolerates an already-
				// absent path), so a retry after a PARTIAL Uninstall - or
				// one that actually succeeded but errored on DB bookkeeping
				// - is always safe, never a double-apply: the raw copy is
				// never claimed as "gone" (members=nil) before it actually
				// is.
				// Single-mod Installer.Uninstall, not UninstallBatch - no
				// before/after hooks run for this undeploy (hooks are only
				// wired through the batch path's BatchOptions). User-observable:
				// a game's uninstall/before_each/after_each hooks never fire
				// for this reconcile-driven raw-pak takedown.
				if uerr := installer.Uninstall(ctx, game, &mod.Mod, profileName); uerr != nil {
					return warnings, fmt.Errorf("undeploying raw pak for %s: %w", ref, uerr)
				}
				if werr := cache.MarkFileCompleteWithMembers(versionDir, fileID, nil); werr != nil {
					return warnings, fmt.Errorf("flipping %s to merged-claimed: %w", ref, werr)
				}
			} else {
				// #241 fix: claim ONLY the member(s) belonging to THIS
				// fileID, never gameCache.ListFiles' entry-wide union.
				// deployableFiles unions every recorded manifest's members
				// before narrowing (internal/core/deployable.go), so a mod
				// carrying a SECOND pak fileID would have had the first
				// fileID's member claimed as if it belonged to the second
				// too. ListFiles still supplies the candidates;
				// rawPakMembers narrows them to this fileID's own member(s)
				// by content identity with its retained source - see its
				// doc comment for why that is the one attribution that
				// survives a convert flip.
				files, lerr := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
				if lerr != nil {
					return warnings, lerr
				}
				members, aerr := rawPakMembers(versionDir, retained, files)
				if aerr != nil {
					return warnings, aerr
				}
				if len(members) == 0 {
					// #250 heal: no cache member matches the retained
					// source - the deployable copy was pruned while this pak
					// was converted (members=nil made it unclaimed, and
					// released PruneUnclaimed versions deleted it on any
					// sibling-file re-ingest). The retained source still
					// holds the exact bytes, so restore the copy and claim
					// it. Without this, the mark below would record an EMPTY
					// member set and Install would deploy nothing, silently
					// - the raw fallback's whole purpose defeated.
					restoredName, herr := restoreRawPakCopy(mc, versionDir, retained, fileID, mod.ID)
					if herr != nil {
						return warnings, fmt.Errorf("restoring pruned raw pak for %s: %w", ref, herr)
					}
					members = []string{restoredName}
				}
				// #221 partial-failure fix: a manifest-shape match alone
				// (recorded + matching member set) does not prove the raw
				// pak is ACTUALLY deployed - the mark below must be written
				// BEFORE Install (Installer.Install's deployableFiles
				// narrowing reads THIS manifest to decide what to deploy,
				// so writing the target members first is what makes Install
				// deploy the right file at all; unlike the participating
				// branch above, this order can't be flipped). That means a
				// PRIOR pass's Install could have failed AFTER the mark was
				// already written, leaving the manifest saying "raw" while
				// nothing is actually on disk - a shape-only check would
				// then treat that as converged forever. Confirming every
				// claimed member is actually present under game.ModPath
				// (mirroring syncMergedPak's own outer fast-path stat
				// check for the merged artifact) closes that gap: the next
				// reconcile pass retries instead of masking it.
				if recorded && len(members) > 0 && sameMemberSet(currentMembers, members) && allMembersDeployed(game.ModPath, currentMembers) {
					continue // already converged (raw)
				}
				if werr := cache.MarkFileCompleteWithMembers(versionDir, fileID, members); werr != nil {
					return warnings, fmt.Errorf("flipping %s to raw-deploy: %w", ref, werr)
				}
				// Single-mod Installer.Install, not InstallBatch - same hook
				// gap as the Uninstall call above: no install hooks run for
				// this reconcile-driven raw-pak fallback deploy.
				if ierr := installer.Install(ctx, game, &mod.Mod, profileName); ierr != nil {
					return warnings, fmt.Errorf("deploying raw pak for %s: %w", ref, ierr)
				}
			}
		}
	}
	return warnings, nil
}

// allMembersDeployed reports whether every one of members (version-dir-
// relative paths, as recorded by a cache manifest) is actually present
// under modPath - the on-disk confirmation reconcilePakManifests' raw-
// fallback branch needs so it never trusts recorded manifest shape alone
// as proof of a completed deploy (#221).
func allMembersDeployed(modPath string, members []string) bool {
	for _, m := range members {
		if _, err := os.Stat(filepath.Join(modPath, m)); err != nil {
			return false
		}
	}
	return true
}

// rawPakMembers returns the version-dir-relative cache members that belong
// to ONE pak fileID (#241): the candidate entry files whose content matches
// the fileID's retained source at retainedPath. Ingest stages a
// convert-eligible pak's deployable copy from the SAME bytes as its
// retained source (DownloadModToCache and Import both copy the one archive
// to both names), and that copy is the only member a pak fileID ever
// contributes - so content identity with the retained source IS the
// cache's fileID->member attribution. It is also the only attribution that
// survives a convert flip: the participating branch's members=nil mark
// erases the ingest-time manifest, so the way BACK to raw cannot simply
// read the mapping from FileManifests.
//
// Size is compared before hashing, so unrelated candidates (sibling
// fileIDs' members) are skipped without reading their bytes; the retained
// source itself is hashed at most once. Two pak fileIDs with byte-identical
// content would each claim both copies - degenerate but harmless: the
// claimed union (what deploy and prune consume) is identical either way.
func rawPakMembers(versionDir, retainedPath string, candidates []string) ([]string, error) {
	retainedInfo, err := os.Stat(retainedPath)
	if err != nil {
		return nil, fmt.Errorf("reading retained source: %w", err)
	}
	var retainedSum string
	var members []string
	for _, f := range candidates {
		fullPath := filepath.Join(versionDir, f)
		info, err := os.Stat(fullPath)
		if err != nil {
			return nil, fmt.Errorf("reading cache member %s: %w", f, err)
		}
		if info.Size() != retainedInfo.Size() {
			continue
		}
		if retainedSum == "" {
			if retainedSum, err = md5File(retainedPath); err != nil {
				return nil, fmt.Errorf("hashing retained source: %w", err)
			}
		}
		sum, err := md5File(fullPath)
		if err != nil {
			return nil, fmt.Errorf("hashing cache member %s: %w", f, err)
		}
		if sum == retainedSum {
			members = append(members, f)
		}
	}
	return members, nil
}

// rawPakRestoreName picks the on-disk name restoreRawPakCopy publishes a
// healed deployable pak copy under (#250). An import-path fileID IS the
// original archive filename (Importer.Import stages the deployable copy
// under exactly that name), so the restore is name-exact - detected by
// asking the compile source whether the fileID names one of its
// convertible artifacts (#256: the format test lives behind the seam). A
// download-path fileID (the literal icarus "pak") never carried the
// deployable name: it came from the download URL's basename at ingest
// time, the convert flip erased the manifest that recorded it, and
// nothing else durably stores it - so the source synthesizes its
// deterministic mod-scoped fallback name instead
// (mc.RestoredArtifactName; Icarus's "_P.pak" override convention). The
// import-vs-download provenance SPLIT stays in core - it follows from how
// ingest keys fileIDs, which is uniform across games - while both format
// questions inside it are the source's. Both inputs are
// source-controlled, so both are Base'd before use as a path component.
func rawPakRestoreName(mc source.MergeCompiler, fileID, modID string) string {
	base := filepath.Base(fileID)
	if mc.IsConvertibleArtifact(base) {
		return base
	}
	return mc.RestoredArtifactName(filepath.Base(modID))
}

// restoreRawPakCopy re-creates fileID's deployable pak copy in versionDir
// from its retained source at retainedPath and returns the restored name
// (#250) - the heal for a cache entry a released PruneUnclaimed damaged
// (deployable copy missing, retained source present, manifest still in the
// post-convert members=nil state). It refuses to overwrite an existing
// file at the target name: such a file necessarily holds DIFFERENT content
// (rawPakMembers just matched nothing), and it could be a sibling fileID's
// claimed member - failing loudly beats corrupting it, and the next
// reconcile pass retries.
func restoreRawPakCopy(mc source.MergeCompiler, versionDir, retainedPath, fileID, modID string) (string, error) {
	name := rawPakRestoreName(mc, fileID, modID)
	target := filepath.Join(versionDir, name)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("restore target %s already exists with content not matching the retained source; refusing to overwrite", name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking restore target %s: %w", name, err)
	}
	if err := copyFileStreaming(retainedPath, target); err != nil {
		return "", fmt.Errorf("restoring deployable copy %s: %w", name, err)
	}
	return name, nil
}

// sameMemberSet reports whether a and b claim the same members, ignoring
// order: the two sides come from different producers (a manifest read back
// in mark-time order vs a fresh ListFiles walk), and "same claim" is a set
// property. Duplicates are counted, not collapsed, so a doubled entry on
// one side can never mask a missing one.
func sameMemberSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, m := range a {
		counts[m]++
	}
	for _, m := range b {
		if counts[m] == 0 {
			return false
		}
		counts[m]--
	}
	return true
}

// classifyCompileDeployMods pre-classifies each mod DeployProfile is about
// to deploy on a DeployCompile game (#255): a mod contributing at least one
// enabled merge source is DeployModMerged (its content will ride the merged
// artifact built after the loop); a mod holding a retained convertible file
// that enabledMergeSources excluded - the game- or mod-level ConvertPaks
// opt-out (#221) - is DeployModRaw (it deploys its raw copy individually);
// everything else is the zero DeployModIndividual. Keyed by
// domain.ModKey(sourceID, modID), the same "sourceID:modID" form
// MergeSource.ModRef carries. Best-effort by design - classification is a
// readout, never a reason to fail the deploy, and the zero class is always
// a safe rendering default: a non-compile game or an enabledMergeSources
// failure returns nil, and a compiler-resolution failure mid-walk (only
// reachable when nothing enabled is retained but a mod in mods still holds
// a retained file, e.g. a disabled mod under --all with no compile source
// configured) returns the classes computed so far.
func (s *Service) classifyCompileDeployMods(ctx context.Context, game *domain.Game, profileName string, mods []*domain.InstalledMod) map[string]DeployModClass {
	if game.DeployMode != domain.DeployCompile {
		return nil
	}
	sources, err := s.enabledMergeSources(ctx, game, profileName)
	if err != nil {
		return nil
	}
	mergedRefs := make(map[string]bool, len(sources))
	for _, src := range sources {
		mergedRefs[src.ModRef] = true
	}
	gameCache := s.GetGameCache(game)
	// Lazily resolved on the first retained file found, exactly like
	// enabledMergeSources/reconcilePakManifests (#256).
	var mc source.MergeCompiler
	classes := make(map[string]DeployModClass, len(mods))
	for _, mod := range mods {
		ref := domain.ModKey(mod.SourceID, mod.ID)
		if mergedRefs[ref] {
			classes[ref] = DeployModMerged
			continue
		}
		for _, fileID := range mod.FileIDs {
			retained := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(fileID))
			if _, statErr := os.Stat(retained); statErr != nil {
				continue // nothing retained for this fileID - not a merge source of any kind
			}
			if mc == nil {
				var mcErr error
				if mc, mcErr = s.mergeCompilerForGame(game); mcErr != nil {
					return classes
				}
			}
			if _, convertible := mc.ClassifyMergeSource(fileID); convertible {
				classes[ref] = DeployModRaw
				break
			}
		}
	}
	return classes
}

// recordMergeOutcome reports #255's post-sync merge readout: after a
// successful syncMergedPak on a DeployCompile game, the just-written merge
// fingerprint (MergedPakOutcomes) is the authoritative record of which
// mods' content the merged artifact carries and which participants fell
// back to a raw individual deploy. The artifact name and counts land on
// result (for progress-less callers) and as one DeployMergeSynced
// event. A missing fingerprint means no merged artifact exists (zero merge
// participants - the uninstall-to-zero path) so there is nothing to report;
// resolution failures likewise skip the readout rather than fail an
// already-successful deploy. Counts are per MOD, not per file: a mod
// contributing both a converted and a failed file counts once on each side,
// which is the accurate reading of that (rare) state.
func (s *Service) recordMergeOutcome(ctx context.Context, game *domain.Game, profileName string, result *DeployResult, emit func(DeployProgress)) {
	if game.DeployMode != domain.DeployCompile {
		return
	}
	outcomes, ok := s.MergedPakOutcomes(ctx, game, profileName)
	if !ok {
		return
	}
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return
	}
	merged := make(map[string]bool, len(outcomes))
	raw := make(map[string]bool)
	for _, o := range outcomes {
		ref := domain.ModKey(o.SourceID, o.ModID)
		if o.Converted {
			merged[ref] = true
		} else {
			raw[ref] = true
		}
	}
	result.MergedArtifact = mc.MergedArtifactName()
	result.MergedMods = len(merged)
	result.RawFallbacks = len(raw)
	emit(DeployProgress{Phase: DeployMergeSynced, Total: result.MergedMods, Detail: result.MergedArtifact, RawFallbacks: result.RawFallbacks})
}

// MergedPakOutcomes returns the stored merge fingerprint's per-mod entries
// (with #221 conversion outcomes), if a merged pak exists for game+profile.
// The game's compile source interprets the stored Kind strings (#256); a
// stored fingerprint implies a source produced it, so failing to resolve
// one now (unconfigured since) reads as "no outcomes available".
func (s *Service) MergedPakOutcomes(ctx context.Context, game *domain.Game, profileName string) ([]MergedFingerprintEntry, bool) {
	gameCache := s.GetGameCache(game)
	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	fp, ok := readMergedFingerprint(cachePath)
	if !ok {
		return nil, false
	}
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return nil, false
	}
	return normalizeOutcomes(fp.Mods, mc.ClassifyMergeSource), true
}

// normalizeOutcomes forces a trivially-successful outcome on every
// non-convertible entry (#221 C1 fix): conversion failure is definitionally
// a convertible-kind concern, but a pre-#221 fingerprint marker unmarshals
// its (exmodz-only, at the time) entries as Kind:"", Converted:false -
// those fields didn't exist yet, and fingerprintInputs/
// mergedFingerprintsEqual deliberately never regenerate them for an
// unchanged profile (input equality ignores outcomes). Without this
// normalization, every consumer of MergedPakOutcomes (verify's
// conversion_failed rows, status's conversion-failure counts) would report
// a spurious, permanent "CONVERSION FAILED" for every exmodz mod on any
// profile that predates #221 - forever, since nothing ever rewrites the
// stored marker's outcome fields for inputs that haven't changed. Kind==""
// is the legacy shape and the current native kind is the modern one - the
// classifier maps both to non-convertible, therefore trivially "converted".
func normalizeOutcomes(mods []MergedFingerprintEntry, classify mergeSourceClassifier) []MergedFingerprintEntry {
	out := make([]MergedFingerprintEntry, len(mods))
	for i, m := range mods {
		if _, convertible := classify(m.Kind); !convertible {
			m.Converted = true
			m.FailReason = ""
		}
		out[i] = m
	}
	return out
}

// PakNeedsReingest reports whether mod's fileID is a convert-eligible pak
// whose cache entry predates #221 pak retention (deployable pak present, no
// retained source) - the lazy-migration detector verify --fix uses to heal a
// pre-#221 pak install (design §6): the widened ingest predicate (Task 9)
// retains the source on redownload, and the next SyncMergedPak picks it up.
// Gated on BOTH game- and mod-level ConvertPaks (like enabledMergeSources'
// own participation gate) so a legacy/opted-out mod is never touched.
func (s *Service) PakNeedsReingest(ctx context.Context, game *domain.Game, mod *domain.InstalledMod, fileID string) (bool, error) {
	if game.DeployMode != domain.DeployCompile || !game.ConvertPaks || !mod.ConvertPaks {
		return false, nil
	}
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return false, err
	}
	if _, convertible := mc.ClassifyMergeSource(fileID); !convertible {
		return false, nil
	}
	gameCache := s.GetGameCache(game)
	versionDir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	if _, err := os.Stat(filepath.Join(versionDir, cache.RetainedSourceName(fileID))); err == nil {
		return false, nil // already retained
	} else if !os.IsNotExist(err) {
		return false, err
	}
	// Only flag entries that actually exist (an entirely-missing cache entry
	// is the MISSING status's business, not ours).
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil || len(files) == 0 {
		return false, err
	}
	return true, nil
}

// readMergedFingerprint reads and decodes cachePath's stored merge
// fingerprint marker, if any. ok is false when no cache entry/marker
// exists yet (first-ever merge for this profile) or the marker is
// unreadable/corrupt - both degrade to "regenerate", never a crash or a
// false "unchanged".
func readMergedFingerprint(cachePath string) (fp MergedFingerprint, ok bool) {
	data, err := os.ReadFile(cache.MergeFingerprintPath(cachePath))
	if err != nil {
		return MergedFingerprint{}, false
	}
	if err := json.Unmarshal(data, &fp); err != nil {
		return MergedFingerprint{}, false
	}
	return fp, true
}

// currentMergedFingerprint computes what game+profileName's merged pak
// SHOULD look like right now: the live base pak's IndexHash plus every
// currently-enabled exmodz mod's identity/version/content checksum, in
// profile load order. Returns a nil sources/zero-value fingerprint (not an
// error) when there is nothing to merge - callers distinguish "nothing to
// do" from "failed to compute" via the returned slice's length, exactly
// like syncMergedPak's own zero-sources branch does.
func (s *Service) currentMergedFingerprint(ctx context.Context, game *domain.Game, profileName string) (MergedFingerprint, []source.MergeSource, error) {
	sources, err := s.enabledMergeSources(ctx, game, profileName)
	if err != nil {
		return MergedFingerprint{}, nil, fmt.Errorf("listing enabled merge sources: %w", err)
	}
	if len(sources) == 0 {
		return MergedFingerprint{}, sources, nil
	}

	// Non-empty sources imply enabledMergeSources already resolved the
	// compile source, so this cannot newly fail (#256).
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return MergedFingerprint{}, sources, err
	}
	basePakPath, err := mc.ResolveBaseArtifact(game)
	if err != nil {
		return MergedFingerprint{}, sources, err
	}
	liveHash, err := mc.FingerprintBase(basePakPath)
	if err != nil {
		return MergedFingerprint{}, sources, fmt.Errorf("reading base pak for merge fingerprint: %w", err)
	}

	current := MergedFingerprint{BaseIndexHash: liveHash}
	for _, src := range sources {
		sum, herr := md5File(src.SourcePath)
		if herr != nil {
			return MergedFingerprint{}, sources, fmt.Errorf("hashing %s: %w", src.SourcePath, herr)
		}
		sourceID, modID, _ := strings.Cut(src.ModRef, ":")
		current.Mods = append(current.Mods, MergedFingerprintEntry{SourceID: sourceID, ModID: modID, Checksum: sum, Kind: src.Kind, Converted: true})
	}

	mods, err := s.GetInstalledModsInProfileOrder(ctx, game.ID, profileName)
	if err != nil {
		return MergedFingerprint{}, sources, fmt.Errorf("loading profile mods: %w", err)
	}
	versionByRef := make(map[string]string, len(mods))
	for _, m := range mods {
		versionByRef[m.SourceID+":"+m.ID] = m.Version
	}
	for i, src := range sources {
		current.Mods[i].Version = versionByRef[src.ModRef]
	}

	return current, sources, nil
}

// CheckMergedPakStaleness reports whether game+profileName's merged pak no
// longer matches the current enabled-mod set/order/versions/base pak
// (#197, generalizing #196's per-mod CheckBaseStaleness to the merged
// model). Returns nil, nil - not an error - when the merged pak is
// up to date, when there is nothing to merge (zero enabled exmodz mods),
// or when game is not a DeployCompile game.
func (s *Service) CheckMergedPakStaleness(ctx context.Context, game *domain.Game, profileName string) (*domain.Update, error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}

	current, sources, err := s.currentMergedFingerprint(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	// Non-empty sources imply currentMergedFingerprint already resolved the
	// compile source, so this cannot newly fail (#256).
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return nil, err
	}

	gameCache := s.GetGameCache(game)
	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	stored, ok := readMergedFingerprint(cachePath)
	// #197 postsmoke UX fix: reason defaults to the fingerprint-mismatch
	// case ("base pak updated") and is only downgraded to "not deployed"
	// below, once we know the fingerprint actually matched - callers
	// (verify/update's console output) must not blame the base pak when
	// the real cause is a missing artifact.
	reason := "base pak updated"
	if ok {
		if eq, eqErr := mergedFingerprintsEqual(current, stored, mc.ClassifyMergeSource); eqErr == nil && eq {
			// #197 I5 fix: mirrors syncMergedPak's identical fast-path
			// check - a matching fingerprint alone doesn't prove the pak
			// is actually deployed (a prior failed Install, or a purge
			// that intentionally kept the cache entry, #197 I2). Without
			// this, `lmm update`/`lmm verify` would report "up to date"
			// for a profile whose game directory doesn't actually hold
			// the merged pak at all - the exact wedge this fix closes.
			if _, statErr := os.Stat(filepath.Join(game.ModPath, mc.MergedArtifactName())); statErr == nil {
				return nil, nil
			}
			reason = "not deployed"
		}
	}

	return &domain.Update{
		InstalledMod: domain.InstalledMod{
			Mod: domain.Mod{
				ID: mergedPakModID, SourceID: domain.SourceMerged,
				Name: mc.MergedArtifactLabel(), Version: mergedPakVersion, GameID: game.ID,
			},
		},
		NewVersion:      mergedPakVersion,
		RecompileNeeded: true,
		RecompileReason: reason,
	}, nil
}

// ApplyMergedPakRegen regenerates game+profileName's merged pak (#197 -
// replaces #196's per-mod ApplyRecompile). No lock gate: a locked mod's
// retained exmodz still participates in every re-merge (design decision 3
// - locking pins THAT mod's own version, it does not freeze the whole
// merged pak or exclude the mod's diff; reading a locked mod's retained
// source to feed the merge is not "touching" it in the sense a lock
// protects against).
func (s *Service) ApplyMergedPakRegen(ctx context.Context, game *domain.Game, profileName string, progress func(DeployProgress)) (*UpdateApplyResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &UpdateApplyResult{}, err
	}
	defer release()
	return s.applyMergedPakRegen(ctx, game, profileName, progress)
}

func (s *Service) applyMergedPakRegen(ctx context.Context, game *domain.Game, profileName string, progress func(DeployProgress)) (*UpdateApplyResult, error) {
	result := &UpdateApplyResult{}
	// Resolved up front: the Applied entry below reports the merged
	// artifact by the name only the compile source knows (#256), and a
	// regen request for a game without one is a misconfiguration worth
	// failing loud on before touching anything.
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		return result, err
	}
	warnings, err := s.syncMergedPak(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	result.Applied = []string{mc.MergedArtifactName()}
	if progress != nil {
		progress(DeployProgress{Phase: UpdateDownloadDone})
	}
	return result, nil
}

// PurgeMergedPak explicitly undeploys game+profileName's merged pak (#197
// I2 fix). `lmm purge`'s own contract is "remove ALL deployed mod files...
// resetting the game directory back to its pre-modded state" - but exmodz
// mods deploy EXCLUSIVELY through this one shared artifact, never their
// own per-mod cache entry (Task 2/3), so purgeMods' per-real-mod
// Installer.Uninstall loop is a no-op for every one of them. Nor can this
// be left to syncMergedPak's own fingerprint-diffing: a plain (non-
// --uninstall) purge intentionally leaves each mod's Enabled bit
// untouched (only Deployed flips), so the merge INPUTS never change and
// syncMergedPak's fast path would silently do nothing, leaving the pak
// deployed - the exact regression this fixes.
//
// deleteCache mirrors purge's own --uninstall distinction for real mods:
// false (plain purge) undeploys the FILE but keeps the cache entry and
// fingerprint, so a later `lmm deploy` redeploys the identical merged pak
// without recomputing anything (relies on syncMergedPak's fast path also
// confirming the deployed artifact still exists - #197 I5's fix); true
// (--uninstall) also clears the cache entry, matching every real mod's
// full removal.
func (s *Service) PurgeMergedPak(ctx context.Context, game *domain.Game, profileName string, deleteCache bool) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.purgeMergedPak(ctx, game, profileName, deleteCache)
}

func (s *Service) purgeMergedPak(ctx context.Context, game *domain.Game, profileName string, deleteCache bool) error {
	if game.DeployMode != domain.DeployCompile {
		return nil
	}
	installer, err := s.GetInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return err
	}
	syntheticMod := &domain.Mod{ID: mergedPakModID, SourceID: domain.SourceMerged, Version: mergedPakVersion, GameID: game.ID}
	if err := installer.Uninstall(ctx, game, syntheticMod, profileName); err != nil {
		return fmt.Errorf("removing merged pak: %w", err)
	}
	if deleteCache {
		gameCache := s.GetGameCache(game)
		if err := gameCache.Delete(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion); err != nil {
			return fmt.Errorf("clearing merged pak cache entry: %w", err)
		}
	}
	return nil
}
