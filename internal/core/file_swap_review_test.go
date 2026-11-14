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

// #639 review B1: a superset of the files another profile's entry holds
// never grows that entry. default pins {20}; other pins {20,21}; switching
// to other and back deploys only default's file, and default is converged.
func TestFileSwap_SupersetNeverGrowsAnotherProfilesEntry(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}}))

	plan, err := svc.PlanProfileSwitch(ctx, game, "other")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)
	orow, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	t.Logf("other row variant=%q files=%v", orow.Variant, orow.FileIDs)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	files, _ := svc.GetGameCache(game).ListFiles(plain)
	t.Logf("plain entry now: %v", files)

	plan, err = svc.PlanProfileSwitch(ctx, game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "default pins only 20; B must not be deployed")

	files, err = svc.GetGameCache(game).ListFiles(plain)
	require.NoError(t, err)
	assert.Equal(t, []string{"mod1-a.esp"}, files, "default's entry holds only its own file")
	ap, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, ap.NoChanges)
}

// #639 review B2: `lmm install --file 21` into profile other, while
// default's entry holds 20 at the same version, gets an entry of its own.
func TestFileSwap_InstallFileKeepsOutOfAnotherSetsEntry(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	sp, err := svc.PlanProfileSwitch(ctx, game, "other")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(ctx, game, sp, nil)
	require.NoError(t, err)

	plan, err := svc.PlanInstall(ctx, game, "other", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{TargetFileIDs: []string{"21"}}, nil)
	require.NoError(t, err)
	orow, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	t.Logf("other row variant=%q files=%v", orow.Variant, orow.FileIDs)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	files, _ := svc.GetGameCache(game).ListFiles(plain)
	t.Logf("plain entry now: %v", files)
	ofiles, _ := svc.GetGameCache(game).ListFiles(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0", Variant: orow.Variant})
	assert.Equal(t, []string{"mod1-b.esp"}, ofiles, "other's tree holds only B")
	assert.Equal(t, []string{"mod1-a.esp"}, files, "default's entry untouched")
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "other pins only 21")
	// switch back to default: only A
	sp2, err := svc.PlanProfileSwitch(ctx, game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(ctx, game, sp2, nil)
	require.NoError(t, err)
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "default pins only 20")
}

// #639 review B2, reversed: `install --file 20` where another profile's
// entry holds {20,21} is never served from that entry.
func TestFileSwap_InstallSubsetIsNotServedFromASuperset(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	sp, err := svc.PlanProfileSwitch(ctx, game, "other")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(ctx, game, sp, nil)
	require.NoError(t, err)
	plan, err := svc.PlanInstall(ctx, game, "other", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{TargetFileIDs: []string{"20"}}, nil)
	require.NoError(t, err)
	orow, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	ofiles, _ := svc.GetGameCache(game).ListFiles(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0", Variant: orow.Variant})
	assert.Equal(t, []string{"mod1-a.esp"}, ofiles, "other's tree holds only A")
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "other pins only 20")
}

// #639 (owner ruling): an installation upgraded from before markers -
// plain entries with no completion markers, rows and refs pinning their
// files - applies with no changes and downloads nothing; a second profile
// listing the same files reuses the unmarked entry a row pins, and the
// apply backfills its markers.
func TestFileSwap_UnmarkedEntriesOfPinnedRowsNeedNothing(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	src := newSameVersionSwapSource(t)
	svc.RegisterSource(src)
	pm := svc.NewProfileManager()
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(plain, "mod1-a.esp", []byte("a")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"20"},
	}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.True(t, deployedFile(t, game, "mod1-a.esp"))

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "an upgraded install is converged")

	// A second profile pinning the same file: the unmarked entry a row
	// pins exactly is reused, not re-downloaded, and gains its markers.
	_, err = pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	require.NoError(t, pm.SetDefault(ctx, game.ID, "other"))
	plan, err = svc.PlanProfileApply(ctx, game, "other")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.True(t, plan.ToInstall[0].Cached, "the pinned row's unmarked entry")
	manifests, err := svc.GetGameCache(game).FileManifests(plain)
	require.NoError(t, err)
	assert.Empty(t, manifests, "a plan writes nothing")
	_, err = svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Zero(t, src.DownloadCount(), "nothing was downloaded")
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	assert.Empty(t, row.Variant)
	assert.True(t, svc.GetGameCache(game).HasFileIDs(plain, []string{"20"}), "the apply backfilled the markers")

	// Another set against that entry gets a variant: it names no set of
	// its own, and no row pins this one.
	setRefFiles(t, svc, game.ID, "other", "21")
	plan, err = svc.PlanProfileApply(ctx, game, "other")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.NotEmpty(t, plan.ToInstall[0].Mod.Variant)
}
