package core

// adapter.RootRouter (#606) is a seam core does not honour yet: deploying a
// file to the game's install_path rather than its mod_path is #629's story
// (the deploy, every removal path, converge, verify, purge, conflicts and
// the deployed_files record all have to learn a second root). Until then a
// file an adapter routes to RootInstallPath must deploy exactly as it did
// before #606 - under mod_path - and this test pins that, so the story that
// changes it changes this test on purpose.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

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
		if f == "skse64_loader.exe" {
			out[i] = adapter.RootInstallPath
		}
	}
	return out
}

func (rootRouter) RootForModType(*domain.Game, string) (adapter.DeployRoot, bool) {
	return adapter.RootInstallPath, true
}

func TestRootInstallPathStillDeploysUnderModPath(t *testing.T) {
	c := cache.New(t.TempDir())
	key := cache.Key{GameID: "g", SourceID: "src", ModID: "skse", Version: "1.0"}
	require.NoError(t, c.Store(key, "skse64_loader.exe", []byte("loader")))
	require.NoError(t, c.Store(key, "Scripts/SKSE.pex", []byte("script")))

	install := t.TempDir()
	game := &domain.Game{ID: "g", InstallPath: install, ModPath: filepath.Join(install, "Data"), LinkMethod: domain.LinkSymlink}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))

	// The adapter does say install_path for the loader...
	routes := adapter.RouteRoots(rootRouter{}, game, []string{"skse64_loader.exe", "Scripts/SKSE.pex"})
	require.Equal(t, []adapter.DeployRoot{adapter.RootInstallPath, adapter.RootModPath}, routes)

	// ...and core deploys it under mod_path all the same.
	files, err := deployableFiles(c, rootRouter{}, game, key)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"skse64_loader.exe", filepath.Join("Scripts", "SKSE.pex")}, files)

	inst := NewInstaller(c, linker.New(domain.LinkSymlink), nil)
	inst.setAdapter(rootRouter{})
	mod := &domain.Mod{ID: "skse", SourceID: "src", Version: "1.0", Name: "SKSE64", GameID: "g"}
	require.NoError(t, inst.Install(context.Background(), game, mod, "default"))

	_, err = os.Lstat(filepath.Join(game.ModPath, "skse64_loader.exe"))
	assert.NoError(t, err, "a RootInstallPath file still deploys under mod_path until #629")
	_, err = os.Lstat(filepath.Join(game.InstallPath, "skse64_loader.exe"))
	assert.True(t, os.IsNotExist(err), "nothing is written to install_path until #629")
}
