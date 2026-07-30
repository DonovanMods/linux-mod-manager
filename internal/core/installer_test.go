package core_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// failingLinker fails Deploy after a number of successful calls (for rollback tests).
type failingLinker struct {
	linker.Linker
	deployCount int
	failAfter   int
}

func (f *failingLinker) Deploy(src, dst string) error {
	f.deployCount++
	if f.deployCount > f.failAfter {
		return fmt.Errorf("simulated deploy failure")
	}
	return f.Linker.Deploy(src, dst)
}

type conditionalFailingLinker struct {
	linker.Linker
	failSrcSubstring string
	failAfter        int
	matches          int
}

func (f *conditionalFailingLinker) Deploy(src, dst string) error {
	if strings.Contains(src, f.failSrcSubstring) {
		f.matches++
		if f.matches > f.failAfter {
			return fmt.Errorf("simulated deploy failure")
		}
	}
	return f.Linker.Deploy(src, dst)
}

func TestInstaller_Install_DeployFailureRollsBackAndClearsDB(t *testing.T) {
	// When Deploy fails after some files are deployed, roll back all deployed
	// files and clear DB records so disk and DB stay consistent.
	tempDir := t.TempDir()
	gameDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	modCache := cache.New(tempDir)
	require.NoError(t, modCache.Store("g", "src", "1", "v1", "a.esp", []byte("a")))
	require.NoError(t, modCache.Store("g", "src", "1", "v1", "b.esp", []byte("b")))

	realLnk := linker.New(domain.LinkSymlink)
	lnk := &failingLinker{Linker: realLnk, failAfter: 1}
	inst := core.NewInstaller(modCache, lnk, database)

	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "1", SourceID: "src", Version: "v1", GameID: "g"}

	err = inst.Install(context.Background(), game, mod, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploying b.esp")

	// All deployed files should have been rolled back
	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.True(t, os.IsNotExist(err), "a.esp should have been rolled back")
	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "b.esp should not exist")

	// DB should have no records for this mod
	paths, err := database.GetDeployedFilesForMod("g", "default", "src", "1")
	require.NoError(t, err)
	assert.Empty(t, paths, "DB should have no deployed file records for this mod")
}

func TestInstaller_Replace_DeployFailureRestoresOldFiles(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("g", "src", "mod", "old", "same.esp", []byte("old-same")))
	require.NoError(t, modCache.Store("g", "src", "mod", "old", "old-only.esp", []byte("old-only")))
	require.NoError(t, modCache.Store("g", "src", "mod", "new", "same.esp", []byte("new-same")))
	require.NoError(t, modCache.Store("g", "src", "mod", "new", "new-only.esp", []byte("new-only")))

	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	oldMod := &domain.Mod{ID: "mod", SourceID: "src", Version: "old", GameID: "g"}
	newMod := &domain.Mod{ID: "mod", SourceID: "src", Version: "new", GameID: "g"}

	baseLinker := linker.New(domain.LinkSymlink)
	initialInstaller := core.NewInstaller(modCache, baseLinker, nil)
	require.NoError(t, initialInstaller.Install(context.Background(), game, oldMod, "default"))

	lnk := &conditionalFailingLinker{
		Linker:           linker.New(domain.LinkSymlink),
		failSrcSubstring: string(filepath.Separator) + "new" + string(filepath.Separator),
		failAfter:        1,
	}
	installer := core.NewInstaller(modCache, lnk, nil)
	err := installer.Replace(context.Background(), game, oldMod, newMod, "default")
	require.Error(t, err)

	samePath := filepath.Join(gameDir, "same.esp")
	oldOnlyPath := filepath.Join(gameDir, "old-only.esp")
	newOnlyPath := filepath.Join(gameDir, "new-only.esp")

	sameTarget, err := os.Readlink(samePath)
	require.NoError(t, err)
	assert.Equal(t, modCache.GetFilePath("g", "src", "mod", "old", "same.esp"), sameTarget)

	oldOnlyTarget, err := os.Readlink(oldOnlyPath)
	require.NoError(t, err)
	assert.Equal(t, modCache.GetFilePath("g", "src", "mod", "old", "old-only.esp"), oldOnlyTarget)

	_, err = os.Lstat(newOnlyPath)
	assert.True(t, os.IsNotExist(err), "new-only file should not remain after rollback")
}

func TestInstaller_ReplaceWithOldCache_SameVersionRemovesStaleFiles(t *testing.T) {
	oldCacheDir := t.TempDir()
	newCacheDir := t.TempDir()
	gameDir := t.TempDir()

	oldCache := cache.New(oldCacheDir)
	newCache := cache.New(newCacheDir)
	require.NoError(t, oldCache.Store("g", "src", "mod", "1.0", "main.esp", []byte("old-main")))
	require.NoError(t, oldCache.Store("g", "src", "mod", "1.0", "optional.esp", []byte("old-optional")))
	require.NoError(t, newCache.Store("g", "src", "mod", "1.0", "main.esp", []byte("new-main")))

	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "mod", SourceID: "src", Version: "1.0", GameID: "g"}

	initialInstaller := core.NewInstaller(oldCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, initialInstaller.Install(context.Background(), game, mod, "default"))

	installer := core.NewInstaller(newCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, installer.ReplaceWithOldCache(context.Background(), game, oldCache, mod, mod, "default"))

	mainTarget, err := os.Readlink(filepath.Join(gameDir, "main.esp"))
	require.NoError(t, err)
	assert.Equal(t, newCache.GetFilePath("g", "src", "mod", "1.0", "main.esp"), mainTarget)

	_, err = os.Lstat(filepath.Join(gameDir, "optional.esp"))
	assert.True(t, os.IsNotExist(err), "stale optional file should be removed")
}

func TestGetConflicts(t *testing.T) {
	// Setup test with in-memory DB and temp cache
	tempDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	defer func() { _ = database.Close() }()

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

// --- #144 item 4: same-version cache sharing on file-only updates ---

// seedSameDirUpdate builds the degenerate same-version update shape: ONE cache
// version directory holding both the old file's members (deployed) and the new
// file's members (just downloaded into the same dir), each file ID carrying a
// member manifest unless the caller passes it in legacyIDs (bare marker, no
// manifest - the pre-existing-cache shape). shared.esp belongs to BOTH files.
func seedSameDirUpdate(t *testing.T, modCache *cache.Cache, game *domain.Game, mod *domain.Mod, legacyIDs ...string) {
	t.Helper()

	require.NoError(t, modCache.Store(game.ID, mod.SourceID, mod.ID, mod.Version, "a.esp", []byte("a")))
	require.NoError(t, modCache.Store(game.ID, mod.SourceID, mod.ID, mod.Version, "shared.esp", []byte("shared")))
	require.NoError(t, modCache.Store(game.ID, mod.SourceID, mod.ID, mod.Version, "b.esp", []byte("b")))

	versionDir := modCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	manifests := map[string][]string{
		"fileA": {"a.esp", "shared.esp"},
		"fileB": {"b.esp", "shared.esp"},
	}
	legacy := make(map[string]bool, len(legacyIDs))
	for _, id := range legacyIDs {
		legacy[id] = true
	}
	for id, members := range manifests {
		if legacy[id] {
			require.NoError(t, cache.MarkFileComplete(versionDir, id))
			continue
		}
		require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, id, members))
	}
}

func TestInstaller_ReplaceForUpdate_SameCacheDir_UndeploysSupersededSoleMembers(t *testing.T) {
	gameDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	modCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "mod", SourceID: "src", Version: "1.0", GameID: "g"}
	seedSameDirUpdate(t, modCache, game, mod)

	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), database)
	// The "old deployment": in reality Install ran before fileB was downloaded,
	// but deploying the union changes nothing observable - what matters is that
	// a.esp and shared.esp are deployed before the update.
	require.NoError(t, inst.Install(context.Background(), game, mod, "default"))

	require.NoError(t, inst.ReplaceForUpdate(context.Background(), game, mod, mod, "default", []string{"fileA"}))

	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.True(t, os.IsNotExist(err), "the superseded file's sole member must be undeployed")
	_, err = os.Lstat(filepath.Join(gameDir, "shared.esp"))
	assert.NoError(t, err, "a member also listed in a surviving file's manifest must stay deployed")
	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.NoError(t, err, "the superseding file's member must be deployed")

	rows, err := database.GetDeployedFilesForMod("g", "default", "src", "mod")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"shared.esp", "b.esp"}, rows,
		"the superseded member's deployed-files row must be removed like any other obsolete file's")
}

// TestInstaller_ReplaceForUpdate_SameCacheDir_FallsBackToUnionWithoutProvenance
// is #144 item 4's hard backward-compat rule: when positive provenance is
// incomplete in ANY way, behave exactly as before - deploy the union, undeploy
// nothing, no error. Three incompleteness shapes:
//   - the superseded file's own marker is a legacy bare one,
//   - a SURVIVOR's marker is bare (its members unknowable, so sole ownership
//     of anything is unprovable),
//   - the dir holds content no recorded manifest attributes (an unmanifested
//     contributor exists - e.g. an entry populated by `lmm import`).
func TestInstaller_ReplaceForUpdate_SameCacheDir_FallsBackToUnionWithoutProvenance(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, modCache *cache.Cache, game *domain.Game, mod *domain.Mod)
	}{
		{"superseded marker is legacy-bare", func(t *testing.T, modCache *cache.Cache, game *domain.Game, mod *domain.Mod) {
			seedSameDirUpdate(t, modCache, game, mod, "fileA")
		}},
		{"survivor marker is legacy-bare", func(t *testing.T, modCache *cache.Cache, game *domain.Game, mod *domain.Mod) {
			seedSameDirUpdate(t, modCache, game, mod, "fileB")
		}},
		{"unattributed content present", func(t *testing.T, modCache *cache.Cache, game *domain.Game, mod *domain.Mod) {
			seedSameDirUpdate(t, modCache, game, mod)
			require.NoError(t, modCache.Store(game.ID, mod.SourceID, mod.ID, mod.Version, "orphan.esp", []byte("o")))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gameDir := t.TempDir()
			modCache := cache.New(t.TempDir())
			game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
			mod := &domain.Mod{ID: "mod", SourceID: "src", Version: "1.0", GameID: "g"}
			tc.seed(t, modCache, game, mod)

			inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
			require.NoError(t, inst.Install(context.Background(), game, mod, "default"))

			require.NoError(t, inst.ReplaceForUpdate(context.Background(), game, mod, mod, "default", []string{"fileA"}),
				"incomplete provenance must fall back silently, never error")

			for _, f := range []string{"a.esp", "shared.esp", "b.esp"} {
				_, err := os.Lstat(filepath.Join(gameDir, f))
				assert.NoError(t, err, "union fallback must leave %s deployed", f)
			}
		})
	}
}

// TestInstaller_ReplaceForUpdate_SameCacheDir_ReverseRestoresOldDeployment
// pins the compensation path ApplyUpdate leans on: after a forward
// ReplaceForUpdate, a failed DB/profile write compensates with the REVERSE
// call (new->old, superseded = the NEW file IDs). That must redeploy the
// superseded old members and remove the new file's sole members - the update
// that didn't commit leaves the game dir exactly as before.
func TestInstaller_ReplaceForUpdate_SameCacheDir_ReverseRestoresOldDeployment(t *testing.T) {
	gameDir := t.TempDir()
	modCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "mod", SourceID: "src", Version: "1.0", GameID: "g"}
	seedSameDirUpdate(t, modCache, game, mod)

	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, inst.Install(context.Background(), game, mod, "default"))
	require.NoError(t, inst.ReplaceForUpdate(context.Background(), game, mod, mod, "default", []string{"fileA"}))

	require.NoError(t, inst.ReplaceForUpdate(context.Background(), game, mod, mod, "default", []string{"fileB"}))

	_, err := os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "the compensated update must not leave the superseded member undeployed")
	_, err = os.Lstat(filepath.Join(gameDir, "shared.esp"))
	assert.NoError(t, err)
	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "the uncommitted new file's sole member must be removed")
}

// TestInstaller_ReplaceForUpdate_SameCacheDir_MidReplaceFailureRestoresSuperseded:
// a deploy failure INSIDE the replace must restore the already-undeployed
// superseded member (it is in removedOld, and the shared version dir still
// holds its bytes - the rollback-fidelity precondition for never re-keying
// this cache).
func TestInstaller_ReplaceForUpdate_SameCacheDir_MidReplaceFailureRestoresSuperseded(t *testing.T) {
	gameDir := t.TempDir()
	modCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "mod", SourceID: "src", Version: "1.0", GameID: "g"}
	seedSameDirUpdate(t, modCache, game, mod)

	initial := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, initial.Install(context.Background(), game, mod, "default"))

	lnk := &conditionalFailingLinker{
		Linker:           linker.New(domain.LinkSymlink),
		failSrcSubstring: string(filepath.Separator) + "b.esp",
		failAfter:        0,
	}
	inst := core.NewInstaller(modCache, lnk, nil)
	err := inst.ReplaceForUpdate(context.Background(), game, mod, mod, "default", []string{"fileA"})
	require.Error(t, err)

	_, statErr := os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, statErr, "the superseded member must be restored after a mid-replace failure")
	_, statErr = os.Lstat(filepath.Join(gameDir, "shared.esp"))
	assert.NoError(t, statErr)
}

// TestInstaller_ReplaceForUpdate_DistinctCacheDirs_MatchesReplace: on a normal
// different-version update the old and new cache keys differ, the existing
// obsolete-file loop already handles supersession, and the superseded set must
// change nothing.
func TestInstaller_ReplaceForUpdate_DistinctCacheDirs_MatchesReplace(t *testing.T) {
	gameDir := t.TempDir()
	modCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	oldMod := &domain.Mod{ID: "mod", SourceID: "src", Version: "1.0", GameID: "g"}
	newMod := &domain.Mod{ID: "mod", SourceID: "src", Version: "2.0", GameID: "g"}

	require.NoError(t, modCache.Store("g", "src", "mod", "1.0", "old-only.esp", []byte("old")))
	require.NoError(t, modCache.Store("g", "src", "mod", "1.0", "shared.esp", []byte("shared")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(modCache.ModPath("g", "src", "mod", "1.0"), "fileA", []string{"old-only.esp", "shared.esp"}))
	require.NoError(t, modCache.Store("g", "src", "mod", "2.0", "new-only.esp", []byte("new")))
	require.NoError(t, modCache.Store("g", "src", "mod", "2.0", "shared.esp", []byte("shared-2")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(modCache.ModPath("g", "src", "mod", "2.0"), "fileB", []string{"new-only.esp", "shared.esp"}))

	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, inst.Install(context.Background(), game, oldMod, "default"))

	require.NoError(t, inst.ReplaceForUpdate(context.Background(), game, oldMod, newMod, "default", []string{"fileA"}))

	_, err := os.Lstat(filepath.Join(gameDir, "old-only.esp"))
	assert.True(t, os.IsNotExist(err), "the obsolete-file loop already removes old-only files")
	_, err = os.Lstat(filepath.Join(gameDir, "new-only.esp"))
	assert.NoError(t, err)
	target, err := os.Readlink(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, modCache.GetFilePath("g", "src", "mod", "2.0", "shared.esp"), target,
		"shared members must point at the NEW version's cache entry")
}
