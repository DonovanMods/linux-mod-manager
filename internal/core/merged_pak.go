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

// mergedPakModID/mergedPakVersion/mergedPakFileName identify the merged pak
// as a synthetic, singleton "mod" per (game, profile) - domain.SourceMerged
// is the matching sourceID. This reuses Installer.Install/Uninstall and
// cache.Cache verbatim (#197 design decision 2) rather than a parallel
// deploy/tracking mechanism: zero schema changes, and the SAME
// deployed_files ownership (and #168-class residue risk) as every other
// deployed file.
const (
	mergedPakModID = "merged-pak"
	// mergedPakVersion is fixed ("merged", not a real upstream version) -
	// there is exactly one merged pak per (game, profile) at any time, and
	// every regeneration REPLACES it outright (mirrors #166's directory-
	// source "replace, don't overlay" precedent) rather than versioning it.
	mergedPakVersion = "merged"
	// mergedPakFileName sorts LAST among files UE mounts from a profile's
	// mods directory: paks mount in filename-sort order and a later mount
	// wins same-path conflicts (this repo's own icarusContentMountPoint doc
	// comment, and #197's issue body, both note this) - "zzz" is a
	// long-standing UE-modding convention for "load last, highest
	// priority", so the merged pak's authoritative combined table state can
	// never be silently shadowed by a plain prebuilt .pak mod that happens
	// to also carry a table override. "LMM" makes the file greppable as
	// lmm-owned; "_P" matches this codebase's existing override-pak suffix
	// convention (compiledFileName).
	mergedPakFileName = "zzz_LMM_Merged_P.pak"
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
	Checksum string // MD5 of the retained .exmodz bytes (md5File)
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
func mergedFingerprintsEqual(a, b MergedFingerprint) (bool, error) {
	aBytes, err := marshalMergedFingerprint(a)
	if err != nil {
		return false, err
	}
	bBytes, err := marshalMergedFingerprint(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(aBytes, bBytes), nil
}

// enabledExmodzSources returns every enabled mod's retained .exmodz files
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
func (s *Service) enabledExmodzSources(game *domain.Game, profileName string) ([]source.MergeSource, error) {
	mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile mods: %w", err)
	}

	gameCache := s.GetGameCache(game)
	var sources []source.MergeSource
	for _, mod := range mods {
		if !mod.Enabled {
			continue
		}
		for _, fileID := range mod.FileIDs {
			retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(fileID))
			if _, statErr := os.Stat(retainedPath); statErr != nil {
				continue // not a retained exmodz file (a plain .pak's fileID, or nothing ingested)
			}
			sources = append(sources, source.MergeSource{
				ModRef:     mod.SourceID + ":" + mod.ID,
				ExmodzPath: retainedPath,
			})
		}
	}
	return sources, nil
}

// EnabledExmodzSourcesForTest exposes enabledExmodzSources to external
// (core_test package) tests - the method itself stays unexported since it
// is an internal implementation detail of syncMergedPak, not part of
// Service's public API.
func (s *Service) EnabledExmodzSourcesForTest(game *domain.Game, profileName string) ([]source.MergeSource, error) {
	return s.enabledExmodzSources(game, profileName)
}

// syncMergedPak regenerates game+profileName's merged pak if its recorded
// fingerprint no longer matches the CURRENT enabled-mod set/order/versions/
// base pak (#197). Cheap when nothing changed: the fast path is one
// directory read (enabledExmodzSources), one base-pak footer read
// (basePakIndexHash - never the pak's full content), and N small MD5s
// (md5File over each retained .exmodz - real files here are small, see
// #175's own research on real base-table sizes), then a byte comparison.
// Safe to call unconditionally from ANY mutation flow regardless of game
// type - it no-ops immediately for a non-DeployCompile game.
//
// Zero enabled exmodz sources uninstalls any existing merged pak instead of
// generating an empty one (#197 design decision 2's "uninstall-to-zero"
// requirement) - Installer.Uninstall on the synthetic merged-pak mod is
// idempotent when there is nothing deployed (linker.Undeploy tolerates an
// already-absent path, matching every other uninstall in this codebase),
// so calling it unconditionally here is safe even when no pak was ever
// generated.
func (s *Service) syncMergedPak(ctx context.Context, game *domain.Game, profileName string) (warnings []string, err error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}

	current, sources, err := s.currentMergedFingerprint(game, profileName)
	if err != nil {
		return nil, err
	}

	gameCache := s.GetGameCache(game)
	syntheticMod := &domain.Mod{ID: mergedPakModID, SourceID: domain.SourceMerged, Version: mergedPakVersion, GameID: game.ID}

	installer, err := s.GetInstallerForProfile(game, profileName)
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
		return nil, nil
	}

	basePakPath, err := resolveBasePak(game)
	if err != nil {
		return nil, err
	}

	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	if stored, ok := readMergedFingerprint(cachePath); ok {
		if eq, eqErr := mergedFingerprintsEqual(current, stored); eqErr == nil && eq {
			return nil, nil // fast path: nothing changed
		}
	}

	mc, err := s.mergeCompilerSourceForGame(game.ID)
	if err != nil {
		return nil, err
	}

	stagePath := cachePath + ".staging"
	if err := os.RemoveAll(stagePath); err != nil {
		return nil, fmt.Errorf("clearing merged pak staging: %w", err)
	}
	if err := os.MkdirAll(stagePath, 0755); err != nil {
		return nil, fmt.Errorf("preparing merged pak staging: %w", err)
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck

	outputPath := filepath.Join(stagePath, mergedPakFileName)
	mergeWarnings, err := mc.MergeCompile(ctx, basePakPath, sources, outputPath)
	if err != nil {
		return nil, fmt.Errorf("merging %d exmodz mod(s): %w", len(sources), err)
	}
	warnings = mergeWarnings

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
	return warnings, nil
}

// SyncMergedPakForTest exposes syncMergedPak to external (core_test
// package) tests - see enabledExmodzSources/EnabledExmodzSourcesForTest's
// identical rationale.
func (s *Service) SyncMergedPakForTest(ctx context.Context, game *domain.Game, profileName string) ([]string, error) {
	return s.syncMergedPak(ctx, game, profileName)
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
func (s *Service) currentMergedFingerprint(game *domain.Game, profileName string) (MergedFingerprint, []source.MergeSource, error) {
	sources, err := s.enabledExmodzSources(game, profileName)
	if err != nil {
		return MergedFingerprint{}, nil, fmt.Errorf("listing enabled exmodz mods: %w", err)
	}
	if len(sources) == 0 {
		return MergedFingerprint{}, sources, nil
	}

	basePakPath, err := resolveBasePak(game)
	if err != nil {
		return MergedFingerprint{}, sources, err
	}
	liveHash, err := basePakIndexHash(basePakPath)
	if err != nil {
		return MergedFingerprint{}, sources, fmt.Errorf("reading base pak for merge fingerprint: %w", err)
	}

	current := MergedFingerprint{BaseIndexHash: liveHash}
	for _, src := range sources {
		sum, herr := md5File(src.ExmodzPath)
		if herr != nil {
			return MergedFingerprint{}, sources, fmt.Errorf("hashing %s: %w", src.ExmodzPath, herr)
		}
		sourceID, modID, _ := strings.Cut(src.ModRef, ":")
		current.Mods = append(current.Mods, MergedFingerprintEntry{SourceID: sourceID, ModID: modID, Checksum: sum})
	}

	mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
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
func (s *Service) CheckMergedPakStaleness(game *domain.Game, profileName string) (*domain.Update, error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}

	current, sources, err := s.currentMergedFingerprint(game, profileName)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	gameCache := s.GetGameCache(game)
	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	stored, ok := readMergedFingerprint(cachePath)
	if ok {
		if eq, eqErr := mergedFingerprintsEqual(current, stored); eqErr == nil && eq {
			return nil, nil
		}
	}

	return &domain.Update{
		InstalledMod: domain.InstalledMod{
			Mod: domain.Mod{
				ID: mergedPakModID, SourceID: domain.SourceMerged,
				Name: "Icarus Merged Pak", Version: mergedPakVersion, GameID: game.ID,
			},
		},
		NewVersion:      mergedPakVersion,
		RecompileNeeded: true,
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
	result := &UpdateApplyResult{}
	warnings, err := s.syncMergedPak(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	result.Applied = []string{mergedPakFileName}
	if progress != nil {
		progress(DeployProgress{Phase: UpdateDownloadDone})
	}
	return result, nil
}
