package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
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

// UpsertMod adds or updates a mod reference in a profile.
// If the mod exists, it updates Version and FileIDs while preserving position.
// If the mod doesn't exist, it appends to the end.
// This is the preferred method for install/update operations.
func (pm *ProfileManager) UpsertMod(gameID, profileName string, mod domain.ModReference) error {
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	// Look for existing mod and update in place
	found := false
	for i := range profile.Mods {
		if profile.Mods[i].SourceID == mod.SourceID && profile.Mods[i].ModID == mod.ModID {
			profile.Mods[i].Version = mod.Version
			profile.Mods[i].FileIDs = mod.FileIDs
			found = true
			break
		}
	}

	// If not found, append
	if !found {
		profile.Mods = append(profile.Mods, mod)
	}

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
// and deploying the new profile's mods. It fails fast on any error and rolls back
// to the previous state (game dir and default profile) so the system is never left
// in a mixed old/new state.
func (pm *ProfileManager) Switch(ctx context.Context, game *domain.Game, newProfileName string) error {
	newProfile, err := config.LoadProfile(pm.configDir, game.ID, newProfileName)
	if err != nil {
		return fmt.Errorf("loading new profile: %w", err)
	}

	currentProfile, err := pm.GetDefault(game.ID)
	if err != nil && !errors.Is(err, domain.ErrProfileNotFound) {
		return fmt.Errorf("getting current profile: %w", err)
	}
	hadCurrentProfile := currentProfile != nil
	// Ensure current is never nil so rollbacks can safely use it (no mods/overrides when there was no default).
	if currentProfile == nil {
		currentProfile = &domain.Profile{Name: "", GameID: game.ID, Mods: nil, Overrides: nil}
	}

	// Phase 1: Undeploy current profile (fail-fast). Skip if no default was set, or switching to same profile. On error, rollback by re-deploying undeployed mods.
	if hadCurrentProfile && currentProfile.Name != newProfileName && len(currentProfile.Mods) > 0 {
		for i, modRef := range currentProfile.Mods {
			if err := pm.undeployModRefFailFast(ctx, game, modRef); err != nil {
				rollbackErr := pm.rollbackRedeploy(ctx, game, currentProfile.Mods[:i])
				return joinSwitchErr(fmt.Errorf("undeploy %s:%s: %w", modRef.SourceID, modRef.ModID, err), rollbackErr)
			}
		}
	}

	// Phase 2: Deploy new profile (fail-fast). On error, rollback: undeploy new mods we deployed, then redeploy old.
	for i, modRef := range newProfile.Mods {
		if err := pm.deployMod(ctx, game, modRef); err != nil {
			rollbackErr := pm.rollbackAfterDeployFailure(ctx, game, currentProfile, newProfile.Mods[:i])
			return joinSwitchErr(fmt.Errorf("deploy %s:%s: %w", modRef.SourceID, modRef.ModID, err), rollbackErr)
		}
	}

	// Phase 3: Apply new profile overrides. On error, rollback: revert to old profile (mods + overrides).
	if err := ApplyProfileOverrides(game, newProfile); err != nil {
		rollbackErr := pm.rollbackAfterOverridesFailure(ctx, game, currentProfile, newProfile)
		return joinSwitchErr(fmt.Errorf("apply overrides: %w", err), rollbackErr)
	}

	// Phase 4: Set default. On error, rollback full switch so game dir and default stay consistent.
	if err := pm.SetDefault(game.ID, newProfileName); err != nil {
		rollbackErr := pm.rollbackAfterOverridesFailure(ctx, game, currentProfile, newProfile)
		return joinSwitchErr(fmt.Errorf("set default profile: %w", err), rollbackErr)
	}
	return nil
}

func joinSwitchErr(primary, rollback error) error {
	if rollback != nil {
		return fmt.Errorf("profile switch failed: %w (rollback: %v)", primary, rollback)
	}
	return fmt.Errorf("profile switch failed: %w", primary)
}

// undeployModRefFailFast removes a mod from the game directory, returning on first file error.
func (pm *ProfileManager) undeployModRefFailFast(ctx context.Context, game *domain.Game, modRef domain.ModReference) error {
	files, err := pm.cache.ListFiles(game.ID, modRef.SourceID, modRef.ModID, modRef.Version)
	if err != nil {
		return fmt.Errorf("listing cached files: %w", err)
	}
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		dstPath := filepath.Join(game.ModPath, file)
		if err := pm.linker.Undeploy(dstPath); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

// rollbackRedeploy re-deploys the given mods (used after undeploy fail-fast).
func (pm *ProfileManager) rollbackRedeploy(ctx context.Context, game *domain.Game, mods []domain.ModReference) error {
	var errs []error
	for _, modRef := range mods {
		if err := pm.deployMod(ctx, game, modRef); err != nil {
			errs = append(errs, fmt.Errorf("redeploy %s:%s: %w", modRef.SourceID, modRef.ModID, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// rollbackAfterDeployFailure undeploys the new mods we deployed, then redeploys the old profile.
func (pm *ProfileManager) rollbackAfterDeployFailure(ctx context.Context, game *domain.Game, current *domain.Profile, deployedNew []domain.ModReference) error {
	var errs []error
	for _, modRef := range deployedNew {
		if err := pm.undeployModRef(ctx, game, modRef); err != nil {
			errs = append(errs, fmt.Errorf("undeploy new %s:%s: %w", modRef.SourceID, modRef.ModID, err))
		}
	}
	if current != nil && len(current.Mods) > 0 {
		for _, modRef := range current.Mods {
			if err := pm.deployMod(ctx, game, modRef); err != nil {
				errs = append(errs, fmt.Errorf("redeploy old %s:%s: %w", modRef.SourceID, modRef.ModID, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// rollbackAfterOverridesFailure restores old profile: undeploy new mods, deploy old mods, apply old overrides.
func (pm *ProfileManager) rollbackAfterOverridesFailure(ctx context.Context, game *domain.Game, current *domain.Profile, newProfile *domain.Profile) error {
	var errs []error
	for _, modRef := range newProfile.Mods {
		if err := pm.undeployModRef(ctx, game, modRef); err != nil {
			errs = append(errs, fmt.Errorf("undeploy new %s:%s: %w", modRef.SourceID, modRef.ModID, err))
		}
	}
	if current != nil {
		for _, modRef := range current.Mods {
			if err := pm.deployMod(ctx, game, modRef); err != nil {
				errs = append(errs, fmt.Errorf("redeploy old %s:%s: %w", modRef.SourceID, modRef.ModID, err))
			}
		}
		if len(current.Overrides) > 0 {
			if err := ApplyProfileOverrides(game, current); err != nil {
				errs = append(errs, fmt.Errorf("restore old overrides: %w", err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
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

	var errs []error
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dstPath := filepath.Join(game.ModPath, file)
		if err := pm.linker.Undeploy(dstPath); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Export exports a profile to a portable format
func (pm *ProfileManager) Export(gameID, profileName string) ([]byte, error) {
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return nil, err
	}

	// Get installed mods to populate FileIDs
	installedMods, err := pm.db.GetInstalledMods(gameID, profileName)
	if err == nil {
		// Build lookup map of installed mods by source:mod key
		installedMap := make(map[string]*domain.InstalledMod)
		for i := range installedMods {
			key := installedMods[i].SourceID + ":" + installedMods[i].ID
			installedMap[key] = &installedMods[i]
		}

		// Populate FileIDs in profile mods
		for i := range profile.Mods {
			key := profile.Mods[i].SourceID + ":" + profile.Mods[i].ModID
			if installed, ok := installedMap[key]; ok {
				profile.Mods[i].FileIDs = installed.FileIDs
			}
		}
	}

	return config.ExportProfile(profile)
}

// Import imports a profile from portable format
func (pm *ProfileManager) Import(data []byte) (*domain.Profile, error) {
	return pm.ImportWithOptions(data, false)
}

// ImportWithOptions imports a profile with optional force overwrite
func (pm *ProfileManager) ImportWithOptions(data []byte, force bool) (*domain.Profile, error) {
	profile, err := config.ImportProfile(data)
	if err != nil {
		return nil, err
	}

	// Check if profile already exists
	_, existErr := config.LoadProfile(pm.configDir, profile.GameID, profile.Name)
	if existErr == nil && !force {
		return nil, fmt.Errorf("profile already exists: %s (use --force to overwrite)", profile.Name)
	}

	if err := config.SaveProfile(pm.configDir, profile); err != nil {
		return nil, err
	}

	return profile, nil
}

// ParseProfile parses profile data without saving (for preview)
func (pm *ProfileManager) ParseProfile(data []byte) (*domain.Profile, error) {
	return config.ImportProfile(data)
}
