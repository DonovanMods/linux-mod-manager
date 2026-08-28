package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestDoProfileApply_DeployCompile_SyncsMergedPakOnDisable is the #197
// postsmoke regression test for doProfileApply: it is a bespoke
// disable/enable/install reimplementation (like batchInstallMods) that
// never went through a core seam syncing the merged pak. Removing the
// LAST enabled exmodz mod from the profile (toDisable path) must undeploy
// the merged pak - proof the sync now actually runs.
func TestDoProfileApply_DeployCompile_SyncsMergedPakOnDisable(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	game.SourceIDs = map[string]string{"fake-compiler": "external-icarus-id"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	const modID, version, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := getProfileManager(svc)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must exist before removing the mod from the profile")

	// Remove the mod from profile.Mods (still installed+enabled in the DB)
	// - doProfileApply's toDisable path.
	require.NoError(t, pm.RemoveMod(game.ID, "default", "fake-compiler", modID))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	require.NoError(t, doProfileApply(context.Background(), svc, game, nil))

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "doProfileApply must sync the merged pak when disabling the last exmodz mod")
}

// TestDoProfileSync_DeployCompile_AddingDriftedModDeploysMergedPak is the
// #197 postsmoke regression test for doProfileSync: an installed+enabled
// exmodz mod whose profile.yaml entry drifted away (toAdd path, pm.AddMod)
// changes profile MEMBERSHIP - the input enabledMergeSources actually
// requires - with no other seam to sync it. Proves the merged pak deploys
// once doProfileSync re-adds the mod to the profile.
func TestDoProfileSync_DeployCompile_AddingDriftedModDeploysMergedPak(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	game.SourceIDs = map[string]string{"fake-compiler": "external-icarus-id"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	const modID, version, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	// Deliberately NOT added to profile.Mods - simulates profile.yaml drift
	// (e.g. hand-edited or lost) that doProfileSync's toAdd path exists to
	// repair.

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err := os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "precondition: nothing deployed yet - the mod isn't in the profile")

	require.NoError(t, doProfileSync(context.Background(), svc, game, nil))

	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err, "doProfileSync must sync the merged pak after re-adding a drifted mod to the profile")
	require.Equal(t, "bear-bytes", string(data))
}
