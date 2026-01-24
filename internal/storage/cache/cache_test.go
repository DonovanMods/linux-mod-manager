package cache_test

import (
	"os"
	"path/filepath"
	"testing"

	"lmm/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_ModPath(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	path := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	expected := filepath.Join(dir, "skyrim-se", "nexusmods-12345", "1.0.0")
	assert.Equal(t, expected, path)
}

func TestCache_Store(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	content := []byte("test mod content")
	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "testfile.txt", content)
	require.NoError(t, err)

	// Verify file exists
	storedPath := filepath.Join(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "testfile.txt")
	data, err := os.ReadFile(storedPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestCache_Exists(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	assert.False(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))

	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data"))
	require.NoError(t, err)

	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))
}

func TestCache_ListFiles(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// Store multiple files
	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "file1.txt", []byte("1"))
	require.NoError(t, err)
	err = c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "subdir/file2.txt", []byte("2"))
	require.NoError(t, err)

	files, err := c.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

func TestCache_Delete(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data"))
	require.NoError(t, err)
	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))

	err = c.Delete("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.False(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))
}
