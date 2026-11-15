package core_test

// #657 and C8: a collection update knows what the import decided about a
// gone pin. A substitute is not the user's edit - it stays while the
// curator pins the same gone file, and follows the curator's newer pin. A
// skipped mod is never re-added silently: it stays out while the same
// file is pinned, and a new pin that is available brings it back, listed
// for the user's attention, unless they keep it skipped. And the new
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

// goneTen makes the update fixture's mod 10 pin (file 100) gone - the
// list has no record of it and the listing leaves it out - with 101
// (version 2) its current file and 102 (version 3) served too.
func (f *updateFixture) goneTen(t *testing.T) {
	t.Helper()
	f.src.recipe.collection.Items[0].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"10": {
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
func (f *updateFixture) bumpTo(t *testing.T, tenFile string, tenGone bool) {
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
	defer func() {
		if tenGone {
			c.Items[0].ModID = "" // NexusMods has no record of the file
		}
	}()
	c.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: tenFile, Name: "Ten", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Eleven", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, ModID: "13", FileID: "130", Name: "Thirteen", FileCategory: "MAIN"},
	}
}

func TestCollectionUpdate_ASubstituteStandsWhileTheSameFileIsPinned(t *testing.T) {
	f := newUpdateFixture(t)
	f.goneTen(t)
	f.importChoosing(t, core.GonePinChoices{UseCurrentAll: true})
	require.NotNil(t, f.profile(t).FindRef("nexusmods", "10").SubstitutedFor)
	f.bumpTo(t, "100", true)

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
	f.bumpTo(t, "102", false)

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
	f.bumpTo(t, "100", true)

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

// TestCollectionUpdate_ASkippedModComesBackWhenRePinned (owner ruling,
// 2026-11-15): a skipped mod the new version pins at an available file is
// listed for the user's attention and added by default; keeping it skipped
// is the user's choice, which keeps its record.
func TestCollectionUpdate_ASkippedModComesBackWhenRePinned(t *testing.T) {
	for _, keep := range []bool{false, true} {
		f := newUpdateFixture(t)
		f.goneTen(t)
		f.importChoosing(t, core.GonePinChoices{Skip: []string{"nexusmods:10"}})
		f.bumpTo(t, "102", false)

		req := core.CollectionUpdateRequest{Profile: "coll"}
		if keep {
			req.KeepSkipped = []string{"nexusmods:10"}
		}
		plan, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, req)
		require.NoError(t, err)
		require.Len(t, plan.Reoffered, 1)
		back := plan.Reoffered[0]
		assert.Equal(t, "nexusmods:10", back.Key())
		assert.Equal(t, "3", back.NewVersion)
		assert.Contains(t, back.Note, "previously skipped because its pinned file was gone; the collection now pins file 102 (version 3), which is available")
		assert.Equal(t, keep, back.KeepSkipped)
		require.NotNil(t, plan.Source.Summary)
		assert.Contains(t, plan.Source.Summary.Attention, core.CollectionAttentionItem{ID: "10:102", Reasons: []core.CollectionAttentionReason{core.CollectionAttentionReoffered}},
			"#654's attention group says it comes back")
		assert.NotContains(t, keys(plan.Added), "nexusmods:10", "listed once, for attention")
		assert.NotContains(t, keys(plan.Conflicts), "nexusmods:10")

		result, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
		require.NoError(t, err)
		saved := f.profile(t)
		if keep {
			assert.Nil(t, saved.FindRef("nexusmods", "10"), "kept skipped")
			assert.Len(t, saved.CollectionBaseline.Skipped, 1, "the record stays")
			assert.Equal(t, []string{"nexusmods:10"}, plan.KeepSkipped)
			continue
		}
		assert.Equal(t, 2, result.Added, "13 and the returning 10")
		ten := saved.FindRef("nexusmods", "10")
		require.NotNil(t, ten, "added by default")
		assert.Equal(t, []string{"102"}, ten.FileIDs)
		assert.True(t, ten.FromCollection)
		assert.Empty(t, saved.CollectionBaseline.Skipped, "installed: no longer skipped")
		_, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", "skyrim", "coll")
		require.NoError(t, err)
	}
}

func TestCollectionUpdate_FindsAndDecidesTheNewVersionsGonePins(t *testing.T) {
	f := newUpdateFixture(t)
	f.importNexus(t)
	f.bumpTo(t, "100", false)
	f.src.recipe.collection.Items[2].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"13": {
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
	f.bumpTo(t, "100", false)
	f.src.recipe.collection.Items[2].ModID = ""
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

// publish makes recipe, listed as items, the collection's revision rev.
func (f *updateFixture) publish(t *testing.T, rev int, recipe string, items []source.CollectionItem) {
	t.Helper()
	archive := recipeArchive(t, recipe)
	f.archive.Store(&archive)
	c := &f.src.recipe.collection
	c.Revision, c.Items = rev, items
	f.src.listCalls = nil
}

// tenAt is a recipe pinning mod 10 at file, beside 11.
func tenAt(file string) string {
	return `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "` + file + `"},
	{"name": "Eleven", "modId": "11", "fileId": "110"}]}`
}

func tenItems(file string) []source.CollectionItem {
	return []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: file, Name: "Ten", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Eleven", FileCategory: "MAIN"},
	}
}

// TestCollectionUpdate_KeepSkippedPersists (review): the pin a user keeps
// skipped is recorded, so the next update does not offer it again; a newer
// pin is offered.
func TestCollectionUpdate_KeepSkippedPersists(t *testing.T) {
	f := newUpdateFixture(t)
	f.goneTen(t)
	f.importChoosing(t, core.GonePinChoices{Skip: []string{"nexusmods:10"}})
	f.src.listing["10"] = append(f.src.listing["10"],
		domain.DownloadableFile{ID: "102", FileName: "f102.zip", Version: "3", Category: "MAIN"},
		domain.DownloadableFile{ID: "103", FileName: "f103.zip", Version: "4", Category: "MAIN"})
	f.src.versions["103"] = "4"
	f.downloads.AddDownload("103", createTestZipBytes(t, map[string]string{"file103.txt": "content 103"}))
	ctx := context.Background()

	f.publish(t, 4, tenAt("102"), tenItems("102"))
	plan, err := f.svc.PlanCollectionUpdate(ctx, f.game, core.CollectionUpdateRequest{Profile: "coll", KeepSkipped: []string{"nexusmods:10"}})
	require.NoError(t, err)
	require.Len(t, plan.Reoffered, 1)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, []domain.CollectionSkip{{SourceID: "nexusmods", ModID: "10", Declined: true,
		Pin: domain.CollectionPin{Version: "3", FileIDs: []string{"102"}}}}, f.profile(t).CollectionBaseline.Skipped)

	// The same pin again: not offered, still skipped.
	f.publish(t, 5, tenAt("102"), tenItems("102"))
	plan = f.planUpdate(t)
	assert.Empty(t, plan.Reoffered, "the declined pin is not offered again")
	require.Equal(t, []string{"nexusmods:10"}, keys(plan.Skipped))
	assert.Contains(t, plan.Skipped[0].Note, "you kept it skipped")
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Nil(t, f.profile(t).FindRef("nexusmods", "10"))

	// A newer pin: offered, and added by default.
	f.publish(t, 6, tenAt("103"), tenItems("103"))
	plan = f.planUpdate(t)
	require.Equal(t, []string{"nexusmods:10"}, keys(plan.Reoffered))
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	ten := f.profile(t).FindRef("nexusmods", "10")
	require.NotNil(t, ten)
	assert.Equal(t, []string{"103"}, ten.FileIDs)
	assert.Empty(t, f.profile(t).CollectionBaseline.Skipped)
}

// TestCollectionUpdate_AConflictsGonePinIsDecidedOnlyWhenOverridden
// (review): an entry the user owns keeps their version unless the apply
// overrides the conflict, so its new pin being gone needs a decision only
// then.
func TestCollectionUpdate_AConflictsGonePinIsDecidedOnlyWhenOverridden(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importNexus(t)
	_, err := f.svc.SetModLock(ctx, "nexusmods", "11", "skyrim", "coll", "")
	require.NoError(t, err)
	// Revision 4 pins 11 at 111, which NexusMods no longer offers; 112 is
	// its main file now.
	f.publish(t, 4, `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "100"},
	{"name": "Eleven", "modId": "11", "fileId": "111"}]}`, []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, FileID: "111"},
	})
	f.src.listing = map[string][]domain.DownloadableFile{"11": {
		{ID: "112", FileName: "f112.zip", Name: "Eleven 3", Version: "3", Category: "MAIN", IsPrimary: true}}}
	f.src.versions["112"] = "3"
	f.downloads.AddDownload("112", createTestZipBytes(t, map[string]string{"file112.txt": "content 112"}))

	plan := f.planUpdate(t)
	require.Contains(t, keys(plan.Conflicts), "nexusmods:11")
	require.Len(t, plan.Source.GonePins, 1)
	assert.True(t, plan.Source.GonePins[0].Conflict)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Override: []string{"nexusmods:11"}}, nil)
	require.True(t, core.IsCollectionGonePins(err), "overriding needs the decision: %v", err)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err, "the user's version wins: nothing to decide")
	assert.Equal(t, []string{"110"}, f.profile(t).FindRef("nexusmods", "11").FileIDs)

	f.publish(t, 5, `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "100"},
	{"name": "Eleven", "modId": "11", "fileId": "111"}]}`, []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, FileID: "111"},
	})
	plan, err = f.svc.PlanCollectionUpdate(ctx, f.game, core.CollectionUpdateRequest{Profile: "coll",
		GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:11"}}})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Override: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"112"}, f.profile(t).FindRef("nexusmods", "11").FileIDs)
}
