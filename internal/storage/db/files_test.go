package db_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveDeployedFile(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	err = database.SaveDeployedFile("skyrim-se", "default", "meshes/test.nif", "nexusmods", "12345")
	require.NoError(t, err)

	// Verify it was saved
	owner, err := database.GetFileOwner("skyrim-se", "default", "meshes/test.nif")
	require.NoError(t, err)
	assert.Equal(t, "nexusmods", owner.SourceID)
	assert.Equal(t, "12345", owner.ModID)
}

func TestSaveDeployedFile_Upsert(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	// Save initial owner
	err = database.SaveDeployedFile("skyrim-se", "default", "meshes/test.nif", "nexusmods", "111")
	require.NoError(t, err)

	// Overwrite with new owner
	err = database.SaveDeployedFile("skyrim-se", "default", "meshes/test.nif", "nexusmods", "222")
	require.NoError(t, err)

	// Verify new owner
	owner, err := database.GetFileOwner("skyrim-se", "default", "meshes/test.nif")
	require.NoError(t, err)
	assert.Equal(t, "222", owner.ModID)
}

func TestGetFileOwner_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	owner, err := database.GetFileOwner("skyrim-se", "default", "nonexistent.nif")
	require.NoError(t, err)
	assert.Nil(t, owner)
}

func TestDeleteDeployedFiles(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	// Save some files
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/a.nif", "nexusmods", "123"))
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/b.nif", "nexusmods", "123"))
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/c.nif", "nexusmods", "456"))

	// Delete files for mod 123
	err = database.DeleteDeployedFiles("skyrim-se", "default", "nexusmods", "123")
	require.NoError(t, err)

	// Verify 123's files are gone
	owner, _ := database.GetFileOwner("skyrim-se", "default", "meshes/a.nif")
	assert.Nil(t, owner)
	owner, _ = database.GetFileOwner("skyrim-se", "default", "meshes/b.nif")
	assert.Nil(t, owner)

	// Verify 456's files remain
	owner, _ = database.GetFileOwner("skyrim-se", "default", "meshes/c.nif")
	assert.NotNil(t, owner)
	assert.Equal(t, "456", owner.ModID)
}

func TestGetDeployedFilesForMod(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/a.nif", "nexusmods", "123"))
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/b.nif", "nexusmods", "123"))
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/c.nif", "nexusmods", "456"))

	files, err := database.GetDeployedFilesForMod("skyrim-se", "default", "nexusmods", "123")
	require.NoError(t, err)
	assert.Len(t, files, 2)
	assert.Contains(t, files, "meshes/a.nif")
	assert.Contains(t, files, "meshes/b.nif")
}

func TestGetDeployedFilesForMod_Empty(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	files, err := database.GetDeployedFilesForMod("skyrim-se", "default", "nexusmods", "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestCheckFileConflicts(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	// Mod 123 owns some files
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/shared.nif", "nexusmods", "123"))
	require.NoError(t, database.SaveDeployedFile("skyrim-se", "default", "meshes/only123.nif", "nexusmods", "123"))

	// Check conflicts for new mod that wants to deploy shared.nif and newfile.nif
	paths := []string{"meshes/shared.nif", "meshes/newfile.nif"}
	conflicts, err := database.CheckFileConflicts("skyrim-se", "default", paths)
	require.NoError(t, err)

	// Only shared.nif should conflict
	assert.Len(t, conflicts, 1)
	assert.Equal(t, "meshes/shared.nif", conflicts[0].RelativePath)
	assert.Equal(t, "123", conflicts[0].ModID)
}

func TestCheckFileConflicts_Empty(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	// No paths to check
	conflicts, err := database.CheckFileConflicts("skyrim-se", "default", nil)
	require.NoError(t, err)
	assert.Empty(t, conflicts)

	// Empty slice
	conflicts, err = database.CheckFileConflicts("skyrim-se", "default", []string{})
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

func TestCheckFileConflicts_NoConflicts(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	// No existing files, check paths
	paths := []string{"meshes/new1.nif", "meshes/new2.nif"}
	conflicts, err := database.CheckFileConflicts("skyrim-se", "default", paths)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}
