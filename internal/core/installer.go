package core

import (
	"context"
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
			if rollbackErr := rollbackDeploy(i.linker, game.ModPath, deployed); rollbackErr != nil {
				err = fmt.Errorf("deploying %s: %w; rollback failed (some files may remain deployed): %v", file, err, rollbackErr)
			} else {
				err = fmt.Errorf("deploying %s: %w", file, err)
			}
			if i.db != nil {
				_ = i.db.DeleteDeployedFiles(game.ID, profileName, mod.SourceID, mod.ID)
			}
			return err
		}
		deployed = append(deployed, file)

		// Track file ownership in database (for conflict detection)
		if i.db != nil {
			if err := i.db.SaveDeployedFile(game.ID, profileName, file, mod.SourceID, mod.ID); err != nil {
				// Roll back only the file that failed to track; leave previously
				// deployed+tracked files and DB records intact.
				if rollbackErr := rollbackDeploy(i.linker, game.ModPath, []string{file}); rollbackErr != nil {
					return fmt.Errorf("tracking deployed file %s: %w; rollback failed (file may remain deployed but untracked): %v", file, err, rollbackErr)
				}
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
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
