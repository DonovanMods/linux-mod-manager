package main

import (
	"archive/zip"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImportCommand_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// This is a placeholder for manual testing
	// Full integration tests would require:
	// 1. Setting up a test config directory
	// 2. Creating a test game config
	// 3. Running the import command
	// 4. Verifying files are deployed

	t.Log("Import command integration test - run manually with: ./lmm import testmod.zip -g testgame")
}

func TestCreateTestArchive_Helper(t *testing.T) {
	// Verify the test helper works correctly
	tempDir := t.TempDir()
	archivePath := tempDir + "/test.zip"

	createTestArchive(t, archivePath, map[string]string{
		"file1.txt":     "content1",
		"dir/file2.txt": "content2",
	})

	// Verify archive was created
	info, err := os.Stat(archivePath)
	require.NoError(t, err)
	require.True(t, info.Size() > 0, "archive should not be empty")
}

func createTestArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}
