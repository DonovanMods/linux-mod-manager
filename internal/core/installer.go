package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
)

// Installer handles mod installation and uninstallation
type Installer struct {
	cache  *cache.Cache
	linker linker.Linker
	db     *db.DB // Optional: enables file tracking for conflict detection
}

// NewInstaller creates a new installer
// The db parameter is optional - if nil, file tracking is disabled
func NewInstaller(cache *cache.Cache, linker linker.Linker, database *db.DB) *Installer {
	return &Installer{
		cache:  cache,
		linker: linker,
		db:     database,
	}
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
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("listing cached files: %w", err)
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

		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			rollbackErr := rollbackDeploy(i.linker, game.ModPath, deployed)
			if i.db != nil {
				_ = i.db.DeleteDeployedFiles(game.ID, profileName, mod.SourceID, mod.ID)
			}
			if rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("deploying %s", file), Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("deploying %s: %w", file, err)
		}
		deployed = append(deployed, file)

		// Track file ownership in database (for conflict detection)
		if i.db != nil {
			if err := i.db.SaveDeployedFile(game.ID, profileName, file, mod.SourceID, mod.ID); err != nil {
				// Roll back only the file that failed to track; leave previously
				// deployed+tracked files and DB records intact.
				if rollbackErr := rollbackDeploy(i.linker, game.ModPath, []string{file}); rollbackErr != nil {
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
	return i.replaceWithCaches(ctx, game, i.cache, i.cache, oldMod, newMod, profileName, nil)
}

// ReplaceForUpdate is Replace carrying the update path's superseded file IDs:
// the OLD source file IDs whose FileIDReplacements were actually applied
// (ApplyUpdate resolves them; see flows.go). They matter only in the
// degenerate same-version shape - a file-only update whose version string
// does not change shares ONE version-keyed cache directory between the old
// and new files, so the plain union replace could never undeploy the
// superseded file's members (#144 item 4). See supersededOnlyMembers for the
// provenance rules; on a normal different-version update (distinct cache
// dirs) this behaves exactly like Replace.
func (i *Installer) ReplaceForUpdate(ctx context.Context, game *domain.Game, oldMod, newMod *domain.Mod, profileName string, supersededFileIDs []string) error {
	return i.replaceWithCaches(ctx, game, i.cache, i.cache, oldMod, newMod, profileName, supersededFileIDs)
}

// ReplaceWithCaches swaps an existing deployment using explicit old and new caches.
func (i *Installer) ReplaceWithCaches(ctx context.Context, game *domain.Game, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, oldCache, newCache, oldMod, newMod, profileName, nil)
}

// ReplaceWithOldCache swaps an existing deployment using an alternate cache
// snapshot for the old version.
func (i *Installer) ReplaceWithOldCache(ctx context.Context, game *domain.Game, oldCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, oldCache, i.cache, oldMod, newMod, profileName, nil)
}

func (i *Installer) replaceWithCaches(ctx context.Context, game *domain.Game, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string, supersededFileIDs []string) error {
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
	newFiles, err := newCache.ListFiles(game.ID, newMod.SourceID, newMod.ID, newMod.Version)
	if err != nil {
		return fmt.Errorf("listing new cached files: %w", err)
	}

	// #144 item 4: in the degenerate same-version shape (old and new resolve
	// to ONE cache dir, so both listings are the same union), members owned
	// solely by superseded file IDs are excluded from the NEW side - the
	// obsolete-file loop below then undeploys them like any other old-only
	// file, and the deploy/DB loops never touch them. An empty result (no
	// superseded IDs, distinct dirs, or incomplete provenance) leaves the
	// historical union behavior byte-for-byte intact.
	if supersededOnly := supersededOnlyMembers(game.ID, oldCache, newCache, oldMod, newMod, supersededFileIDs, newFiles); len(supersededOnly) > 0 {
		kept := make([]string, 0, len(newFiles))
		for _, file := range newFiles {
			if !supersededOnly[file] {
				kept = append(kept, file)
			}
		}
		newFiles = kept
	}

	oldSet := make(map[string]bool, len(oldFiles))
	for _, file := range oldFiles {
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
		if err := i.linker.Undeploy(filepath.Join(game.ModPath, file)); err != nil {
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
		if err := i.db.DeleteDeployedFiles(game.ID, profileName, oldMod.SourceID, oldMod.ID); err != nil {
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
				return &domain.DeployError{Op: "resetting file tracking", Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("resetting file tracking: %w", err)
		}
		for _, file := range newFiles {
			if err := i.db.SaveDeployedFile(game.ID, profileName, file, newMod.SourceID, newMod.ID); err != nil {
				_ = i.db.DeleteDeployedFiles(game.ID, profileName, newMod.SourceID, newMod.ID)
				for _, oldFile := range oldFiles {
					_ = i.db.SaveDeployedFile(game.ID, profileName, oldFile, oldMod.SourceID, oldMod.ID)
				}
				if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
					return &domain.DeployError{Op: fmt.Sprintf("tracking deployed file %s", file), Primary: err, Rollback: rollbackErr}
				}
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
	}

	return nil
}

// supersededOnlyMembers resolves which members of a SHARED old/new cache
// directory are owned solely by superseded file IDs and must therefore be
// undeployed by a same-version file-only update (#144 item 4). It returns nil
// - meaning "fall back to today's union behavior exactly" - unless EVERY
// condition for positive provenance holds:
//
//   - there are superseded IDs and the old and new keys resolve to the same
//     version directory (the degenerate shape; distinct dirs are already
//     handled by the obsolete-file loop),
//   - every completion marker in that directory carries a recorded member
//     manifest (a legacy bare marker - any pre-manifest cache entry - makes
//     both what a superseded file contributed and what a survivor still needs
//     unknowable),
//   - every superseded ID actually has a marker there,
//   - every file in the directory listing is attributed by at least one
//     manifest (unattributed content proves an unmanifested contributor
//     exists, e.g. an entry populated directly by `lmm import`).
//
// The result is union(superseded manifests) minus union(surviving manifests):
// a member also listed by ANY surviving file stays. The fallback is
// deliberately silent - pre-manifest caches are the norm for existing
// installs, and they must not produce a warning storm; they simply keep the
// historical union behavior. Never guess, never undeploy without positive
// provenance.
func supersededOnlyMembers(gameID string, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, supersededFileIDs []string, unionFiles []string) map[string]bool {
	if len(supersededFileIDs) == 0 {
		return nil
	}
	oldDir := oldCache.ModPath(gameID, oldMod.SourceID, oldMod.ID, oldMod.Version)
	newDir := newCache.ModPath(gameID, newMod.SourceID, newMod.ID, newMod.Version)
	if filepath.Clean(oldDir) != filepath.Clean(newDir) {
		return nil
	}

	manifests, err := newCache.FileManifests(gameID, newMod.SourceID, newMod.ID, newMod.Version)
	if err != nil {
		return nil // unreadable bookkeeping is absent bookkeeping: union fallback
	}
	attributed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			return nil
		}
		for _, member := range m.Members {
			attributed[member] = true
		}
	}
	for _, file := range unionFiles {
		if !attributed[file] {
			return nil
		}
	}

	superseded := make(map[string]bool, len(supersededFileIDs))
	for _, id := range supersededFileIDs {
		if _, ok := manifests[id]; !ok {
			return nil
		}
		superseded[id] = true
	}

	result := make(map[string]bool)
	for id := range superseded {
		for _, member := range manifests[id].Members {
			result[member] = true
		}
	}
	for id, m := range manifests {
		if superseded[id] {
			continue
		}
		for _, member := range m.Members {
			delete(result, member)
		}
	}
	return result
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
	// Get list of files in the cached mod
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("listing cached files: %w", err)
	}

	// Undeploy each file
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dstPath := filepath.Join(game.ModPath, file)

		if err := i.linker.Undeploy(dstPath); err != nil {
			return fmt.Errorf("undeploying %s: %w", file, err)
		}
	}

	// Remove file ownership records from database
	if i.db != nil {
		if err := i.db.DeleteDeployedFiles(game.ID, profileName, mod.SourceID, mod.ID); err != nil {
			return fmt.Errorf("removing file tracking: %w", err)
		}
	}

	// Clean up any empty directories left behind
	linker.CleanupEmptyDirs(game.ModPath)

	return nil
}

// IsInstalled checks if a mod is currently deployed. Returns true only if every
// cached file is deployed (partial installs report as not installed).
func (i *Installer) IsInstalled(game *domain.Game, mod *domain.Mod) (bool, error) {
	// Check if mod is cached first
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return false, nil
	}

	// Get list of files in the cached mod
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return false, fmt.Errorf("listing cached files: %w", err)
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
	RelativePath    string
	CurrentSourceID string
	CurrentModID    string
}

// GetConflicts checks if installing a mod would overwrite files from other mods.
// Returns conflicts for files owned by OTHER mods (not the mod being installed).
func (i *Installer) GetConflicts(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) ([]Conflict, error) {
	if i.db == nil {
		return nil, nil
	}

	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, fmt.Errorf("listing cached files: %w", err)
	}

	// Check for conflicts
	dbConflicts, err := i.db.CheckFileConflicts(game.ID, profileName, files)
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

	return conflicts, nil
}

// GetDeployedFiles returns the list of files deployed for a mod
func (i *Installer) GetDeployedFiles(game *domain.Game, mod *domain.Mod) ([]string, error) {
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, nil
	}

	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, err
	}

	var deployed []string
	for _, file := range files {
		dstPath := filepath.Join(game.ModPath, file)
		isDeployed, err := i.linker.IsDeployed(dstPath)
		if err != nil {
			continue
		}
		if isDeployed {
			deployed = append(deployed, file)
		}
	}

	return deployed, nil
}

// BatchOptions configures batch install/uninstall operations
type BatchOptions struct {
	Hooks       *ResolvedHooks // Hooks to run during batch operation
	HookRunner  *HookRunner    // Runner for executing hooks
	HookContext HookContext    // Base context for hooks (mod-specific fields added per-mod)
	Force       bool           // If true, bypass before_* hook failures
}

// SkippedMod represents a mod that was skipped during batch operation
type SkippedMod struct {
	Mod    *domain.Mod
	Reason string
}

// InstalledModResult is a successfully installed mod (wraps domain.Mod for batch result)
type InstalledModResult struct {
	domain.Mod
}

// UninstalledModResult is a successfully uninstalled mod (wraps domain.Mod for batch result)
type UninstalledModResult struct {
	domain.Mod
}

// BatchResult contains the results of a batch install/uninstall operation
type BatchResult struct {
	Installed   []InstalledModResult   // Successfully installed mods (for InstallBatch)
	Uninstalled []UninstalledModResult // Successfully uninstalled mods (for UninstallBatch)
	Skipped     []SkippedMod           // Mods skipped due to hook failure or error
	Errors      []error                // Non-fatal errors (after_* hook failures)
}

// InstallBatch installs multiple mods with hook support
// Hook behavior:
// - install.before_all: If fails, return error immediately (unless Force)
// - install.before_each: If fails, skip that mod, continue others
// - install.after_each: If fails, warn (add to Errors), continue
// - install.after_all: If fails, warn (add to Errors)
func (i *Installer) InstallBatch(ctx context.Context, game *domain.Game, mods []*domain.Mod, versions []string, profileName string, opts BatchOptions) (*BatchResult, error) {
	result := &BatchResult{}

	// Run before_all hook
	if opts.Hooks != nil && opts.Hooks.Install.BeforeAll != "" && opts.HookRunner != nil {
		hookCtx := opts.HookContext
		hookCtx.HookName = "install.before_all"
		_, err := opts.HookRunner.Run(ctx, opts.Hooks.Install.BeforeAll, hookCtx)
		if err != nil {
			if !opts.Force {
				return nil, fmt.Errorf("install.before_all hook failed: %w", err)
			}
			// Force mode: add to errors but continue
			result.Errors = append(result.Errors, fmt.Errorf("install.before_all hook failed (forced): %w", err))
		}
	}

	// Install each mod
	for idx, mod := range mods {
		// Apply version if provided
		if idx < len(versions) && versions[idx] != "" {
			mod.Version = versions[idx]
		}

		// Run before_each hook
		if opts.Hooks != nil && opts.Hooks.Install.BeforeEach != "" && opts.HookRunner != nil {
			hookCtx := opts.HookContext
			hookCtx.HookName = "install.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			_, err := opts.HookRunner.Run(ctx, opts.Hooks.Install.BeforeEach, hookCtx)
			if err != nil {
				result.Skipped = append(result.Skipped, SkippedMod{
					Mod:    mod,
					Reason: fmt.Sprintf("install.before_each hook failed: %v", err),
				})
				continue
			}
		}

		// Install the mod
		if err := i.Install(ctx, game, mod, profileName); err != nil {
			result.Skipped = append(result.Skipped, SkippedMod{
				Mod:    mod,
				Reason: fmt.Sprintf("install failed: %v", err),
			})
			continue
		}

		// Run after_each hook
		if opts.Hooks != nil && opts.Hooks.Install.AfterEach != "" && opts.HookRunner != nil {
			hookCtx := opts.HookContext
			hookCtx.HookName = "install.after_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			_, err := opts.HookRunner.Run(ctx, opts.Hooks.Install.AfterEach, hookCtx)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("install.after_each hook failed for %s: %w", mod.ID, err))
			}
		}

		result.Installed = append(result.Installed, InstalledModResult{Mod: *mod})
	}

	// Run after_all hook
	if opts.Hooks != nil && opts.Hooks.Install.AfterAll != "" && opts.HookRunner != nil {
		hookCtx := opts.HookContext
		hookCtx.HookName = "install.after_all"
		_, err := opts.HookRunner.Run(ctx, opts.Hooks.Install.AfterAll, hookCtx)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("install.after_all hook failed: %w", err))
		}
	}

	return result, nil
}

// UninstallBatch uninstalls multiple mods with hook support
// Hook behavior:
// - uninstall.before_all: If fails, return error immediately (unless Force)
// - uninstall.before_each: If fails, skip that mod, continue others
// - uninstall.after_each: If fails, warn (add to Errors), continue
// - uninstall.after_all: If fails, warn (add to Errors)
func (i *Installer) UninstallBatch(ctx context.Context, game *domain.Game, mods []*domain.InstalledMod, profileName string, opts BatchOptions) (*BatchResult, error) {
	result := &BatchResult{}

	// Run before_all hook
	if opts.Hooks != nil && opts.Hooks.Uninstall.BeforeAll != "" && opts.HookRunner != nil {
		hookCtx := opts.HookContext
		hookCtx.HookName = "uninstall.before_all"
		_, err := opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.BeforeAll, hookCtx)
		if err != nil {
			if !opts.Force {
				return nil, fmt.Errorf("uninstall.before_all hook failed: %w", err)
			}
			// Force mode: add to errors but continue
			result.Errors = append(result.Errors, fmt.Errorf("uninstall.before_all hook failed (forced): %w", err))
		}
	}

	// Uninstall each mod
	for _, installedMod := range mods {
		mod := &installedMod.Mod

		// Run before_each hook
		if opts.Hooks != nil && opts.Hooks.Uninstall.BeforeEach != "" && opts.HookRunner != nil {
			hookCtx := opts.HookContext
			hookCtx.HookName = "uninstall.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			_, err := opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.BeforeEach, hookCtx)
			if err != nil {
				result.Skipped = append(result.Skipped, SkippedMod{
					Mod:    mod,
					Reason: fmt.Sprintf("uninstall.before_each hook failed: %v", err),
				})
				continue
			}
		}

		// Uninstall the mod
		if err := i.Uninstall(ctx, game, mod, profileName); err != nil {
			result.Skipped = append(result.Skipped, SkippedMod{
				Mod:    mod,
				Reason: fmt.Sprintf("uninstall failed: %v", err),
			})
			continue
		}

		// Run after_each hook
		if opts.Hooks != nil && opts.Hooks.Uninstall.AfterEach != "" && opts.HookRunner != nil {
			hookCtx := opts.HookContext
			hookCtx.HookName = "uninstall.after_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			_, err := opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.AfterEach, hookCtx)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("uninstall.after_each hook failed for %s: %w", mod.ID, err))
			}
		}

		result.Uninstalled = append(result.Uninstalled, UninstalledModResult{Mod: *mod})
	}

	// Run after_all hook
	if opts.Hooks != nil && opts.Hooks.Uninstall.AfterAll != "" && opts.HookRunner != nil {
		hookCtx := opts.HookContext
		hookCtx.HookName = "uninstall.after_all"
		_, err := opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.AfterAll, hookCtx)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("uninstall.after_all hook failed: %w", err))
		}
	}

	return result, nil
}
