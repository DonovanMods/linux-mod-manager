package core_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createImportTestZip creates a simple zip archive for testing
func createImportTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, f.Close())
	}()

	w := zip.NewWriter(f)
	defer func() {
		require.NoError(t, w.Close())
	}()

	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
}

func TestImportMod_LocalArchive(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "TestMod.zip")

	// Create test archive with multiple top-level items (no single directory)
	// so DetectModName falls back to archive basename
	createImportTestZip(t, archivePath, map[string]string{
		"plugin.esp":   "test plugin",
		"textures.dds": "test texture",
	})

	// Create cache and database
	modCache := cache.New(cacheDir)

	game := &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: filepath.Join(tempDir, "game", "mods"),
	}

	// Create importer
	importer := core.NewImporter(modCache)

	// Import the mod
	ctx := context.Background()
	result, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		ProfileName: "default",
	})
	require.NoError(t, err)

	// Verify result
	assert.Equal(t, domain.SourceLocal, result.Mod.SourceID)
	assert.Equal(t, "TestMod", result.Mod.Name) // Falls back to archive name without extension
	assert.Equal(t, 2, result.FilesExtracted)
	assert.False(t, result.AutoDetected)

	// Verify files are in cache
	assert.True(t, modCache.Exists(game.ID, domain.SourceLocal, result.Mod.ID, result.Mod.Version))
}

func TestImportMod_NexusModsFilename(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "SkyUI-12604-5-2SE.zip")

	// Create test archive
	createImportTestZip(t, archivePath, map[string]string{
		"SkyUI/plugin.esp": "test plugin",
	})

	modCache := cache.New(cacheDir)

	game := &domain.Game{
		ID:      "skyrim-se",
		Name:    "Skyrim SE",
		ModPath: filepath.Join(tempDir, "game", "mods"),
	}

	importer := core.NewImporter(modCache)

	ctx := context.Background()
	result, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		ProfileName: "default",
	})
	require.NoError(t, err)

	// Should detect NexusMods pattern but use "local" since we can't verify
	// (no API call in basic import - linking happens in command layer)
	assert.Equal(t, "12604", result.Mod.ID)
	assert.Equal(t, "5.2SE", result.Mod.Version)
	assert.True(t, result.AutoDetected)
}

func TestImportMod_UnsupportedFormat(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "mod.txt")

	// Create a non-archive file
	require.NoError(t, os.WriteFile(archivePath, []byte("not an archive"), 0644))

	modCache := cache.New(cacheDir)

	game := &domain.Game{ID: "testgame"}

	importer := core.NewImporter(modCache)

	ctx := context.Background()
	_, err := importer.Import(ctx, archivePath, game, core.ImportOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestImportMod_FileNotFound(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")

	modCache := cache.New(cacheDir)

	game := &domain.Game{ID: "testgame"}

	importer := core.NewImporter(modCache)

	ctx := context.Background()
	_, err := importer.Import(ctx, "/nonexistent/file.zip", game, core.ImportOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestImportMod_ExplicitSourceAndModID(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "SomeMod.zip")

	// Create test archive
	createImportTestZip(t, archivePath, map[string]string{
		"readme.txt": "test content",
	})

	modCache := cache.New(cacheDir)

	game := &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: filepath.Join(tempDir, "game", "mods"),
	}

	importer := core.NewImporter(modCache)

	ctx := context.Background()
	result, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		SourceID: "nexusmods",
		ModID:    "99999",
	})
	require.NoError(t, err)

	// Should use explicit source and mod ID
	assert.Equal(t, "nexusmods", result.Mod.SourceID)
	assert.Equal(t, "99999", result.Mod.ID)
	assert.False(t, result.AutoDetected)
}

func TestImportMod_ReimportOverwritesCache(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "TestMod.zip")

	modCache := cache.New(cacheDir)

	game := &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: filepath.Join(tempDir, "game", "mods"),
	}

	importer := core.NewImporter(modCache)
	ctx := context.Background()

	// First import with one file
	createImportTestZip(t, archivePath, map[string]string{
		"file1.txt": "original content",
	})

	result1, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		SourceID: domain.SourceLocal,
		ModID:    "test-mod-123",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result1.FilesExtracted)

	// Re-import with different files
	require.NoError(t, os.Remove(archivePath))
	createImportTestZip(t, archivePath, map[string]string{
		"file2.txt": "new content",
		"file3.txt": "more content",
	})

	result2, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		SourceID: domain.SourceLocal,
		ModID:    "test-mod-123",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result2.FilesExtracted)

	// Verify old file is gone and new files exist
	files, err := modCache.ListFiles(game.ID, domain.SourceLocal, "test-mod-123", result2.Mod.Version)
	require.NoError(t, err)
	assert.Equal(t, 2, len(files))
}
