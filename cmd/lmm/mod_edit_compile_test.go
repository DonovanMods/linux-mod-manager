package main

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

// TestDoModEdit_DeployCompile_VersionEditSyncsMergedPak is the #197
// postsmoke regression test for doModEdit: a --version edit changes
// mod.Version, one of syncMergedPak's own documented regeneration
// triggers, with no seam that used to catch it. Edits to a version with
// no retained source (nothing was ever cached there) must still sync -
// proof the sync now actually runs and reacts to the mod dropping out of
// the merge (mirrors the real-world "recorded the wrong version" repair
// this flag exists for).
func TestDoModEdit_DeployCompile_VersionEditSyncsMergedPak(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	installDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := getProfileManager(svc)
	_, err = pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))

	const modID, oldVersion, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, oldVersion, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: oldVersion, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: oldVersion, FileIDs: []string{fileID}}))

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must exist before the version edit")

	oldVer := editVersion
	editVersion = "2.0" // no retained source exists at 2.0
	t.Cleanup(func() { editVersion = oldVer })

	require.NoError(t, doModEdit(context.Background(), svc, game, modID))

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "doModEdit --version must sync the merged pak - the mod no longer resolves under its new version and must drop out")
}
