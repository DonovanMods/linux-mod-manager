package core

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
)

// ProfileManager handles profile CRUD operations. Profile switching lives in
// Service.PlanProfileSwitch/ApplyProfileSwitch (internal/core/flows.go).
type ProfileManager struct {
	configDir string
	db        *db.DB
}

// NewProfileManager creates a new profile manager
func NewProfileManager(configDir string, database *db.DB) *ProfileManager {
	return &ProfileManager{
		configDir: configDir,
		db:        database,
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
			installedMap[domain.ModKey(installedMods[i].SourceID, installedMods[i].ID)] = &installedMods[i]
		}

		// Populate FileIDs in profile mods
		for i := range profile.Mods {
			key := domain.ModKey(profile.Mods[i].SourceID, profile.Mods[i].ModID)
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
