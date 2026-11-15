package core_test

// #657 and C8: a collection update knows what the import decided about a
// gone pin. A substitute is not the user's edit - it stays while the
// curator pins the same gone file, and follows the curator's newer pin. A
// skipped mod is never re-added silently: it stays out while the same
// file is pinned, and a new pin is offered as a conflict. And the new
// version's own gone pins are found and decided as an import's are.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goneTen makes the update fixture's mod 10 pin (file 100) archived, with
// 101 (version 2) its current file and 102 (version 3) served too.
func (f *updateFixture) goneTen(t *testing.T) {
	t.Helper()
	f.src.recipe.collection.Items[0].FileCategory = "ARCHIVED"
	f.src.listing = map[string][]domain.DownloadableFile{"10": {
		{ID: "100", FileName: "f100.zip", Version: "1", Category: "ARCHIVED"},
		{ID: "101", FileName: "f101.zip", Name: "Ten 2", Version: "2", Category: "MAIN", IsPrimary: true},
	}}
	f.src.versions["102"] = "3"
	f.downloads.AddDownload("102", createTestZipBytes(t, map[string]string{"file102.txt": "content 102"}))
}

func (f *updateFixture) importChoosing(t *testing.T, choices core.GonePinChoices) {
	t.Helper()
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef", ProfileName: "coll", GonePins: choices,
	})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

// bumpTo publishes revision 4 pinning tenFile for mod 10, beside 11 and
// the new 13.
func (f *updateFixture) bumpTo(t *testing.T, tenFile, tenCategory string) {
	t.Helper()
	archive := recipeArchive(t, `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "`+tenFile+`"},
	{"name": "Eleven", "modId": "11", "fileId": "110"},
	{"name": "Thirteen", "modId": "13", "fileId": "130"}]}`)
	f.archive.Store(&archive)
	c := &f.src.recipe.collection
	c.Revision = 4
	if tenFile == "102" && f.src.listing != nil {
		f.src.listing["10"] = append(f.src.listing["10"], domain.DownloadableFile{ID: "102", FileName: "f102.zip", Version: "3", Category: "MAIN"})
	}
	f.src.listCalls = nil
	c.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: tenFile, Name: "Ten", FileCategory: tenCategory},
		{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Eleven"},
		{Kind: source.CollectionItemModFile, ModID: "13", FileID: "130", Name: "Thirteen"},
	}
}

func TestCollectionUpdate_ASubstituteStandsWhileTheSameFileIsPinned(t *testing.T) {
	f := newUpdateFixture(t)
	f.goneTen(t)
	f.importChoosing(t, core.GonePinChoices{UseCurrentAll: true})
	require.NotNil(t, f.profile(t).FindRef("nexusmods", "10").SubstitutedFor)
	f.bumpTo(t, "100", "ARCHIVED")

	plan := f.planUpdate(t)
	assert.NotContains(t, keys(plan.Changed), "nexusmods:10", "the substitute is not the user's edit, and the pin did not move")
	assert.NotContains(t, keys(plan.Conflicts), "nexusmods:10")
	assert.Empty(t, plan.Source.GonePins, "nothing to ask: the substitute stands")
	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	ten := f.profile(t).FindRef("nexusmods", "10")
	assert.Equal(t, []string{"101"}, ten.FileIDs)
	assert.Equal(t, &domain.CollectionPin{Version: "1", FileIDs: []string{"100"}}, ten.SubstitutedFor)
	assert.True(t, ten.FromCollection)
}

func TestCollectionUpdate_ASubstituteFollowsTheCuratorsNewerPin(t *testing.T) {
	f := newUpdateFixture(t)
	f.goneTen(t)
	f.importChoosing(t, core.GonePinChoices{UseCurrentAll: true})
	f.bumpTo(t, "102", "MAIN")

	plan := f.planUpdate(t)
	require.Contains(t, keys(plan.Changed), "nexusmods:10", "the curator's newer pin is offered")
	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	ten := f.profile(t).FindRef("nexusmods", "10")
	assert.Equal(t, []string{"102"}, ten.FileIDs)
	assert.Equal(t, "3", ten.Version)
	assert.Nil(t, ten.SubstitutedFor, "the collection's own pin again")
}

func TestCollectionUpdate_ASkippedModStaysOutWhileTheSameFileIsPinned(t *testing.T) {
	f := newUpdateFixture(t)
	f.goneTen(t)
	f.importChoosing(t, core.GonePinChoices{Skip: []string{"nexusmods:10"}})
	f.bumpTo(t, "100", "ARCHIVED")

	plan := f.planUpdate(t)
	assert.Equal(t, []string{"nexusmods:10"}, keys(plan.Skipped))
	assert.NotContains(t, keys(plan.Added), "nexusmods:10", "never re-added silently")
	assert.Empty(t, plan.Source.GonePins, "the user already decided")
	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	saved := f.profile(t)
	assert.Nil(t, saved.FindRef("nexusmods", "10"))
	require.Len(t, saved.CollectionBaseline.Skipped, 1, "the record is kept")
}

func TestCollectionUpdate_ASkippedModsNewPinIsAConflict(t *testing.T) {
	for _, override := range []bool{false, true} {
		f := newUpdateFixture(t)
		f.goneTen(t)
		f.importChoosing(t, core.GonePinChoices{Skip: []string{"nexusmods:10"}})
		f.bumpTo(t, "102", "MAIN")

		plan := f.planUpdate(t)
		require.Contains(t, keys(plan.Conflicts), "nexusmods:10")
		assert.NotContains(t, keys(plan.Added), "nexusmods:10")
		for _, c := range plan.Conflicts {
			if c.Key() == "nexusmods:10" {
				assert.Contains(t, c.Note, "skipped")
				assert.Equal(t, "3", c.NewVersion)
			}
		}
		opts := core.CollectionUpdateOptions{}
		if override {
			opts.Override = []string{"nexusmods:10"}
		}
		_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, opts, nil)
		require.NoError(t, err)
		saved := f.profile(t)
		if !override {
			assert.Nil(t, saved.FindRef("nexusmods", "10"), "kept out unless overridden")
			assert.Len(t, saved.CollectionBaseline.Skipped, 1)
			continue
		}
		ten := saved.FindRef("nexusmods", "10")
		require.NotNil(t, ten, "overridden: added at the new pin")
		assert.Equal(t, []string{"102"}, ten.FileIDs)
		assert.True(t, ten.FromCollection)
		assert.Empty(t, saved.CollectionBaseline.Skipped, "no longer skipped")
	}
}

func TestCollectionUpdate_FindsAndDecidesTheNewVersionsGonePins(t *testing.T) {
	f := newUpdateFixture(t)
	f.importNexus(t)
	f.bumpTo(t, "100", "")
	f.src.recipe.collection.Items[2].FileCategory = "ARCHIVED"
	f.src.listing = map[string][]domain.DownloadableFile{"13": {
		{ID: "130", FileName: "f130.zip", Version: "1", Category: "ARCHIVED"},
		{ID: "131", FileName: "f131.zip", Name: "Thirteen 2", Version: "2", Category: "MAIN", IsPrimary: true},
	}}
	f.downloads.AddDownload("131", createTestZipBytes(t, map[string]string{"file131.txt": "content 131"}))

	plan := f.planUpdate(t)
	require.Len(t, plan.Source.GonePins, 1)
	assert.Equal(t, "nexusmods:13", plan.Source.GonePins[0].Key())
	assert.Equal(t, 1, f.src.listCalls["13"])
	assert.Zero(t, f.src.listCalls["10"], "an unchanged pin needs no check")
	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.True(t, core.IsCollectionGonePins(err), "got %v", err)

	plan, err = f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{
		Profile: "coll", GonePins: core.GonePinChoices{Skip: []string{"nexusmods:13"}}})
	require.NoError(t, err)
	assert.NotContains(t, keys(plan.Added), "nexusmods:13")
	_, err = f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	saved := f.profile(t)
	assert.Nil(t, saved.FindRef("nexusmods", "13"))
	assert.Equal(t, []domain.CollectionSkip{{SourceID: "nexusmods", ModID: "13", Pin: domain.CollectionPin{Version: "1", FileIDs: []string{"130"}}}},
		saved.CollectionBaseline.Skipped)
}

func TestCollectionUpdate_UseCurrentForAnAddedMod(t *testing.T) {
	f := newUpdateFixture(t)
	f.importNexus(t)
	f.bumpTo(t, "100", "")
	f.src.recipe.collection.Items[2].FileCategory = "ARCHIVED"
	f.src.listing = map[string][]domain.DownloadableFile{"13": {
		{ID: "131", FileName: "f131.zip", Name: "Thirteen 2", Version: "2", Category: "MAIN", IsPrimary: true},
	}}
	f.downloads.AddDownload("131", createTestZipBytes(t, map[string]string{"file131.txt": "content 131"}))

	plan, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{
		Profile: "coll", GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:13"}}})
	require.NoError(t, err)
	require.Contains(t, keys(plan.Added), "nexusmods:13")
	_, err = f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	thirteen := f.profile(t).FindRef("nexusmods", "13")
	require.NotNil(t, thirteen)
	assert.Equal(t, []string{"131"}, thirteen.FileIDs)
	assert.Equal(t, &domain.CollectionPin{Version: "1", FileIDs: []string{"130"}}, thirteen.SubstitutedFor)
}
