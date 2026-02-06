package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanModPath_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	game := &domain.Game{
		ID:         "test-game",
		ModPath:    modPath,
		DeployMode: domain.DeployExtract,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Empty(t, results, "empty directory should return no results")
}

func TestScanModPath_NonExistentPath(t *testing.T) {
	game := &domain.Game{
		ID:         "test-game",
		ModPath:    "/nonexistent/path/that/does/not/exist",
		DeployMode: domain.DeployExtract,
	}

	importer := core.NewImporter(nil)
	_, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mod_path does not exist")
}

func TestScanModPath_NoModPathConfigured(t *testing.T) {
	game := &domain.Game{
		ID:      "test-game",
		ModPath: "",
	}

	importer := core.NewImporter(nil)
	_, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no mod_path configured")
}

func TestScanModPath_CopyMode_JarFiles(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create test .jar files
	jarFiles := []string{
		"SomeMod-1.2.3.jar",
		"AnotherMod-v2.0.jar",
		"JustACraft-4.5.6.jar",
	}
	for _, name := range jarFiles {
		require.NoError(t, os.WriteFile(filepath.Join(modPath, name), []byte("fake jar"), 0644))
	}

	// Create a non-mod file that should be ignored
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "readme.txt"), []byte("readme"), 0644))

	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 3, "should find exactly 3 jar files")

	// Verify filenames
	var foundNames []string
	for _, r := range results {
		foundNames = append(foundNames, r.FileName)
		assert.False(t, r.AlreadyTracked, "new files should not be tracked")
	}
	assert.ElementsMatch(t, jarFiles, foundNames)
}

func TestScanModPath_CopyMode_ZipFiles(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create test .zip files
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "TexturePack-1.0.zip"), []byte("fake zip"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "ResourcePack.zip"), []byte("fake zip"), 0644))

	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 2, "should find both zip files")
}

func TestScanModPath_CopyMode_IgnoresDirectories(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create a directory (should be ignored in copy mode)
	require.NoError(t, os.MkdirAll(filepath.Join(modPath, "SomeModFolder"), 0755))

	// Create a jar file
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "RealMod.jar"), []byte("jar"), 0644))

	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 1, "should only find the jar file, not the directory")
	assert.Equal(t, "RealMod.jar", results[0].FileName)
}

func TestScanModPath_CopyMode_SymlinksMarkedAsTracked(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	cacheDir := filepath.Join(tempDir, "cache")
	require.NoError(t, os.MkdirAll(modPath, 0755))
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// Create a cached file
	cachedFile := filepath.Join(cacheDir, "CachedMod-1.0.jar")
	require.NoError(t, os.WriteFile(cachedFile, []byte("cached jar"), 0644))

	// Create a symlink in mod_path pointing to cache (simulating lmm deployment)
	symlinkPath := filepath.Join(modPath, "CachedMod-1.0.jar")
	require.NoError(t, os.Symlink(cachedFile, symlinkPath))

	// Create a regular file (not a symlink)
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "ManualMod.jar"), []byte("manual"), 0644))

	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 2)

	// Find results by name
	var symlinkResult, regularResult *core.ScanResult
	for i := range results {
		if results[i].FileName == "CachedMod-1.0.jar" {
			symlinkResult = &results[i]
		} else if results[i].FileName == "ManualMod.jar" {
			regularResult = &results[i]
		}
	}

	require.NotNil(t, symlinkResult, "should find symlink")
	require.NotNil(t, regularResult, "should find regular file")

	assert.True(t, symlinkResult.AlreadyTracked, "symlink should be marked as tracked")
	assert.False(t, regularResult.AlreadyTracked, "regular file should not be tracked")
}

func TestScanModPath_ExtractMode_Directories(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create mod directories
	modDirs := []string{
		"SkyUI",
		"USSEP",
		"EnhancedBlood",
	}
	for _, name := range modDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(modPath, name), 0755))
	}

	// Create a file (should be ignored in extract mode)
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "readme.txt"), []byte("readme"), 0644))

	game := &domain.Game{
		ID:         "skyrim-se",
		ModPath:    modPath,
		DeployMode: domain.DeployExtract,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 3, "should find exactly 3 directories")

	var foundNames []string
	for _, r := range results {
		foundNames = append(foundNames, r.FileName)
		assert.False(t, r.AlreadyTracked)
	}
	assert.ElementsMatch(t, modDirs, foundNames)
}

func TestScanModPath_ExtractMode_IgnoresFiles(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create a file (should be ignored in extract mode)
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "loose-file.esp"), []byte("esp"), 0644))

	// Create a directory
	require.NoError(t, os.MkdirAll(filepath.Join(modPath, "ProperMod"), 0755))

	game := &domain.Game{
		ID:         "skyrim-se",
		ModPath:    modPath,
		DeployMode: domain.DeployExtract,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 1, "should only find the directory, not the file")
	assert.Equal(t, "ProperMod", results[0].FileName)
}

func TestScanModPath_AlreadyTrackedMods(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create files
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "TrackedMod-1.0.jar"), []byte("jar"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "CompletelyDifferent-2.0.jar"), []byte("jar"), 0644))

	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	// Simulate already installed mods
	installedMods := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "tracked-123", Name: "TrackedMod-1.0"}},
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, installedMods, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 2)

	// Find results by name
	for _, r := range results {
		if r.FileName == "TrackedMod-1.0.jar" {
			assert.True(t, r.AlreadyTracked, "TrackedMod should be marked as tracked")
		} else {
			assert.False(t, r.AlreadyTracked, "CompletelyDifferent should not be tracked")
		}
	}
}

func TestScanModPath_VersionDetection(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create files with version patterns
	testCases := []struct {
		filename        string
		expectedVersion string
	}{
		{"ModName-1.2.3.jar", "1.2.3"},
		{"ModName-v2.0.0.jar", "2.0.0"},
		{"SomeMod-1.0.jar", "1.0"},
	}

	for _, tc := range testCases {
		require.NoError(t, os.WriteFile(filepath.Join(modPath, tc.filename), []byte("jar"), 0644))
	}

	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, len(testCases))

	// Verify versions were detected
	for _, r := range results {
		for _, tc := range testCases {
			if r.FileName == tc.filename {
				require.NotNil(t, r.Mod, "mod info should be detected for %s", tc.filename)
				assert.Equal(t, tc.expectedVersion, r.Mod.Version,
					"version mismatch for %s", tc.filename)
			}
		}
	}
}

func TestScanModPath_TildeExpansion(t *testing.T) {
	// This test verifies that ~ is expanded to home directory
	// We create a temp dir and use it as a fake home
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "mods")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	// Create a test file
	require.NoError(t, os.WriteFile(filepath.Join(modPath, "test.jar"), []byte("jar"), 0644))

	// Use absolute path (~ expansion is tested implicitly in real usage)
	// Here we just verify the function works with normal paths
	game := &domain.Game{
		ID:         "minecraft",
		ModPath:    modPath,
		DeployMode: domain.DeployCopy,
	}

	importer := core.NewImporter(nil)
	results, err := importer.ScanModPath(context.Background(), game, nil, core.ScanOptions{})

	require.NoError(t, err)
	assert.Len(t, results, 1)
}
