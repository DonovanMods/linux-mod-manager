package core_test

// #592: the mod list's effective category (the user's, else the source's
// name), the grouping rule both frontends share, SetModCategory and
// ListCategories, and the update check's category-name backfill.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedCategorised installs modID with the source category name given.
func seedCategorised(t *testing.T, svc *core.Service, game *domain.Game, modID, sourceCategory string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: "Mod " + modID, Version: "1.0", GameID: game.ID, CategoryName: sourceCategory},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(ctx, game.ID, "default"); err != nil {
		_, err = pm.Create(ctx, game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
}

func listingsByID(t *testing.T, svc *core.Service, game *domain.Game) map[string]core.ModListing {
	t.Helper()
	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	out := map[string]core.ModListing{}
	for _, m := range list.Mods {
		out[m.ID] = m
	}
	return out
}

func TestListMods_EffectiveCategory(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedCategorised(t, svc, game, "a", "Armour")
	seedCategorised(t, svc, game, "b", "Armour")
	seedCategorised(t, svc, game, "c", "")
	ctx := context.Background()

	_, err := svc.SetModCategory(ctx, "src", "b", game.ID, "default", "Favourites")
	require.NoError(t, err)

	got := listingsByID(t, svc, game)
	assert.Equal(t, "Armour", got["a"].EffectiveCategory)
	assert.Equal(t, core.CategorySourceSource, got["a"].CategorySource)
	assert.Equal(t, "Favourites", got["b"].EffectiveCategory, "the user's category overrides the source's")
	assert.Equal(t, core.CategorySourceUser, got["b"].CategorySource)
	assert.Equal(t, "Armour", got["b"].CategoryName, "the source's name is still there")
	assert.Empty(t, got["c"].EffectiveCategory)
	assert.Empty(t, got["c"].CategorySource)

	cats, err := svc.ListCategories(ctx, game, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"Armour", "Favourites"}, cats)
}

func TestSetModCategory_WritesTheProfileAndClears(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedCategorised(t, svc, game, "a", "Armour")
	ctx := context.Background()

	result, err := svc.SetModCategory(ctx, "src", "a", game.ID, "default", "  Quality of Life  ")
	require.NoError(t, err)
	assert.Equal(t, "Quality of Life", result.EffectiveCategory, "trimmed")
	assert.Equal(t, core.CategorySourceUser, result.CategorySource)
	assert.Equal(t, "a", result.Mod.ID)

	prof, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Quality of Life", prof.FindRef("src", "a").Category)

	result, err = svc.SetModCategory(ctx, "src", "a", game.ID, "default", "   ")
	require.NoError(t, err)
	assert.Equal(t, "Armour", result.EffectiveCategory, "cleared: the source's name again")
	assert.Equal(t, core.CategorySourceSource, result.CategorySource)
	prof, err = svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, prof.FindRef("src", "a").Category)
}

func TestSetModCategory_RejectsInvalidNames(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedCategorised(t, svc, game, "a", "")
	ctx := context.Background()

	for name, category := range map[string]string{
		"too long":     strings.Repeat("é", core.MaxCategoryLength+1),
		"newline":      "UI\nstuff",
		"tab":          "UI\tstuff",
		"control char": "UI\x07",
	} {
		_, err := svc.SetModCategory(ctx, "src", "a", game.ID, "default", category)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, core.ErrInvalidCategory, name)
		var invalid *core.InvalidCategoryError
		assert.True(t, errors.As(err, &invalid), name)
	}
	_, err := svc.SetModCategory(ctx, "src", "a", game.ID, "default", strings.Repeat("é", core.MaxCategoryLength))
	assert.NoError(t, err, "exactly the limit, counted in runes")

	prof, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("é", core.MaxCategoryLength), prof.FindRef("src", "a").Category)
}

func TestSetModCategory_UnknownModIsNotFound(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedCategorised(t, svc, game, "a", "")
	_, err := svc.SetModCategory(context.Background(), "src", "nope", game.ID, "default", "UI")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestUpsertMod_PreservesCategory(t *testing.T) {
	svc, game, _ := newModDetailTestService(t)
	seedCategorised(t, svc, game, "a", "")
	ctx := context.Background()
	_, err := svc.SetModCategory(ctx, "src", "a", game.ID, "default", "UI")
	require.NoError(t, err)

	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "2.0"}))
	prof, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "UI", prof.FindRef("src", "a").Category, "an install/update write never drops it")
	assert.Equal(t, "2.0", prof.FindRef("src", "a").Version)
}

func TestGroupModListByCategory(t *testing.T) {
	l := func(id, cat string) core.ModListing {
		return core.ModListing{InstalledMod: domain.InstalledMod{Mod: domain.Mod{ID: id}}, EffectiveCategory: cat}
	}
	groups := core.GroupModListByCategory([]core.ModListing{
		l("1", "ui"), l("2", ""), l("3", "Armour"), l("4", "UI"), l("5", "ui"), l("6", ""),
	})
	var names []string
	ids := map[string][]string{}
	for _, g := range groups {
		names = append(names, g.Name)
		for _, m := range g.Mods {
			ids[g.Name] = append(ids[g.Name], m.ID)
		}
	}
	assert.Equal(t, []string{"Armour", "UI", "ui", ""}, names, "case-insensitive by name, uncategorised last")
	assert.Equal(t, []string{"1", "5"}, ids["ui"], "load order kept within a group")
	assert.Equal(t, []string{"2", "6"}, ids[""])
	assert.Empty(t, core.GroupModListByCategory(nil))
}

// categorySource reports a fresh mod for every installed one its update
// check sees, as nexusmods and curseforge do, and offers no updates.
type categorySource struct {
	*mockSource
	names map[string]string
}

func (c *categorySource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, inst := range installed {
		source.ReportFetchedMod(ctx, domain.Mod{ID: inst.ID, SourceID: c.id, GameID: inst.GameID, CategoryName: c.names[inst.ID]})
	}
	return nil, nil
}

func TestCheckGameUpdates_BackfillsCategoryNames(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	src := &categorySource{mockSource: newMockSource("src"), names: map[string]string{"a": "Armour", "b": "", "c": "Maps"}}
	svc.RegisterSource(src)
	seedCategorised(t, svc, game, "a", "")
	seedCategorised(t, svc, game, "b", "Weapons")
	seedCategorised(t, svc, game, "c", "Maps")
	ctx := context.Background()
	_, err := svc.SetModCategory(ctx, "src", "a", game.ID, "default", "Mine")
	require.NoError(t, err)
	before, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)

	installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	_, err = svc.CheckGameUpdates(ctx, game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)

	got := listingsByID(t, svc, game)
	assert.Equal(t, "Armour", got["a"].CategoryName, "an empty stored name is backfilled")
	assert.Equal(t, "Weapons", got["b"].CategoryName, "an empty fresh name never erases a stored one")
	assert.Equal(t, "Maps", got["c"].CategoryName)
	assert.Equal(t, "1.0", got["a"].Version)
	after, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, before.Mods, after.Mods, "the profile is untouched")
}

// An applied update records the fresh source category name on the row.
func TestApplyUpdate_RecordsCategoryName(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"10"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &fileVersionDivergesSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1", CategoryName: "Gameplay"})
	mock.AddDownload("20", []byte("new-content"))

	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default", domain.Update{InstalledMod: *old, NewVersion: "2.0"})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "Gameplay", updated.CategoryName)
}

// An install records the source's category name on the new row.
func TestApplyInstall_RecordsCategoryName(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	installCategorisedMod(t, svc, game, nil)

	got, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "Gameplay", got.CategoryName)
}

func installCategorisedMod(t *testing.T, svc *core.Service, game *domain.Game, _ *mockSourceWithDownloads) {
	t.Helper()
	src := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "f1", Name: "File 1", FileName: "mod1-f1.zip", IsPrimary: true}},
	}
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1", CategoryName: "Gameplay"})
	zipBytes, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{"a.esp": "a"}))
	require.NoError(t, err)
	src.AddDownload("f1", zipBytes)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	plan.Files = src.files
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
}

func TestModDetail_CarriesTheEffectiveCategory(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", Name: "Mod a", Version: "1.0", GameID: game.ID, CategoryName: "Armour (fresh)"})
	seedCategorised(t, svc, game, "a", "Armour")
	ctx := context.Background()

	detail, err := svc.ModDetail(ctx, game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, "Armour (fresh)", detail.Mod.CategoryName, "the fetched mod's own name")
	require.NotNil(t, detail.Installed)
	assert.Equal(t, "Armour", detail.Installed.EffectiveCategory, "the stored name, as the list groups it")
	assert.Equal(t, core.CategorySourceSource, detail.Installed.CategorySource)

	_, err = svc.SetModCategory(ctx, "src", "a", game.ID, "default", "Mine")
	require.NoError(t, err)
	detail, err = svc.ModDetail(ctx, game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, "Mine", detail.Installed.EffectiveCategory)
	assert.Equal(t, core.CategorySourceUser, detail.Installed.CategorySource)
}

// A re-link keeps the user's category (it is intent about the mod, not the
// identity) and takes the new identity's source category name, clearing the
// old one when the new source has none.
func TestApplyRelinkMod_KeepsUserCategoryAndRefreshesTheName(t *testing.T) {
	for name, fresh := range map[string]string{"new name": "Maps", "no name": ""} {
		t.Run(name, func(t *testing.T) {
			svc, game, src := newModDetailTestService(t)
			game.SourceIDs["src"] = game.ID
			seedCategorised(t, svc, game, "a", "Armour")
			src.AddMod(game.ID, &domain.Mod{ID: "b", SourceID: "src", Name: "Mod b", Version: "1.0", GameID: game.ID, CategoryName: fresh})
			ctx := context.Background()
			_, err := svc.SetModCategory(ctx, "src", "a", game.ID, "default", "Mine")
			require.NoError(t, err)

			plan, err := svc.PlanRelinkMod(ctx, game, "default", "src", "a", "src", "b")
			require.NoError(t, err)
			_, err = svc.ApplyRelinkMod(ctx, game, plan, core.RelinkOptions{}, nil)
			require.NoError(t, err)

			prof, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
			require.NoError(t, err)
			require.NotNil(t, prof.FindRef("src", "b"))
			assert.Equal(t, "Mine", prof.FindRef("src", "b").Category)
			row, err := svc.GetInstalledMod(ctx, "src", "b", game.ID, "default")
			require.NoError(t, err)
			assert.Equal(t, fresh, row.CategoryName, "the old identity's source name does not survive")
		})
	}
}
