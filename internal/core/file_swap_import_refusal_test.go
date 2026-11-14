package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #639 fourth review: the plain 2.0 entry is gone (pruned) though "other" still names it for
// 201; "third" deploys 2.0 of 200 from its file-set variant V. default runs
// `lmm import Auctionator-2.0.zip --source msrc --id 42`, which conflicts
// with a mod default already deploys, and is refused without
// --accept-conflicts. The refusal must not delete third's entry.
func TestFileSwap_ImportArchiveRefusalKeepsAnotherProfilesEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	pm := f.svc.NewProfileManager()
	_, err := f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)

	v := core.FileSetVariantForTest("", []string{"200"})
	kv := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0", Variant: v}
	require.NoError(t, gc.Store(kv, "Auctionator/Auctionator-new.lua", []byte("new")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(kv), "200", []string{"Auctionator/Auctionator-new.lua"}))
	require.NoError(t, cache.StampKey(gc.ModPath(kv), kv))
	for _, p := range []struct {
		name, variant string
		ids           []string
	}{{"third", v, []string{"200"}}, {"other", "", []string{"201"}}} {
		_, err := pm.Create(ctx, "g1", p.name)
		require.NoError(t, err)
		require.NoError(t, pm.UpsertMod(ctx, "g1", p.name, domain.ModReference{SourceID: "msrc", ModID: "42", Version: "2.0", FileIDs: p.ids}))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod: domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1", Variant: p.variant}, ProfileName: p.name,
			UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: p.ids,
		}))
	}
	// default deploys another mod over the archive's path.
	seedUpdatableMod(t, f.svc, f.game, "local", "x", "X", "1.0", nil, map[string][]byte{"Auctionator/Auctionator-new.lua": []byte("x")})

	archive := f.archive(t, "Auctionator-2.0.zip")
	opts := core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"}
	plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, opts)
	require.NoError(t, err)
	t.Logf("plan version=%q entryPreExists=%v conflicts=%d", plan.Mod.Version, plan.EntryPreExists, len(plan.Conflicts))
	require.NotEmpty(t, plan.Conflicts)
	res, err := f.svc.ApplyImportArchive(ctx, f.game, "default", plan, opts, nil)
	var ce *core.ConflictError
	require.True(t, errors.As(err, &ce), "err=%v", err)
	if res != nil && res.Mod != nil {
		t.Logf("ingest variant=%q (V=%q)", res.Mod.Variant, v)
	}
	assert.True(t, gc.Exists(kv), "third's entry must survive default's refused import")
	assert.True(t, gc.HasFileIDs(kv, []string{"200"}))
}
