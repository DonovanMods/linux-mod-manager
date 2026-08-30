package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLockedMod_DiffStillParticipatesInMerge: a locked mod's retained
// exmodz contributes to the merge exactly like an unlocked one - locking
// pins THAT mod's own VERSION, it does not exclude its diff or freeze the
// merged pak (design decision 3, coordinator-confirmed).
func TestLockedMod_DiffStillParticipatesInMerge(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "fake-compiler", "bear-mount", ""))

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err, "a locked mod must not block the merge")
	require.Empty(t, warnings)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-bytes", string(data), "the locked mod's own diff must be included")
}

// TestLockedMod_DoesNotBlockAnotherModsChangeFromReachingTheMerge: enabling
// a SECOND, unlocked mod alongside a locked one must still trigger
// regeneration and include BOTH mods' diffs.
func TestLockedMod_DoesNotBlockAnotherModsChangeFromReachingTheMerge(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "fake-compiler", "bear-mount", ""))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err, "a lock on one mod must never block ANOTHER mod's change from reaching the merged pak")
	require.Empty(t, warnings)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-byteswolf-bytes", string(data), "both mods' diffs must be present - the lock excluded neither")
}

// TestLockedMod_CheckMergedPakStaleness_NotBlockedByLock proves the CHECK
// side (not just apply) also treats a locked mod normally.
func TestLockedMod_CheckMergedPakStaleness_NotBlockedByLock(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "fake-compiler", "bear-mount", ""))

	upd, err := svc.CheckMergedPakStaleness(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, upd, "a never-yet-generated merged pak is stale regardless of a lock elsewhere in the profile")

	_, err = svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err)

	upd, err = svc.CheckMergedPakStaleness(context.Background(), game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "after applying, the locked mod's presence must not cause a spurious permanent-stale state")
}

// TestLockedMod_ApplyMergedPakRegen_NeverErrorsForALock proves
// ApplyMergedPakRegen has NO lock-gate at all (unlike #196's ApplyRecompile,
// which refused a locked MOD's own recompile) - it is a profile-level
// operation, and design decision 3 explicitly rejects "freeze the whole
// merge on any lock present."
func TestLockedMod_ApplyMergedPakRegen_NeverErrorsForALock(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "fake-compiler", "bear-mount", ""))

	_, err := svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err, "ApplyMergedPakRegen must never refuse due to a lock - #196's ErrModLocked gate does not apply here")
}
