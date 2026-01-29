package core

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// Installer handles mod installation and uninstallation
type Installer struct {
	cache  *cache.Cache
	linker linker.Linker
}

// NewInstaller creates a new installer
func NewInstaller(cache *cache.Cache, linker linker.Linker) *Installer {
	return &Installer{
		cache:  cache,
		linker: linker,
	}
}

// Install deploys a mod to the game directory
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

	// Deploy each file
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		srcPath := i.cache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)

		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			return fmt.Errorf("deploying %s: %w", file, err)
		}
	}

	return nil
}

// Uninstall removes a mod from the game directory
func (i *Installer) Uninstall(ctx context.Context, game *domain.Game, mod *domain.Mod) error {
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

	// Clean up any empty directories left behind
	linker.CleanupEmptyDirs(game.ModPath)

	return nil
}

// IsInstalled checks if a mod is currently deployed
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

	// Check if the first file is deployed
	dstPath := filepath.Join(game.ModPath, files[0])
	return i.linker.IsDeployed(dstPath)
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
