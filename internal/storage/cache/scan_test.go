package cache_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entry writes a committed-looking entry for k: one member, a completion
// marker and the identity marker.
func entry(t *testing.T, c *cache.Cache, k cache.Key, member string, at time.Time) string {
	t.Helper()
	dir := c.ModPath(k)
	require.NoError(t, c.Store(k, member, []byte("content of "+member)))
	require.NoError(t, cache.MarkFileComplete(dir, "f1"))
	require.NoError(t, cache.StampKey(dir, k))
	require.NoError(t, os.Chtimes(dir, at, at))
	return dir
}

// TestScanRoot_FindsEntriesByTheirMarkers (#678): a mod container's
// entries are the directories lmm's markers vouch for, named by the
// identity marker (or, for a legacy entry, by the directory). Anything
// else - an unmarked directory, a commit's backup, lmm's own leftovers, a
// symlink - is listed as unrecognised, never as an entry.
func TestScanRoot_FindsEntriesByTheirMarkers(t *testing.T) {
	base := t.TempDir()
	c := cache.New(base)
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	entry(t, c, cache.Key{GameID: "g", SourceID: "nexusmods", ModID: "1", Version: "1.0"}, "a.txt", t0)
	entry(t, c, cache.Key{GameID: "g", SourceID: "nexusmods", ModID: "1", Version: "2.0", Variant: "abcdef123456"}, "a.txt", t0.Add(time.Hour))
	// A legacy entry: a completion marker, no identity marker.
	legacy := c.ModPath(cache.Key{GameID: "g", SourceID: "nexusmods", ModID: "1", Version: "0.9"})
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "x"), []byte("x"), 0o644))
	require.NoError(t, cache.MarkFileComplete(legacy, "f0"))
	container := filepath.Dir(legacy)
	// Not entries.
	require.NoError(t, os.MkdirAll(filepath.Join(container, "unmarked", "sub"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(container, "1.0.backup"), 0o755))
	require.NoError(t, cache.MarkFileComplete(filepath.Join(container, "1.0.backup"), "f1"))
	require.NoError(t, os.MkdirAll(filepath.Join(container, ".lmm-copy-123"), 0o755))
	require.NoError(t, os.Symlink(legacy, filepath.Join(container, "linked")))

	mods, err := cache.ScanRoot(c.Root("g"))
	require.NoError(t, err)
	require.Len(t, mods, 1)
	m := mods[0]
	assert.Equal(t, "nexusmods-1", m.Dir)
	got := map[string]cache.ScannedEntry{}
	for _, e := range m.Entries {
		got[filepath.Base(e.Path)] = e
	}
	require.Len(t, got, 3)
	assert.Equal(t, "1.0", got["1.0"].Version)
	assert.False(t, got["1.0"].Legacy)
	assert.True(t, got["1.0"].ModTime.Equal(t0))
	assert.Equal(t, "2.0", got["2.0@abcdef123456"].Version)
	assert.Equal(t, "abcdef123456", got["2.0@abcdef123456"].Variant)
	assert.Equal(t, "0.9", got["0.9"].Version)
	assert.True(t, got["0.9"].Legacy)
	assert.ElementsMatch(t, []string{"unmarked", "1.0.backup", "linked"}, m.Unrecognised)
}

// TestScanRoot_MissingRootIsEmpty: a game that never cached anything has
// no root, which is no error.
func TestScanRoot_MissingRootIsEmpty(t *testing.T) {
	mods, err := cache.ScanRoot(filepath.Join(t.TempDir(), "absent"))
	require.NoError(t, err)
	assert.Empty(t, mods)
}

// TestRoot_GameScoped: a per-game cache_path is its own root.
func TestRoot_GameScoped(t *testing.T) {
	base := t.TempDir()
	assert.Equal(t, base, cache.NewGameScoped(base).Root("g"))
	assert.Equal(t, filepath.Join(base, "g"), cache.New(base).Root("g"))
	assert.Equal(t, "src-mod", cache.ModDirName("src", "mod"))
}

// TestDiskUsage_CountsEverythingAndSeesNestedEntries: an entry's usage is
// every byte under it, lmm's bookkeeping and retained sources included,
// and an entry with another entry's identity marker below it says so.
func TestDiskUsage_CountsEverythingAndSeesNestedEntries(t *testing.T) {
	base := t.TempDir()
	c := cache.New(base)
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"}
	dir := entry(t, c, k, "data/a.txt", time.Now())
	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.RetainedSourceName("f1")), make([]byte, 100), 0o644))

	n, nested, err := cache.DiskUsage(dir)
	require.NoError(t, err)
	assert.False(t, nested)
	assert.GreaterOrEqual(t, n, int64(100+len("content of data/a.txt")))

	inner := c.ModPath(cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0/beta"})
	require.NoError(t, os.MkdirAll(inner, 0o755))
	require.NoError(t, cache.StampKey(inner, cache.Key{Version: "1.0/beta"}))
	_, nested, err = cache.DiskUsage(dir)
	require.NoError(t, err)
	assert.True(t, nested)
}

// TestRemoveEntry_TakesTheWholeDirectory: a removed entry is gone at once
// (moved aside, then deleted), and a leftover of an interrupted removal is
// swept by the next scan.
func TestRemoveEntry_TakesTheWholeDirectory(t *testing.T) {
	base := t.TempDir()
	c := cache.New(base)
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"}
	dir := entry(t, c, k, "a.txt", time.Now())
	entry(t, c, cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "2.0"}, "a.txt", time.Now())

	require.NoError(t, cache.RemoveEntry(dir))
	assert.NoDirExists(t, dir)
	assert.False(t, c.Exists(k))

	leftover := filepath.Join(filepath.Dir(dir), ".lmm-prune-1.0-123")
	require.NoError(t, os.MkdirAll(filepath.Join(leftover, "x"), 0o755))
	_, err := cache.ScanRoot(c.Root("g"))
	require.NoError(t, err)
	assert.NoDirExists(t, leftover)
}
