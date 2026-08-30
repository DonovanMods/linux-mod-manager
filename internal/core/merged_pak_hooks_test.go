package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestEnableMod_SyncsMergedPak proves enabling an exmodz mod deploys the
// merged pak without a separate `lmm update` step.
func TestEnableMod_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	require.NoError(t, svc.SetModEnabledForTest(context.Background(), "fake-compiler", "bear-mount", game.ID, "default", false))

	_, err := svc.EnableMod(context.Background(), game, "default", "fake-compiler", "bear-mount")
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "EnableMod must sync the merged pak, not just this mod's own (empty) cache entry")
}

// TestDisableMod_SyncsMergedPak_RemovesWhenLastModDisabled proves disabling
// the LAST enabled exmodz mod removes the merged pak.
func TestDisableMod_SyncsMergedPak_RemovesWhenLastModDisabled(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err)

	_, err = svc.DisableMod(context.Background(), game, "default", "fake-compiler", "bear-mount")
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "DisableMod must sync the merged pak, removing it once the last exmodz mod is disabled")
}

// TestUninstallMod_SyncsMergedPak_RemovesWhenLastModUninstalled mirrors
// the disable case for a full uninstall.
func TestUninstallMod_SyncsMergedPak_RemovesWhenLastModUninstalled(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")

	_, err = svc.UninstallMod(context.Background(), game, "default", "fake-compiler", "bear-mount", core.UninstallOptions{})
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "UninstallMod must sync the merged pak")
}

// TestDeployProfile_SyncsMergedPak proves a full `lmm deploy` also
// generates the merged pak (the pre-existing per-mod loop deploys zero
// files for an exmodz mod's own cache entry - Tasks 2/3 - so without this
// hook a fresh deploy would silently produce no merged pak at all).
func TestDeployProfile_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-bytes", string(data))
}

// TestApplyRollback_SyncsMergedPak proves a rollback (a version + FileIDs
// change - a documented regeneration trigger) reaches the merged pak
// (#197 I1 fix - previously missed, so a rolled-back mod's stale diff
// stayed deployed until an unrelated flow happened to sync it).
func TestApplyRollback_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "bear-mount", "1.0", cache.RetainedSourceName("exmodz-v1"), []byte("v1-bytes")))
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "bear-mount", "2.0", cache.RetainedSourceName("exmodz-v2"), []byte("v2-bytes")))

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:             domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Name: "bear-mount", Version: "2.0", GameID: game.ID},
		ProfileName:     "default",
		Enabled:         true,
		FileIDs:         []string{"exmodz-v2"},
		PreviousVersion: "1.0",
		PreviousFileIDs: []string{"exmodz-v1"},
		UpdatePolicy:    domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "bear-mount", Version: "2.0", FileIDs: []string{"exmodz-v2"}}))

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "v2-bytes", string(before))

	plan, err := svc.PlanRollback(context.Background(), game, "default", "fake-compiler", "bear-mount")
	require.NoError(t, err)
	_, err = svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)

	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "v1-bytes", string(after), "ApplyRollback must sync the merged pak to reflect the rolled-back version")
}

// TestPurgeProfile_UndeploysMergedPak_KeepsCacheWithoutUninstall proves a
// plain `lmm purge` (no --uninstall) removes the deployed merged pak even
// though the purge loop's own per-mod Installer.Uninstall calls are no-ops
// for exmodz mods (zero deployment members of their own, #197 I2 fix) -
// and that the cache entry survives, matching purge's "keep records,
// redeploy later" contract for real mods.
func TestPurgeProfile_UndeploysMergedPak_KeepsCacheWithoutUninstall(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must exist before purging")

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", game.ID, "default")
	require.NoError(t, err)

	_, err = svc.PurgeProfile(context.Background(), game, "default", []domain.InstalledMod{*mod}, core.PurgeOptions{}, nil)
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "purge must undeploy the merged pak, not just no-op per-mod")

	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists(game.ID, domain.SourceMerged, "merged-pak", "merged"),
		"a plain purge (no --uninstall) must keep the merged pak's cache entry, mirroring real-mod purge semantics")
}

// TestPurgeProfile_Uninstall_DeletesMergedPakCacheToo proves `lmm purge
// --uninstall` also clears the merged pak's cache entry, matching every
// real mod's full removal.
func TestPurgeProfile_Uninstall_DeletesMergedPakCacheToo(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", game.ID, "default")
	require.NoError(t, err)

	_, err = svc.PurgeProfile(context.Background(), game, "default", []domain.InstalledMod{*mod}, core.PurgeOptions{Uninstall: true}, nil)
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err))

	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, domain.SourceMerged, "merged-pak", "merged"),
		"--uninstall must also clear the merged pak's cache entry")
}

// TestApplyProfileSwitch_SyncsMergedPakForToProfile proves switching TO a
// profile with enabled exmodz mods deploys ITS merged pak (plan.To, not
// plan.From).
func TestApplyProfileSwitch_SyncsMergedPakForToProfile(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "other")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	// Move the mod's profile membership to "other" too, so switching there
	// has something enabled to merge.
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "other", domain.ModReference{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0", FileIDs: []string{"exmodz-file"}}))
	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", game.ID, "default")
	require.NoError(t, err)
	mod.ProfileName = "other"
	require.NoError(t, svc.SaveInstalledMod(context.Background(), mod))

	plan := &core.SwitchPlan{From: "default", To: "other"}
	_, err = svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "ApplyProfileSwitch must sync the merged pak for the TO profile")
}

// TestReorderProfileMods_SyncsMergedPak proves a load-order change (a
// documented regeneration trigger) actually reaches the merged pak.
func TestReorderProfileMods_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-a", []byte("A"))
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-b", []byte("B"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "AB", string(before))

	// Swap load order: wolf-mount now first.
	err = svc.ReorderProfileMods(context.Background(), game.ID, "default", []domain.ModReference{
		{SourceID: "fake-compiler", ModID: "wolf-mount", Version: "1.0", FileIDs: []string{"exmodz-b"}},
		{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0", FileIDs: []string{"exmodz-a"}},
	})
	require.NoError(t, err)

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "BA", string(after), "reordering must be reflected in a subsequent sync (fingerprint changed)")
}
