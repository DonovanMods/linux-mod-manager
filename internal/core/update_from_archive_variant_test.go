package core_test

// #614 review item 1: an update from a file is a plain tree (until the FOMOD
// resolve step says otherwise), so it must not inherit the installed row's
// variant - the ingest wrote the plain entry, and every step after it
// (marking it complete, the replace, the DB update) must read that one.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moveToVariant turns the fixture's installed 1.0 row into a variant row:
// its cache entry moved (and stamped) into <version>@<variant>, the row
// naming the variant.
func moveToVariant(t *testing.T, f *fromFileFixture, variant string) {
	t.Helper()
	c := f.svc.GetGameCache(f.game)
	plain := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "1.0"}
	v := plain
	v.Variant = variant
	require.NoError(t, os.Rename(c.ModPath(plain), c.ModPath(v)))
	require.NoError(t, cache.StampKey(c.ModPath(v), v))
	row := f.row(t)
	row.Variant = variant
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), row))
	require.True(t, c.Exists(v))
}

func TestUpdateFromArchive_FromAVariantRow_InstallsThePlainTree(t *testing.T) {
	f := newFromFileFixture(t)
	moveToVariant(t, f, "aaaaaaaaaaaa")

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)

	row := f.row(t)
	assert.Equal(t, "2.0", row.Version)
	assert.Empty(t, row.Variant, "the file's tree is the plain one")
	assert.Equal(t, "aaaaaaaaaaaa", row.PreviousVariant, "rollback knows the old tree")
	got, err := os.ReadFile(filepath.Join(f.gameDir, "Auctionator", "Auctionator-new.lua"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
	assert.True(t, f.svc.GetGameCache(f.game).HasFileIDs(cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}, []string{"200"}),
		"the matched file is marked complete on the entry the ingest wrote")

	rb, err := f.svc.PlanRollback(context.Background(), f.game, "default", "msrc", "42")
	require.NoError(t, err)
	assert.False(t, rb.CacheMissing)
	_, err = f.svc.ApplyRollback(context.Background(), f.game, rb, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	row = f.row(t)
	assert.Equal(t, "aaaaaaaaaaaa", row.Variant)
	_, err = os.Lstat(filepath.Join(f.gameDir, "Auctionator", "Auctionator-old.lua"))
	require.NoError(t, err, "the variant tree is redeployed")
}
