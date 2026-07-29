package cache_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

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

func TestCache_ModPath_GameScoped(t *testing.T) {
	dir := t.TempDir()
	c := cache.NewGameScoped(dir)

	path := c.ModPath("starrupture", "nexusmods", "35", "1.00")
	expected := filepath.Join(dir, "nexusmods-35", "1.00")
	assert.Equal(t, expected, path, "game-scoped cache omits gameID from path")
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

// TestCache_HasFiles is the #96 review round 1 finding 2 guard: HasFiles
// must be a stronger check than Exists - Exists only checks the version
// directory's presence, which can wrongly report "cached" for a directory
// left PARTIALLY populated by a previous download run that broke partway
// through a multi-file mod (each file is committed to the cache
// individually - see DownloadModToCache's doc comment at
// internal/core/service.go:411-414). HasFiles must report false whenever
// any named file is actually missing, even though the directory itself
// exists.
func TestCache_HasFiles(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// No directory at all yet.
	assert.False(t, c.HasFiles("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"a.esp"}))

	// Directory exists but is completely empty (e.g. MkdirAll ran but no
	// file was ever committed) - still not "has files".
	require.NoError(t, os.MkdirAll(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), 0755))
	assert.False(t, c.HasFiles("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"a.esp"}))

	// Directory has ONE of two expected files - a partial download.
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "a.esp", []byte("a")))
	assert.False(t, c.HasFiles("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"a.esp", "b.esp"}),
		"a partially-populated cache entry must not report having all requested files")

	// Both expected files present - fully cached.
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "b.esp", []byte("b")))
	assert.True(t, c.HasFiles("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"a.esp", "b.esp"}))

	// An empty filenames list against a nonexistent entry is still false -
	// HasFiles is never weaker than Exists.
	assert.False(t, c.HasFiles("skyrim-se", "nexusmods", "no-such-mod", "1.0.0", nil))

	// An empty filenames list against an entry that DOES exist has nothing
	// left to verify beyond Exists itself.
	assert.True(t, c.HasFiles("skyrim-se", "nexusmods", "12345", "1.0.0", nil))

	// A blank filename can never be verified - the safe direction is false
	// (triggers a redundant re-download, not a silently-trusted gap).
	assert.False(t, c.HasFiles("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"a.esp", ""}))
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

func TestCache_Exists_ListFiles_GameScoped(t *testing.T) {
	dir := t.TempDir()
	c := cache.NewGameScoped(dir)

	err := c.Store("starrupture", "nexusmods", "35", "1.00", "file.pak", []byte("data"))
	require.NoError(t, err)
	assert.True(t, c.Exists("starrupture", "nexusmods", "35", "1.00"))

	files, err := c.ListFiles("starrupture", "nexusmods", "35", "1.00")
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.Equal(t, "file.pak", files[0])
}

func TestCache_Size(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "file1.txt", []byte("12345")))       // 5 bytes
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "sub/file2.txt", []byte("1234567"))) // 7 bytes

	size, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.EqualValues(t, 12, size)
}

func TestCache_Size_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// The mod directory exists (created by Store then Delete leaves nothing,
	// so create it directly) but contains no files.
	modPath := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	size, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.EqualValues(t, 0, size)
}

// TestCache_Size_MissingDir pins that Size on a mod version that was never
// cached is an error, not a silent 0: filepath.WalkDir fails to stat a
// nonexistent root and Size wraps that failure rather than treating "not
// cached" as "zero bytes".
func TestCache_Size_MissingDir(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	size, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")

	require.Error(t, err)
	assert.Zero(t, size)
	assert.Contains(t, err.Error(), "calculating cache size")
}

func TestCache_CloneMod(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src := cache.New(srcDir)
	dst := cache.New(dstDir)

	require.NoError(t, src.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "main.esp", []byte("main")))
	require.NoError(t, src.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "optional/patch.esp", []byte("patch")))

	require.NoError(t, src.CloneMod(dst, "skyrim-se", "nexusmods", "12345", "1.0.0"))

	files, err := dst.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"main.esp", "optional/patch.esp"}, files)
}
