package cache_test

import (
	"context"
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

// TestKey_SeparatorInAVersionIsAnOrdinaryVersion: the cache never refuses
// a version, even one carrying VariantSeparator - a version a source
// publishes stays installable (#603). Such an entry is written, read,
// copied, cloned and deleted like any other.
func TestKey_SeparatorInAVersionIsAnOrdinaryVersion(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@beta"}
	entry := filepath.Join(dir, "g", "s-m", "1.0@beta")

	require.NoError(t, c.Store(k, "f.pak", []byte("x")))
	assert.Equal(t, entry, c.ModPath(k))
	assert.True(t, c.Exists(k))
	files, err := c.ListFiles(k)
	require.NoError(t, err)
	assert.Equal(t, []string{"f.pak"}, files)
	size, err := c.Size(k)
	require.NoError(t, err)
	assert.Equal(t, int64(1), size)

	require.NoError(t, c.CopyModAs(context.Background(), k, "s", "other"))
	assert.FileExists(t, filepath.Join(dir, "g", "s-other", "1.0@beta", "f.pak"))

	snapshot := cache.New(t.TempDir())
	require.NoError(t, c.CloneMod(context.Background(), snapshot, k))
	assert.True(t, snapshot.Exists(k))

	require.NoError(t, c.Delete(k))
	assert.NoDirExists(t, entry)
}

// TestKey_PlainAndVariantKeysCanSpellOneDirectory pins the collision
// Key's doc comment hands to the variant minter (F2, #614): a plain version
// that happens to end in "@<variant>" names the same directory as that
// variant of the shorter version. The cache does not arbitrate; if this
// test starts failing, the spelling changed and existing entries moved.
func TestKey_PlainAndVariantKeysCanSpellOneDirectory(t *testing.T) {
	c := cache.New(t.TempDir())
	plain := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@abcdef123456"}
	variant := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0", Variant: "abcdef123456"}
	assert.Equal(t, c.ModPath(plain), c.ModPath(variant))
}
