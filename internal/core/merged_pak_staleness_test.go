package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCheckMergedPakStaleness_NotStaleWhenUnchanged(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "an up-to-date merged pak must not be reported stale")
}

func TestCheckMergedPakStaleness_StaleAfterModEnable(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.NotNil(t, upd)
	require.True(t, upd.RecompileNeeded)
	require.Equal(t, upd.InstalledMod.Version, upd.NewVersion, "a staleness row has no real version change")
}

func TestCheckMergedPakStaleness_NilWhenNoMergedPakEverGenerated(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "zero enabled exmodz mods means nothing to report - not an error, not a staleness row")
}

func TestCheckMergedPakStaleness_NonCompileGame_Nil(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "skyrim-se", ModPath: t.TempDir(), DeployMode: domain.DeployExtract}
	require.NoError(t, svc.AddGame(game))
	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd)
}

// TestApplyMergedPakRegen_Regenerates proves the apply-side wiring: given
// a stale merged pak, applying regenerates and redeploys it.
func TestApplyMergedPakRegen_Regenerates(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	result, err := svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-byteswolf-bytes", string(data))
}

// TestApplyMergedPakRegen_LockedModDiffStillParticipates is the dedicated
// coordinator-flagged design-decision test - see Task 13 for the FULL
// suite; this is the minimal smoke case proving a LOCKED mod's retained
// exmodz is not excluded from a merge triggered by an UNLOCKED mod's
// change.
func TestApplyMergedPakRegen_LockedModDiffStillParticipates(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("locked-bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	_, err := svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err, "a locked mod elsewhere in the profile must not block the merge")

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "locked-bear-bytes", "the locked mod's diff must still be included in the merge")
	require.Contains(t, string(data), "wolf-bytes")
}
