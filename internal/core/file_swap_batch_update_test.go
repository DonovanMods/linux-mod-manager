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

// rrSwitchTo plans and applies a switch to p.
func rrSwitchTo(t *testing.T, svc *core.Service, game *domain.Game, p string) {
	t.Helper()
	sp, err := svc.PlanProfileSwitch(context.Background(), game, p)
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(context.Background(), game, sp, nil)
	require.NoError(t, err)
}

// #639 re-review: the batch (multi-select / dependency) install of
// mod1's primary {20} into other, while default's entry holds {21}, gets
// its own entry.
func TestFileSwap_BatchInstallKeepsOutOfAnotherSetsEntry(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	rrSwitchTo(t, svc, game, "other")

	mod, err := svc.GetMod(ctx, "src", game.ID, "mod1")
	require.NoError(t, err)
	plan, err := svc.PlanInstallMany(ctx, game, "other", []*domain.Mod{mod}, false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	orow, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	t.Logf("other row variant=%q files=%v", orow.Variant, orow.FileIDs)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	files, _ := svc.GetGameCache(game).ListFiles(plain)
	t.Logf("plain entry now: %v", files)
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "other pins 20 only")

	rrSwitchTo(t, svc, game, "default")
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "default pins 21 only")
}

// #639 re-review: a same-version update 20->21 in other, while default
// shares the entry holding {20}, leaves default's tree alone.
func TestFileSwap_SameVersionUpdateLeavesASharedEntryAlone(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	rrSwitchTo(t, svc, game, "other")
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	t.Logf("other row variant=%q", row.Variant)

	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "other", domain.Update{
		InstalledMod: *row, NewVersion: "1.0", FileIDReplacements: map[string]string{"20": "21"},
	})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	row, err = svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	t.Logf("other row after variant=%q files=%v", row.Variant, row.FileIDs)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	files, _ := svc.GetGameCache(game).ListFiles(plain)
	m, _ := svc.GetGameCache(game).FileManifests(plain)
	t.Logf("plain entry now: %v markers %v", files, m)
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "other's update retired 20")

	rrSwitchTo(t, svc, game, "default")
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "default pins 20 only")
	ap, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, ap.NoChanges)
}

// #639 re-review: after a same-version update 20->21, a fresh profile
// pinning {21} deploys only 21.
func TestFileSwap_SameVersionUpdateLeavesNoMixedTree(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", domain.Update{
		InstalledMod: *row, NewVersion: "1.0", FileIDReplacements: map[string]string{"20": "21"},
	})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	row, err = svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	files, _ := svc.GetGameCache(game).ListFiles(plain)
	m, _ := svc.GetGameCache(game).FileManifests(plain)
	t.Logf("row variant=%q files=%v; plain entry now: %v markers %v", row.Variant, row.FileIDs, files, m)
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.False(t, deployedFile(t, game, "mod1-a.esp"))
	// Now a fresh profile pinning {21}: does it reuse a clean tree?
	_, err = pm.Create(ctx, game.ID, "third")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "third", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}}))
	rrSwitchTo(t, svc, game, "third")
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "third pins 21 only")
}

// #639 re-review ruling 5: the install plan's conflict preview reads the
// entry the install will use - the selected files' own - never another
// set's tree that sits at the same version.
func TestFileSwap_PlanInstallPreviewsOnlyItsOwnEntry(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	// "other" is deployed and owns mod1-b.esp.
	seedInstalledMod(t, svc, game, "src", "other", "1.0", true, map[string][]byte{"mod1-b.esp": []byte("o")})
	require.NoError(t, svc.GetInstallerForTest(game).Install(ctx, game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
	// mod1's version entry holds file 21's tree (mod1-b.esp); the install
	// picks the primary, 20.
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(plain, "mod1-b.esp", []byte("b")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(svc.GetGameCache(game).ModPath(plain), "21", []string{"mod1-b.esp"}))

	plan, err := svc.PlanInstall(ctx, game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	require.Equal(t, "20", plan.Files[0].ID)
	assert.Empty(t, plan.Conflicts, "file 21's tree is not what installing 20 deploys")
}
