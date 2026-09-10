package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// Installer handles mod installation and uninstallation
type Installer struct {
	cache  *cache.Cache
	linker linker.Linker
	db     *db.DB // Optional: enables file tracking for conflict detection
	log    *slog.Logger

	// originals is #350's originals store, set by
	// Service.getInstallerForProfile. Nil disables capture entirely, which
	// is what keeps the white-box tests that build an Installer directly
	// working unchanged. Every deploy in the product funnels through this
	// type, which is why ONE field here covers the accepted-Overwrite
	// install, the archive import and a compile game's merged artifact
	// alike - see internal/core/originals.go's package comment.
	originals *originalsStore
}

// NewInstaller creates a new installer
// The db parameter is optional - if nil, file tracking is disabled
func NewInstaller(cache *cache.Cache, linker linker.Linker, database *db.DB) *Installer {
	return &Installer{
		cache:  cache,
		linker: linker,
		db:     database,
		log:    slog.New(slog.DiscardHandler),
	}
}

// SetLogger sets the logger used for diagnostics. nil resets to discard,
// which is also the default.
func (i *Installer) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	i.log = l
}

// setOriginals wires the originals store this Installer captures into
// (#350). Unexported: an Installer is a core primitive, and the store is
// resolved from the Service's data directory, never by a caller.
func (i *Installer) setOriginals(store *originalsStore) { i.originals = store }

// captureOriginal preserves whatever is at dstPath before a deploy
// replaces it, when that file is one lmm does not own.
//
// "Does not own" is three cheap tests in order of cost: the destination
// must exist (Lstat), it must be a REGULAR file (a symlink is lmm's own
// deployment, or another manager's link - never stock content), and the
// deployed_files table must not already attribute it to a mod in this GAME
// (any profile - minor 7). The DB query only ever runs for a destination
// that is already a real file, which on a normal deploy is nothing at all,
// so this costs a stat per file and no more.
//
// A capture failure does NOT fail the deploy - a backup that blocks the
// operation it exists to protect is worse than no backup - but it is not
// silent either: the store records it on the always-on user channel and on
// the pending list the running flow drains onto its result's Warnings
// (review finding 5; the diagnostic Warn line stays for the log).
func (i *Installer) captureOriginal(ctx context.Context, game *domain.Game, profileName, relPath, dstPath string, mod *domain.Mod) {
	if i.originals == nil {
		return
	}
	info, err := os.Lstat(dstPath)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if i.db != nil {
		// Game-scoped, not profile-scoped (#350 review minor 7): the
		// question here is "did lmm put this here at all", and `lmm deploy
		// -p B` over a copy deployment made under profile A must not treat
		// A's own file as stock content.
		owned, err := i.db.AnyProfileOwnsFile(ctx, game.ID, filepath.ToSlash(relPath))
		if err == nil && owned {
			return
		}
	}
	row := OriginalFile{
		Root: OriginalRootModPath, RelativePath: filepath.ToSlash(relPath),
		Op: OriginalOpDeploy, Profile: profileName,
	}
	if mod != nil {
		row.SourceID, row.ModID = mod.SourceID, mod.ID
	}
	if err := i.originals.capture(row, dstPath); err != nil {
		i.log.Warn("could not preserve the file this deploy replaces; it will not be restorable from a snapshot",
			"path", dstPath, "err", err)
		i.originals.noteFailure(fmt.Sprintf(
			"could not preserve %s before replacing it; it will not be restorable from a snapshot: %v", dstPath, err))
	}
}

// restoreReplacedOriginal puts back whatever lmm displaced at relPath, at
// the moment lmm's own file there is removed (coordinator ruling on review
// note 13). No-op when this Installer has no store, or when nothing was
// ever captured for that path - which is every ordinary uninstall.
//
// Never fatal: a removal that succeeded must not be reported as a failure
// because the original could not go back. The failure is recorded on the
// store's always-on channel instead (review finding 5), which is where a
// user needs it - the file lmm cannot return is the one it holds the only
// copy of.
func (i *Installer) restoreReplacedOriginal(relPath, dstPath string) {
	if i.originals == nil {
		return
	}
	restored, err := i.originals.release(OriginalRootModPath, filepath.ToSlash(relPath), dstPath)
	if err != nil {
		i.log.Warn("could not put back the file this mod replaced", "path", dstPath, "err", err)
		i.originals.noteFailure(fmt.Sprintf(
			"could not put back the file lmm replaced at %s; `lmm snapshot restore` can still do it: %v", dstPath, err))
		return
	}
	if restored {
		i.log.Debug("put back the file this mod replaced", "path", dstPath)
	}
}

// foreignFile reports whether dstPath holds content lmm did not put there:
// a REGULAR file (a symlink is a deployment, lmm's or another tool's) with
// no deployed_files row for this game and profile.
//
// It is the same judgement captureOriginal makes before storing an
// original, and it is deliberately conservative in the same direction: an
// Installer with no database cannot tell, and answers false, so a
// db-less Installer behaves exactly as it always has.
func (i *Installer) foreignFile(ctx context.Context, game *domain.Game, profileName, relPath, dstPath string) bool {
	if i.db == nil {
		return false
	}
	info, err := os.Lstat(dstPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	owner, err := i.db.GetFileOwner(ctx, game.ID, profileName, relPath)
	return err == nil && owner == nil
}

// Install deploys a mod to the game directory. If DB tracking is enabled and a
// SaveDeployedFile fails, only the file that failed to track is rolled back so
// the filesystem stays consistent with the database (previously deployed+tracked
// files are left in place).
func (i *Installer) Install(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := deployableFiles(i.cache, game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("resolving deployable files: %w", err)
	}

	var deployed []string
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		srcPath := i.cache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)

		// #350: preserve whatever is there before the deploy replaces it.
		i.captureOriginal(ctx, game, profileName, file, dstPath, mod)

		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			rollbackErr := rollbackDeploy(i.linker, game.ModPath, deployed)
			// A rolled-back install must leave no hole either: every file
			// it removed on the way out gets its original back, including
			// the one whose deploy just failed (capture ran before it).
			i.restoreReplacedOriginals(game, append(append([]string(nil), deployed...), file))
			if i.db != nil {
				_ = i.db.DeleteDeployedFiles(ctx, game.ID, profileName, mod.SourceID, mod.ID)
			}
			if rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("deploying %s", file), Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("deploying %s: %w", file, err)
		}
		deployed = append(deployed, file)

		// Track file ownership in database (for conflict detection)
		if i.db != nil {
			if err := i.db.SaveDeployedFile(ctx, game.ID, profileName, file, mod.SourceID, mod.ID); err != nil {
				// Roll back only the file that failed to track; leave previously
				// deployed+tracked files and DB records intact.
				rollbackErr := rollbackDeploy(i.linker, game.ModPath, []string{file})
				i.restoreReplacedOriginals(game, []string{file})
				if rollbackErr != nil {
					return &domain.DeployError{Op: fmt.Sprintf("tracking deployed file %s", file), Primary: err, Rollback: rollbackErr}
				}
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
	}

	return nil
}

// Replace swaps an existing deployment with a new cached version and restores
// the old files if the replacement fails.
func (i *Installer) Replace(ctx context.Context, game *domain.Game, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, i.cache, i.cache, oldMod, newMod, profileName, nil, nil)
}

// ReplaceForUpdate is Replace carrying the update path's file-ID transition:
// the mod's installed file IDs BEFORE the update (oldFileIDs) and the full
// set being installed by it (newFileIDs - ApplyUpdate's downloadedFileIDs,
// exactly what it records to the DB row and profile ref). They matter only in
// the degenerate same-version shape - a file-only update whose version string
// does not change shares ONE version-keyed cache directory between the old
// and new files, so the plain union replace could never undeploy a departing
// file's members (#144 item 4). ApplyRollback wires the same transition
// reversed - current FileIDs -> PreviousFileIDs - so a same-version rollback
// narrows identically instead of deploying the union (#150). See
// resolveSharedDirUpdate for the exact
// ownership rules; on a normal different-version update (distinct cache dirs)
// this behaves exactly like Replace.
func (i *Installer) ReplaceForUpdate(ctx context.Context, game *domain.Game, oldMod, newMod *domain.Mod, profileName string, oldFileIDs, newFileIDs []string) error {
	return i.replaceWithCaches(ctx, game, i.cache, i.cache, oldMod, newMod, profileName, oldFileIDs, newFileIDs)
}

// ReplaceWithCaches swaps an existing deployment using explicit old and new caches.
func (i *Installer) ReplaceWithCaches(ctx context.Context, game *domain.Game, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, oldCache, newCache, oldMod, newMod, profileName, nil, nil)
}

// ReplaceWithOldCache swaps an existing deployment using an alternate cache
// snapshot for the old version.
func (i *Installer) ReplaceWithOldCache(ctx context.Context, game *domain.Game, oldCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, oldCache, i.cache, oldMod, newMod, profileName, nil, nil)
}

func (i *Installer) replaceWithCaches(ctx context.Context, game *domain.Game, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string, oldFileIDs, newFileIDs []string) error {
	if !oldCache.Exists(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version) {
		return fmt.Errorf("old mod not in cache: %s/%s@%s", oldMod.SourceID, oldMod.ID, oldMod.Version)
	}
	if !newCache.Exists(game.ID, newMod.SourceID, newMod.ID, newMod.Version) {
		return fmt.Errorf("new mod not in cache: %s/%s@%s", newMod.SourceID, newMod.ID, newMod.Version)
	}

	oldFiles, err := oldCache.ListFiles(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version)
	if err != nil {
		return fmt.Errorf("listing old cached files: %w", err)
	}
	newFiles, err := deployableFiles(newCache, game.ID, newMod.SourceID, newMod.ID, newMod.Version)
	if err != nil {
		return fmt.Errorf("resolving deployable new-side files: %w", err)
	}

	// #144 item 4: in the degenerate same-version shape (old and new resolve
	// to ONE cache dir, so both listings are the same union), the deploy set
	// narrows to the members the mod's CURRENT file IDs own - every other
	// listed member is excluded from the NEW side, so the obsolete-file loop
	// below undeploys it like any other old-only file (Undeploy tolerates an
	// already-absent path) and the deploy/DB loops never touch it. Rollback
	// likewise narrows to what the OLD side's IDs actually owned. A false ok
	// (distinct dirs, no departing ID, or incomplete provenance) leaves the
	// historical union behavior byte-for-byte intact.
	newCurrent, oldDeployed, provenanceOK := resolveSharedDirUpdate(game.ID, oldCache, newCache, oldMod, newMod, oldFileIDs, newFileIDs, newFiles)

	// oldRestorable starts as the old side's deployable set (deploy-direction,
	// #210), not the raw oldFiles union: a rollback restores the pre-replace
	// DEPLOYMENT, which is whatever deployableFiles resolved for the old
	// entry, never the union. An old-side mixed entry (recorded markers plus
	// a retained source plus an unclaimed stale file) never deploys the stale
	// file in the first place, so a mid-replace failure must not link it
	// fresh either - using oldFiles here would resurrect it through this
	// error path even though the #210 narrowing kept it off disk on every
	// success path.
	oldRestorable, err := deployableFiles(oldCache, game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version)
	if err != nil {
		return fmt.Errorf("resolving deployable old-side files: %w", err)
	}
	if provenanceOK {
		kept := make([]string, 0, len(newFiles))
		for _, file := range newFiles {
			if newCurrent[file] {
				kept = append(kept, file)
			}
		}
		newFiles = kept

		kept = make([]string, 0, len(oldFiles))
		for _, file := range oldFiles {
			if oldDeployed[file] {
				kept = append(kept, file)
			}
		}
		oldRestorable = kept
	}

	// oldSet drives every restore decision: only members the OLD deployment
	// actually owned may be put back by a rollback. Without provenance it is
	// the full old listing, preserving the historical behavior exactly.
	oldSet := make(map[string]bool, len(oldRestorable))
	for _, file := range oldRestorable {
		oldSet[file] = true
	}
	newSet := make(map[string]bool, len(newFiles))
	for _, file := range newFiles {
		newSet[file] = true
	}

	var removedOld []string
	for _, file := range oldFiles {
		if newSet[file] {
			continue
		}
		dstPath := filepath.Join(game.ModPath, file)
		// #350 / review finding 4: this loop iterates the OLD entry's RAW
		// ListFiles union, not its deployable set, so a member lmm never
		// deployed is visited here - the #210 narrowing case, and the
		// stale-unclaimed-member case. Under copy or hardlink Undeploy
		// removes whatever is at the path, which for such a member is the
		// game's own content. Same guard, same reason, as Uninstall's:
		// leave a regular file with no deployed_files row alone. Nothing
		// is captured, because nothing is being replaced - the file stays
		// exactly where it is.
		if i.foreignFile(ctx, game, profileName, file, dstPath) {
			i.log.Debug("leaving a file this update does not own where it is", "path", dstPath)
			continue
		}
		if err := i.linker.Undeploy(dstPath); err != nil {
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, nil, oldSet); rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("removing obsolete file %s", file), Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("removing obsolete file %s: %w", file, err)
		}
		removedOld = append(removedOld, file)
	}

	var replacedOrAdded []string
	for _, file := range newFiles {
		select {
		case <-ctx.Done():
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
				return &domain.DeployError{Primary: ctx.Err(), Rollback: rollbackErr}
			}
			return ctx.Err()
		default:
		}

		srcPath := newCache.GetFilePath(game.ID, newMod.SourceID, newMod.ID, newMod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)
		// #350: a replace can also land on a file lmm does not own - a
		// new version whose file list grew into stock content - so the
		// original is preserved here before the new file goes over it.
		// The obsolete-file loop above needs no capture of its own: since
		// review finding 4 it SKIPS a path lmm does not own rather than
		// removing it, and restoreOldFiles only ever puts lmm's own files
		// back. What that loop DOES need is the release, which is at the
		// end of this function - see the comment there.
		i.captureOriginal(ctx, game, profileName, file, dstPath, newMod)
		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			cleanupErr := i.linker.Undeploy(dstPath)
			rollbackFiles := append(append([]string(nil), replacedOrAdded...), file)
			rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, rollbackFiles, oldSet)
			if cleanupErr != nil || rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("deploying %s", file), Primary: err, Cleanup: cleanupErr, Rollback: rollbackErr}
			}
			return fmt.Errorf("deploying %s: %w", file, err)
		}
		replacedOrAdded = append(replacedOrAdded, file)
	}

	if i.db != nil {
		if err := i.db.DeleteDeployedFiles(ctx, game.ID, profileName, oldMod.SourceID, oldMod.ID); err != nil {
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
				return &domain.DeployError{Op: "resetting file tracking", Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("resetting file tracking: %w", err)
		}
		for _, file := range newFiles {
			if err := i.db.SaveDeployedFile(ctx, game.ID, profileName, file, newMod.SourceID, newMod.ID); err != nil {
				_ = i.db.DeleteDeployedFiles(ctx, game.ID, profileName, newMod.SourceID, newMod.ID)
				for _, oldFile := range oldRestorable {
					_ = i.db.SaveDeployedFile(ctx, game.ID, profileName, oldFile, oldMod.SourceID, oldMod.ID)
				}
				if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
					return &domain.DeployError{Op: fmt.Sprintf("tracking deployed file %s", file), Primary: err, Rollback: rollbackErr}
				}
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
	}

	// #350 re-review finding N1 - ruling (a) for the obsolete-file loop.
	// That loop removed lmm's OWN files (a member the new side no longer
	// ships), which is exactly the removal ruling (a) covers: whatever each
	// of them replaced goes back, so `lmm update` and `lmm update rollback`
	// stop leaving the hole every other removal path has stopped leaving.
	//
	// Deliberately HERE rather than inside the loop, because this function -
	// unlike Uninstall and Install's rollbacks - can still fail after the
	// removal: every error path above replays removedOld through
	// restoreOldFiles, which would deploy the old mod's file back OVER a
	// just-restored original whose manifest row had already been dropped,
	// leaving lmm with no record and the user with the wrong bytes. Past the
	// last failure point there is nothing left to roll back, and "the row
	// goes once the original is back in place" stays true. A failed put-back
	// is reported on the always-on channel and keeps its row, so
	// `lmm snapshot restore` can still do it (restoreReplacedOriginal).
	i.restoreReplacedOriginals(game, removedOld)

	return nil
}

// resolveSharedDirUpdate resolves member ownership for a same-version
// file-only update whose old and new cache keys share ONE version directory
// (#144 item 4). It returns ok=false - meaning "fall back to today's union
// behavior exactly" - unless EVERY condition for positive provenance holds:
//
//   - both ID sets are known, the transition actually CHANGES the installed
//     ID set (set(oldFileIDs) != set(newFileIDs) - a symmetric predicate, so
//     the forward call and its swapped-transition compensation call always
//     answer the same way; asking only "does an old ID depart?" diverges on
//     pure-removal/pure-addition transitions and made a compensated failure
//     union-deploy a stale generation's never-deployed member), and the old
//     and new keys resolve to the same version directory (distinct dirs are
//     already handled by the obsolete-file loop),
//   - every ID in oldFileIDs and newFileIDs has a completion marker with a
//     RECORDED member manifest (a legacy bare marker - any pre-manifest
//     cache entry - makes what that file contributed, or what it still
//     needs, unknowable),
//   - every file in the shared directory's listing is attributed by at least
//     one recorded manifest, stale markers included (unattributed content
//     proves an unmanifested contributor exists, e.g. an entry populated
//     directly by `lmm import`).
//
// unionFiles is the caller's deploy-direction set (deployableFiles' output,
// #210), not always the raw ListFiles union: when that resolver narrowed to
// recorded members, every entry here is attributed by construction, since
// resolveSharedDirUpdate independently requires all-recorded provenance too;
// when it fell back to the full union, the attribution check below behaves
// exactly as before.
//
// The ownership rule: the DEPLOY set is exactly the members attributed to the
// mod's current (new-side) file IDs - newCurrent. Every other listed member
// is undeployed, its provenance being its own manifest: that covers both the
// departing IDs of THIS update and stale members left by earlier same-version
// updates, whose markers remain in the shared dir after their IDs left the
// installed set. Those stale markers are deliberately NOT survivors - a
// survivor is an ID the mod still installs, never "any marker present" -
// otherwise chained same-version updates would resurrect the members the
// previous update correctly removed. oldDeployed (the members attributed to
// the old-side IDs) is what a rollback may restore: the pre-replace
// deployment, not the union.
//
// The fallback is deliberately silent - pre-manifest caches are the norm for
// existing installs, and they must not produce a warning storm; they simply
// keep the historical union behavior. Never guess, never undeploy without
// positive provenance.
func resolveSharedDirUpdate(gameID string, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, oldFileIDs, newFileIDs, unionFiles []string) (newCurrent, oldDeployed map[string]bool, ok bool) {
	if len(oldFileIDs) == 0 || len(newFileIDs) == 0 {
		return nil, nil, false
	}
	newIDs := make(map[string]bool, len(newFileIDs))
	for _, id := range newFileIDs {
		newIDs[id] = true
	}
	oldIDs := make(map[string]bool, len(oldFileIDs))
	for _, id := range oldFileIDs {
		oldIDs[id] = true
	}
	sameIDSet := len(oldIDs) == len(newIDs)
	if sameIDSet {
		for id := range oldIDs {
			if !newIDs[id] {
				sameIDSet = false
				break
			}
		}
	}
	if sameIDSet {
		return nil, nil, false
	}
	oldDir := oldCache.ModPath(gameID, oldMod.SourceID, oldMod.ID, oldMod.Version)
	newDir := newCache.ModPath(gameID, newMod.SourceID, newMod.ID, newMod.Version)
	if filepath.Clean(oldDir) != filepath.Clean(newDir) {
		return nil, nil, false
	}

	manifests, err := newCache.FileManifests(gameID, newMod.SourceID, newMod.ID, newMod.Version)
	if err != nil {
		return nil, nil, false // unreadable bookkeeping is absent bookkeeping: union fallback
	}
	memberSet := func(ids []string) (map[string]bool, bool) {
		set := make(map[string]bool)
		for _, id := range ids {
			m, present := manifests[id]
			if !present || !m.Recorded {
				return nil, false
			}
			for _, member := range m.Members {
				set[member] = true
			}
		}
		return set, true
	}
	newCurrent, ok = memberSet(newFileIDs)
	if !ok {
		return nil, nil, false
	}
	oldDeployed, ok = memberSet(oldFileIDs)
	if !ok {
		return nil, nil, false
	}

	attributed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			continue // a bare STALE marker attributes nothing; old/new-side bareness already failed above
		}
		for _, member := range m.Members {
			attributed[member] = true
		}
	}
	for _, file := range unionFiles {
		if !attributed[file] {
			return nil, nil, false
		}
	}
	return newCurrent, oldDeployed, true
}

func (i *Installer) restoreOldFiles(oldCache *cache.Cache, game *domain.Game, oldMod *domain.Mod, removedOld, replacedOrAdded []string, oldSet map[string]bool) error {
	var errs []error

	for j := len(replacedOrAdded) - 1; j >= 0; j-- {
		file := replacedOrAdded[j]
		dstPath := filepath.Join(game.ModPath, file)
		if oldSet[file] {
			srcPath := oldCache.GetFilePath(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version, file)
			if err := i.linker.Deploy(srcPath, dstPath); err != nil {
				errs = append(errs, fmt.Errorf("restoring %s: %w", file, err))
			}
			continue
		}
		if err := i.linker.Undeploy(dstPath); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", file, err))
		}
	}

	for j := len(removedOld) - 1; j >= 0; j-- {
		file := removedOld[j]
		// Only restore members the OLD deployment actually owned. Without
		// provenance oldSet is the full old listing and this never skips;
		// with it, a stale member routed through the obsolete loop (its
		// Undeploy was a no-op - it wasn't deployed) must not be deployed
		// by the rollback either.
		if !oldSet[file] {
			continue
		}
		srcPath := oldCache.GetFilePath(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)
		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			errs = append(errs, fmt.Errorf("restoring removed %s: %w", file, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// rollbackDeploy undeploys the given relative paths under modPath (reverse order).
// Returns the first Undeploy error encountered, if any.
// restoreReplacedOriginals is restoreReplacedOriginal over a set of
// relative paths - the rollback shape.
func (i *Installer) restoreReplacedOriginals(game *domain.Game, relativePaths []string) {
	if i.originals == nil {
		return
	}
	for _, rel := range relativePaths {
		i.restoreReplacedOriginal(rel, filepath.Join(game.ModPath, rel))
	}
}

func rollbackDeploy(lnk linker.Linker, modPath string, relativePaths []string) error {
	var firstErr error
	for j := len(relativePaths) - 1; j >= 0; j-- {
		dstPath := filepath.Join(modPath, relativePaths[j])
		if err := lnk.Undeploy(dstPath); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Uninstall removes a mod from the game directory
func (i *Installer) Uninstall(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	// Deliberately the full ListFiles union, not deployableFiles (#210):
	// removal must cover anything that might ever have been linked, including
	// stale unclaimed files a pre-fix deploy linked. Narrowing this would
	// strand those links forever.
	//
	// An absent cache entry is not an error (#260): uninstall must stay
	// idempotent when the entry is already gone - the steady state
	// syncMergedPak's zero branch and purge --uninstall leave behind. The
	// deployment can still be fully on disk, though (a copy/hardlink deploy
	// owns real files, not links back into the cache), so fall back to the
	// DB's tracked deployed paths rather than orphaning them while erasing
	// the only record that they were ours. Ownership rows upsert on
	// overwrite ("new mod takes ownership"), so the fallback never removes
	// a path another mod has since claimed.
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("listing cached files: %w", err)
		}
		files = nil
		if i.db != nil {
			if files, err = i.db.GetDeployedFilesForMod(ctx, game.ID, profileName, mod.SourceID, mod.ID); err != nil {
				return fmt.Errorf("listing tracked deployed files: %w", err)
			}
		}
	}

	// Undeploy each file
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dstPath := filepath.Join(game.ModPath, file)

		// #350: never delete a file lmm does not own.
		//
		// This loop undeploys every path the mod's cache entry NAMES,
		// which is not the same as every path the mod actually put there.
		// The copy and hardlink linkers remove whatever is at the path, so
		// a deploy (which undeploys before it installs), a purge or an
		// ordinary uninstall would DELETE stock game content sitting where
		// one of this mod's files would go - silently, and with no way
		// back. The symlink linker refuses to remove a non-symlink, which
		// is exactly why this went unnoticed: the default link method does
		// not have the bug.
		//
		// lmm's own deployments are unaffected: a symlink is not a regular
		// file, and a copy/hardlink deployment carries a deployed_files
		// row written by the same loop that created it.
		if i.foreignFile(ctx, game, profileName, file, dstPath) {
			i.log.Debug("leaving a file this mod does not own where it is", "path", dstPath)
			continue
		}

		if err := i.linker.Undeploy(dstPath); err != nil {
			return fmt.Errorf("undeploying %s: %w", file, err)
		}
		// lmm's own file is gone; whatever it displaced goes back.
		i.restoreReplacedOriginal(file, dstPath)
	}

	// Remove file ownership records from database
	if i.db != nil {
		if err := i.db.DeleteDeployedFiles(ctx, game.ID, profileName, mod.SourceID, mod.ID); err != nil {
			return fmt.Errorf("removing file tracking: %w", err)
		}
	}

	// Clean up any empty directories left behind
	linker.CleanupEmptyDirs(game.ModPath)

	return nil
}

// IsInstalled checks if a mod is currently deployed. Returns true only if every
// cached file is deployed (partial installs report as not installed).
func (i *Installer) IsInstalled(ctx context.Context, game *domain.Game, mod *domain.Mod) (bool, error) {
	// Check if mod is cached first
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return false, nil
	}

	// Get list of files in the cached mod
	files, err := deployableFiles(i.cache, game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return false, fmt.Errorf("resolving deployable files: %w", err)
	}

	if len(files) == 0 {
		return false, nil
	}

	// Consider installed only if all files are deployed
	for _, file := range files {
		dstPath := filepath.Join(game.ModPath, file)
		deployed, err := i.linker.IsDeployed(dstPath)
		if err != nil {
			return false, err
		}
		if !deployed {
			return false, nil
		}
	}
	return true, nil
}

// Conflict represents a file that would be overwritten by installing a mod
type Conflict struct {
	RelativePath    string `json:"relative_path"`
	CurrentSourceID string `json:"current_source_id"`
	CurrentModID    string `json:"current_mod_id"`
}

// GetConflicts checks if installing a mod would overwrite files from other mods.
// Returns conflicts for files owned by OTHER mods (not the mod being installed).
//
// It stays EXPORTED with no in-tree caller outside this package: Task 19
// (#291) folded ImportArchive's own conflict check inline, which removed
// its last cmd/lmm caller, production and test alike. Unlike the
// Service-primitive ratchet elsewhere in this phase, this is one method on
// an exported type - package main still legitimately holds an *Installer
// via Service.GetInstaller (see that method's own doc comment for the
// precedent: cmd/lmm's tests build fixtures through it), so unexporting
// just this method would buy nothing but a fourth shim.
func (i *Installer) GetConflicts(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) ([]Conflict, error) {
	if i.db == nil {
		return nil, nil
	}

	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := deployableFiles(i.cache, game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving deployable files: %w", err)
	}

	return i.conflictsForPaths(ctx, game, mod, profileName, files)
}

// conflictsForPaths is GetConflicts' twin for a caller that already knows the
// game-dir-relative paths in question rather than owning a cache entry to
// derive them from (#314): PlanImportArchive computes an archive's file list
// from its LISTING, so it can ask the conflict question before anything has
// been ingested. Same self-filter, same wrapping - only the source of the
// path list differs.
func (i *Installer) conflictsForPaths(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string, paths []string) ([]Conflict, error) {
	if i.db == nil {
		return nil, nil
	}

	dbConflicts, err := i.db.CheckFileConflicts(ctx, game.ID, profileName, paths)
	if err != nil {
		return nil, fmt.Errorf("checking conflicts: %w", err)
	}

	// Filter out conflicts with self (re-installing same mod)
	var conflicts []Conflict
	for _, c := range dbConflicts {
		if c.SourceID != mod.SourceID || c.ModID != mod.ID {
			conflicts = append(conflicts, Conflict{
				RelativePath:    c.RelativePath,
				CurrentSourceID: c.SourceID,
				CurrentModID:    c.ModID,
			})
		}
	}
	sortConflicts(conflicts)

	return conflicts, nil
}

// sortConflicts puts a conflict list in the one order every renderer wants
// (#315, Ruling 4's determinism rule): OWNING MOD first, path second. The
// DB answers ordered by path alone, which interleaves two owners' files -
// and every renderer groups per owning mod (the CLI's "From <mod> (<id>):"
// block, `--json`'s details.conflicts), so grouping a path-ordered list
// meant iterating a map, and the group order varied run to run. Sorting
// here means the group order falls out of the slice order, once, for every
// caller and every frontend.
func sortConflicts(conflicts []Conflict) {
	sort.Slice(conflicts, func(a, b int) bool {
		x, y := conflicts[a], conflicts[b]
		if x.CurrentSourceID != y.CurrentSourceID {
			return x.CurrentSourceID < y.CurrentSourceID
		}
		if x.CurrentModID != y.CurrentModID {
			return x.CurrentModID < y.CurrentModID
		}
		return x.RelativePath < y.RelativePath
	})
}

// GetDeployedFiles returns the list of files deployed for a mod
func (i *Installer) GetDeployedFiles(ctx context.Context, game *domain.Game, mod *domain.Mod) ([]string, error) {
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, nil
	}

	files, err := deployableFiles(i.cache, game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving deployable files: %w", err)
	}

	var deployed []string
	for _, file := range files {
		dstPath := filepath.Join(game.ModPath, file)
		isDeployed, err := i.linker.IsDeployed(dstPath)
		if err != nil {
			i.log.Debug("checking deployed state failed", "path", dstPath, "err", err)
			continue
		}
		if isDeployed {
			deployed = append(deployed, file)
		}
	}

	return deployed, nil
}
