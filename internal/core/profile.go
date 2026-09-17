package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

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
	// warn is the Service's always-on user channel (ServiceConfig.
	// WarnWriter), where a save reports what it did beyond writing the
	// document (save); nil for a ProfileManager built without a Service.
	warn io.Writer
}

// NewProfileManager creates a new profile manager
func NewProfileManager(configDir string, database *db.DB) *ProfileManager {
	return &ProfileManager{
		configDir: configDir,
		db:        database,
	}
}

// save writes profile to its file and prints on warn whatever the save owes
// the user a word about (config.SaveReport.Notices): a layout it had to
// rewrite whole and where it kept the original, or a write it could not make
// atomically (#441 review F6, F10).
func (pm *ProfileManager) save(profile *domain.Profile) error {
	report, err := config.SaveProfileReporting(pm.configDir, profile)
	pm.report(report)
	return err
}

// saveRenamed is save for config.SaveRenamedProfile.
func (pm *ProfileManager) saveRenamed(profile *domain.Profile, oldName string) error {
	report, err := config.SaveRenamedProfileReporting(pm.configDir, profile, oldName)
	pm.report(report)
	return err
}

func (pm *ProfileManager) report(report config.SaveReport) {
	if pm.warn == nil {
		return
	}
	for _, notice := range report.Notices() {
		_, _ = fmt.Fprintf(pm.warn, "warning: %s\n", notice)
	}
}

// Create creates a new profile for a game. A game's first profile is
// written marked active (isFirstProfile).
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

	first, err := pm.isFirstProfile(gameID)
	if err != nil {
		return nil, err
	}
	profile := &domain.Profile{
		Name:      name,
		GameID:    gameID,
		Mods:      []domain.ModReference{},
		IsDefault: first,
	}

	if err := pm.save(profile); err != nil {
		return nil, fmt.Errorf("saving profile: %w", err)
	}

	return profile, nil
}

// isFirstProfile reports whether gameID has no profile file yet, so the one
// about to be written is its only profile - and, marked `is_default: true`,
// its active one (#445 review F2, ruling B). Without the marker a second
// profile would leave the game with none marked, which every flow that
// deploys or removes files refuses as ambiguous.
func (pm *ProfileManager) isFirstProfile(gameID string) (bool, error) {
	names, err := config.ListProfiles(pm.configDir, gameID)
	if err != nil {
		return false, fmt.Errorf("listing profiles: %w", err)
	}
	return len(names) == 0, nil
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
//
// The reset profile is the game's active one unless another profile
// already is (#446): re-configuring a game whose user has switched to
// another profile must not leave two profiles marked `is_default`. A game
// whose only profile file is another, unmarked one has that profile active
// too (#445 review F2, ruling A), and one whose files mark no single
// profile - or cannot all be read - is left as it is rather than resolved
// by a guess: default is marked only when it already was, or when it is
// the game's only profile.
func (pm *ProfileManager) CreateOrResetDefault(ctx context.Context, gameID string) (*domain.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// The path is checked first, so an unusable game id reads as exactly
	// that rather than as a listing failure.
	if _, err := config.ProfilePath(pm.configDir, gameID, "default"); err != nil {
		return nil, err
	}
	flags, err := readProfileFlags(pm.configDir, gameID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	others := slices.DeleteFunc(slices.Clone(flags.names), func(n string) bool { return n == "default" })
	markedOthers := slices.DeleteFunc(slices.Clone(flags.flagged), func(n string) bool { return n == "default" })
	profile := &domain.Profile{
		Name:      "default",
		GameID:    gameID,
		IsDefault: len(markedOthers) == 0 && (len(others) == 0 || slices.Contains(flags.flagged, "default")),
	}
	if err := pm.save(profile); err != nil {
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

// Delete removes a profile. The game's active profile is refused with
// ErrProfileActive (#446) - its mods are the ones in the game directory, and
// without its file the game has no active profile - unless it is the game's
// only profile and nothing is installed under it.
func (pm *ProfileManager) Delete(ctx context.Context, gameID, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := pm.refuseDeletingActive(ctx, gameID, name); err != nil {
		return err
	}
	return config.DeleteProfile(pm.configDir, gameID, name)
}

// refuseDeletingActive is Delete's #446 guard.
func (pm *ProfileManager) refuseDeletingActive(ctx context.Context, gameID, name string) error {
	live, err := pm.liveProfile(ctx, gameID)
	if err != nil || live != name {
		return err
	}
	names, err := config.ListProfiles(pm.configDir, gameID)
	if err != nil {
		return err
	}
	installed, err := pm.db.GetInstalledMods(ctx, gameID, name)
	if err != nil {
		return err
	}
	if len(names) <= 1 && len(installed) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %q is the active profile of %s, and its mods are the ones in the game directory - switch to another profile first (`lmm profile switch <name>`), then delete it",
		ErrProfileActive, name, gameID)
}

// liveProfile is Service.liveProfile for a caller holding only a
// ProfileManager.
func (pm *ProfileManager) liveProfile(ctx context.Context, gameID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	flags, err := readProfileFlags(pm.configDir, gameID)
	if err != nil {
		return "", fmt.Errorf("resolving the active profile for %s: %w", gameID, err)
	}
	return flags.active()
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
		if err := pm.saveRenamed(&renamed, oldName); err != nil {
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
				if saveErr := pm.save(&stale); saveErr == nil {
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

// SetDefault makes name gameID's active profile: its file says
// `is_default: true`, and no other profile's does.
//
// name is marked FIRST (#446), so a write that fails leaves the game with
// the active profile it had rather than with none. A profile that then
// cannot be unmarked leaves two, and the error names it.
func (pm *ProfileManager) SetDefault(ctx context.Context, gameID, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Load the profile to verify it exists
	profile, err := config.LoadProfile(pm.configDir, gameID, name)
	if err != nil {
		return err
	}
	profiles, err := pm.List(ctx, gameID)
	if err != nil {
		return err
	}

	if !profile.IsDefault {
		profile.IsDefault = true
		if err := pm.save(profile); err != nil {
			return err
		}
	}

	for _, p := range profiles {
		if p.IsDefault && p.Name != name {
			p.IsDefault = false
			if err := pm.save(p); err != nil {
				return fmt.Errorf("made %q the active profile, but could not clear is_default on %q, so both are marked active - fix or remove %q's is_default by hand: %w",
					name, p.Name, p.Name, err)
			}
		}
	}
	return nil
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
	return pm.save(profile)
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
			//
			// Disabled (#431) follows the identical rule and for the
			// identical reason: it is the user's per-profile off intent,
			// set and cleared only by DisableMod/EnableMod. Every
			// install/update/converge caller builds a fresh ModReference
			// with Disabled false, so copying mod.Disabled here would let a
			// reinstall or a version convergence silently switch a mod the
			// user turned off back on - the very thing #431 exists to stop.
			found = true
			break
		}
	}

	// If not found, append
	if !found {
		profile.Mods = append(profile.Mods, mod)
	}

	return pm.save(profile)
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
			return pm.save(profile)
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
			return pm.save(profile)
		}
	}

	return fmt.Errorf("mod %s:%s not found in profile %q", sourceID, modID, profileName)
}

// SetModDisabled records #431's per-profile off intent on the profile ref
// for sourceID/modID: disabled true writes the `disabled: true` marker,
// false clears it (and, because the field is `omitempty`, removes the key
// entirely, so a profile whose mods are all enabled again is byte-identical
// to one that never had the marker at all).
//
// Mirrors SetModLock/ClearModLock's load -> mutate-in-place -> save shape
// and, like them, touches nothing else on the ref: the load-order position
// and the pinned Version are exactly what a disable must NOT throw away.
//
// A mod listed more than once (a hand edit) has every copy written, so the
// document never ends up with copies that disagree (see firstRefs).
//
// A mod the profile does not list returns domain.ErrModNotFound, so the
// caller can tell "nothing to record here" apart from a real write failure.
// That is not an error condition for disable/enable: an installed row whose
// profile never listed it has no desired-state entry to mark, and no
// converge pass will consult one.
func (pm *ProfileManager) SetModDisabled(ctx context.Context, gameID, profileName, sourceID, modID string, disabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}

	found, changed := false, false
	for i := range profile.Mods {
		if profile.Mods[i].SourceID == sourceID && profile.Mods[i].ModID == modID {
			found = true
			changed = changed || profile.Mods[i].Disabled != disabled
			profile.Mods[i].Disabled = disabled
		}
	}
	switch {
	case !found:
		return domain.ErrModNotFound
	case !changed:
		// Already what it should be - don't rewrite the file. A hand-edited
		// profile stays byte-for-byte as its author left it whenever the
		// intent already matches.
		return nil
	}
	return pm.save(profile)
}

// profileDisabledKeys is which of gameID/profileName's mod references carry
// #431's `disabled:` marker, keyed by domain.ModKey - the document's own
// answer to "is this mod switched off?", for a flow that selects mods from
// the database and has to honour the desired state as well.
//
// A profile that cannot be loaded (none exists yet, or it is unreadable)
// returns an empty set, which is exactly "the document marks nothing off":
// the callers all have DB rows to work from either way, and a converge or
// deploy pass must not fail over a missing desired-state document it is
// only consulting.
func (s *Service) profileDisabledKeys(ctx context.Context, gameID, profileName string) map[string]bool {
	if ctx.Err() != nil {
		return nil
	}
	return s.documentDisabledKeys(gameID, profileName)
}

// documentDisabledKeys is profileDisabledKeys for a caller with no ctx to
// check - the plan snapshot (snapshotOf) - reading the same file the same
// way.
func (s *Service) documentDisabledKeys(gameID, profileName string) map[string]bool {
	profile, err := loadProfile(s.configDir, gameID, profileName)
	if err != nil {
		return nil
	}
	return disabledKeysOf(profile)
}

// disabledKeysOf is which of profile's mods the document marks
// `disabled:`, keyed by domain.ModKey - each decided by its first
// reference (firstRefs).
func disabledKeysOf(profile *domain.Profile) map[string]bool {
	disabled := make(map[string]bool)
	for key, ref := range firstRefs(profile.Mods) {
		if ref.Disabled {
			disabled[key] = true
		}
	}
	return disabled
}

// firstRefs keys refs by domain.ModKey, keeping each mod's FIRST reference.
// lmm never lists a mod twice, but a hand edit can, and the copies can
// disagree about the `disabled:` marker (#431). Every flow decides such a
// mod by its first copy - the one domain.Profile.FindRef and a profile's
// load order already name - so no two flows disagree about whether it is
// switched off.
func firstRefs(refs []domain.ModReference) map[string]domain.ModReference {
	byKey := make(map[string]domain.ModReference, len(refs))
	for _, ref := range refs {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if _, seen := byKey[key]; !seen {
			byKey[key] = ref
		}
	}
	return byKey
}

// liveProfile returns the profile whose mods gameID's game directory holds:
// the one profile file that says `is_default: true` - what `lmm profile
// switch` last wrote - or the game's only profile file, or "default" for a
// game with no profile file at all (the profile both frontends resolve for
// it). A game has one directory and one deployed profile, so a flow that
// writes into that directory acts for this profile or not at all (#444,
// #445).
//
// Anything else is ErrActiveProfileUnknown (#445 review F2): a profile file
// that cannot be read, several marked, or none marked among several.
// GetDefault answers those with a guess - it skips an unreadable file and
// falls back to the first readable profile - which is fine for choosing
// what to display and wrong for deciding what to remove.
func (s *Service) liveProfile(ctx context.Context, gameID string) (string, error) {
	return s.NewProfileManager().liveProfile(ctx, gameID)
}

// refuseInactive refuses verb for profileName unless it is gameID's live
// profile (#445): the game directory holds the active profile's
// deployment, so deploying another profile there mixes two profiles' mods,
// and purging one removes files that profile does not own.
func (s *Service) refuseInactive(ctx context.Context, gameID, profileName, verb string) error {
	live, err := s.liveProfile(ctx, gameID)
	if err != nil {
		return err
	}
	if live == profileName {
		return nil
	}
	return fmt.Errorf("%w: cannot %s profile %q of %s - the game directory holds the active profile %q's mods; run `lmm profile switch %s` to make it active first",
		ErrProfileNotActive, verb, profileName, gameID, live, profileName)
}

// requireActiveProfile refuses verb - a deploy-direction write: install,
// update, rollback, enable, archive import, adopt - for profileName unless
// it is gameID's active profile (#462), as refuseInactive does for deploy
// and apply (#445). A game with no profile file at all is the exception: the
// flow creates the game's first profile, and a game's only profile is its
// active one (createsFirstProfile).
func (s *Service) requireActiveProfile(ctx context.Context, gameID, profileName string, verb DeployVerb) error {
	first, err := s.createsFirstProfile(ctx, gameID, profileName)
	if err != nil || first {
		return err
	}
	return s.refuseInactive(ctx, gameID, profileName, string(verb))
}

// createsFirstProfile reports whether a flow for profileName would create
// gameID's first profile, and so its active one (#462): the game has no
// profile file, and its DB records no other profile's files in the
// directory - rows left by profile files deleted by hand make that
// directory liveProfile's, as for any other game.
func (s *Service) createsFirstProfile(ctx context.Context, gameID, profileName string) (bool, error) {
	names, err := config.ListProfiles(s.configDir, gameID)
	if err != nil {
		return false, fmt.Errorf("resolving the active profile for %s: %w", gameID, err)
	}
	if len(names) > 0 {
		return false, nil
	}
	records, err := s.db.DeployedPathRecords(ctx, gameID)
	if err != nil {
		return false, fmt.Errorf("resolving the active profile for %s: %w", gameID, err)
	}
	for _, rs := range records {
		for _, r := range rs {
			if r.Profile != profileName {
				return false, nil
			}
		}
	}
	return true, nil
}

// DeployVerb names a deploy-direction flow in its active-profile refusal
// ("cannot <verb> profile ..."). The flows and the frontends that ask
// CheckDeployTarget ahead of them share these, so a refusal reads the same
// whichever of them words it (#462).
type DeployVerb string

// The deploy-direction flows requireActiveProfile gates, by the words each
// one's refusal uses.
const (
	VerbInstall       DeployVerb = "install into"
	VerbUpdate        DeployVerb = "update a mod in"
	VerbUpdateMany    DeployVerb = "update mods in"
	VerbRollback      DeployVerb = "roll back a mod in"
	VerbEnable        DeployVerb = "enable a mod in"
	VerbImportArchive DeployVerb = "import an archive into"
	VerbAdopt         DeployVerb = "adopt files into"
	VerbRegenMerged   DeployVerb = "regenerate the merged artifact of"
)

// CheckDeployTarget reports whether the deploy-direction flow verb names
// may act for profileName in gameID (#462): nil for the game's active
// profile - or a game with no profile yet - else ErrProfileNotActive naming
// `lmm profile switch`, or ErrActiveProfileUnknown, worded as the flow
// itself words it. It changes nothing. A frontend asks it before work the
// flow's own refusal would come too late for - the web UI's enable toggle,
// which has no plan to carry the refusal; the CLI's install and update,
// ahead of their source reads - and the flow itself asks again.
func (s *Service) CheckDeployTarget(ctx context.Context, gameID, profileName string, verb DeployVerb) error {
	return s.requireActiveProfile(ctx, gameID, profileName, verb)
}

// CheckRemovalTarget reports whether a removal-direction flow - uninstall,
// disable - can run for profileName in gameID at all (#462): nil when the
// game's active profile can be told, whether or not it is profileName (a
// non-active profile's removal runs recorded-only), else
// ErrActiveProfileUnknown. It changes nothing; the web UI's disable toggle
// asks it so that refusal answers the request instead of failing a job.
func (s *Service) CheckRemovalTarget(ctx context.Context, gameID, profileName string) error {
	_, _, err := s.profileScope(ctx, gameID, profileName)
	return err
}

// profileScope is how a removal-direction flow acting for profileName may
// touch gameID's game directory (#462): recordedOnly is false when
// profileName is the active profile, live, and the flow acts on the
// directory as it always has; it is true otherwise, and the flow may take
// out only what profileName itself recorded putting there and nothing else
// still claims (clearRecorded) - the rule #445 gave purge. A game whose
// active profile cannot be told is ErrActiveProfileUnknown: any answer
// would be a guess. A deploy-direction flow asks refuseInactive instead.
func (s *Service) profileScope(ctx context.Context, gameID, profileName string) (live string, recordedOnly bool, err error) {
	live, err = s.liveProfile(ctx, gameID)
	if err != nil {
		return "", false, err
	}
	return live, live != profileName, nil
}

// flaggedActiveProfile returns gameID's one profile whose file says
// `is_default: true`, by file name, or "" when none does, several do, or a
// profile file cannot be read - the cases where "which profile is active?"
// has no answer that is not a guess (BackfillProfileDisabledMarkers draws
// the same line).
//
// It is the only profile whose installed rows' enabled flag says what the
// user chose: every profile switch writes enabled = 0 onto the profile it
// leaves, for mods the user wants on there (#444). A flow that would read
// that flag as intent reads it for this profile alone, and takes every
// other profile's intent from its document.
func (s *Service) flaggedActiveProfile(gameID string) string {
	flagged, unreadable, err := s.explicitDefaults(gameID)
	if err != nil || len(unreadable) > 0 || len(flagged) != 1 {
		return ""
	}
	return flagged[0]
}

// RemoveMod removes a mod's references - every one of them - from a profile
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
	return pm.save(profile)
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
	return pm.save(profile)
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
	existing, existErr := config.LoadProfile(pm.configDir, profile.GameID, profile.Name)
	if existErr == nil && !force {
		return nil, fmt.Errorf("profile already exists: %s (use --force to overwrite)", profile.Name)
	}
	// An exported document carries no is_default - which profile is active
	// is local state - so a replaced profile keeps its own (#446): replacing
	// the active profile must not leave the game with none.
	if existErr == nil {
		profile.IsDefault = existing.IsDefault
	} else {
		first, err := pm.isFirstProfile(profile.GameID)
		if err != nil {
			return nil, err
		}
		profile.IsDefault = first
	}

	if err := pm.save(profile); err != nil {
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
