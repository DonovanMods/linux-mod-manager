package thunderstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the index INVENTORY (#410): what `lmm source index --all`
// lists and what `lmm source index prune` may delete. Deletion is held to
// the project's fail-closed rule - a directory is removable only when it is
// provably one of this source's indexes and nothing else - and these tests
// are the cases that rule exists for.

var _ source.IndexInventory = (*thunderstore.Source)(nil)

// builtSource is a Source with the fixture indexed under testCommunity.
func builtSource(t *testing.T) (*thunderstore.Source, string, *testClock) {
	t.Helper()
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, clock := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	return src, cacheDir, clock
}

func findIndex(t *testing.T, list []source.CachedIndex, gameID string) source.CachedIndex {
	t.Helper()
	for _, ci := range list {
		if ci.GameID == gameID {
			return ci
		}
	}
	require.Failf(t, "not listed", "no cached index for %s in %+v", gameID, list)
	return source.CachedIndex{}
}

func TestCachedIndexes_ListsWhatIsOnDisk(t *testing.T) {
	src, cacheDir, clock := builtSource(t)

	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1)
	ci := list[0]
	assert.Equal(t, testCommunity, ci.GameID)
	assert.True(t, ci.Present)
	assert.Equal(t, 12, ci.Packages)
	assert.Equal(t, clock.Now().Unix(), ci.FetchedAt.Unix())
	assert.True(t, ci.Removable, ci.Reason)
	assert.Empty(t, ci.Reason)

	var want int64
	entries, err := os.ReadDir(indexDir(cacheDir, testCommunity))
	require.NoError(t, err)
	for _, e := range entries {
		info, err := e.Info()
		require.NoError(t, err)
		want += info.Size()
	}
	assert.Equal(t, want, ci.Bytes, "the footprint is everything in the directory")
}

func TestCachedIndexes_NoCacheRootIsAnEmptyList(t *testing.T) {
	src := thunderstore.New(thunderstore.Options{CacheDir: filepath.Join(t.TempDir(), "never-made"), BaseURL: "http://127.0.0.1:1"})
	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	assert.Empty(t, list)
}

// TestCachedIndexes_AnOldSchemaIsListedAsAbsentButRemovable: an index a
// previous lmm wrote is not usable, still costs its bytes, and is exactly
// what pruning is for.
func TestCachedIndexes_AnOldSchemaIsListedAsAbsentButRemovable(t *testing.T) {
	_, cacheDir, _ := builtSource(t)
	wmPath := filepath.Join(indexDir(cacheDir, testCommunity), "watermark.json")
	require.NoError(t, os.WriteFile(wmPath, []byte(`{"last_modified":"x","fetched_at":1757505600,"packages":12,"schema":2,"generation":"g"}`), 0o644))

	// A fresh process: nothing resident to vouch for the old bytes.
	list, err := thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: "http://127.0.0.1:1"}).CachedIndexes(t.Context())
	require.NoError(t, err)
	ci := findIndex(t, list, testCommunity)
	assert.False(t, ci.Present)
	assert.Equal(t, time.Unix(1757505600, 0).UTC(), ci.FetchedAt, "the age is still known")
	assert.True(t, ci.Removable)
}

// TestCachedIndexes_IgnoresWhatIsNotACommunityDirectory: a name no
// community can have, or a plain file, is not this source's, so it is
// neither listed nor ever a candidate for removal.
func TestCachedIndexes_IgnoresWhatIsNotACommunityDirectory(t *testing.T) {
	src, cacheDir, _ := builtSource(t)
	root := filepath.Join(cacheDir, "_thunderstore")
	require.NoError(t, os.Mkdir(filepath.Join(root, "Not_A_Slug"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray-file"), []byte("x"), 0o644))

	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, testCommunity, list[0].GameID)
}

// TestRemoveIndex_RemovesTheWholeIndexAndReportsWhatItFreed is the ordinary
// case: every file goes, then the directory, and the next search is cold.
func TestRemoveIndex_RemovesTheWholeIndexAndReportsWhatItFreed(t *testing.T) {
	src, cacheDir, _ := builtSource(t)
	// A staging file a crashed build left behind is part of the index too.
	require.NoError(t, os.WriteFile(filepath.Join(indexDir(cacheDir, testCommunity), ".packages-123"), []byte("partial"), 0o600))
	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	before := findIndex(t, list, testCommunity)
	require.True(t, before.Removable, before.Reason)

	freed, err := src.RemoveIndex(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.Equal(t, before.Bytes, freed)
	assert.NoDirExists(t, indexDir(cacheDir, testCommunity))

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.False(t, status.Present)

	// Rebuildable on demand, as a cache must be.
	res, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity})
	require.NoError(t, err)
	assert.Equal(t, 12, res.TotalCount)
}

func TestRemoveIndex_AMissingIndexIsNothingToDo(t *testing.T) {
	src, _, _ := builtSource(t)
	freed, err := src.RemoveIndex(t.Context(), "content-warning")
	require.NoError(t, err)
	assert.Zero(t, freed)
}

func TestRemoveIndex_RefusesAnInvalidSlug(t *testing.T) {
	src, _, _ := builtSource(t)
	for _, slug := range []string{"", "../x", "a/b", "UPPER"} {
		_, err := src.RemoveIndex(t.Context(), slug)
		assert.ErrorIs(t, err, thunderstore.ErrCommunityNotConfigured, "slug %q", slug)
	}
}

// TestRemoveIndex_KeepsADirectoryHoldingAnythingElse: one file lmm did not
// write is enough to make the whole directory not provably an index, and
// NOTHING in it is removed - not even the files that are lmm's.
func TestRemoveIndex_KeepsADirectoryHoldingAnythingElse(t *testing.T) {
	src, cacheDir, _ := builtSource(t)
	dir := indexDir(cacheDir, testCommunity)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644))

	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	ci := findIndex(t, list, testCommunity)
	assert.False(t, ci.Removable)
	assert.Contains(t, ci.Reason, "notes.txt")

	_, err = src.RemoveIndex(t.Context(), testCommunity)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notes.txt")
	for _, name := range []string{"notes.txt", "index.json", "packages.jsonl", "watermark.json"} {
		assert.FileExists(t, filepath.Join(dir, name))
	}
}

// TestRemoveIndex_KeepsASubdirectory: a nested directory is not part of any
// index lmm writes.
func TestRemoveIndex_KeepsASubdirectory(t *testing.T) {
	src, cacheDir, _ := builtSource(t)
	dir := indexDir(cacheDir, testCommunity)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))

	_, err := src.RemoveIndex(t.Context(), testCommunity)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(dir, "index.json"))
}

// TestRemoveIndex_NeverFollowsASymlinkedCommunity: a community entry that
// is a symbolic link points somewhere lmm did not create. It is listed (it
// is in lmm's namespace) and never removed, and neither is its target.
func TestRemoveIndex_NeverFollowsASymlinkedCommunity(t *testing.T) {
	src, cacheDir, _ := builtSource(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "index.json")
	require.NoError(t, os.WriteFile(victim, []byte("someone else's"), 0o644))
	link := indexDir(cacheDir, "content-warning")
	require.NoError(t, os.Symlink(outside, link))

	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	ci := findIndex(t, list, "content-warning")
	assert.False(t, ci.Removable)
	assert.Contains(t, ci.Reason, "symbolic link")

	_, err = src.RemoveIndex(t.Context(), "content-warning")
	require.Error(t, err)
	assert.FileExists(t, victim)
	_, err = os.Lstat(link)
	assert.NoError(t, err, "the link itself is left alone too")
}

// TestRemoveIndex_NeverFollowsASymlinkedFile: the same rule one level down.
func TestRemoveIndex_NeverFollowsASymlinkedFile(t *testing.T) {
	src, cacheDir, _ := builtSource(t)
	dir := indexDir(cacheDir, testCommunity)
	outside := filepath.Join(t.TempDir(), "precious")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, "watermark.json")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "watermark.json")))

	_, err := src.RemoveIndex(t.Context(), testCommunity)
	require.Error(t, err)
	assert.FileExists(t, outside)
	assert.FileExists(t, filepath.Join(dir, "index.json"))
}

// TestRemoveIndex_RefusesASymlinkedIndexRoot is the coordinator's named
// case: when lmm's own _thunderstore root is itself a link, nothing under
// it is provably lmm's, so nothing is listed as removable and nothing is
// removed.
func TestRemoveIndex_RefusesASymlinkedIndexRoot(t *testing.T) {
	sandboxEnv(t)
	cacheDir := t.TempDir()
	real := t.TempDir()
	community := filepath.Join(real, testCommunity)
	require.NoError(t, os.Mkdir(community, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(community, "index.json"), []byte("{}"), 0o644))
	require.NoError(t, os.Symlink(real, filepath.Join(cacheDir, "_thunderstore")))
	src := thunderstore.New(thunderstore.Options{CacheDir: cacheDir, BaseURL: "http://127.0.0.1:1"})

	_, err := src.CachedIndexes(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbolic link")

	_, err = src.RemoveIndex(t.Context(), testCommunity)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(community, "index.json"))
}

// TestRemoveIndex_ASymlinkedCacheDirIsFine: a user who keeps lmm's whole
// cache on another disk through a link is the ordinary case, not a hazard -
// everything below the link is still lmm's own tree.
func TestRemoveIndex_ASymlinkedCacheDirIsFine(t *testing.T) {
	sandboxEnv(t)
	srv := newIndexServer(t, fixtureDocument(t))
	realCache := t.TempDir()
	linked := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.Symlink(realCache, linked))
	src := thunderstore.New(thunderstore.Options{CacheDir: linked, BaseURL: srv.URL})
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)

	list, err := src.CachedIndexes(t.Context())
	require.NoError(t, err)
	require.True(t, findIndex(t, list, testCommunity).Removable)

	_, err = src.RemoveIndex(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(realCache, "_thunderstore", testCommunity))
}
