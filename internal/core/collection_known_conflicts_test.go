package core_test

// #668: a collection's own conflicts are intended. An import marks every
// conflict between two of its mods known (origin collection), a finish
// marks the pairs the mods it installs bring, and a collection update
// recomputes them against the new version and keeps the user's own.

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedZip is a mod archive that ships shared.txt, which every other
// sharedZip ships too, beside a file of its own.
func sharedZip(t *testing.T, id string) []byte {
	t.Helper()
	return createTestZipBytes(t, map[string]string{"shared.txt": "from " + id, "own" + id + ".txt": id})
}

func collectionPair(winner, loser string) domain.KnownConflict {
	return domain.KnownConflict{Winner: "nexusmods:" + winner, Loser: "nexusmods:" + loser, Origin: domain.KnownConflictCollection}
}

func userPair(winner, loser string) domain.KnownConflict {
	return domain.KnownConflict{Winner: "nexusmods:" + winner, Loser: "nexusmods:" + loser, Origin: domain.KnownConflictUser}
}

func TestCollectionKnownConflicts_ImportMarksTheCollectionsOwnPairs(t *testing.T) {
	f := newUpdateFixture(t)
	for _, file := range []string{"100", "110", "120"} {
		f.downloads.AddDownload(file, sharedZip(t, file))
	}
	f.importNexusWithOptional(t)

	saved := f.profile(t)
	want := domain.KnownConflicts{collectionPair("12", "10"), collectionPair("12", "11")}
	assert.Equal(t, want, saved.KnownConflicts, "the last of the collection's mods wins, as the curator ordered")
	require.NotNil(t, saved.CollectionBaseline)
	assert.Equal(t, want, saved.CollectionBaseline.KnownConflicts, "the baseline records what the collection marked")

	conflicts, err := f.svc.GetProfileConflicts(context.Background(), f.game, "coll")
	require.NoError(t, err)
	require.NotEmpty(t, conflicts)
	for _, c := range conflicts {
		assert.True(t, c.Known, "%s is the collection's intended overwrite", c.Path)
	}
}

// importNexusWithOptional is importNexus taking the optional mod 12 too.
func (f *updateFixture) importNexusWithOptional(t *testing.T) {
	t.Helper()
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef", ProfileName: "coll", WithOptional: true,
	})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

// revisionFourShared publishes revision 4 of the shared-file collection:
// mod 11 is dropped, 13 is added before 12, and 10 moves to file 101.
func (f *updateFixture) revisionFourShared(t *testing.T) {
	t.Helper()
	archive := recipeArchive(t, `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "101"},
	{"name": "Thirteen", "modId": "13", "fileId": "130"},
	{"name": "Twelve", "modId": "12", "fileId": "120"}]}`)
	f.archive.Store(&archive)
	c := &f.src.recipe.collection
	c.Revision = 4
	c.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: "101", Name: "Ten", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, ModID: "13", FileID: "130", Name: "Thirteen", FileCategory: "MAIN"},
		{Kind: source.CollectionItemModFile, ModID: "12", FileID: "120", Name: "Twelve", FileCategory: "MAIN"},
	}
}

func TestCollectionKnownConflicts_UpdateRecomputesThemAndKeepsTheUsers(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	for _, file := range []string{"100", "101", "110", "120", "130"} {
		f.downloads.AddDownload(file, sharedZip(t, file))
	}
	f.importNexusWithOptional(t)
	require.Equal(t, domain.KnownConflicts{collectionPair("12", "10"), collectionPair("12", "11")}, f.profile(t).KnownConflicts)

	// The user unmarks one of the collection's pairs, and marks one of
	// their own.
	_, err := f.svc.UnmarkConflictsKnown(ctx, f.game, "coll", []core.ConflictPair{{Winner: "12", Loser: "10"}})
	require.NoError(t, err)
	_, err = f.svc.MarkConflictsKnown(ctx, f.game, "coll", []core.ConflictPair{{Winner: "11", Loser: "10"}})
	require.NoError(t, err)

	f.revisionFourShared(t)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, f.planUpdate(t), core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)

	saved := f.profile(t)
	assert.Equal(t, domain.KnownConflicts{
		// The user's own pair stays.
		userPair("11", "10"),
		// 12 over 10 still holds in revision 4, but the user unmarked it:
		// it stays unmarked. 12 over 13 is the new version's own. 12 over
		// 11 is gone: 11 left the collection, so it is the user's mod now.
		collectionPair("12", "13"),
	}, saved.KnownConflicts)
	assert.Equal(t, domain.KnownConflicts{collectionPair("12", "10"), collectionPair("12", "13")}, saved.CollectionBaseline.KnownConflicts,
		"the baseline is the new version's pairs")
}

func TestCollectionKnownConflicts_UpdateRemovingAMemberDropsItsPairs(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	for _, file := range []string{"100", "101", "110", "120", "130"} {
		f.downloads.AddDownload(file, sharedZip(t, file))
	}
	f.importNexusWithOptional(t)
	_, err := f.svc.MarkConflictsKnown(ctx, f.game, "coll", []core.ConflictPair{{Winner: "11", Loser: "10"}})
	require.NoError(t, err)

	f.revisionFourShared(t)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, f.planUpdate(t), core.CollectionUpdateOptions{Remove: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err)

	saved := f.profile(t)
	require.Nil(t, saved.FindRef("nexusmods", "11"))
	assert.Equal(t, domain.KnownConflicts{collectionPair("12", "10"), collectionPair("12", "13")}, saved.KnownConflicts,
		"the removed mod's pairs go, the user's included")
}

func TestCollectionKnownConflicts_FinishMarksThePairsItsInstallsBring(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	for id, file := range map[string]string{"10": "100", "11": "110", "12": "120"} {
		f.src.AddDownload(file, sharedZip(t, id))
	}
	f.src.urlErrs = map[string]error{"11": errors.New("HTTP 503 from the CDN")}
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{WithOptional: true}), core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{collectionPair("12", "10")}, saved.KnownConflicts, "mod 11 is not installed yet")

	f.src.urlErrs = nil
	_, result, err := f.finish(t, core.CollectionFinishRequest{Retry: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Installed)
	saved, err = config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{collectionPair("12", "10"), collectionPair("12", "11")}, saved.KnownConflicts)
	assert.Equal(t, saved.KnownConflicts, saved.CollectionBaseline.KnownConflicts)
}
