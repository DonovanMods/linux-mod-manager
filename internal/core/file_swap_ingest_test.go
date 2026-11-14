package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #639 third review: "other" holds 2.0 of file 201 (Classic) in the plain
// 2.0 entry; default updates from the hand-downloaded 2.0 archive of file
// 200. The update fills an entry of its own, and other still deploys 201.
func TestFileSwap_UpdateFromFileKeepsOutOfAnotherSetsEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	k := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}
	require.NoError(t, gc.Store(k, "Auctionator/Classic.lua", []byte("classic")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(k), "201", []string{"Auctionator/Classic.lua"}))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1"}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"201"},
	}))

	_, err := f.svc.NewProfileManager().Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, f.svc.NewProfileManager().UpsertMod(ctx, "g1", "other", domain.ModReference{SourceID: "msrc", ModID: "42", Version: "2.0", FileIDs: []string{"201"}}))
	archive := f.archive(t, "Auctionator-2.0.zip")
	plan := f.plan(t, archive, core.UpdateFromArchiveOptions{})
	_, err = f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)
	row := f.row(t)
	t.Logf("default row: version=%q variant=%q files=%v", row.Version, row.Variant, row.FileIDs)
	files, err := gc.ListFiles(k)
	require.NoError(t, err)
	t.Logf("plain 2.0 entry: %v", files)
	assert.Equal(t, []string{"Auctionator/Classic.lua"}, files, "other's 201 tree must not be replaced by 200's")
	sp, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
	require.NoError(t, err)
	t.Logf("switch plan: install=%d", len(sp.ToInstall))
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, sp, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, row.Variant, "200's tree has an entry of its own")
	assert.True(t, deployedFile(t, f.game, "Auctionator/Classic.lua"), "other deploys its own 201 tree")
}

// #639 third review: `lmm import <archive> --source msrc --id 42` in
// default, at 2.0, while "other" deploys 2.0 of file 201 from the plain
// entry: the import fills the archive's own file's entry.
func TestFileSwap_ImportArchiveKeepsOutOfAnotherSetsEntry(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	gc := f.svc.GetGameCache(f.game)
	pm := f.svc.NewProfileManager()
	k := cache.Key{GameID: "g1", SourceID: "msrc", ModID: "42", Version: "2.0"}
	require.NoError(t, gc.Store(k, "Auctionator/Classic.lua", []byte("classic")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(gc.ModPath(k), "201", []string{"Auctionator/Classic.lua"}))
	_, err := pm.Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(ctx, "g1", "other", domain.ModReference{SourceID: "msrc", ModID: "42", Version: "2.0", FileIDs: []string{"201"}}))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1"}, ProfileName: "other",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"201"},
	}))
	// default no longer has 42: uninstall it first.
	_, err = f.svc.UninstallMod(ctx, f.game, "default", "msrc", "42", core.UninstallOptions{KeepCache: true})
	t.Logf("uninstall err: %v", err)
	archive := f.archive(t, "Auctionator-2.0.zip")
	plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"})
	require.NoError(t, err)
	t.Logf("plan version=%q entryPreExists=%v", plan.Mod.Version, plan.EntryPreExists)
	_, err = f.svc.ApplyImportArchive(ctx, f.game, "default", plan, core.ImportArchiveOptions{SourceID: "msrc", ModID: "42"}, nil)
	require.NoError(t, err)
	files, _ := gc.ListFiles(k)
	t.Logf("plain 2.0 entry: %v", files)
	assert.Equal(t, []string{"Auctionator/Classic.lua"}, files)
}
