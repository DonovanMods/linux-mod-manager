package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

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

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

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
	installer := core.NewInstaller(modCache, lnk, nil)

	// Install first
	err = installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Verify installed
	espPath := filepath.Join(gameDir, "test.esp")
	_, err = os.Lstat(espPath)
	require.NoError(t, err)

	// Uninstall
	err = installer.Uninstall(context.Background(), game, mod, "default")
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

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

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

func TestInstaller_IsInstalled_PartialInstallReturnsFalse(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "test", "123", "1.0.0", "a.esp", []byte("a")))
	require.NoError(t, modCache.Store("skyrim", "test", "123", "1.0.0", "b.esp", []byte("b")))

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
	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	// Deploy only one file (partial install)
	aSrc := modCache.GetFilePath("skyrim", "test", "123", "1.0.0", "a.esp")
	aDst := filepath.Join(gameDir, "a.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(aDst), 0755))
	require.NoError(t, os.Symlink(aSrc, aDst))

	// IsInstalled must return false when not all files are deployed
	installed, err := installer.IsInstalled(game, mod)
	require.NoError(t, err)
	assert.False(t, installed, "partial install should not report as installed")
}

func TestInstaller_Install_VerifyFilesInModPath(t *testing.T) {
	// This test verifies that files are actually deployed to game.ModPath
	cacheDir := t.TempDir()
	modPath := t.TempDir() // This is where mods should be deployed

	// Create cache with multiple files including nested directories
	modCache := cache.New(cacheDir)
	testFiles := map[string][]byte{
		"plugin.esp":            []byte("plugin data"),
		"textures/texture1.dds": []byte("texture data 1"),
		"textures/texture2.dds": []byte("texture data 2"),
		"meshes/model.nif":      []byte("mesh data"),
		"scripts/script.pex":    []byte("script data"),
	}

	for path, content := range testFiles {
		err := modCache.Store("testgame", "nexusmods", "999", "2.0.0", path, content)
		require.NoError(t, err)
	}

	game := &domain.Game{
		ID:         "testgame",
		Name:       "Test Game",
		ModPath:    modPath,
		LinkMethod: domain.LinkSymlink,
	}

	mod := &domain.Mod{
		ID:       "999",
		SourceID: "nexusmods",
		Name:     "Test Mod",
		Version:  "2.0.0",
		GameID:   "testgame",
	}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	// Install the mod
	err := installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Verify ALL files exist in mod_path
	for path := range testFiles {
		fullPath := filepath.Join(modPath, path)
		info, err := os.Lstat(fullPath)
		require.NoError(t, err, "File should exist: %s", path)
		assert.True(t, info.Mode()&os.ModeSymlink != 0, "File should be a symlink: %s", path)

		// Verify symlink points to correct source
		target, err := os.Readlink(fullPath)
		require.NoError(t, err)
		expectedSource := modCache.GetFilePath("testgame", "nexusmods", "999", "2.0.0", path)
		assert.Equal(t, expectedSource, target, "Symlink should point to cache")
	}

	// Verify we can read the content through the symlink
	content, err := os.ReadFile(filepath.Join(modPath, "plugin.esp"))
	require.NoError(t, err)
	assert.Equal(t, []byte("plugin data"), content)
}

func TestInstaller_Install_WithExpandedTildePath(t *testing.T) {
	// This test verifies installation works with real absolute paths
	// (simulating what happens after tilde expansion)
	cacheDir := t.TempDir()
	modPath := t.TempDir()

	modCache := cache.New(cacheDir)
	err := modCache.Store("game", "src", "1", "1.0", "mod.file", []byte("content"))
	require.NoError(t, err)

	// Use absolute path (as would happen after tilde expansion)
	game := &domain.Game{
		ID:         "game",
		Name:       "Game",
		ModPath:    modPath, // absolute path
		LinkMethod: domain.LinkSymlink,
	}

	mod := &domain.Mod{
		ID:       "1",
		SourceID: "src",
		Version:  "1.0",
		GameID:   "game",
	}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	err = installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Verify file exists at absolute path
	deployedPath := filepath.Join(modPath, "mod.file")
	_, err = os.Lstat(deployedPath)
	require.NoError(t, err, "File should be deployed to mod_path")
}

func TestInstaller_Integration_GamesYAMLToDeployment(t *testing.T) {
	// Integration test: Load game from YAML with tilde path, deploy mod, verify files
	configDir := t.TempDir()
	cacheDir := t.TempDir()
	modPath := t.TempDir() // This simulates expanded ~/games/test/mods

	// Write games.yaml with the mod_path pointing to our temp dir
	// (In real use, this would have ~ which gets expanded)
	gamesYAML := `
games:
  testgame:
    name: Test Game
    install_path: /tmp/game
    mod_path: ` + modPath + `
    sources:
      nexusmods: testgame
    link_method: symlink
`
	err := os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(gamesYAML), 0644)
	require.NoError(t, err)

	// Load games from YAML (this is what the service does)
	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, games, "testgame")

	game := games["testgame"]
	assert.Equal(t, modPath, game.ModPath, "ModPath should match")

	// Create cache with mod files
	modCache := cache.New(cacheDir)
	err = modCache.Store("testgame", "nexusmods", "123", "1.0", "Data/plugin.esp", []byte("plugin"))
	require.NoError(t, err)
	err = modCache.Store("testgame", "nexusmods", "123", "1.0", "Data/textures/tex.dds", []byte("texture"))
	require.NoError(t, err)

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "nexusmods",
		Version:  "1.0",
		GameID:   "testgame",
	}

	// Deploy using the game loaded from YAML
	installer := core.NewInstaller(modCache, linker.New(game.LinkMethod), nil)
	err = installer.Install(context.Background(), game, mod, "default")
	require.NoError(t, err)

	// Verify files are deployed to the mod_path from YAML
	pluginPath := filepath.Join(modPath, "Data", "plugin.esp")
	_, err = os.Lstat(pluginPath)
	require.NoError(t, err, "plugin.esp should be deployed to mod_path")

	texturePath := filepath.Join(modPath, "Data", "textures", "tex.dds")
	_, err = os.Lstat(texturePath)
	require.NoError(t, err, "texture should be deployed to mod_path")

	// Verify content is accessible
	content, err := os.ReadFile(pluginPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("plugin"), content)
}

func TestGetConflicts(t *testing.T) {
	// Setup test with in-memory DB and temp cache
	tempDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	c := cache.New(tempDir)
	lnk := linker.New(domain.LinkSymlink)
	inst := core.NewInstaller(c, lnk, database)

	game := &domain.Game{ID: "test-game", ModPath: filepath.Join(tempDir, "mods")}
	err = os.MkdirAll(game.ModPath, 0755)
	require.NoError(t, err)

	// Create cached mod files for mod A
	err = c.Store(game.ID, "nexusmods", "111", "1.0", "shared.txt", []byte("a"))
	require.NoError(t, err)

	// Deploy mod A
	modA := &domain.Mod{SourceID: "nexusmods", ID: "111", Version: "1.0", GameID: game.ID}
	err = inst.Install(context.Background(), game, modA, "default")
	require.NoError(t, err)

	// Create cached mod files for mod B (with overlapping file)
	err = c.Store(game.ID, "nexusmods", "222", "1.0", "shared.txt", []byte("b"))
	require.NoError(t, err)
	err = c.Store(game.ID, "nexusmods", "222", "1.0", "unique.txt", []byte("b"))
	require.NoError(t, err)

	// Check conflicts for mod B
	modB := &domain.Mod{SourceID: "nexusmods", ID: "222", Version: "1.0", GameID: game.ID}
	conflicts, err := inst.GetConflicts(context.Background(), game, modB, "default")
	require.NoError(t, err)

	assert.Len(t, conflicts, 1)
	assert.Equal(t, "shared.txt", conflicts[0].RelativePath)
	assert.Equal(t, "111", conflicts[0].CurrentModID)
}

func TestGetConflicts_ReinstallSelf(t *testing.T) {
	// Setup
	tempDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	c := cache.New(tempDir)
	lnk := linker.New(domain.LinkSymlink)
	inst := core.NewInstaller(c, lnk, database)

	game := &domain.Game{ID: "test-game", ModPath: filepath.Join(tempDir, "mods")}
	err = os.MkdirAll(game.ModPath, 0755)
	require.NoError(t, err)

	// Create and deploy mod A
	err = c.Store(game.ID, "nexusmods", "111", "1.0", "file.txt", []byte("a"))
	require.NoError(t, err)

	modA := &domain.Mod{SourceID: "nexusmods", ID: "111", Version: "1.0", GameID: game.ID}
	err = inst.Install(context.Background(), game, modA, "default")
	require.NoError(t, err)

	// Check conflicts for same mod (re-install) - should be empty
	conflicts, err := inst.GetConflicts(context.Background(), game, modA, "default")
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

func TestGetConflicts_NoDatabase(t *testing.T) {
	// Setup without database
	tempDir := t.TempDir()
	c := cache.New(tempDir)
	lnk := linker.New(domain.LinkSymlink)
	inst := core.NewInstaller(c, lnk, nil) // nil DB

	game := &domain.Game{ID: "test-game", ModPath: filepath.Join(tempDir, "mods")}
	err := os.MkdirAll(game.ModPath, 0755)
	require.NoError(t, err)

	err = c.Store(game.ID, "nexusmods", "111", "1.0", "file.txt", []byte("a"))
	require.NoError(t, err)

	modA := &domain.Mod{SourceID: "nexusmods", ID: "111", Version: "1.0", GameID: game.ID}

	// With nil DB, GetConflicts should return nil, nil
	conflicts, err := inst.GetConflicts(context.Background(), game, modA, "default")
	require.NoError(t, err)
	assert.Nil(t, conflicts)
}
