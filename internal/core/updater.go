package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// Updater checks for and applies mod updates
type Updater struct {
	registry *source.Registry
}

// NewUpdater creates a new updater
func NewUpdater(registry *source.Registry) *Updater {
	return &Updater{
		registry: registry,
	}
}

// CheckUpdates checks for available updates for installed mods. game supplies
// the source-ID mapping: installed rows persist the lmm game ID, but sources
// like NexusMods address games by their own domain, so each source's batch is
// translated via game.SourceIDs before the call (empty mapping = keep the lmm
// id, matching the search-side semantics in Service.SearchMods/GetMod).
func (u *Updater) CheckUpdates(ctx context.Context, game *domain.Game, installed []domain.InstalledMod) ([]domain.Update, error) {
	var checkable []domain.InstalledMod
	for _, mod := range installed {
		if UpdateCheckable(mod) {
			checkable = append(checkable, mod)
		}
	}

	if len(checkable) == 0 {
		return nil, nil
	}

	// Group mods by source
	bySource := make(map[string][]domain.InstalledMod)
	for _, mod := range checkable {
		bySource[mod.SourceID] = append(bySource[mod.SourceID], mod)
	}

	var allUpdates []domain.Update
	var checkErrs []error

	// Check each source
	for sourceID, mods := range bySource {
		select {
		case <-ctx.Done():
			return allUpdates, ctx.Err()
		default:
		}

		src, err := u.registry.Get(sourceID)
		if err != nil {
			checkErrs = append(checkErrs, fmt.Errorf("source %s: %w", sourceID, err))
			continue
		}

		if game != nil {
			if mappedID, ok := game.SourceIDs[sourceID]; ok && mappedID != "" {
				translated := make([]domain.InstalledMod, len(mods))
				copy(translated, mods)
				for i := range translated {
					translated[i].GameID = mappedID
				}
				mods = translated
			}
		}

		updates, err := src.CheckUpdates(ctx, mods)
		allUpdates = append(allUpdates, updates...)
		if err != nil {
			checkErrs = append(checkErrs, fmt.Errorf("source %s: %w", sourceID, err))
		}
	}

	if len(checkErrs) > 0 {
		return allUpdates, fmt.Errorf("update check had %d source error(s): %w", len(checkErrs), errors.Join(checkErrs...))
	}
	return allUpdates, nil
}

// UpdateCheckable reports whether CheckUpdates will query a source for mod.
// Two reasons it will not: the mod is pinned (a user choice, reversible with
// `lmm mod set-update`), or it is a local import with no remote to ask.
//
// Exported because both interfaces need to explain the gap between "mods
// installed" and "mods checked" - without it, a filtered mod silently vanishes
// from update output and the user is told everything is up to date. Callers
// must use this rather than re-testing the fields, so the reported counts can
// never drift from what CheckUpdates actually skipped.
func UpdateCheckable(mod domain.InstalledMod) bool {
	return mod.UpdatePolicy != domain.UpdatePinned && mod.SourceID != domain.SourceLocal
}

// UpdateSkips counts the mods CheckUpdates will filter out, by reason. The two
// are reported separately because the remedies differ: a pin can be lifted, a
// local mod can never be checked.
type UpdateSkips struct {
	Pinned int
	Local  int
}

// Total is the number of mods that will not be checked at all.
func (s UpdateSkips) Total() int { return s.Pinned + s.Local }

// CountUpdateSkips tallies why CheckUpdates will skip mods in installed. A mod
// that is both pinned and local counts once, as pinned, so Total never exceeds
// len(installed).
func CountUpdateSkips(installed []domain.InstalledMod) UpdateSkips {
	var s UpdateSkips
	for _, mod := range installed {
		switch {
		case mod.UpdatePolicy == domain.UpdatePinned:
			s.Pinned++
		case mod.SourceID == domain.SourceLocal:
			s.Local++
		}
	}
	return s
}

// GetAutoUpdateMods filters installed mods to those with auto-update enabled
func (u *Updater) GetAutoUpdateMods(installed []domain.InstalledMod) []domain.InstalledMod {
	var autoUpdate []domain.InstalledMod
	for _, mod := range installed {
		if mod.UpdatePolicy == domain.UpdateAuto {
			autoUpdate = append(autoUpdate, mod)
		}
	}
	return autoUpdate
}

// CompareVersions delegates to domain.CompareVersions.
// Kept for backward compatibility with existing callers.
func CompareVersions(v1, v2 string) int {
	return domain.CompareVersions(v1, v2)
}

// IsNewerVersion delegates to domain.IsNewerVersion.
// Kept for backward compatibility with existing callers.
func IsNewerVersion(currentVersion, newVersion string) bool {
	return domain.IsNewerVersion(currentVersion, newVersion)
}

// CheckBaseStaleness scans installed for DeployCompile mods whose compiled
// artifact no longer matches game's live base data.pak IndexHash (#196,
// "the Friday problem": a weekly base-pak refresh silently reverts the
// tables a compiled mod patches, and nothing before #196 ever noticed).
// This is entirely local/offline - unlike Updater.CheckUpdates it never
// contacts a source, so it runs for EVERY installed mod regardless of
// UpdatePolicy or SourceID, including pinned and domain.SourceLocal mods
// (pinning fixes the mod version, not the base pak; a pure local import has
// no remote to check version-wise but can still go stale against the base
// pak - see #196 design point 3).
//
// Only entries carrying the #196 fingerprint (cache.BaseIndexHashes)
// participate: a missing fingerprint is treated as "not a compiled entry we
// can reason about", not "stale" - a game whose DeployMode is DeployCompile
// can still legitimately serve prebuilt, never-compiled .pak files (see
// isExmodzFile's doc comment), and there is no local signal to tell those
// apart from a compiled entry that merely predates #196. (Design point 4's
// literal "missing fingerprint = always stale" was deliberately narrowed
// during #196 review to avoid exactly that false positive - see the PR
// description.) Any mod actually compiled under #196 always carries the
// fingerprint by construction (stageCompileFingerprint writes it in the
// SAME atomic commit as the compiled pak), so this narrowing costs nothing
// for anything compiled going forward.
func (s *Service) CheckBaseStaleness(game *domain.Game, installed []domain.InstalledMod) ([]domain.Update, error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}
	basePakPath, err := resolveBasePak(game)
	if err != nil {
		// No installed base pak to compare against: nothing to report this
		// pass, not an error - matches CheckUpdates' own per-source
		// tolerance for a signal that simply isn't available right now.
		return nil, nil //nolint:nilerr
	}
	liveHash, err := basePakIndexHash(basePakPath)
	if err != nil {
		return nil, fmt.Errorf("reading base pak for staleness check: %w", err)
	}

	gameCache := s.GetGameCache(game)
	var stale []domain.Update
	for _, mod := range installed {
		hashes, err := gameCache.BaseIndexHashes(game.ID, mod.SourceID, mod.ID, mod.Version)
		if err != nil {
			continue // unreadable bookkeeping: silently skip, matching FileManifests' own tolerance
		}
		for _, recorded := range hashes {
			if recorded != liveHash {
				stale = append(stale, domain.Update{InstalledMod: mod, NewVersion: mod.Version, RecompileNeeded: true})
				break
			}
		}
	}
	return stale, nil
}

// CheckGameUpdates is the single seam CLI and TUI both check updates
// through (#196): it combines Updater.CheckUpdates' remote version checks
// with CheckBaseStaleness' local base-pak staleness scan, so "does this mod
// need attention" means the same thing in both interfaces. A mod that
// already has a real version update available is not separately reported
// as stale even if it is: applying that update recompiles it fresh against
// the CURRENT base pak as a normal side effect of the compile step, so
// there is nothing left to flag once the real update lands.
//
// Errors from either half are tolerated the same way CheckUpdates already
// tolerates a single source failing: whatever updates were found are still
// returned, with the first non-nil error surfaced (checkErr takes priority
// as the richer, multi-source diagnostic when both fail).
func (s *Service) CheckGameUpdates(ctx context.Context, game *domain.Game, installed []domain.InstalledMod) ([]domain.Update, error) {
	updates, checkErr := s.NewUpdater().CheckUpdates(ctx, game, installed)

	stale, staleErr := s.CheckBaseStaleness(game, installed)
	if staleErr != nil && checkErr == nil {
		checkErr = staleErr
	}

	if len(stale) > 0 {
		reported := make(map[string]bool, len(updates))
		for _, u := range updates {
			reported[domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)] = true
		}
		for _, u := range stale {
			key := domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)
			if !reported[key] {
				updates = append(updates, u)
				reported[key] = true
			}
		}
	}

	return updates, checkErr
}

// ApplyRecompile recompiles mod IN PLACE at its CURRENT version against
// game's live base pak (#196: a base data.pak refresh left its already-
// deployed compile(s) patching stale tables - "the Friday problem").
// Every compiled entry recorded for mod (cache.BaseIndexHashes) is
// recompiled, not just the ones CheckBaseStaleness found mismatched - a
// mod's compiled files always share one base pak, so a partial refresh
// would leave the entry internally inconsistent for no benefit.
//
// Recompiles from each file's retained .exmodz (offline) when present;
// falls back to re-downloading it from mod's source when the retained copy
// is missing AND a real fileID/source connection exists (a download-
// compiled entry). An import-compiled entry has no such connection (Import
// resolves no real source file ID - see stageCompileFingerprint) and a
// domain.SourceLocal mod has no source at all either way: for both, a
// missing retained source fails loud naming the fix (re-import/re-add the
// mod) rather than guessing.
//
// Locked mods are refused outright before any work happens - mirrors
// ApplyUpdate's own lock gate exactly (lock-wins: recompiling still
// rewrites the mod's deployed FILES even though its Version doesn't move,
// which is exactly what a lock forbids). Pinned mods ARE recompiled -
// pinning fixes the mod's VERSION, not the base pak (#196 design point 3);
// callers must not route a pinned mod through this gate at all.
//
// The cache update is staged and committed atomically (this package's own
// staging/commit pattern - prepareStaging + commitStagedCache), so a
// mid-recompile failure never touches the existing good entry. Redeploy
// reuses Installer.ReplaceForUpdate with the mod's OWN file IDs on both
// sides of the transition: with nothing IDs-wise changing, it degrades to
// its historical union-replace behavior, which simply re-links/re-copies
// every current member - exactly what refreshes a non-symlink deployment's
// stale on-disk bytes (a symlink deployment already reflects the new cache
// content once the atomic swap above lands, so this step is a correctness
// no-op for it, not a wasted one).
func (s *Service) ApplyRecompile(ctx context.Context, game *domain.Game, profileName string, mod domain.InstalledMod, progress func(DeployProgress)) (result *UpdateApplyResult, err error) {
	result = &UpdateApplyResult{}
	emit := func(p DeployProgress) {
		if progress != nil {
			progress(p)
		}
	}
	base := DeployProgress{ModName: mod.Name, ModID: mod.ID, SourceID: mod.SourceID}

	if prof, perr := s.NewProfileManager().Get(game.ID, profileName); perr == nil {
		if ref := prof.FindRef(mod.SourceID, mod.ID); ref != nil && ref.Locked {
			return result, LockedRefRefusalError(mod.Mod, profileName, ref)
		}
	}

	basePakPath, err := resolveBasePak(game)
	if err != nil {
		return result, err
	}

	gameCache := s.GetGameCache(game)
	hashes, err := gameCache.BaseIndexHashes(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return result, fmt.Errorf("reading compile fingerprints: %w", err)
	}
	if len(hashes) == 0 {
		return result, fmt.Errorf("%s has no compiled entries to recompile", mod.Name)
	}

	compiler, err := s.compilerSourceForGame(game.ID)
	if err != nil {
		return result, err
	}

	manifests, err := gameCache.FileManifests(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return result, fmt.Errorf("reading cache manifests: %w", err)
	}

	cacheModRef := &domain.Mod{ID: mod.ID, SourceID: mod.SourceID, Version: mod.Version, GameID: game.ID}
	cachePath, stagePath, err := prepareStaging(gameCache, game, cacheModRef)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck
	if err := os.MkdirAll(stagePath, 0755); err != nil {
		return result, fmt.Errorf("preparing recompile staging: %w", err)
	}

	// Lazily fetched: the common path (retained source present for every
	// compiled file) never needs the source's file listing at all.
	var sourceFiles []domain.DownloadableFile
	var sourceFilesErr error
	getSourceFiles := func() ([]domain.DownloadableFile, error) {
		if sourceFiles == nil && sourceFilesErr == nil {
			sourceFiles, sourceFilesErr = s.GetModFiles(ctx, mod.SourceID, &mod.Mod)
		}
		return sourceFiles, sourceFilesErr
	}

	for fileID := range hashes {
		// destName is the compiled output's own filename: for a download-
		// compiled entry it's the recorded manifest member; for an import-
		// compiled entry (no manifest - see importer.go's compile branch)
		// fileID already IS that name (stageCompileFingerprint's doc
		// comment), so the fallback is exact, not a guess.
		destName := fileID
		if m, ok := manifests[fileID]; ok && m.Recorded && len(m.Members) == 1 {
			destName = m.Members[0]
		}

		retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(fileID))
		sourcePath := retainedPath
		if _, statErr := os.Stat(retainedPath); statErr != nil {
			if mod.SourceID == domain.SourceLocal {
				return result, fmt.Errorf("%s: retained compile source for %q is missing and this mod has no remote source to re-download from - re-import the .exmodz to restore it", mod.Name, fileID)
			}
			files, ferr := getSourceFiles()
			if ferr != nil {
				return result, fmt.Errorf("%s: retained compile source for %q is missing; fetching source files: %w", mod.Name, fileID, ferr)
			}
			var match *domain.DownloadableFile
			for i := range files {
				if files[i].ID == fileID {
					match = &files[i]
					break
				}
			}
			if match == nil {
				return result, fmt.Errorf("%s: retained compile source for %q is missing and no matching source file was found to re-download", mod.Name, fileID)
			}
			url, uerr := s.GetDownloadURL(ctx, mod.SourceID, &mod.Mod, match.ID)
			if uerr != nil {
				return result, fmt.Errorf("%s: re-downloading compile source: %w", mod.Name, uerr)
			}
			tempDir, terr := newStagingDir(s.stagingRoot(), "lmm-recompile-*")
			if terr != nil {
				return result, terr
			}
			defer os.RemoveAll(tempDir) //nolint:errcheck
			dlPath := filepath.Join(tempDir, match.FileName)
			evt := base
			evt.Phase, evt.Detail = UpdateNote, fmt.Sprintf("retained compile source missing for %s - re-downloading", destName)
			emit(evt)
			if _, derr := s.downloader.DownloadWithHeaders(ctx, url, dlPath, nil, nil); derr != nil {
				return result, fmt.Errorf("%s: re-downloading compile source: %w", mod.Name, derr)
			}
			sourcePath = dlPath
		}

		outPath := filepath.Join(stagePath, destName)
		if cerr := compiler.Compile(ctx, basePakPath, sourcePath, outPath); cerr != nil {
			return result, fmt.Errorf("recompiling %s: %w", destName, cerr)
		}
		if ferr := stageCompileFingerprint(stagePath, fileID, basePakPath, sourcePath); ferr != nil {
			return result, ferr
		}
		result.Applied = append(result.Applied, destName)
	}

	if err := commitStagedCache(cachePath, stagePath); err != nil {
		return result, err
	}

	installer, err := s.GetInstallerForProfile(game, profileName)
	if err != nil {
		return result, fmt.Errorf("recompiled %s but could not redeploy: %w", mod.Name, err)
	}
	if err := installer.ReplaceForUpdate(ctx, game, &mod.Mod, &mod.Mod, profileName, mod.FileIDs, mod.FileIDs); err != nil {
		return result, fmt.Errorf("recompiled %s but redeploying failed: %w", mod.Name, err)
	}

	return result, nil
}
