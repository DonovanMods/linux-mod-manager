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
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileManager_Create(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	profile, err := pm.Create("skyrim-se", "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", profile.Name)
	assert.Equal(t, "skyrim-se", profile.GameID)
}

func TestProfileManager_Create_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "survival")
	require.NoError(t, err)

	_, err = pm.Create("skyrim-se", "survival")
	assert.Error(t, err) // Should fail - duplicate name
}

func TestProfileManager_List(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "survival")
	require.NoError(t, err)
	_, err = pm.Create("skyrim-se", "combat")
	require.NoError(t, err)

	profiles, err := pm.List("skyrim-se")
	require.NoError(t, err)
	assert.Len(t, profiles, 2)
}

func TestProfileManager_Get(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "survival")
	require.NoError(t, err)

	profile, err := pm.Get("skyrim-se", "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", profile.Name)
}

func TestProfileManager_Get_NotFound(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Get("skyrim-se", "nonexistent")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestProfileManager_Delete(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "survival")
	require.NoError(t, err)

	err = pm.Delete("skyrim-se", "survival")
	require.NoError(t, err)

	_, err = pm.Get("skyrim-se", "survival")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestProfileManager_SetDefault(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "profile1")
	require.NoError(t, err)
	_, err = pm.Create("skyrim-se", "profile2")
	require.NoError(t, err)

	err = pm.SetDefault("skyrim-se", "profile2")
	require.NoError(t, err)

	defaultProfile, err := pm.GetDefault("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "profile2", defaultProfile.Name)
}

func TestProfileManager_AddMod(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "survival")
	require.NoError(t, err)

	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
	}

	err = pm.AddMod("skyrim-se", "survival", modRef)
	require.NoError(t, err)

	profile, err := pm.Get("skyrim-se", "survival")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "12345", profile.Mods[0].ModID)
}

func TestProfileManager_RemoveMod(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	_, err = pm.Create("skyrim-se", "survival")
	require.NoError(t, err)

	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
	}
	err = pm.AddMod("skyrim-se", "survival", modRef)
	require.NoError(t, err)

	err = pm.RemoveMod("skyrim-se", "survival", "nexusmods", "12345")
	require.NoError(t, err)

	profile, err := pm.Get("skyrim-se", "survival")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods)
}

func TestProfileManager_Switch(t *testing.T) {
	dir := t.TempDir()
	modPath := filepath.Join(dir, "game", "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	cacheDir := filepath.Join(dir, "cache")
	modCache := cache.New(cacheDir)
	lnk := linker.NewSymlink()

	pm := core.NewProfileManager(dir, database, modCache, lnk)

	game := &domain.Game{
		ID:      "skyrim-se",
		Name:    "Skyrim SE",
		ModPath: modPath,
	}

	// Create two profiles
	_, err = pm.Create("skyrim-se", "profile1")
	require.NoError(t, err)
	_, err = pm.Create("skyrim-se", "profile2")
	require.NoError(t, err)

	// Add mod to profile1 and cache it
	modRef := domain.ModReference{SourceID: "nexusmods", ModID: "123", Version: "1.0"}
	err = pm.AddMod("skyrim-se", "profile1", modRef)
	require.NoError(t, err)

	// Create cached mod file
	err = modCache.Store("skyrim-se", "nexusmods", "123", "1.0", "test.esp", []byte("mod data"))
	require.NoError(t, err)

	// Switch to profile1 should deploy mods
	err = pm.Switch(context.Background(), game, "profile1")
	require.NoError(t, err)

	// Verify mod is deployed
	deployedPath := filepath.Join(modPath, "test.esp")
	_, err = os.Lstat(deployedPath)
	require.NoError(t, err)

	// Switch to profile2 should undeploy profile1 mods
	err = pm.Switch(context.Background(), game, "profile2")
	require.NoError(t, err)

	// Verify mod is no longer deployed
	_, err = os.Lstat(deployedPath)
	assert.True(t, os.IsNotExist(err))
}

func TestProfileManager_ExportImport(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	pm := core.NewProfileManager(dir, database, cache.New(dir), linker.NewSymlink())

	// Create a profile with mods
	_, err = pm.Create("skyrim-se", "original")
	require.NoError(t, err)

	err = pm.AddMod("skyrim-se", "original", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "123",
		Version:  "1.0",
	})
	require.NoError(t, err)

	// Export it
	data, err := pm.Export("skyrim-se", "original")
	require.NoError(t, err)
	assert.Contains(t, string(data), "original")
	assert.Contains(t, string(data), "123")

	// Delete the original
	err = pm.Delete("skyrim-se", "original")
	require.NoError(t, err)

	// Import it back
	imported, err := pm.Import(data)
	require.NoError(t, err)
	assert.Equal(t, "original", imported.Name)
	assert.Len(t, imported.Mods, 1)

	// Verify it exists
	profile, err := pm.Get("skyrim-se", "original")
	require.NoError(t, err)
	assert.Equal(t, "original", profile.Name)
}

func TestProfileManager_UpsertMod(t *testing.T) {
	dir := t.TempDir()

	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	cacheDir := filepath.Join(dir, "cache")
	modCache := cache.New(cacheDir)
	lnk := linker.NewSymlink()

	pm := core.NewProfileManager(dir, database, modCache, lnk)

	// Create a profile
	_, err = pm.Create("skyrim-se", "test")
	require.NoError(t, err)

	// Upsert a new mod (should add it)
	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		FileIDs:  []string{"100"},
	}
	err = pm.UpsertMod("skyrim-se", "test", modRef)
	require.NoError(t, err)

	profile, err := pm.Get("skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "12345", profile.Mods[0].ModID)
	assert.Equal(t, "1.0.0", profile.Mods[0].Version)
	assert.Equal(t, []string{"100"}, profile.Mods[0].FileIDs)

	// Upsert the same mod with updated version and FileIDs (should update in place)
	modRef2 := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "2.0.0",
		FileIDs:  []string{"200", "201"},
	}
	err = pm.UpsertMod("skyrim-se", "test", modRef2)
	require.NoError(t, err)

	profile, err = pm.Get("skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1) // Should still be 1 mod, not 2
	assert.Equal(t, "12345", profile.Mods[0].ModID)
	assert.Equal(t, "2.0.0", profile.Mods[0].Version)
	assert.Equal(t, []string{"200", "201"}, profile.Mods[0].FileIDs)

	// Upsert a different mod (should add it)
	modRef3 := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "67890",
		Version:  "1.0.0",
		FileIDs:  []string{"300"},
	}
	err = pm.UpsertMod("skyrim-se", "test", modRef3)
	require.NoError(t, err)

	profile, err = pm.Get("skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2) // Now should be 2 mods
	// First mod should still be in position 0
	assert.Equal(t, "12345", profile.Mods[0].ModID)
	assert.Equal(t, "67890", profile.Mods[1].ModID)
}
