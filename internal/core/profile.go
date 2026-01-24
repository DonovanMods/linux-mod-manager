package core

import (
	"context"
	"fmt"
	"path/filepath"

	"lmm/internal/domain"
	"lmm/internal/linker"
	"lmm/internal/storage/cache"
	"lmm/internal/storage/config"
	"lmm/internal/storage/db"
)

// ProfileManager handles profile CRUD operations and switching
type ProfileManager struct {
	configDir string
	db        *db.DB
	cache     *cache.Cache
	linker    linker.Linker
}

// NewProfileManager creates a new profile manager
func NewProfileManager(configDir string, database *db.DB, cache *cache.Cache, lnk linker.Linker) *ProfileManager {
	return &ProfileManager{
		configDir: configDir,
		db:        database,
		cache:     cache,
		linker:    lnk,
	}
}

// Create creates a new profile for a game
func (pm *ProfileManager) Create(gameID, name string) (*domain.Profile, error) {
	// Check if profile already exists
	_, err := config.LoadProfile(pm.configDir, gameID, name)
	if err == nil {
		return nil, fmt.Errorf("profile already exists: %s", name)
	}
	if err != domain.ErrProfileNotFound {
		return nil, fmt.Errorf("checking profile: %w", err)
	}

	profile := &domain.Profile{
		Name:   name,
		GameID: gameID,
		Mods:   []domain.ModReference{},
	}

	if err := config.SaveProfile(pm.configDir, profile); err != nil {
		return nil, fmt.Errorf("saving profile: %w", err)
	}

	return profile, nil
}

// List returns all profiles for a game
func (pm *ProfileManager) List(gameID string) ([]*domain.Profile, error) {
	names, err := config.ListProfiles(pm.configDir, gameID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}

	profiles := make([]*domain.Profile, 0, len(names))
	for _, name := range names {
		profile, err := config.LoadProfile(pm.configDir, gameID, name)
		if err != nil {
			continue // Skip profiles that can't be loaded
		}
		profiles = append(profiles, profile)
	}

	return profiles, nil
}

// Get retrieves a specific profile
func (pm *ProfileManager) Get(gameID, name string) (*domain.Profile, error) {
	return config.LoadProfile(pm.configDir, gameID, name)
}

// Delete removes a profile
func (pm *ProfileManager) Delete(gameID, name string) error {
	return config.DeleteProfile(pm.configDir, gameID, name)
}

// SetDefault sets a profile as the default for a game
func (pm *ProfileManager) SetDefault(gameID, name string) error {
	// Load the profile to verify it exists
	profile, err := config.LoadProfile(pm.configDir, gameID, name)
	if err != nil {
		return err
	}

	// Clear default flag on all other profiles
	profiles, err := pm.List(gameID)
	if err != nil {
		return err
	}

	for _, p := range profiles {
		if p.IsDefault && p.Name != name {
			p.IsDefault = false
			if err := config.SaveProfile(pm.configDir, p); err != nil {
				return fmt.Errorf("clearing default on %s: %w", p.Name, err)
			}
		}
	}

	// Set this profile as default
	profile.IsDefault = true
	return config.SaveProfile(pm.configDir, profile)
}

// GetDefault returns the default profile for a game
func (pm *ProfileManager) GetDefault(gameID string) (*domain.Profile, error) {
	profiles, err := pm.List(gameID)
	if err != nil {
		return nil, err
	}

	for _, p := range profiles {
		if p.IsDefault {
			return p, nil
		}
	}

	// Return first profile if no default set
	if len(profiles) > 0 {
		return profiles[0], nil
	}

	return nil, domain.ErrProfileNotFound
}

// AddMod adds a mod reference to a profile
func (pm *ProfileManager) AddMod(gameID, profileName string, mod domain.ModReference) error {
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	// Check for duplicates
	for _, m := range profile.Mods {
		if m.SourceID == mod.SourceID && m.ModID == mod.ModID {
			return fmt.Errorf("mod already in profile: %s:%s", mod.SourceID, mod.ModID)
		}
	}

	profile.Mods = append(profile.Mods, mod)
	return config.SaveProfile(pm.configDir, profile)
}

// RemoveMod removes a mod reference from a profile
func (pm *ProfileManager) RemoveMod(gameID, profileName, sourceID, modID string) error {
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	found := false
	newMods := make([]domain.ModReference, 0, len(profile.Mods))
	for _, m := range profile.Mods {
		if m.SourceID == sourceID && m.ModID == modID {
			found = true
			continue
		}
		newMods = append(newMods, m)
	}

	if !found {
		return domain.ErrModNotFound
	}

	profile.Mods = newMods
	return config.SaveProfile(pm.configDir, profile)
}

// ReorderMods updates the load order of mods in a profile
func (pm *ProfileManager) ReorderMods(gameID, profileName string, mods []domain.ModReference) error {
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	profile.Mods = mods
	return config.SaveProfile(pm.configDir, profile)
}

// Switch switches to a different profile, undeploying the current profile's mods
// and deploying the new profile's mods
func (pm *ProfileManager) Switch(ctx context.Context, game *domain.Game, newProfileName string) error {
	// Load the new profile
	newProfile, err := config.LoadProfile(pm.configDir, game.ID, newProfileName)
	if err != nil {
		return fmt.Errorf("loading new profile: %w", err)
	}

	// Get current default profile to undeploy its mods
	currentProfile, err := pm.GetDefault(game.ID)
	if err == nil && currentProfile.Name != newProfileName {
		// Undeploy current profile's mods
		for _, modRef := range currentProfile.Mods {
			if err := pm.undeployModRef(ctx, game, modRef); err != nil {
				// Log but continue - try to undeploy as much as possible
				continue
			}
		}
	}

	// Deploy mods for the new profile
	for _, modRef := range newProfile.Mods {
		if err := pm.deployMod(ctx, game, modRef); err != nil {
			// Log but continue - deploy as much as possible
			continue
		}
	}

	// Set new profile as default
	return pm.SetDefault(game.ID, newProfileName)
}

// deployMod deploys a single mod to the game directory
func (pm *ProfileManager) deployMod(ctx context.Context, game *domain.Game, modRef domain.ModReference) error {
	// Check if mod is cached
	if !pm.cache.Exists(game.ID, modRef.SourceID, modRef.ModID, modRef.Version) {
		return fmt.Errorf("mod not cached: %s:%s@%s", modRef.SourceID, modRef.ModID, modRef.Version)
	}

	// Get list of files
	files, err := pm.cache.ListFiles(game.ID, modRef.SourceID, modRef.ModID, modRef.Version)
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

		srcPath := pm.cache.GetFilePath(game.ID, modRef.SourceID, modRef.ModID, modRef.Version, file)
		dstPath := filepath.Join(game.ModPath, file)

		if err := pm.linker.Deploy(srcPath, dstPath); err != nil {
			return fmt.Errorf("deploying %s: %w", file, err)
		}
	}

	return nil
}

// undeployModRef removes a mod from the game directory using a ModReference
func (pm *ProfileManager) undeployModRef(ctx context.Context, game *domain.Game, modRef domain.ModReference) error {
	// Get list of files
	files, err := pm.cache.ListFiles(game.ID, modRef.SourceID, modRef.ModID, modRef.Version)
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
		if err := pm.linker.Undeploy(dstPath); err != nil {
			// Log but continue
			continue
		}
	}

	return nil
}

// Export exports a profile to a portable format
func (pm *ProfileManager) Export(gameID, profileName string) ([]byte, error) {
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return nil, err
	}

	return config.ExportProfile(profile)
}

// Import imports a profile from portable format
func (pm *ProfileManager) Import(data []byte) (*domain.Profile, error) {
	profile, err := config.ImportProfile(data)
	if err != nil {
		return nil, err
	}

	// Check if profile already exists
	_, existErr := config.LoadProfile(pm.configDir, profile.GameID, profile.Name)
	if existErr == nil {
		return nil, fmt.Errorf("profile already exists: %s", profile.Name)
	}

	if err := config.SaveProfile(pm.configDir, profile); err != nil {
		return nil, err
	}

	return profile, nil
}
