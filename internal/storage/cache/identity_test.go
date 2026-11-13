package cache_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pair TestKey_PlainAndVariantKeysCanSpellOneDirectory pins: one
// directory, two keys (#614, F0's collision comment).
var (
	collidingPlain   = cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@abcdef123456"}
	collidingVariant = cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0", Variant: "abcdef123456"}
)

// TestIdentity_StampedEntryIsItsOwnKeyOnly: a stamped entry exists for its
// own key and for no other key that spells the same directory - in both
// directions, through Exists and HasFileIDs alike, so a cache-first install
// never skips its download because a colliding entry is present.
func TestIdentity_StampedEntryIsItsOwnKeyOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		owner, peer cache.Key
	}{
		{"plain entry, variant reader", collidingPlain, collidingVariant},
		{"variant entry, plain reader", collidingVariant, collidingPlain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cache.New(t.TempDir())
			dir := c.ModPath(tc.owner)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, cache.StampKey(dir, tc.owner))
			require.NoError(t, cache.MarkFileComplete(dir, "f1"))

			assert.True(t, c.Exists(tc.owner))
			assert.True(t, c.HasFileIDs(tc.owner, []string{"f1"}))
			assert.False(t, c.Exists(tc.peer), "a colliding key must not see the entry")
			assert.False(t, c.HasFileIDs(tc.peer, []string{"f1"}), "nor its completion markers")

			var collision *cache.KeyCollisionError
			require.ErrorAs(t, c.Claim(tc.peer), &collision)
			assert.Equal(t, dir, collision.Path)
			assert.Equal(t, tc.peer, collision.Want)
			assert.NoError(t, c.Claim(tc.owner))
		})
	}
}

// TestIdentity_LegacyEntryIsPlain: an entry from before the marker existed
// carries none. No variant entry existed then, so it reads as plain: the
// plain key sees it and may claim it; a variant key refuses it.
func TestIdentity_LegacyEntryIsPlain(t *testing.T) {
	c := cache.New(t.TempDir())
	dir := c.ModPath(collidingPlain)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.pak"), []byte("x"), 0o644))

	assert.True(t, c.Exists(collidingPlain))
	assert.NoError(t, c.Claim(collidingPlain))
	assert.False(t, c.Exists(collidingVariant))
	var collision *cache.KeyCollisionError
	require.ErrorAs(t, c.Claim(collidingVariant), &collision)
	assert.True(t, collision.Legacy)
	assert.Contains(t, collision.Error(), "1.0@abcdef123456")
}

// TestIdentity_ClaimOfAnAbsentEntryIsFree: nothing on disk, nothing to
// collide with.
func TestIdentity_ClaimOfAnAbsentEntryIsFree(t *testing.T) {
	c := cache.New(t.TempDir())
	assert.NoError(t, c.Claim(collidingPlain))
	assert.NoError(t, c.Claim(collidingVariant))
}

// TestIdentity_MarkerIsBookkeeping: the marker is under ReservedPrefix, so
// it is never listed, sized or deployed - and a clone or a copy carries it,
// keeping the copy its key's own.
func TestIdentity_MarkerIsBookkeeping(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store(collidingVariant, "a.pak", []byte("abc")))

	files, err := c.ListFiles(collidingVariant)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.pak"}, files)
	size, err := c.Size(collidingVariant)
	require.NoError(t, err)
	assert.Equal(t, int64(3), size)
	assert.True(t, strings.HasPrefix(cache.KeyMarkerName, cache.ReservedPrefix))

	snapshot := cache.New(t.TempDir())
	require.NoError(t, c.CloneMod(context.Background(), snapshot, collidingVariant))
	assert.True(t, snapshot.Exists(collidingVariant))
	assert.False(t, snapshot.Exists(collidingPlain))

	require.NoError(t, c.CopyModAs(context.Background(), collidingVariant, "s", "other"))
	copied := collidingVariant
	copied.ModID = "other"
	assert.True(t, c.Exists(copied), "the copy keeps the version and variant it was made from")
}

// TestIdentity_StoreClaimsItsEntry: Store (the seeding API) stamps a
// variant entry it creates, and refuses to write into another key's entry.
func TestIdentity_StoreClaimsItsEntry(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store(collidingVariant, "a.pak", []byte("x")))
	assert.True(t, c.Exists(collidingVariant))

	err := c.Store(collidingPlain, "b.pak", []byte("y"))
	var collision *cache.KeyCollisionError
	require.ErrorAs(t, err, &collision)
	assert.NoFileExists(t, filepath.Join(c.ModPath(collidingPlain), "b.pak"))
}

// TestIdentity_DeleteRefusesAnotherKeysEntry: deleting a key whose
// directory belongs to a colliding key removes nothing and says so -
// uninstalling the plain version must not take variant "abc" of version
// "1.0" with it, nor report a removal that did not happen.
func TestIdentity_DeleteRefusesAnotherKeysEntry(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store(collidingVariant, "a.pak", []byte("x")))

	var collision *cache.KeyCollisionError
	require.ErrorAs(t, c.Delete(collidingPlain), &collision)
	assert.True(t, c.Exists(collidingVariant))
	assert.FileExists(t, filepath.Join(c.ModPath(collidingVariant), "a.pak"))

	require.NoError(t, c.Delete(collidingVariant))
	assert.NoDirExists(t, c.ModPath(collidingVariant))
}

// TestIdentity_GarbledMarkerIsLegacy: an empty or unparseable marker (a
// crash mid-write, a hand edit) vouches for no key, so the entry is legacy:
// a plain key still owns it - it exists, a commit may claim it, and a
// delete removes it - while a variant key is refused.
func TestIdentity_GarbledMarkerIsLegacy(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "garbage": "garbage", "half": "lmm cache key 1\nversion \"1.0"} {
		t.Run(name, func(t *testing.T) {
			c := cache.New(t.TempDir())
			dir := c.ModPath(collidingPlain)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, cache.KeyMarkerName), []byte(body), 0o644))

			assert.True(t, c.Exists(collidingPlain))
			assert.NoError(t, c.Claim(collidingPlain))
			assert.False(t, c.Exists(collidingVariant))
			var collision *cache.KeyCollisionError
			assert.True(t, errors.As(c.Claim(collidingVariant), &collision))

			require.NoError(t, c.Delete(collidingPlain))
			assert.NoDirExists(t, dir, "a delete never silently skips a plain entry")
		})
	}
}

// TestIdentity_StaleMarkerIsLegacy: a marker whose key spells another
// directory belongs to an entry renamed after it was stamped (an identity
// rename whose restamp failed, a manual mv). It vouches for no key here:
// the entry is legacy - its plain key owns it, a variant is refused.
func TestIdentity_StaleMarkerIsLegacy(t *testing.T) {
	c := cache.New(t.TempDir())
	old := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"}
	moved := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.1"}
	require.NoError(t, os.MkdirAll(c.ModPath(old), 0o755))
	require.NoError(t, cache.StampKey(c.ModPath(old), old))
	require.NoError(t, os.WriteFile(filepath.Join(c.ModPath(old), "a.pak"), []byte("x"), 0o644))
	require.NoError(t, os.Rename(c.ModPath(old), c.ModPath(moved))) // a manual mv

	assert.True(t, c.Exists(moved), "the moved entry is its directory's plain version")
	assert.NoError(t, c.Claim(moved))
	movedVariant := moved
	movedVariant.Variant = "abcdef123456"
	assert.False(t, c.Exists(movedVariant))

	// A variant entry moved under a plain version's name is stale too.
	v := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "2.0", Variant: "abcdef123456"}
	require.NoError(t, c.Store(v, "b.pak", []byte("y")))
	plain2 := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "2.1"}
	require.NoError(t, os.Rename(c.ModPath(v), c.ModPath(plain2)))
	assert.True(t, c.Exists(plain2))
}

// TestIdentity_StampKeyRoundTrips: what StampKey writes, CheckKey accepts
// for that key in its own directory - including versions carrying quotes or
// the separator, which a source may publish - and leaves no temporary file.
func TestIdentity_StampKeyRoundTrips(t *testing.T) {
	for _, k := range []cache.Key{
		{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"},
		{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0", Variant: "abcdef123456"},
		{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@beta"},
		{GameID: "g", SourceID: "s", ModID: "m", Version: "odd version \"quoted\""},
	} {
		c := cache.New(t.TempDir())
		dir := c.ModPath(k)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, cache.StampKey(dir, k))
		assert.NoError(t, cache.CheckKey(dir, k), "%q", k.Version)
		_, err := os.Stat(filepath.Join(dir, cache.KeyMarkerName+".tmp"))
		assert.True(t, os.IsNotExist(err), "no temporary file is left behind")
	}
	// The colliding pair: each key's current marker refuses the other.
	c := cache.New(t.TempDir())
	dir := c.ModPath(collidingVariant)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, cache.StampKey(dir, collidingVariant))
	assert.Error(t, cache.CheckKey(dir, collidingPlain))
}

// TestIdentity_UnreadableDirectoryKeepsThePlainAnswer: a directory whose
// marker cannot be read at all (no permission) still exists for a plain
// key, exactly as it did before markers existed, so the error surfaces
// where the entry is read rather than as "not cached". A variant key has
// nothing vouching for it and does not see it.
func TestIdentity_UnreadableDirectoryKeepsThePlainAnswer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	c := cache.New(t.TempDir())
	dir := c.ModPath(collidingPlain)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	assert.True(t, c.Exists(collidingPlain))
	assert.False(t, c.Exists(collidingVariant))
}

// TestIdentity_AVersionWithASeparatorIsItsOwnEntry: a free-text version
// containing '/' is spelled as nested directories; its marker still
// matches the directory it sits in, variant included.
func TestIdentity_AVersionWithASeparatorIsItsOwnEntry(t *testing.T) {
	c := cache.New(t.TempDir())
	for _, k := range []cache.Key{
		{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0/beta", Variant: "abcdef123456"},
		{GameID: "g", SourceID: "s", ModID: "m", Version: "2.0/rc"},
	} {
		require.NoError(t, c.Store(k, "a.pak", []byte("x")))
		require.NoError(t, cache.StampKey(c.ModPath(k), k))
		assert.True(t, c.Exists(k), "%+v", k)
		assert.NoError(t, cache.CheckKey(c.ModPath(k), k))
	}
}
