package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// ProfileResult reports the profile a profile-management mutation acted on:
// the profile as it stands AFTER the mutation for `lmm profile create`,
// `reorder`, `rename` and `set-default`, and as it stood immediately BEFORE
// it for `lmm profile delete` (there is nothing left to report afterwards).
// It is the document those five commands emit under --json (v2 Phase 3
// Ruling 15); domain.Profile already carries the name, the game and the
// ordered mod refs, which is everything their plain-text output states.
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
		return nil, fmt.Errorf("%w: %s", ErrProfileExists, name)
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

// CreateOrResetDefaultAfterGameSave behaves exactly like CreateOrResetDefault,
// except the write always finishes even under an already-cancelled ctx:
// 'lmm game add' and 'lmm game detect' both save the game's own config row
// FIRST, so by the time this runs a committed write already exists with no
// default profile behind it yet - the class-(A) shape Ruling 16 is built on.
// Routing through completeProfileWrite keeps that pair from disagreeing
// under a cancellation the way every other class-(A) site does: the profile
// is still created, and the caller still ends the run with context.Canceled
// (v2 Phase 3 Ruling 16, task 18 re-review round 2 NEW-4).
func (pm *ProfileManager) CreateOrResetDefaultAfterGameSave(ctx context.Context, gameID string) (*domain.Profile, error) {
	var profile *domain.Profile
	err := completeProfileWrite(ctx, func(ctx context.Context) error {
		p, err := pm.CreateOrResetDefault(ctx, gameID)
		profile = p
		return err
	})
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
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

// Rename renames gameID's profile oldName to newName, moving everything
// that names it: the profile file itself (the filename AND the name inside
// it), and every DB row keyed by profile - installed_mods,
// installed_mod_files and deployed_files (db.RenameProfile). It returns the
// profile as it now stands, under its new name.
//
// Everything else that could conceivably reference a profile was checked
// and needs nothing (#332):
//
//   - The DEFAULT-profile setting is not stored outside the profile: it is
//     the is_default flag INSIDE the profile file (SetDefault/GetDefault),
//     so it travels with the rename and a renamed default stays default.
//   - Hooks and config overrides likewise live inside the profile file.
//   - games.yaml carries no profile field at all.
//   - Cache paths are keyed by game/source/mod/version, never by profile.
//   - The merged pak's fingerprint marker lives at a per-GAME cache path;
//     its deployed-file ownership rows move with deployed_files above.
//   - Deployed files themselves live under the game directory at paths
//     that never contain the profile name, so nothing on disk moves.
//
// Refusals happen before anything is written: an unknown oldName is
// domain.ErrProfileNotFound, an already-taken newName (oldName == newName
// included, and a name orphaned DB rows still claim - see
// refuseOccupiedName) is core.ErrProfileExists, and an unusable newName
// fails config's own path validation.
//
// Once the first write lands, ctx cancellation can no longer stop the
// chain (completeRename), but an I/O FAILURE still can, and is met with
// best-effort compensation rather than left to draw two files: a step-2
// (db.RenameProfile) failure removes the file step 1 just wrote, so the
// world looks exactly like the rename never started; a step-3
// (config.DeleteProfile(oldName)) failure is different in kind, because by
// then the DB half is already committed - the rename DID take effect, so
// undoing step 1 would leave the DB naming a profile with no file behind
// it. The new name wins: the old file is left in place (reported, not
// silently dropped) but stripped of IsDefault if the source profile carried
// it, so it can never read as a second default alongside the one that just
// moved. Either failure's error names what was compensated.
func (pm *ProfileManager) Rename(ctx context.Context, gameID, oldName, newName string) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, oldName)
	if err != nil {
		return nil, err
	}
	if err := pm.refuseOccupiedName(ctx, gameID, newName); err != nil {
		return nil, err
	}

	renamed := *profile
	renamed.Name = newName
	err = completeRename(ctx, func(ctx context.Context) error {
		if err := config.SaveProfile(pm.configDir, &renamed); err != nil {
			return err
		}
		if err := pm.db.RenameProfile(ctx, gameID, oldName, newName); err != nil {
			if rmErr := config.DeleteProfile(pm.configDir, gameID, newName); rmErr != nil {
				return fmt.Errorf("renaming profile: %w (compensation also failed: could not remove %s: %v)", err, newName, rmErr)
			}
			return fmt.Errorf("renaming profile: %w (compensated: removed the newly written %s)", err, newName)
		}
		if err := config.DeleteProfile(pm.configDir, gameID, oldName); err != nil {
			compensated := ""
			if profile.IsDefault {
				stale := *profile
				stale.IsDefault = false
				if saveErr := config.SaveProfile(pm.configDir, &stale); saveErr == nil {
					compensated = " (compensated: cleared is_default on the old file so it cannot be mistaken for a second default)"
				} else {
					compensated = fmt.Sprintf(" (compensation also failed: could not clear is_default on the old file: %v)", saveErr)
				}
			}
			return fmt.Errorf("renamed %s to %s, but could not remove the old profile file: %w%s", oldName, newName, err, compensated)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &renamed, nil
}

// refuseOccupiedName refuses name if a profile already claims it for
// gameID - either on disk (config.LoadProfile succeeds) or, orphaned from a
// Delete that only ever removed the file (ProfileManager.Delete's own doc
// comment), in the DB's installed_mods rows specifically - not every table
// a profile name can appear in (deployed_files and installed_mod_files
// both key on it too, unchecked here; see the doc comment where this is
// called from Rename for what still catches an orphan in either of THOSE).
// Checking disk and installed_mods closes a reproducible gap (#332 I2): a
// name can be free on disk and still occupied in the DB, where
// installed_mods' UNIQUE(source_id, mod_id, game_id, profile_name)
// constraint (migrations.go) would otherwise reject db.RenameProfile's
// UPDATE AFTER config.SaveProfile has already written the new profile
// file. Checking both refusals up front, before Rename's first write,
// keeps THAT failure from ever being reachable through this path.
func (pm *ProfileManager) refuseOccupiedName(ctx context.Context, gameID, name string) error {
	if _, err := config.LoadProfile(pm.configDir, gameID, name); err == nil {
		return fmt.Errorf("%w: %s", ErrProfileExists, name)
	}
	rows, err := pm.db.GetInstalledMods(ctx, gameID, name)
	if err != nil {
		return fmt.Errorf("checking %q for orphaned rows: %w", name, err)
	}
	if len(rows) > 0 {
		return fmt.Errorf("%w: %s (installed_mods rows remain from a deleted profile of that name)", ErrProfileExists, name)
	}
	return nil
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
				// #311: routed through the canonical constructor rather
				// than hand-worded here. This gate refuses only at a
				// DIFFERENT version - moving the lock genuinely unblocks it
				// - so it is LockedRefRefusalError's kind (move-or-unlock),
				// not the unlock-only sibling's. The profile ref carries no
				// display name, so ModKey stands in for it, exactly as the
				// hand-worded sentence did; the "refusing to record vX"
				// datum this gate alone has is appended, since it names the
				// version the write was ASKING for, which the shared
				// sentence has no field for. #294 made this line
				// unconditional on apply/sync/switch, which is what made
				// its old `profile %q` quoting user-visible drift.
				locked := profile.Mods[i]
				return fmt.Errorf("%w (refusing to record v%s)",
					LockedRefRefusalError(
						domain.Mod{ID: mod.ModID, SourceID: mod.SourceID, Name: domain.ModKey(mod.SourceID, mod.ModID)},
						profileName,
						&locked,
					),
					mod.Version)
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
// mod refuses `lmm update` while this is set - see update.go's ApplyUpdate
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

// loadForExport loads gameID/profileName's profile and backfills each mod
// ref's FileIDs from its installed-mods DB row - the enrichment step Export
// (YAML) and Service.ExportProfile (`lmm profile export --json`, #309)
// share, so the portable document always carries the same FileIDs
// regardless of format.
func (pm *ProfileManager) loadForExport(ctx context.Context, gameID, profileName string) (*domain.Profile, error) {
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

	return profile, nil
}

// Export exports a profile to a portable format
func (pm *ProfileManager) Export(ctx context.Context, gameID, profileName string) ([]byte, error) {
	profile, err := pm.loadForExport(ctx, gameID, profileName)
	if err != nil {
		return nil, err
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

// RenameProfile is the gated Service seam over ProfileManager.Rename: it
// takes the same one-slot mutation semaphore every other mutating flow does
// (a rename moves DB rows and profile files, so it must not interleave with
// an install or a deploy) and returns the ProfileResult document `lmm
// profile rename --json` emits - the profile under its new name.
//
// It is one of the sanctioned single-step mutations rather than a
// Plan/Apply pair: there is nothing to preview. A rename's effect is
// entirely described by the two names the caller already typed.
func (s *Service) RenameProfile(ctx context.Context, gameID, oldName, newName string) (*ProfileResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	profile, err := s.NewProfileManager().Rename(ctx, gameID, oldName, newName)
	if err != nil {
		return nil, err
	}
	return &ProfileResult{Profile: *profile}, nil
}

// CreateProfile is the gated Service seam over ProfileManager.Create: the
// same one-slot mutation semaphore every other mutating flow takes, so a
// create cannot interleave with a deploy or an install racing to write the
// same game's state. Returns the ProfileResult document `lmm profile create
// --json` emits - the profile as created, empty apart from its identity.
//
// It is one of the sanctioned single-step mutations rather than a
// Plan/Apply pair: there is nothing to preview. A name already taken
// surfaces as the typed ErrProfileExists, detected INSIDE this gate by
// ProfileManager.Create itself (#332 M6) rather than by an un-gated
// pre-check that could race the write.
func (s *Service) CreateProfile(ctx context.Context, gameID, name string) (*ProfileResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	profile, err := s.NewProfileManager().Create(ctx, gameID, name)
	if err != nil {
		return nil, err
	}
	return &ProfileResult{Profile: *profile}, nil
}

// DeleteProfile is the gated Service seam over ProfileManager.Delete: the
// same one-slot mutation semaphore every other mutating flow takes, so a
// delete cannot race a deploy that is mid-flight for the very profile being
// removed (#332 I1) - ProfileManager.Delete only ever removes the profile
// FILE, so an un-gated delete racing a deploy job left deployed_files rows
// naming a profile that no longer existed. Returns the ProfileResult
// document `lmm profile delete --json` emits: the profile as it stood
// immediately BEFORE the delete (core.ProfileResult's own doc comment) - a
// readable profile is read first, an unreadable-but-present one still
// deletes and the document then names it and nothing else, mirroring the
// CLI's own fallback (cmd/lmm/profile.go's doProfileDelete).
func (s *Service) DeleteProfile(ctx context.Context, gameID, name string) (*ProfileResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	pm := s.NewProfileManager()
	deleted := domain.Profile{Name: name, GameID: gameID}
	if p, err := pm.Get(ctx, gameID, name); err == nil {
		deleted = *p
	}

	if err := pm.Delete(ctx, gameID, name); err != nil {
		return nil, err
	}
	return &ProfileResult{Profile: deleted}, nil
}

// SetDefaultProfile is the gated Service seam over ProfileManager.
// SetDefault: the same one-slot mutation semaphore every other mutating
// flow takes, so a set-default cannot interleave with a deploy or install
// racing to write the same game's profiles. Returns the ProfileResult of
// the profile that is now the default, re-read after the write so the
// document carries is_default as persisted rather than as requested -
// SetDefault clears the flag on every other profile itself.
func (s *Service) SetDefaultProfile(ctx context.Context, gameID, name string) (*ProfileResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	pm := s.NewProfileManager()
	if err := pm.SetDefault(ctx, gameID, name); err != nil {
		return nil, err
	}
	profile, err := pm.Get(ctx, gameID, name)
	if err != nil {
		return nil, err
	}
	return &ProfileResult{Profile: *profile}, nil
}
