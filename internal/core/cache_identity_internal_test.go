package core

// #614: the cache-key collision guard at the commit choke point. A plain
// version "1.0@abcdef123456" and variant "abcdef123456" of version "1.0"
// name one directory (cache.Key's spelling is not injective, F0 #603);
// commitStagedCache refuses to replace the other key's entry, in both
// directions, and stamps every entry it commits with its own identity.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	collidePlain   = cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0@abcdef123456"}
	collideVariant = cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0", Variant: "abcdef123456"}
)

// stageFor writes a staging directory for c's entry k holding name.
func stageFor(t *testing.T, c *cache.Cache, k cache.Key, name string) (cachePath, stagePath string) {
	t.Helper()
	cachePath = c.ModPath(k)
	stagePath = cachePath + ".staging"
	require.NoError(t, os.MkdirAll(stagePath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stagePath, name), []byte(name), 0o644))
	return cachePath, stagePath
}

func TestCommitStagedCache_RefusesAnotherKeysEntry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		owner, writer cache.Key
	}{
		{"plain over a variant", collideVariant, collidePlain},
		{"variant over a plain", collidePlain, collideVariant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cache.New(t.TempDir())
			cachePath, stagePath := stageFor(t, c, tc.owner, "owner.esp")
			require.NoError(t, commitStagedCache(tc.owner, cachePath, stagePath))
			require.True(t, c.Exists(tc.owner))

			_, stagePath = stageFor(t, c, tc.writer, "writer.esp")
			err := commitStagedCache(tc.writer, cachePath, stagePath)
			var collision *cache.KeyCollisionError
			require.True(t, errors.As(err, &collision), "got %v", err)
			assert.FileExists(t, filepath.Join(cachePath, "owner.esp"), "the owner's entry is untouched")
			assert.NoFileExists(t, filepath.Join(cachePath, "writer.esp"))
			assert.True(t, c.Exists(tc.owner))
			assert.False(t, c.Exists(tc.writer))
		})
	}
}

// TestCommitStagedCache_LegacyEntries: an entry from before the marker is
// plain - the plain key commits over it and stamps it; a variant refuses it.
func TestCommitStagedCache_LegacyEntries(t *testing.T) {
	c := cache.New(t.TempDir())
	legacy := c.ModPath(collidePlain)
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "old.esp"), []byte("x"), 0o644))

	_, stagePath := stageFor(t, c, collideVariant, "v.esp")
	var collision *cache.KeyCollisionError
	require.True(t, errors.As(commitStagedCache(collideVariant, legacy, stagePath), &collision))
	assert.True(t, collision.Legacy)

	_, stagePath = stageFor(t, c, collidePlain, "new.esp")
	require.NoError(t, commitStagedCache(collidePlain, legacy, stagePath))
	assert.NoError(t, cache.CheckKey(legacy, collidePlain))
	assert.FileExists(t, filepath.Join(legacy, cache.KeyMarkerName), "the plain commit stamps the entry it adopts")
	assert.False(t, c.Exists(collideVariant))
}

// TestCommitStagedCache_StampsItsOwnKey: an ordinary plain commit and a
// variant one each leave the marker naming their key, and a re-commit of
// the same key is allowed.
func TestCommitStagedCache_StampsItsOwnKey(t *testing.T) {
	for _, k := range []cache.Key{
		{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"},
		collideVariant,
	} {
		c := cache.New(t.TempDir())
		cachePath, stagePath := stageFor(t, c, k, "a.esp")
		require.NoError(t, commitStagedCache(k, cachePath, stagePath))
		assert.NoError(t, cache.CheckKey(cachePath, k))
		_, stagePath = stageFor(t, c, k, "b.esp")
		require.NoError(t, commitStagedCache(k, cachePath, stagePath))
		assert.True(t, c.Exists(k))
		files, err := c.ListFiles(k)
		require.NoError(t, err)
		assert.Equal(t, []string{"b.esp"}, files, "the marker is bookkeeping, never a listed file")
	}
}
