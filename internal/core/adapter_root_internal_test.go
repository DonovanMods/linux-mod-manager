package core

// adapter.RootRouter (#606) is honoured since #629: a file an adapter
// routes to RootInstallPath deploys under the game's install_path, is
// recorded there (deployed_files.root), and every removal finds it there.
// This file replaces TestRootInstallPathStillDeploysUnderModPath, which
// pinned the seam being ignored until this story changed it on purpose.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rootRouter routes a script extender's loader to the game root, the shape
// internal/adapter/bethesda has, stated here so the test is about the seam.
type rootRouter struct{ adapter.Generic }

func (rootRouter) ID() string { return "root-router" }

func (rootRouter) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for i, f := range files {
		if f == "skse64_loader.exe" || f == "d3d11.dll" {
			out[i] = adapter.RootInstallPath
		}
	}
	return out
}

func (rootRouter) RootForModType(_ *domain.Game, modType string) (adapter.DeployRoot, bool) {
	switch modType {
	case "":
		return adapter.RootModPath, true
	case "dinput":
		return adapter.RootInstallPath, true
	}
	return adapter.RootModPath, false
}

// rootGame is a Bethesda-shaped game: mod_path is install_path/Data.
func rootGame(t *testing.T, method domain.LinkMethod) *domain.Game {
	t.Helper()
	install := t.TempDir()
	game := &domain.Game{ID: "g", InstallPath: install, ModPath: filepath.Join(install, "Data"), LinkMethod: method}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	return game
}

func TestRootInstallPathDeploysUnderInstallPath(t *testing.T) {
	c := cache.New(t.TempDir())
	key := cache.Key{GameID: "g", SourceID: "src", ModID: "skse", Version: "1.0"}
	require.NoError(t, c.Store(key, "skse64_loader.exe", []byte("loader")))
	require.NoError(t, c.Store(key, "Scripts/SKSE.pex", []byte("script")))
	game := rootGame(t, domain.LinkSymlink)

	files, err := deployableFiles(c, rootRouter{}, game, key)
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.DeployPath{domain.GameRootFile("skse64_loader.exe"), domain.ModPathFile(filepath.Join("Scripts", "SKSE.pex"))}, pathsOf(files))

	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	inst := NewInstaller(c, linker.New(domain.LinkSymlink), database)
	inst.setAdapter(rootRouter{})
	mod := &domain.Mod{ID: "skse", SourceID: "src", Version: "1.0", Name: "SKSE64", GameID: "g"}
	require.NoError(t, inst.Install(context.Background(), game, mod, "default"))

	target, err := os.Readlink(filepath.Join(game.InstallPath, "skse64_loader.exe"))
	require.NoError(t, err, "the loader deploys beside the game's executable")
	assert.Equal(t, c.GetFilePath(key, "skse64_loader.exe"), target)
	_, err = os.Lstat(filepath.Join(game.ModPath, "skse64_loader.exe"))
	assert.True(t, os.IsNotExist(err), "nothing of it lands under mod_path")
	_, err = os.Lstat(filepath.Join(game.ModPath, "Scripts", "SKSE.pex"))
	assert.NoError(t, err, "the mod's Data content still deploys under mod_path")

	paths, err := database.GetDeployedFilesForMod(context.Background(), "g", "default", "src", "skse")
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.DeployPath{domain.ModPathFile("Scripts/SKSE.pex"), domain.GameRootFile("skse64_loader.exe")}, paths)

	require.NoError(t, inst.Uninstall(context.Background(), game, mod, "default"))
	_, err = os.Lstat(filepath.Join(game.InstallPath, "skse64_loader.exe"))
	assert.True(t, os.IsNotExist(err), "uninstall removes the game-root file")
	_, err = os.Lstat(filepath.Join(game.ModPath, "Scripts", "SKSE.pex"))
	assert.True(t, os.IsNotExist(err))
	paths, err = database.GetDeployedFilesForMod(context.Background(), "g", "default", "src", "skse")
	require.NoError(t, err)
	assert.Empty(t, paths)
}

// A game whose mod_path IS its install_path has one directory, so a root
// route changes nothing: the file keeps its plain mod_path identity rather
// than gaining a second name for the same place.
func TestRootInstallPathIsModPathWhenTheyAreOneDirectory(t *testing.T) {
	c := cache.New(t.TempDir())
	key := cache.Key{GameID: "g", SourceID: "src", ModID: "skse", Version: "1.0"}
	require.NoError(t, c.Store(key, "skse64_loader.exe", []byte("loader")))
	install := t.TempDir()
	game := &domain.Game{ID: "g", InstallPath: install, ModPath: install, LinkMethod: domain.LinkSymlink}

	files, err := deployableFiles(c, rootRouter{}, game, key)
	require.NoError(t, err)
	assert.Equal(t, []domain.DeployPath{domain.ModPathFile("skse64_loader.exe")}, pathsOf(files))
}

// A stated mod type (a NexusMods collection's Vortex "dinput") routes the
// whole mod to the root RootForModType names; a type the adapter does not
// know cannot be honoured and is refused, never taken as mod_path (#629,
// for the collections converter, #607).
func TestRootForStatedModType(t *testing.T) {
	game := rootGame(t, domain.LinkSymlink)
	files := []string{"skse64_loader.exe", filepath.Join("Scripts", "SKSE.pex")}

	got, err := deployItemsForType(rootRouter{}, game, "dinput", files)
	require.NoError(t, err)
	assert.Equal(t, []domain.DeployPath{domain.GameRootFile("skse64_loader.exe"), domain.GameRootFile(filepath.Join("Scripts", "SKSE.pex"))}, pathsOf(got))

	got, err = deployItemsForType(rootRouter{}, game, "", files)
	require.NoError(t, err)
	assert.Equal(t, []domain.DeployPath{domain.GameRootFile("skse64_loader.exe"), domain.ModPathFile(filepath.Join("Scripts", "SKSE.pex"))}, pathsOf(got), "no stated type: the adapter routes file by file")

	_, err = deployItemsForType(rootRouter{}, game, "collectionbundledmod", files)
	var unknown *ModTypeUnsupportedError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "collectionbundledmod", unknown.ModType)

	_, err = deployItemsForType(adapter.Generic{}, game, "dinput", files)
	require.ErrorAs(t, err, &unknown, "an adapter with no RootRouter knows no type")
}

// An update made after the game's adapter started routing a file to the
// game root removes the copy an earlier routing put under mod_path - by
// its record - rather than dropping the record and leaving the file.
func TestReplace_RemovesAFileWhereAnEarlierRoutingPutIt(t *testing.T) {
	c := cache.New(t.TempDir())
	oldKey := cache.Key{GameID: "g", SourceID: "src", ModID: "skse", Version: "1.0"}
	newKey := cache.Key{GameID: "g", SourceID: "src", ModID: "skse", Version: "2.0"}
	for _, k := range []cache.Key{oldKey, newKey} {
		require.NoError(t, c.Store(k, "skse64_loader.exe", []byte("loader "+k.Version)))
		require.NoError(t, c.Store(k, "Scripts/SKSE.pex", []byte("script")))
	}
	game := rootGame(t, domain.LinkSymlink)
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	inst := NewInstaller(c, linker.New(domain.LinkSymlink), database)
	oldMod := &domain.Mod{ID: "skse", SourceID: "src", Version: "1.0", GameID: "g"}
	newMod := &domain.Mod{ID: "skse", SourceID: "src", Version: "2.0", GameID: "g"}
	require.NoError(t, inst.Install(context.Background(), game, oldMod, "default"))
	_, err = os.Lstat(filepath.Join(game.ModPath, "skse64_loader.exe"))
	require.NoError(t, err, "the generic routing put it under mod_path")

	inst.setAdapter(rootRouter{})
	require.NoError(t, inst.Replace(context.Background(), game, oldMod, newMod, "default"))
	_, err = os.Lstat(filepath.Join(game.ModPath, "skse64_loader.exe"))
	assert.True(t, os.IsNotExist(err), "the mod_path copy is removed")
	target, err := os.Readlink(filepath.Join(game.InstallPath, "skse64_loader.exe"))
	require.NoError(t, err)
	assert.Equal(t, c.GetFilePath(newKey, "skse64_loader.exe"), target)
	paths, err := database.GetDeployedFilesForMod(context.Background(), "g", "default", "src", "skse")
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.DeployPath{domain.ModPathFile("Scripts/SKSE.pex"), domain.GameRootFile("skse64_loader.exe")}, paths)
}
