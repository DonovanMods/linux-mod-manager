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

// TestIdentity_DeleteLeavesAnotherKeysEntry: deleting a key whose directory
// belongs to a colliding key removes nothing - uninstalling the plain
// version must not take the variant's files with it.
func TestIdentity_DeleteLeavesAnotherKeysEntry(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store(collidingVariant, "a.pak", []byte("x")))

	require.NoError(t, c.Delete(collidingPlain))
	assert.True(t, c.Exists(collidingVariant))
	assert.FileExists(t, filepath.Join(c.ModPath(collidingVariant), "a.pak"))

	require.NoError(t, c.Delete(collidingVariant))
	assert.NoDirExists(t, c.ModPath(collidingVariant))
}

// TestIdentity_UnreadableMarkerIsNotAMatch: a marker that cannot be parsed
// vouches for no key, so neither colliding key claims the entry.
func TestIdentity_UnreadableMarkerIsNotAMatch(t *testing.T) {
	c := cache.New(t.TempDir())
	dir := c.ModPath(collidingPlain)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.KeyMarkerName), []byte("garbage"), 0o644))

	assert.False(t, c.Exists(collidingPlain))
	assert.False(t, c.Exists(collidingVariant))
	var collision *cache.KeyCollisionError
	assert.True(t, errors.As(c.Claim(collidingPlain), &collision))
}

// TestIdentity_StampKeyRoundTrips: what StampKey writes, CheckKey accepts
// for that key alone - including versions carrying newlines or the
// separator, which a source may publish.
func TestIdentity_StampKeyRoundTrips(t *testing.T) {
	for _, k := range []cache.Key{
		{Version: "1.0"},
		{Version: "1.0", Variant: "abcdef123456"},
		{Version: "1.0@beta"},
		{Version: "odd\nversion \"quoted\""},
		{Version: ""},
	} {
		dir := t.TempDir()
		require.NoError(t, cache.StampKey(dir, k))
		assert.NoError(t, cache.CheckKey(dir, k), "%q", k.Version)
		other := k
		other.Variant += "x"
		assert.Error(t, cache.CheckKey(dir, other), "%q", k.Version)
	}
}
