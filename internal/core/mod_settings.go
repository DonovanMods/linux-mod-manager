// Package core: this file holds the small per-mod settings flows - lock/
// unlock/set-update-policy/set-convert-paks/skip-update/unskip-update, and
// #667's skip-all/unskip-all - each a single beginOp-gated
// mutation (no Plan/Apply pair: there is nothing to preview or go stale
// beyond the mod itself, mirroring EnableMod/DisableMod's own shape) that
// returns a ModSettingResult so cmd/lmm's renderers never need their own
// follow-up read to report what changed (v2 Phase 3 Task 10, #303).
package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// ModSettingResult reports sourceID/modID's full settings snapshot after a
// SetModLock/ClearModLock/SetModUpdatePolicy/SetModConvertPaks write -
// composed the same way ModDetail's InstalledDetail is (#86): the DB row
// (Mod, UpdatePolicy), the profile YAML (Locked/LockedVersion), and, only
// when it applies (DeployCompile with a pak merge source - see
// ModHasPakMergeSource), ConvertPaks. A caller that changed only one facet
// still sees every other current setting, so a renderer never needs a
// second read to report the mod's full state.
type ModSettingResult struct {
	Mod           domain.InstalledMod `json:"mod"`
	Locked        bool                `json:"locked"`
	LockedVersion string              `json:"locked_version,omitempty"`
	UpdatePolicy  domain.UpdatePolicy `json:"update_policy"`
	// ConvertPaks is nil when pak conversion does not apply to this mod at
	// all - distinct from a non-nil pointer to false, which means "applies,
	// and is off" (mirrors InstalledDetail.ConvertPaks exactly).
	ConvertPaks *bool `json:"convert_paks,omitempty"`
	// SkippedVersion is the update version the user skipped (#542), the DB
	// row's own Mod.SkippedVersion; empty when nothing is skipped.
	SkippedVersion string `json:"skipped_version,omitempty"`
	// EffectiveCategory and CategorySource are the mod's category as
	// ModListing reports them (#592): the user's category from the profile
	// ref when set (CategorySourceUser), else the row's source category name
	// (CategorySourceSource), else both empty.
	EffectiveCategory string `json:"effective_category,omitempty"`
	CategorySource    string `json:"category_source,omitempty"`
	// LeftCollection is set by ClearModLock when the lock it cleared was
	// the profile's collection's (#667): the mod is the user's now, the
	// collection no longer manages its version, and bulk update checks
	// include it again. omitzero.
	LeftCollection bool `json:"left_collection,omitzero"`
}

// modSettingResult builds sourceID/modID's ModSettingResult in
// gameID/profileName, reloading the DB row and the profile's lock state
// fresh - called as the last step of every setting mutation below, after
// its write has landed.
func (s *Service) modSettingResult(ctx context.Context, sourceID, modID, gameID, profileName string) (*ModSettingResult, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, gameID, profileName)
	if err != nil {
		return nil, err
	}

	prof, readErr := s.NewProfileManager().Get(ctx, gameID, profileName)
	if readErr != nil {
		// lockState's rule: a profile that cannot be read has nothing
		// locked and no user category - except under cancellation, which
		// says nothing about either (Ruling 16 (C)).
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		prof = nil
	}
	locked, lockedVersion := lockStateFromProfile(prof, sourceID, modID)
	userCategory := ""
	if prof != nil {
		if ref := prof.FindRef(sourceID, modID); ref != nil {
			userCategory = ref.Category
		}
	}

	result := &ModSettingResult{
		Mod:            *mod,
		Locked:         locked,
		LockedVersion:  lockedVersion,
		UpdatePolicy:   mod.UpdatePolicy,
		SkippedVersion: mod.SkippedVersion,
	}
	result.EffectiveCategory, result.CategorySource = effectiveCategory(userCategory, mod.CategoryName)

	if game, gerr := s.GetGame(gameID); gerr == nil && game.DeployMode == domain.DeployCompile && s.ModHasPakMergeSource(game, mod) {
		v := mod.ConvertPaks
		result.ConvertPaks = &v
	}

	return result, nil
}

// SetModLock marks sourceID/modID's profile ref as locked in
// gameID/profileName, delegating to ProfileManager.SetModLock under
// beginOp - previously cmd/lmm's doModLock called the ProfileManager method
// directly, unserialized against every other core mutation (v2 Phase 3
// Task 10, #303: lock/unlock move into Service so they compose safely with
// the rest of core's single-mutation-slot serialization). See
// ProfileManager.SetModLock's own doc comment for the locking semantics
// (version == "" locks at whatever is currently installed).
func (s *Service) SetModLock(ctx context.Context, sourceID, modID, gameID, profileName, version string) (*ModSettingResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.setModLock(ctx, sourceID, modID, gameID, profileName, version)
}

func (s *Service) setModLock(ctx context.Context, sourceID, modID, gameID, profileName, version string) (*ModSettingResult, error) {
	if err := s.NewProfileManager().SetModLock(ctx, gameID, profileName, sourceID, modID, version); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
}

// ClearModLock clears sourceID/modID's profile lock marker in
// gameID/profileName, delegating to ProfileManager.ClearModLock under
// beginOp - see SetModLock's doc comment for why this moved out of cmd/lmm.
// Version is left exactly as it is (ProfileManager.ClearModLock's own
// guarantee).
func (s *Service) ClearModLock(ctx context.Context, sourceID, modID, gameID, profileName string) (*ModSettingResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.clearModLock(ctx, sourceID, modID, gameID, profileName)
}

func (s *Service) clearModLock(ctx context.Context, sourceID, modID, gameID, profileName string) (*ModSettingResult, error) {
	// #667: whether the lock was the collection's, read before the clear
	// takes the marker with it. An unreadable profile is ClearModLock's to
	// report.
	var leftCollection bool
	if prof, err := s.NewProfileManager().Get(ctx, gameID, profileName); err == nil {
		ref := prof.FindRef(sourceID, modID)
		leftCollection = ref != nil && ref.Locked && ref.FromCollection
	}
	if err := s.NewProfileManager().ClearModLock(ctx, gameID, profileName, sourceID, modID); err != nil {
		return nil, err
	}
	result, err := s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
	if result != nil {
		result.LeftCollection = leftCollection
	}
	return result, err
}

// SetModUpdatePolicy sets the update policy for an installed mod, returning
// its full settings snapshot afterward (v2 Phase 3 Task 10, #303 - the DB
// write itself is unchanged from the pre-Task-10 (ctx, ..., policy) error
// -only form).
func (s *Service) SetModUpdatePolicy(ctx context.Context, sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) (*ModSettingResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var before domain.UpdatePolicy
	if row, err := s.GetInstalledMod(ctx, sourceID, modID, gameID, profileName); err == nil {
		before = row.UpdatePolicy
	}
	if err := s.setModUpdatePolicy(ctx, sourceID, modID, gameID, profileName, policy); err != nil {
		return nil, err
	}
	// #613 (owner ruling): moving a collection's pinned item to auto is a
	// deliberate choice to leave the pin, so the entry is the user's from
	// now on. The profile write completes the DB write even under a
	// cancelled ctx (Ruling 16).
	if policy == domain.UpdateAuto && before != domain.UpdateAuto {
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return s.NewProfileManager().disownRef(ctx, gameID, profileName, sourceID, modID)
		}); err != nil {
			return nil, err
		}
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
}

// refuseExternalAutoPolicy is #269's gate on `lmm mod set-update auto`:
// notify (the default) and pinned both mean something for an item lmm can
// only report on, and auto does not - lmm cannot apply a Steam Workshop
// update at all, so the policy would never fire.
func (s *Service) refuseExternalAutoPolicy(ctx context.Context, sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
	if policy != domain.UpdateAuto {
		return nil
	}
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, gameID, profileName)
	if err != nil {
		// Not found (or unreadable) is the existing setter's problem to
		// report, in its own words - this gate only ever adds a refusal.
		return nil
	}
	return refuseExternal("set update policy", mod, ReasonExternalNoAutoUpdate)
}

func (s *Service) setModUpdatePolicy(ctx context.Context, sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
	if err := s.refuseExternalAutoPolicy(ctx, sourceID, modID, gameID, profileName, policy); err != nil {
		return err
	}
	return s.db.UpdateModPolicy(ctx, sourceID, modID, gameID, profileName, policy)
}

// SetModConvertPaks toggles per-mod pak-to-exmod conversion (#221),
// returning the mod's full settings snapshot afterward (v2 Phase 3 Task 10,
// #303). A local DB write; the caller re-syncs the merged pak to apply the
// change.
func (s *Service) SetModConvertPaks(ctx context.Context, sourceID, modID, gameID, profileName string, convert bool) (*ModSettingResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.setModConvertPaks(ctx, sourceID, modID, gameID, profileName, convert); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
}

func (s *Service) setModConvertPaks(ctx context.Context, sourceID, modID, gameID, profileName string, convert bool) error {
	return s.db.SetModConvertPaks(ctx, sourceID, modID, gameID, profileName, convert)
}

// ErrNoUpdateToSkip is SkipModUpdate's refusal when no version was named
// and the mod's source offers no update to skip (#542).
var ErrNoUpdateToSkip = errors.New("no update is available to skip")

// SkipModUpdate skips one pending update of sourceID/modID in profileName
// without pinning the mod (#542): the update check leaves out any offer that
// is not newer than version, so the skip lasts until the source publishes
// something newer. An empty version skips the version the source offers
// now, found by a one-mod update check run before the mutation slot is
// taken (it is a network read); with no update offered, that is
// ErrNoUpdateToSkip.
//
// The skip only hides the prompt: an explicit single-mod update still
// applies the skipped version, and clears the skip.
func (s *Service) SkipModUpdate(ctx context.Context, game *domain.Game, sourceID, modID, profileName, version string) (*ModSettingResult, error) {
	if version == "" {
		offered, err := s.offeredUpdateVersion(ctx, game, sourceID, modID, profileName)
		if err != nil {
			return nil, err
		}
		version = offered
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.db.SetModSkippedVersion(ctx, sourceID, modID, game.ID, profileName, version); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, game.ID, profileName)
}

// offeredUpdateVersion is the version an update check offers sourceID/modID
// right now, whether or not it is already skipped - a recompile, which
// offers no version, does not count.
func (s *Service) offeredUpdateVersion(ctx context.Context, game *domain.Game, sourceID, modID, profileName string) (string, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return "", err
	}
	res, err := s.checkGameUpdates(ctx, game, profileName, []domain.InstalledMod{*mod}, nil, UpdateCheckOptions{}, true)
	if err != nil {
		return "", fmt.Errorf("failed to check update: %w", err)
	}
	for _, upd := range slices.Concat(res.updates, res.skipped) {
		if upd.InstalledMod.SourceID == sourceID && upd.InstalledMod.ID == modID && !upd.RecompileNeeded {
			return upd.NewVersion, nil
		}
	}
	return "", fmt.Errorf("%s: %w", mod.Name, ErrNoUpdateToSkip)
}

// UnskipModUpdate clears sourceID/modID's skipped update (#542), so the
// update check offers it again. Clearing a skip that is not set is not an
// error.
func (s *Service) UnskipModUpdate(ctx context.Context, sourceID, modID, gameID, profileName string) (*ModSettingResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.db.SetModSkippedVersion(ctx, sourceID, modID, gameID, profileName, ""); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
}

// UpdateSkipAllResult is what SkipAllUpdates, SkipUpdates and
// UnskipAllUpdates did (#667): the mods whose skip the call set, or cleared,
// in game/profile. `lmm update --skip-all --json` / `--unskip-all --json`,
// and POST /api/v1/updates/skip-all / unskip-all.
type UpdateSkipAllResult struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Mods are the mods the call changed, in the order asked (SkipUpdates,
	// SkipAllUpdates: the update check's) or the profile's installed order
	// (UnskipAllUpdates). Never null: an empty list is "nothing to skip" or
	// "nothing skipped".
	Mods []SkippedUpdateRef `json:"mods"`
	// Applied is len(Mods): the skips set, or cleared.
	Applied int `json:"applied"`
	// NotApplied counts the updates a SkipUpdates caller named that no
	// longer apply - the mod is not installed in the profile any more, or
	// the version named is no version to skip (empty, or the one installed)
	// - and were left alone. Always zero for SkipAllUpdates and
	// UnskipAllUpdates.
	NotApplied int `json:"not_applied"`
}

// SkippedUpdateRef is one mod in an UpdateSkipAllResult: its identity and
// the version skipped - the one recorded, or the one the skip
// UnskipAllUpdates cleared was holding off.
type SkippedUpdateRef struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
}

// UpdateSkipItem names one offered update to skip (#667): the mod and the
// version its update offers - the row a frontend shows, as the per-mod
// skip sends its row's new_version.
type UpdateSkipItem struct {
	SourceID   string `json:"source_id"`
	ModID      string `json:"mod_id"`
	NewVersion string `json:"new_version"`
}

// SkipUpdates skips each of items in gameID/profileName (#667: the web
// UI's Skip all, which sends the rows its Updates card shows). It runs no
// update check: the rows are the caller's, so a check that half failed - a
// source wanting a key beside one that answered - still skips what the
// user saw. Under one beginOp each item is checked against the installed
// mods - it must be installed in the profile, and name a version other
// than the installed one - and every one that passes is written in one
// transaction; one that does not is counted in NotApplied and left alone,
// never failing the rest.
func (s *Service) SkipUpdates(ctx context.Context, gameID, profileName string, items []UpdateSkipItem) (*UpdateSkipAllResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.skipUpdates(ctx, gameID, profileName, items)
}

// skipUpdates is SkipUpdates' body, run under the caller's beginOp.
func (s *Service) skipUpdates(ctx context.Context, gameID, profileName string, items []UpdateSkipItem) (*UpdateSkipAllResult, error) {
	installed, err := s.GetInstalledMods(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	rows := make(map[string]domain.InstalledMod, len(installed))
	for _, mod := range installed {
		rows[domain.ModKey(mod.SourceID, mod.ID)] = mod
	}
	result := &UpdateSkipAllResult{GameID: gameID, Profile: profileName, Mods: []SkippedUpdateRef{}}
	var skips []db.ModSkip
	for _, it := range items {
		row, ok := rows[domain.ModKey(it.SourceID, it.ModID)]
		if !ok || it.NewVersion == "" || it.NewVersion == row.Version {
			result.NotApplied++
			continue
		}
		skips = append(skips, db.ModSkip{SourceID: it.SourceID, ModID: it.ModID, Version: it.NewVersion})
		result.Mods = append(result.Mods, SkippedUpdateRef{SourceID: it.SourceID, ModID: it.ModID, Name: row.Name, Version: it.NewVersion})
	}
	if err := s.db.SetModSkippedVersions(ctx, gameID, profileName, skips); err != nil {
		return nil, err
	}
	result.Applied = len(result.Mods)
	return result, nil
}

// SkipAllUpdates skips every update a bulk check of game/profileName offers
// now (#667, #542's skip in bulk: `lmm update --skip-all`): each mod's
// offered version is recorded as its skipped version, so the updates leave
// the list until their sources publish something newer. The check is the
// one every update surface runs, so a mod the profile's collection locks is
// not among them, and a recompile row, which offers no version, is never
// skipped. The writes are SkipUpdates', one transaction under one beginOp.
//
// A partial check failure refuses: skipping "all" of an answer that is
// missing some sources would leave the user believing every update was
// handled. (The web UI sends the rows it shows to SkipUpdates instead, so
// a half-failed check there still skips what the user saw.)
func (s *Service) SkipAllUpdates(ctx context.Context, game *domain.Game, profileName string) (*UpdateSkipAllResult, error) {
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	// The check is a network read, so it runs BEFORE the mutation slot is
	// taken, as SkipModUpdate's does: holding beginOp across every source's
	// round trip would stall every other mutation behind it. In the window
	// between the two, a mod can be updated or uninstalled; skipUpdates
	// re-reads the installed rows under beginOp and leaves such an item out
	// (NotApplied), so the window can skip nothing the profile no longer
	// offers. A newer version published in the window is not skipped - the
	// next check offers it, as it would any newer version.
	res, err := s.checkGameUpdates(ctx, game, profileName, installed, nil, UpdateCheckOptions{}, false)
	if err != nil {
		return nil, fmt.Errorf("failed to check updates: %w", err)
	}
	var items []UpdateSkipItem
	for _, upd := range res.updates {
		if upd.RecompileNeeded || upd.NewVersion == "" {
			continue
		}
		im := upd.InstalledMod
		items = append(items, UpdateSkipItem{SourceID: im.SourceID, ModID: im.ID, NewVersion: upd.NewVersion})
	}
	return s.SkipUpdates(ctx, game.ID, profileName, items)
}

// UnskipAllUpdates clears every skipped update in gameID/profileName
// (#667), so the update check offers each again. Clearing when nothing is
// skipped is not an error. One transaction under one beginOp.
func (s *Service) UnskipAllUpdates(ctx context.Context, gameID, profileName string) (*UpdateSkipAllResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	installed, err := s.GetInstalledMods(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	result := &UpdateSkipAllResult{GameID: gameID, Profile: profileName, Mods: []SkippedUpdateRef{}}
	var skips []db.ModSkip
	for _, mod := range installed {
		if mod.SkippedVersion == "" {
			continue
		}
		skips = append(skips, db.ModSkip{SourceID: mod.SourceID, ModID: mod.ID})
		result.Mods = append(result.Mods, SkippedUpdateRef{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Version: mod.SkippedVersion})
	}
	if err := s.db.SetModSkippedVersions(ctx, gameID, profileName, skips); err != nil {
		return nil, err
	}
	result.Applied = len(result.Mods)
	return result, nil
}
