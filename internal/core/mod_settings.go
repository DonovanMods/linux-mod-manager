// Package core: this file holds the small per-mod settings flows - lock/
// unlock/set-update-policy/set-convert-paks - each a single beginOp-gated
// mutation (no Plan/Apply pair: there is nothing to preview or go stale
// beyond the mod itself, mirroring EnableMod/DisableMod's own shape) that
// returns a ModSettingResult so cmd/lmm's renderers never need their own
// follow-up read to report what changed (v2 Phase 3 Task 10, #303).
package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

	locked, lockedVersion, err := s.lockState(ctx, gameID, profileName, sourceID, modID)
	if err != nil {
		return nil, err
	}

	result := &ModSettingResult{
		Mod:           *mod,
		Locked:        locked,
		LockedVersion: lockedVersion,
		UpdatePolicy:  mod.UpdatePolicy,
	}

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
	if err := s.NewProfileManager().ClearModLock(ctx, gameID, profileName, sourceID, modID); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
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
	if err := s.setModUpdatePolicy(ctx, sourceID, modID, gameID, profileName, policy); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
}

func (s *Service) setModUpdatePolicy(ctx context.Context, sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
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
