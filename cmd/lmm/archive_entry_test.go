package main

// #644 through the CLI: `lmm import <archive> --source/--id` and `lmm update
// --from-file` never change another profile's cache entry.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherProfileOn gives a second profile modID 2.0 built from the source's
// file fileID - the plain entry a download writes - holding rel.
func otherProfileOn(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, fileID, rel string) cache.Key {
	t.Helper()
	ctx := context.Background()
	gc := svc.GetGameCache(game)
	plain := cache.Key{GameID: game.ID, SourceID: sourceID, ModID: modID, Version: "2.0"}
	require.NoError(t, gc.Store(plain, rel, []byte("source build")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(plain), fileID, []string{rel}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(ctx, game.ID, "default"); err != nil {
		_, err := pm.Create(ctx, game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: modID, SourceID: sourceID, Name: "Mod", Version: "2.0", GameID: game.ID}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{fileID},
	}))
	return plain
}

func requireSourceBuild(t *testing.T, svc *core.Service, game *domain.Game, k cache.Key, rel, fileID string) {
	t.Helper()
	gc := svc.GetGameCache(game)
	data, err := os.ReadFile(filepath.Join(gc.ModPath(k), rel))
	require.NoError(t, err)
	assert.Equal(t, "source build", string(data), "another profile's tree must not change")
	assert.True(t, gc.HasFileIDs(k, []string{fileID}))
}

func TestDoImport_UnmatchedArchiveKeepsAnotherProfilesEntry(t *testing.T) {
	svc, game := setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	src.files = []domain.DownloadableFile{
		{ID: "55", FileName: "mymod-2.0.zip", Version: "2.0"},
		{ID: "56", FileName: "mymod-lite-2.0.zip", Version: "2.0"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	plain := otherProfileOn(t, svc, game, "acme-source", "999", "55", "mymod.esp")

	archivePath := filepath.Join(t.TempDir(), "mymod-custom-2.0.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "custom build"})
	importSource, importModID = "acme-source", "999"
	_, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err)

	requireSourceBuild(t, svc, game, plain, "mymod.esp", "55")
	row, err := svc.GetInstalledMod(context.Background(), "acme-source", "999", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.NotEmpty(t, row.Variant, "the archive is a tree of its own")
	data, err := os.ReadFile(filepath.Join(game.ModPath, "mymod.esp"))
	require.NoError(t, err)
	assert.Equal(t, "custom build", string(data))
}

func TestDoUpdate_FromFile_UnmatchedArchiveKeepsAnotherProfilesEntry(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)
	plain := otherProfileOn(t, svc, game, "test-src", "mod1", "new-1", "mod1-new.esp")
	updateFromFile = writeFromFileArchive(t, dir, "mod1-custom-2.0.zip")
	updateAcceptMismatch = true

	_ = captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, []string{"mod1"}) })

	requireSourceBuild(t, svc, game, plain, "mod1-new.esp", "new-1")
	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.NotEmpty(t, row.Variant)
	assert.Empty(t, row.FileIDs)
}

// The update is from the source's own file, whose entry is the other
// profile's, and its install.before_each hook refuses it: nothing is
// committed.
func TestDoUpdate_FromFile_RefusalLeavesAnotherProfilesEntry(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)
	plain := otherProfileOn(t, svc, game, "test-src", "mod1", "new-1", "mod1-new.esp")
	hook := filepath.Join(t.TempDir(), "fail.sh")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	game.Hooks.Install.BeforeEach = hook
	updateFromFile = writeFromFileArchive(t, dir, "mod1-2.0.zip")

	var err error
	_ = captureStdout(t, func() error {
		err = doUpdate(context.Background(), svc, game, []string{"mod1"})
		return nil
	})
	require.ErrorContains(t, err, "install.before_each hook failed")

	requireSourceBuild(t, svc, game, plain, "mod1-new.esp", "new-1")
	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version)
}
