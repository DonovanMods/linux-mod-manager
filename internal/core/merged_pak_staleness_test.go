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
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "an up-to-date merged pak must not be reported stale")
}

func TestCheckMergedPakStaleness_StaleAfterModEnable(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
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
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
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

// TestSyncMergedPak_FailedDeployLeg_SelfHeals is the #197 I5 regression
// test: the fingerprint is committed to the cache entry BEFORE
// installer.Install runs - if Install fails (or, equivalently here, the
// deployed file is removed by some external actor after a successful
// sync, e.g. a purge that intentionally keeps the cache entry, #197 I2),
// a matching fingerprint alone used to make every LATER syncMergedPak
// call fast-path "nothing changed" forever, even though the game
// directory doesn't actually hold the merged pak. This proves a
// subsequent sync notices the missing artifact and redeploys it WITHOUT
// re-merging (the inputs never changed - src.compileCalls must stay at 1).
func TestSyncMergedPak_FailedDeployLeg_SelfHeals(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must be deployed")

	// Simulate the deployed artifact vanishing without the cache
	// entry/fingerprint changing (a failed Install that left a partial
	// deploy is the same observable state - see #197 I2's purge test for
	// the other real-world path into this state).
	require.NoError(t, os.Remove(deployedPath))

	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 1, src.compileCalls, "precondition: exactly one merge so far")

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "a later sync must notice the missing artifact and redeploy it")
	require.Equal(t, 1, src.compileCalls, "redeploying an unchanged fingerprint must NOT trigger another merge")
}

// TestCheckMergedPakStaleness_MissingArtifact_ReportsStale is
// TestSyncMergedPak_FailedDeployLeg_SelfHeals's CHECK-side twin: `lmm
// update`/`lmm verify` call CheckMergedPakStaleness, not syncMergedPak
// directly, so it needs the identical artifact-existence confirmation -
// otherwise a wedged (fingerprint matches, file missing) profile would
// report "up to date" forever, invisible to the one safety net meant to
// catch exactly this.
func TestCheckMergedPakStaleness_MissingArtifact_ReportsStale(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	require.NoError(t, os.Remove(deployedPath))

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.NotNil(t, upd, "a missing deployed artifact must be reported stale even though the fingerprint hasn't changed")
	require.True(t, upd.RecompileNeeded)
}
