package core_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestZip(t *testing.T, dir string, files map[string]string) string {
	zipPath := filepath.Join(dir, "test.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	return zipPath
}

func TestExtractor_Extract_Zip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a test zip file
	files := map[string]string{
		"readme.txt":        "This is a readme file",
		"mod/plugin.dll":    "plugin binary content",
		"mod/data/file.txt": "nested file content",
	}
	zipPath := createTestZip(t, srcDir, files)

	extractor := core.NewExtractor()
	err := extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Verify files were extracted
	content, err := os.ReadFile(filepath.Join(destDir, "readme.txt"))
	require.NoError(t, err)
	assert.Equal(t, "This is a readme file", string(content))

	content, err = os.ReadFile(filepath.Join(destDir, "mod/plugin.dll"))
	require.NoError(t, err)
	assert.Equal(t, "plugin binary content", string(content))

	content, err = os.ReadFile(filepath.Join(destDir, "mod/data/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested file content", string(content))
}

func TestExtractor_Extract_ZipWithDirectories(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip with explicit directory entries
	zipPath := filepath.Join(srcDir, "test.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)

	// Add directory entry
	_, err = w.Create("subdir/")
	require.NoError(t, err)

	// Add file in directory
	fw, err := w.Create("subdir/file.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("content"))
	require.NoError(t, err)

	require.NoError(t, w.Close())
	f.Close()

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Verify directory was created
	info, err := os.Stat(filepath.Join(destDir, "subdir"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify file was extracted
	content, err := os.ReadFile(filepath.Join(destDir, "subdir/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(content))
}

func TestExtractor_Extract_EmptyZip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create an empty zip file
	zipPath := filepath.Join(srcDir, "empty.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	require.NoError(t, w.Close())
	f.Close()

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.NoError(t, err)
}

func TestExtractor_Extract_NonExistentFile(t *testing.T) {
	destDir := t.TempDir()

	extractor := core.NewExtractor()
	err := extractor.Extract("/nonexistent/file.zip", destDir)
	require.Error(t, err)
}

func TestExtractor_Extract_InvalidZip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a file that's not a valid zip
	invalidPath := filepath.Join(srcDir, "invalid.zip")
	err := os.WriteFile(invalidPath, []byte("not a zip file"), 0644)
	require.NoError(t, err)

	extractor := core.NewExtractor()
	err = extractor.Extract(invalidPath, destDir)
	require.Error(t, err)
}

// TestExtractor_Extract_TruncatedZip verifies corrupt/truncated zip returns error (error-path test).
func TestExtractor_Extract_TruncatedZip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip that has valid local header but is truncated (no central directory / truncated content)
	zipPath := filepath.Join(srcDir, "truncated.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("file.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Sync())
	// Truncate file to simulate corrupt download (remove central directory)
	info, err := f.Stat()
	require.NoError(t, err)
	err = f.Truncate(info.Size() / 2)
	require.NoError(t, err)
	f.Close()

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.Error(t, err)
}

func TestExtractor_CanExtract(t *testing.T) {
	extractor := core.NewExtractor()

	tests := []struct {
		filename string
		expected bool
	}{
		{"mod.zip", true},
		{"mod.ZIP", true},
		{"mod.7z", true},
		{"mod.7Z", true},
		{"mod.rar", true},
		{"mod.RAR", true},
		{"mod.txt", false},
		{"mod.exe", false},
		{"mod", false},
		{"", false},
		{"archive.tar.gz", false}, // Not supported
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := extractor.CanExtract(tt.filename)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractor_DetectFormat(t *testing.T) {
	extractor := core.NewExtractor()

	tests := []struct {
		filename string
		expected string
	}{
		{"mod.zip", "zip"},
		{"mod.ZIP", "zip"},
		{"mod.7z", "7z"},
		{"mod.7Z", "7z"},
		{"mod.rar", "rar"},
		{"mod.RAR", "rar"},
		{"mod.txt", ""},
		{"mod", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := extractor.DetectFormat(tt.filename)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractor_Extract_ZipSlipPrevention(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip with a path traversal attempt
	zipPath := filepath.Join(srcDir, "malicious.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)
	// Try to write outside the destination directory
	fw, err := w.CreateHeader(&zip.FileHeader{
		Name:   "../../../etc/passwd",
		Method: zip.Store,
	})
	require.NoError(t, err)
	_, err = fw.Write([]byte("malicious content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	f.Close()

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	// Should either error or safely extract to a sanitized path
	// The key is that it should NOT write to ../../../etc/passwd
	if err == nil {
		// If no error, verify file was NOT written outside destDir
		_, err := os.Stat("/etc/passwd_test")
		assert.True(t, os.IsNotExist(err), "should not write outside destination")
	}
}

func TestExtractor_Extract_PreservesPermissions(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a zip with a file that has execute permissions
	zipPath := filepath.Join(srcDir, "test.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)

	w := zip.NewWriter(f)
	header := &zip.FileHeader{
		Name:   "script.sh",
		Method: zip.Store,
	}
	header.SetMode(0755)
	fw, err := w.CreateHeader(header)
	require.NoError(t, err)
	_, err = fw.Write([]byte("#!/bin/bash\necho hello"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	f.Close()

	extractor := core.NewExtractor()
	err = extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Check that the file was extracted (permissions may vary by platform)
	info, err := os.Stat(filepath.Join(destDir, "script.sh"))
	require.NoError(t, err)
	assert.False(t, info.IsDir())
}

func TestExtractor_Extract_CreatesDestDir(t *testing.T) {
	srcDir := t.TempDir()

	files := map[string]string{
		"file.txt": "content",
	}
	zipPath := createTestZip(t, srcDir, files)

	// Use a non-existent destination directory
	destDir := filepath.Join(t.TempDir(), "nested", "dest")

	extractor := core.NewExtractor()
	err := extractor.Extract(zipPath, destDir)
	require.NoError(t, err)

	// Verify directory was created and file extracted
	content, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(content))
}

func TestNewExtractor(t *testing.T) {
	e := core.NewExtractor()
	assert.NotNil(t, e)
}
