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

// TestCache_HasFileIDs is the #96 review round 1 finding 2 guard, reworked in
// round 2: the completeness check must be a stronger check than Exists (which
// only checks the version directory's presence, and so wrongly reports
// "cached" for a directory left PARTIALLY populated by a download run that
// broke off partway through a multi-file mod) WITHOUT being archive-blind.
// The round 1 implementation keyed off DownloadableFile.FileName, but the
// default DeployExtract flow stores an archive's EXTRACTED MEMBERS - whose
// names have nothing to do with the archive's own filename - so every
// archive-based mod read as "incomplete" and redownloaded despite a complete
// cache. HasFileIDs instead keys off the per-file completion markers
// MarkFileComplete writes at commit time.
func TestCache_HasFileIDs(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// No directory at all yet.
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}))

	// Directory exists but is completely empty (e.g. MkdirAll ran but no
	// file was ever committed) - still not complete.
	require.NoError(t, os.MkdirAll(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), 0755))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}))

	// A legacy (pre-marker) cache entry - real content, no markers - reads as
	// incomplete: one redundant redownload, which then writes the markers.
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "SomeArchiveMember.esp", []byte("a")))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}),
		"a pre-marker cache entry must read as incomplete")

	// One of two files marked complete - a partial download.
	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1001"))
	assert.True(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001", "1002"}),
		"a partially-populated cache entry must not report having all requested files")

	// Both marked - fully cached, even though NEITHER file ID resembles any
	// on-disk name (the archive-extracted case round 1 got wrong).
	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1002"))
	assert.True(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001", "1002"}))

	// An empty ID list against a nonexistent entry is still false -
	// HasFileIDs is never weaker than Exists.
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "no-such-mod", "1.0.0", nil))

	// An empty ID list against an entry that DOES exist has nothing left to
	// verify beyond Exists itself.
	assert.True(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", nil))

	// A blank or path-bearing file ID can never be verified - the safe
	// direction is false (a redundant re-download, not a silently-trusted
	// gap, and never a marker lookup that escapes the version directory).
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001", ""}))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"../../etc/passwd"}))
}

// TestCache_MarkersAreNeverContent is the #96 round 2 exclusion guard: the
// per-file completion markers MarkFileComplete writes live INSIDE the version
// directory, so every enumerator of that directory must skip them. ListFiles
// is the choke point every deploy/conflict/verify/count path goes through
// (Installer.Install/Replace/Undeploy, DetectConflicts, `lmm verify`'s
// file-count check, DownloadModResult.FilesExtracted, CloneMod); Size is the
// only other walker.
func TestCache_MarkersAreNeverContent(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "real.esp", []byte("12345")))
	sizeBefore, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, int64(5), sizeBefore)

	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1001"))

	files, err := c.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"real.esp"}, files, "markers must never be listed as mod content")

	sizeAfter, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, sizeBefore, sizeAfter, "markers must not be counted toward cache size")

	// A version directory holding ONLY markers still has zero files - `lmm
	// verify` flags "cache dir exists but has 0 files", and a marker must not
	// mask that.
	require.NoError(t, os.MkdirAll(c.ModPath("skyrim-se", "nexusmods", "67890", "2.0.0"), 0755))
	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "67890", "2.0.0"), "1001"))
	empty, err := c.ListFiles("skyrim-se", "nexusmods", "67890", "2.0.0")
	require.NoError(t, err)
	assert.Empty(t, empty, "a marker-only version directory must still count as 0 files")
}

// TestCache_CloneMod_PreservesMarkers is CloneMod's deliberate exception to
// the marker-exclusion rule: it reproduces a cache ENTRY rather than
// enumerating its mod content, and core's reinstall cache transaction
// round-trips a live entry through a staged/snapshot cache and back. Dropping
// the markers there would silently downgrade a complete entry to a
// pre-marker one and cost a redundant redownload.
func TestCache_CloneMod_PreservesMarkers(t *testing.T) {
	src := cache.New(t.TempDir())
	dst := cache.New(t.TempDir())

	require.NoError(t, src.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "real.esp", []byte("data")))
	require.NoError(t, cache.MarkFileComplete(src.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1001"))

	require.NoError(t, src.CloneMod(dst, "skyrim-se", "nexusmods", "12345", "1.0.0"))

	files, err := dst.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"real.esp"}, files, "the clone's CONTENT must still exclude markers")
	assert.True(t, dst.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}),
		"a cloned entry must stay complete - markers travel with the entry")
}

// TestCache_MarkFileComplete_UnverifiableIDs pins MarkFileComplete's refusal
// to write a marker it could never match back: a blank or path-bearing file
// ID is skipped (no error, no file), leaving HasFileIDs to report incomplete
// - the safe direction - rather than writing a marker outside the version
// directory.
func TestCache_MarkFileComplete_UnverifiableIDs(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	modPath := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	require.NoError(t, cache.MarkFileComplete(modPath, ""))
	require.NoError(t, cache.MarkFileComplete(modPath, "../escape"))

	entries, err := os.ReadDir(modPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "an unverifiable file ID must not produce a marker")
	_, err = os.Stat(filepath.Join(filepath.Dir(modPath), ".lmm-file-../escape"))
	assert.Error(t, err)
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
