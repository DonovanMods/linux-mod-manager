package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lmm/internal/core"
	"lmm/internal/domain"
	"lmm/internal/linker"
	"lmm/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstaller_Install(t *testing.T) {
	// Setup directories
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	// Create a mock cached mod file
	modCache := cache.New(cacheDir)
	err := modCache.Store("skyrim", "test", "123", "1.0.0", "data/test.esp", []byte("test plugin content"))
	require.NoError(t, err)
	err = modCache.Store("skyrim", "test", "123", "1.0.0", "data/meshes/test.nif", []byte("test mesh content"))
	require.NoError(t, err)

	game := &domain.Game{
		ID:         "skyrim",
		Name:       "Skyrim",
		ModPath:    gameDir,
		LinkMethod: domain.LinkSymlink,
	}

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink))

	err = installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Verify files were deployed
	espPath := filepath.Join(gameDir, "data", "test.esp")
	_, err = os.Lstat(espPath)
	assert.NoError(t, err, "test.esp should exist")

	nifPath := filepath.Join(gameDir, "data", "meshes", "test.nif")
	_, err = os.Lstat(nifPath)
	assert.NoError(t, err, "test.nif should exist")
}

func TestInstaller_Uninstall(t *testing.T) {
	// Setup directories
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	// Create a mock cached mod file
	modCache := cache.New(cacheDir)
	err := modCache.Store("skyrim", "test", "123", "1.0.0", "test.esp", []byte("test plugin"))
	require.NoError(t, err)

	game := &domain.Game{
		ID:         "skyrim",
		Name:       "Skyrim",
		ModPath:    gameDir,
		LinkMethod: domain.LinkSymlink,
	}

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	}

	lnk := linker.New(domain.LinkSymlink)
	installer := core.NewInstaller(modCache, lnk)

	// Install first
	err = installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Verify installed
	espPath := filepath.Join(gameDir, "test.esp")
	_, err = os.Lstat(espPath)
	require.NoError(t, err)

	// Uninstall
	err = installer.Uninstall(context.Background(), game, mod)
	require.NoError(t, err)

	// Verify removed
	_, err = os.Lstat(espPath)
	assert.True(t, os.IsNotExist(err), "test.esp should be removed")
}

func TestInstaller_IsInstalled(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	modCache := cache.New(cacheDir)
	err := modCache.Store("skyrim", "test", "123", "1.0.0", "test.esp", []byte("test"))
	require.NoError(t, err)

	game := &domain.Game{
		ID:         "skyrim",
		Name:       "Skyrim",
		ModPath:    gameDir,
		LinkMethod: domain.LinkSymlink,
	}

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink))

	// Not installed yet
	installed, err := installer.IsInstalled(game, mod)
	require.NoError(t, err)
	assert.False(t, installed)

	// Install
	err = installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Now installed
	installed, err = installer.IsInstalled(game, mod)
	require.NoError(t, err)
	assert.True(t, installed)
}
