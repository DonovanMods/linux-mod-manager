package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
)

// ProfileResult reports the profile a profile-management mutation acted on:
// the profile as it stands AFTER the mutation for `lmm profile create` and
// `lmm profile reorder`, and as it stood immediately BEFORE it for `lmm
// profile delete` (there is nothing left to report afterwards). It is the
// document those three commands emit under --json (v2 Phase 3 Ruling 15);
// domain.Profile already carries the name, the game and the ordered mod
// refs, which is everything their plain-text output states.
type ProfileResult struct {
	Profile domain.Profile `json:"profile"`
}

// ProfileManager handles profile CRUD operations. Profile switching lives in
// Service.PlanProfileSwitch/ApplyProfileSwitch (internal/core/switch.go).
//
// Every I/O method takes ctx as its first parameter and returns ctx.Err()
// before touching disk, so a cancelled ctx never reads or mutates a profile
// file (v2 Phase 3 Ruling 11). ParseProfile is the exception: a pure
// in-memory parse with no I/O, the same rule that keeps internal/storage/
// config ctx-less.
//
// Callers must not absorb that error into a business-rule warning: a
// mutator that COMPLETES a DB mutation the caller already applied runs
// through core.completeProfileWrite instead, so the profile file and the
// database cannot end a cancelled run disagreeing (Ruling 16).
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
func (pm *ProfileManager) Create(ctx context.Context, gameID, name string) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Check if profile already exists
	_, err := config.LoadProfile(pm.configDir, gameID, name)
	if err == nil {
		return nil, fmt.Errorf("profile already exists: %s", name)
	}
	// The validation error is the user-facing message; don't bury it under
	// the existence-check wrapping.
	if errors.Is(err, domain.ErrInvalidProfileName) || errors.Is(err, domain.ErrInvalidGameID) {
		return nil, err
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

// CreateOrResetDefault creates gameID's "default" profile, or resets it to
// a fresh empty state (no mods) if one already exists. 'lmm game add' and
// 'lmm game detect' have both always overwritten the default profile
// unconditionally when (re-)configuring a game - re-running either against
// an already-configured game intentionally replaces its default profile's
// mod list rather than merge-preserving it (v2 Phase 2 Task 21, preserving
// that behaviour byte-for-byte). Unlike Create, this bypasses the
// existence check on purpose: Create's "profile already exists" error is
// right for standalone profile creation, but wrong for a
// create-or-repair call site.
func (pm *ProfileManager) CreateOrResetDefault(ctx context.Context, gameID string) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	profile := &domain.Profile{
		Name:      "default",
		GameID:    gameID,
		IsDefault: true,
	}
	if err := config.SaveProfile(pm.configDir, profile); err != nil {
		return nil, err
	}
	return profile, nil
}

// List returns all profiles for a game
func (pm *ProfileManager) List(ctx context.Context, gameID string) ([]*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

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

// ListNames returns every profile's bare name (the profiles directory's
// filenames, minus ".yaml") without loading or validating each one -
// tolerant of a profile file List/Get would refuse to parse.
func (pm *ProfileManager) ListNames(ctx context.Context, gameID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return config.ListProfiles(pm.configDir, gameID)
}

// loadProfile is Get's file-read step, indirected through a package
// variable so a test can wrap it to count calls - CountProfileLoadsForTest
// (profile_export_test.go) uses this to verify CheckGameUpdates' lock-state
// stamping loop loads the profile once per call rather than once per
// listed mod. Production code always resolves to config.LoadProfile.
var loadProfile = config.LoadProfile

// Get retrieves a specific profile
func (pm *ProfileManager) Get(ctx context.Context, gameID, name string) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return loadProfile(pm.configDir, gameID, name)
}

// Delete removes a profile
func (pm *ProfileManager) Delete(ctx context.Context, gameID, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return config.DeleteProfile(pm.configDir, gameID, name)
}

// SetDefault sets a profile as the default for a game
func (pm *ProfileManager) SetDefault(ctx context.Context, gameID, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Load the profile to verify it exists
	profile, err := config.LoadProfile(pm.configDir, gameID, name)
	if err != nil {
		return err
	}

	// Clear default flag on all other profiles
	profiles, err := pm.List(ctx, gameID)
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
func (pm *ProfileManager) GetDefault(ctx context.Context, gameID string) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	profiles, err := pm.List(ctx, gameID)
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
func (pm *ProfileManager) AddMod(ctx context.Context, gameID, profileName string, mod domain.ModReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}

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
//
// A LOCKED existing ref refuses a Version move (#143): the record IS the
// lock's target (see the #97 design note), so only explicit lock/unlock
// (SetModLock/ClearModLock) may change a locked ref's Version - never an
// install/update-style upsert. The refusal wraps ErrModLocked and leaves the
// profile unwritten. A same-version upsert (a FileIDs refresh / reinstall
// repair) stays legitimate and preserves the marker as before.
func (pm *ProfileManager) UpsertMod(ctx context.Context, gameID, profileName string, mod domain.ModReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	// Look for existing mod and update in place
	found := false
	for i := range profile.Mods {
		if profile.Mods[i].SourceID == mod.SourceID && profile.Mods[i].ModID == mod.ModID {
			if profile.Mods[i].Locked && profile.Mods[i].Version != mod.Version {
				return fmt.Errorf("%w: %s:%s is locked at v%s in profile %q - refusing to record v%s; move the lock with 'lmm mod lock -s %s -p %s %s <version>' or unlock with 'lmm mod unlock -s %s -p %s %s'",
					ErrModLocked, mod.SourceID, mod.ModID, profile.Mods[i].Version, profileName, mod.Version,
					mod.SourceID, profileName, mod.ModID, mod.SourceID, profileName, mod.ModID)
			}
			profile.Mods[i].Version = mod.Version
			profile.Mods[i].FileIDs = mod.FileIDs
			// Preserve Locked marker on in-place update (#97: survives UpsertMod).
			// Do not modify Locked; it is only changed via explicit lock/unlock operations.
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

// SetModLock marks the profile ref for sourceID/modID as locked (#97: the
// mod refuses `lmm update` while this is set - see flows.go's ApplyUpdate
// gate). A non-empty version also moves the lock's target - ref.Version, the
// same field the installed-version record lives in (a lock has no separate
// target field: the record IS the target while locked). version == ""
// locks at whatever is currently installed, leaving Version untouched.
// Mirrors UpsertMod's load->mutate-in-place->save shape. Returns an error
// naming the mod when it is not already in the profile - a lock must target
// a specific existing install, never create one.
func (pm *ProfileManager) SetModLock(ctx context.Context, gameID, profileName, sourceID, modID, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	for i := range profile.Mods {
		if profile.Mods[i].SourceID == sourceID && profile.Mods[i].ModID == modID {
			profile.Mods[i].Locked = true
			if version != "" {
				profile.Mods[i].Version = version
			}
			return config.SaveProfile(pm.configDir, profile)
		}
	}

	return fmt.Errorf("mod %s:%s not found in profile %q", sourceID, modID, profileName)
}

// ClearModLock clears ONLY the locked marker for sourceID/modID; Version is
// left exactly as it is - it is the installed-version record, not lock-only
// data, and unlocking must not disturb it. Mirrors SetModLock's
// load->mutate-in-place->save shape and not-found error.
func (pm *ProfileManager) ClearModLock(ctx context.Context, gameID, profileName, sourceID, modID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	for i := range profile.Mods {
		if profile.Mods[i].SourceID == sourceID && profile.Mods[i].ModID == modID {
			profile.Mods[i].Locked = false
			return config.SaveProfile(pm.configDir, profile)
		}
	}

	return fmt.Errorf("mod %s:%s not found in profile %q", sourceID, modID, profileName)
}

// RemoveMod removes a mod reference from a profile
func (pm *ProfileManager) RemoveMod(ctx context.Context, gameID, profileName, sourceID, modID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

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
func (pm *ProfileManager) ReorderMods(ctx context.Context, gameID, profileName string, mods []domain.ModReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	profile.Mods = mods
	return config.SaveProfile(pm.configDir, profile)
}

// Export exports a profile to a portable format
func (pm *ProfileManager) Export(ctx context.Context, gameID, profileName string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return nil, err
	}

	// Get installed mods to populate FileIDs.
	installedMods, err := pm.db.GetInstalledMods(ctx, gameID, profileName)
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
func (pm *ProfileManager) Import(ctx context.Context, data []byte) (*domain.Profile, error) {
	return pm.ImportWithOptions(ctx, data, false)
}

// ImportWithOptions imports a profile with optional force overwrite
func (pm *ProfileManager) ImportWithOptions(ctx context.Context, data []byte, force bool) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

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
