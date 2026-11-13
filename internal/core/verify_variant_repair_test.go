package core_test

// #614 review items 2, 3 and 7: verify --fix's version-record repair renames
// the row's cache entry to the effective version. The rename is the row's
// own tree only: a sibling profile with another tree of the version is not
// touched, and the moved entry's identity marker names its new key.

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mod1Key(game *domain.Game, version, variant string) cache.Key {
	return cache.Key{GameID: game.ID, SourceID: "test-src", ModID: "mod1", Version: version, Variant: variant}
}

func TestVerify_Fix_VersionMismatch_SiblingWithAnotherVariantIsNotRewritten(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	gameCache := svc.GetGameCache(game)
	ctx := context.Background()
	const other = "bbbbbbbbbbbb"
	require.NoError(t, gameCache.Store(mod1Key(game, "1.5", other), "2", []byte("other tree")))
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	row, err := svc.GetInstalledMod(ctx, "test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	row.Variant = other
	require.NoError(t, svc.SaveInstalledMod(ctx, row))

	runVersionRepairFix(t, svc, game)

	assert.Equal(t, "1.0", installedVersion(t, svc, game, "default"), "the row itself is repaired")
	assert.Equal(t, "1.5", installedVersion(t, svc, game, "second"),
		"a sibling with another tree lives in a directory the rename did not move")
	assert.True(t, gameCache.Exists(mod1Key(game, "1.5", other)))
	assert.False(t, gameCache.Exists(mod1Key(game, "1.0", other)))
	p, err := svc.NewProfileManager().Get(ctx, game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.5", p.FindRef("test-src", "mod1").Version, "nor is its profile ref")
}

// TestVerify_Fix_VersionMismatch_RenameRestampsTheEntry: a stamped plain
// entry renamed to the effective version carries a marker naming its NEW
// key, so it exists for the repaired row.
func TestVerify_Fix_VersionMismatch_RenameRestampsTheEntry(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	gameCache := svc.GetGameCache(game)
	require.NoError(t, cache.StampKey(gameCache.ModPath(mod1Key(game, "1.5", "")), mod1Key(game, "1.5", "")))

	runVersionRepairFix(t, svc, game)

	assert.Equal(t, "1.0", installedVersion(t, svc, game, "default"))
	assert.True(t, gameCache.Exists(mod1Key(game, "1.0", "")))
	assert.NoError(t, cache.CheckKey(gameCache.ModPath(mod1Key(game, "1.0", "")), mod1Key(game, "1.0", "")))
}

// TestVerify_Fix_VersionMismatch_StampFailureRollsTheRenameBack: when the
// moved entry cannot be restamped, the rename is undone and nothing is
// recorded, so the row, the profile and the cache still agree.
func TestVerify_Fix_VersionMismatch_StampFailureRollsTheRenameBack(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	gameCache := svc.GetGameCache(game)
	old := gameCache.ModPath(mod1Key(game, "1.5", ""))
	require.NoError(t, cache.StampKey(old, mod1Key(game, "1.5", "")))
	chmodForTest(t, old, 0o555) // the marker cannot be rewritten inside it

	result, _ := runVersionRepairFix(t, svc, game)

	require.NoError(t, os.Chmod(old, 0o755))
	assert.Equal(t, "version_mismatch", mismatchFinding(t, result.Findings).Status)
	assert.Equal(t, "1.5", installedVersion(t, svc, game, "default"), "no DB write")
	assert.True(t, gameCache.Exists(mod1Key(game, "1.5", "")), "the entry is back where the row says")
	assert.False(t, gameCache.Exists(mod1Key(game, "1.0", "")))
}
