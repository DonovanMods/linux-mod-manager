package core_test

// Tests for Service.SetModLock/ClearModLock/SetModUpdatePolicy/
// SetModConvertPaks (v2 Phase 3 Task 10, #303): each now returns a
// *ModSettingResult instead of a bare error, and lock/unlock moved out of
// cmd/lmm's direct ProfileManager calls into these Service methods. Reuses
// newModDetailTestService/seedModDetailInstalled/seedModDetailInstalledPak
// (moddetail_test.go), already in this core_test package.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_SetModLock_ReturnsModSettingResult(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	result, err := svc.SetModLock(context.Background(), "src", "a", game.ID, "default", "2.0")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Locked)
	assert.Equal(t, "2.0", result.LockedVersion)
	assert.Equal(t, "a", result.Mod.ID)

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ref := prof.FindRef("src", "a")
	require.NotNil(t, ref)
	assert.True(t, ref.Locked)
	assert.Equal(t, "2.0", ref.Version)
}

// TestService_SetModLock_NotInProfile_ReturnsRawError guards that the
// ProfileManager not-found error passes through unwrapped - cmd/lmm renders
// it verbatim.
func TestService_SetModLock_NotInProfile_ReturnsRawError(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "a", SourceID: "src", Name: "Mod A", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	result, err := svc.SetModLock(context.Background(), "src", "a", game.ID, "default", "")
	require.Error(t, err)
	assert.Equal(t, `mod src:a not found in profile "default"`, err.Error())
	assert.Nil(t, result)
}

func TestService_ClearModLock_ReturnsModSettingResult(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", "2.0"))

	result, err := svc.ClearModLock(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Locked)
	assert.Empty(t, result.LockedVersion)

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ref := prof.FindRef("src", "a")
	require.NotNil(t, ref)
	assert.False(t, ref.Locked)
	assert.Equal(t, "2.0", ref.Version, "unlock must not touch Version")
}

func TestService_SetModUpdatePolicy_ReturnsModSettingResult(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	result, err := svc.SetModUpdatePolicy(context.Background(), "src", "a", game.ID, "default", domain.UpdateAuto)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.UpdateAuto, result.UpdatePolicy)
	assert.Equal(t, domain.UpdateAuto, result.Mod.UpdatePolicy)

	saved, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, saved.UpdatePolicy)
}

// TestService_SetModUpdatePolicy_NotInstalled_ReturnsError guards that the
// DB's own "0 rows affected" ErrModNotFound still surfaces - cmd/lmm's own
// upfront GetInstalledMod check makes this effectively unreachable via the
// CLI, but the Service method itself must not silently succeed.
func TestService_SetModUpdatePolicy_NotInstalled_ReturnsError(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)

	result, err := svc.SetModUpdatePolicy(context.Background(), "src", "missing", game.ID, "default", domain.UpdateAuto)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Nil(t, result)
}

// TestService_SetModConvertPaks_ReturnsModSettingResult_NonCompile guards
// the tri-state ConvertPaks contract: a non-DeployCompile game (or no pak
// merge source) leaves ConvertPaks nil, matching ModDetail's own
// InstalledDetail.ConvertPaks convention exactly.
func TestService_SetModConvertPaks_ReturnsModSettingResult_NonCompile(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")

	result, err := svc.SetModConvertPaks(context.Background(), "src", "a", game.ID, "default", false)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Nil(t, result.ConvertPaks, "a non-compile game must leave ConvertPaks unset, not false")
}

// TestService_SetModConvertPaks_ReturnsModSettingResult_Compile guards the
// applicable case: DeployCompile with a pak merge source populates
// ConvertPaks with the value just written.
func TestService_SetModConvertPaks_ReturnsModSettingResult_Compile(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	game.DeployMode = domain.DeployCompile
	require.NoError(t, svc.SaveGame(context.Background(), game)) // modSettingResult looks the game up via s.GetGame
	seedModDetailInstalledPak(t, svc, game, "a", "1.5", []string{"pak"}, true)

	result, err := svc.SetModConvertPaks(context.Background(), "src", "a", game.ID, "default", false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ConvertPaks)
	assert.False(t, *result.ConvertPaks)
}
