package core_test

// #639 x #610: a file list (or patches) is a tree input, folded into the
// variant the file-set rule builds on, so a file-set variant of a listed
// tree and the same files' unlisted tree never share a cache entry.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileSwap_AListedTreesFileSetEntryIsNeverTheUnlistedOnes(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	sum := md5.Sum([]byte("b"))
	listed := domain.FileLists{"21": {{Path: "mod1-b.esp", MD5: hex.EncodeToString(sum[:])}}}

	// default: {20,21} unlisted, in the version's plain entry.
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}}))
	applyProfile(t, svc, game, "default")
	// third: {21} with 21's file list - the listed tree's own base entry.
	_, err := pm.Create(ctx, game.ID, "third")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "third", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}, FileList: listed}))
	rrSwitchTo(t, svc, game, "third")
	// other: {20,21} with the same list on 21 - the listed base entry holds
	// {21}, so this set gets its file-set variant of the LISTED tree.
	_, err = pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}, FileList: listed}))
	rrSwitchTo(t, svc, game, "other")

	rows := map[string]*domain.InstalledMod{}
	for _, p := range []string{"default", "third", "other"} {
		row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, p)
		require.NoError(t, err)
		rows[p] = row
	}
	assert.Empty(t, rows["default"].Variant, "the unlisted set keeps the plain entry")
	require.NotEmpty(t, rows["third"].Variant, "a listed tree is a variant (#610)")
	assert.NotEqual(t, rows["third"].Variant, rows["other"].Variant, "another set of the listed tree is another entry")
	assert.NotEqual(t, rows["default"].Variant, rows["other"].Variant, "the listed {20,21} is never the unlisted {20,21}")

	// Each entry holds its own tree, untouched by the others.
	key := func(row *domain.InstalledMod) cache.Key {
		return cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0", Variant: row.Variant}
	}
	c := svc.GetGameCache(game)
	assert.True(t, c.HasFileIDs(key(rows["default"]), []string{"20", "21"}))
	assert.True(t, c.HasFileIDs(key(rows["third"]), []string{"21"}))
	assert.False(t, c.HasFileIDs(key(rows["third"]), []string{"20"}), "third's listed entry never gained 20")
	assert.True(t, c.HasFileIDs(key(rows["other"]), []string{"20", "21"}))

	rrSwitchTo(t, svc, game, "default")
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
}
