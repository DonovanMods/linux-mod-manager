package cache_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKey_EmptyVariantIsTheLegacyPath pins F0's (#603) zero-change promise:
// a key with no variant resolves to exactly the path the four loose strings
// did, <base>/<game>/<source>-<mod>/<version>, byte for byte.
func TestKey_EmptyVariantIsTheLegacyPath(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	k := cache.Key{GameID: "skyrim-se", SourceID: "nexusmods", ModID: "12345", Version: "1.0.0"}

	assert.Equal(t, filepath.Join(dir, "skyrim-se", "nexusmods-12345", "1.0.0"), c.ModPath(k))
	assert.Equal(t, filepath.Join(dir, "skyrim-se", "nexusmods-12345", "1.0.0", "a", "b.esp"),
		c.GetFilePath(k, filepath.Join("a", "b.esp")))
}

// TestKey_GameScopedCacheOmitsGameID: a per-game cache_path is already
// game-specific, so the key's game ID never becomes a path segment there -
// with or without a variant.
func TestKey_GameScopedCacheOmitsGameID(t *testing.T) {
	dir := t.TempDir()
	c := cache.NewGameScoped(dir)
	k := cache.Key{GameID: "starrupture", SourceID: "nexusmods", ModID: "35", Version: "1.00"}

	assert.Equal(t, filepath.Join(dir, "nexusmods-35", "1.00"), c.ModPath(k))

	k.Variant = "0123456789ab"
	assert.Equal(t, filepath.Join(dir, "nexusmods-35", "1.00@0123456789ab"), c.ModPath(k))

	require.NoError(t, c.Store(k, "file.pak", []byte("data")))
	assert.FileExists(t, filepath.Join(dir, "nexusmods-35", "1.00@0123456789ab", "file.pak"))
	assert.NoDirExists(t, filepath.Join(dir, "starrupture"), "no <game-id> segment under a cache_path")
}

// TestKey_VariantIsASiblingDirectory: a variant entry lives beside its plain
// version as <version>@<variant>, never inside it, and the two are distinct
// entries.
func TestKey_VariantIsASiblingDirectory(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	plain := cache.Key{GameID: "skyrim-se", SourceID: "nexusmods", ModID: "12345", Version: "1.0.0"}
	variant := plain
	variant.Variant = "0123456789ab"

	got := c.ModPath(variant)
	assert.Equal(t, filepath.Join(dir, "skyrim-se", "nexusmods-12345", "1.0.0@0123456789ab"), got)
	assert.Equal(t, filepath.Dir(c.ModPath(plain)), filepath.Dir(got), "a variant is a sibling of its plain version")

	require.NoError(t, c.Store(variant, "plugin.esp", []byte("v")))
	assert.True(t, c.Exists(variant))
	assert.False(t, c.Exists(plain), "a variant entry is not its plain version's entry")
}

// TestKey_Validate_RefusesVersionSeparator: a version carrying the variant
// separator would spell the same directory as a different version's
// variant ("1.0@ab" vs version "1.0", variant "ab"), so it is refused with a
// typed error.
func TestKey_Validate_RefusesVersionSeparator(t *testing.T) {
	assert.NoError(t, cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0.0"}.Validate())
	assert.NoError(t, cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0.0", Variant: "0123456789ab"}.Validate())

	err := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@beta"}.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, cache.ErrVersionSeparator)
	var sepErr *cache.VersionSeparatorError
	require.True(t, errors.As(err, &sepErr))
	assert.Equal(t, "1.0@beta", sepErr.Version)
	assert.Contains(t, err.Error(), `"1.0@beta"`)
}

// TestCache_NewEntryPath: the path a NEW entry is written at is ModPath's,
// unless the key is one the cache refuses to mint.
func TestCache_NewEntryPath(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "2.0"}

	got, err := c.NewEntryPath(k)
	require.NoError(t, err)
	assert.Equal(t, c.ModPath(k), got)

	k.Version = "2.0@x"
	got, err = c.NewEntryPath(k)
	assert.ErrorIs(t, err, cache.ErrVersionSeparator)
	assert.Empty(t, got)
}

// TestCache_WritesRefuseVersionSeparator: Store and CopyModAs mint entries,
// so they refuse a separator-carrying version and write nothing.
func TestCache_WritesRefuseVersionSeparator(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	bad := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@beta"}

	assert.ErrorIs(t, c.Store(bad, "f.pak", []byte("x")), cache.ErrVersionSeparator)
	assert.NoDirExists(t, c.ModPath(bad))

	// A legacy entry with such a version, made before the refusal existed,
	// is not copied to a new identity either.
	require.NoError(t, os.MkdirAll(c.ModPath(bad), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(c.ModPath(bad), "f.pak"), []byte("x"), 0o644))
	err := c.CopyModAs(context.Background(), bad, "s", "other")
	assert.ErrorIs(t, err, cache.ErrVersionSeparator)
	assert.NoDirExists(t, filepath.Join(dir, "g", "s-other"))
}

// TestCache_ReadsOfALegacySeparatorEntryStillWork: the refusal is about
// minting entries, never reading them - an existing install whose version
// happens to carry the separator keeps resolving, listing and deleting
// exactly as before F0 (#603: no on-disk change for an existing install).
func TestCache_ReadsOfALegacySeparatorEntryStillWork(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	legacy := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@beta"}
	entry := filepath.Join(dir, "g", "s-m", "1.0@beta")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "f.pak"), []byte("x"), 0o644))

	assert.Equal(t, entry, c.ModPath(legacy))
	assert.True(t, c.Exists(legacy))
	files, err := c.ListFiles(legacy)
	require.NoError(t, err)
	assert.Equal(t, []string{"f.pak"}, files)
	size, err := c.Size(legacy)
	require.NoError(t, err)
	assert.Equal(t, int64(1), size)

	snapshot := cache.New(t.TempDir())
	require.NoError(t, c.CloneMod(context.Background(), snapshot, legacy), "a clone reproduces an existing entry")
	assert.True(t, snapshot.Exists(legacy))

	require.NoError(t, c.Delete(legacy))
	assert.NoDirExists(t, entry)
}
