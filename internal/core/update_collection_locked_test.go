package core_test

// #667: a collection is the update unit. A mod whose profile ref is locked
// AND from_collection is never sent to its source's update check - the
// collection decides its version, and `lmm profile sync --collection` moves
// it - and is counted in the report's skipped.locked instead. A lock the
// user set by hand (no from_collection) is checked as before: the update is
// reported with Locked set. Skip all / Unskip all are #542's skip, in bulk.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCollectionLockedService is newSkipTestService plus a third mod and a
// collection profile: mod1 is locked by the collection, mod2 is locked by
// hand, mod3 is neither.
func newCollectionLockedService(t *testing.T, origin *domain.CollectionOrigin) (*core.Service, *domain.Game, *skipTestSource) {
	t.Helper()
	svc, game, src := newSkipTestService(t)
	seedUpdatableMod(t, svc, game, "src", "mod3", "Mod Three", "1.0", []string{"old-3"},
		map[string][]byte{"mod3-old.esp": []byte("old-content")})
	src.AddMod("g1", &domain.Mod{ID: "mod3", SourceID: "src", Name: "Mod Three", Version: "2.0", GameID: "g1"})

	pm := svc.NewProfileManager()
	prof, err := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 3, Title: "Night City", Origin: origin}
	for i := range prof.Mods {
		switch prof.Mods[i].ModID {
		case "mod1":
			prof.Mods[i].Locked, prof.Mods[i].FromCollection = true, true
		case "mod2":
			prof.Mods[i].Locked = true
		}
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))
	src.asked = nil
	return svc, game, src
}

func TestCheckGameUpdateReport_CollectionLockedModIsNeverChecked(t *testing.T) {
	svc, game, src := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123", Revision: 3})

	report := skipReport(t, svc, game)

	assert.NotContains(t, src.asked, "mod1", "a collection-locked mod never reaches the source's update call")
	assert.Contains(t, src.asked, "mod2", "a lock set by hand is still checked")
	assert.Contains(t, src.asked, "mod3")

	ids := make([]string, 0, len(report.Updates))
	for _, u := range report.Updates {
		ids = append(ids, u.InstalledMod.ID)
		if u.InstalledMod.ID == "mod2" {
			assert.True(t, u.Locked, "a hand lock is reported with the update, as before")
		}
	}
	assert.ElementsMatch(t, []string{"mod2", "mod3"}, ids)

	assert.Equal(t, 1, report.Skipped.Locked)
	assert.Equal(t, "Night City", report.Skipped.Collection)
	assert.True(t, report.Skipped.CollectionSync, "the collection has an origin `profile sync --collection` updates from")
	assert.Equal(t, 1, report.Skipped.Total(), "a collection-locked mod was not checked")

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	for _, u := range updates {
		assert.NotEqual(t, "mod1", u.InstalledMod.ID)
	}
}

func TestCheckGameUpdateReport_CollectionWithoutOriginIsNotSyncable(t *testing.T) {
	svc, game, _ := newCollectionLockedService(t, nil)

	report := skipReport(t, svc, game)
	assert.Equal(t, 1, report.Skipped.Locked)
	assert.Equal(t, "Night City", report.Skipped.Collection)
	assert.False(t, report.Skipped.CollectionSync, "a collection imported from a file has nothing to sync from")
}

func TestPlanUpdateBatch_LeavesCollectionLockedModsOut(t *testing.T) {
	svc, game, src := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})

	plan, err := svc.PlanUpdateBatch(context.Background(), game, "default", nil)
	require.NoError(t, err)
	for _, u := range plan.Updates {
		assert.NotEqual(t, "mod1", u.InstalledMod.ID)
	}
	assert.NotContains(t, src.asked, "mod1")
}

func TestPlanUpdate_ExplicitCollectionLockedModIsStillChecked(t *testing.T) {
	// `lmm update <id>` names the mod: one request, and the answer is the
	// lock's refusal naming the update, not a false "up to date".
	svc, game, src := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan.Update)
	assert.True(t, plan.Locked)
	assert.Contains(t, src.asked, "mod1")
}

func TestClearModLock_ReturnsACollectionModToUpdateChecks(t *testing.T) {
	svc, game, src := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})

	result, err := svc.ClearModLock(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, result.LeftCollection, "the unlock says the collection no longer manages the mod")
	hand, err := svc.ClearModLock(context.Background(), "src", "mod2", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, hand.LeftCollection, "a hand lock leaves no collection")
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	ref := prof.FindRef("src", "mod1")
	require.NotNil(t, ref)
	assert.False(t, ref.FromCollection, "unlocking makes the entry the user's")

	report := skipReport(t, svc, game)
	assert.Contains(t, src.asked, "mod1")
	assert.Zero(t, report.Skipped.Locked)
	assert.Empty(t, report.Skipped.Collection)
	found := false
	for _, u := range report.Updates {
		found = found || u.InstalledMod.ID == "mod1"
	}
	assert.True(t, found, "the unlocked mod's update is offered")
}

func TestSkipAllUpdates_SkipsEveryOfferedUpdate(t *testing.T) {
	svc, game, _ := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})

	result, err := svc.SkipAllUpdates(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Equal(t, game.ID, result.GameID)
	assert.Equal(t, "default", result.Profile)
	got := map[string]string{}
	for _, m := range result.Mods {
		got[m.ModID] = m.Version
	}
	assert.Equal(t, map[string]string{"mod2": "2.0", "mod3": "2.0"}, got,
		"every offered update - a hand-locked one too - and never the collection-locked mod")

	report := skipReport(t, svc, game)
	assert.Empty(t, report.Updates, "Skip all empties the offered updates")
	assert.Len(t, report.SkippedUpdates, 2)

	row, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, row.SkippedVersion)
}

func TestSkipAllUpdates_NothingOfferedSkipsNothing(t *testing.T) {
	svc, game, src := newSkipTestService(t)
	src.newer = "1.0"

	result, err := svc.SkipAllUpdates(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Empty(t, result.Mods)
	assert.NotNil(t, result.Mods, "an empty list, never null")
}

func TestUnskipAllUpdates_ClearsEverySkip(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)
	_, err = svc.SkipModUpdate(context.Background(), game, "src", "mod2", "default", "2.0")
	require.NoError(t, err)

	result, err := svc.UnskipAllUpdates(context.Background(), game.ID, "default")
	require.NoError(t, err)
	got := map[string]string{}
	for _, m := range result.Mods {
		got[m.ModID] = m.Version
	}
	assert.Equal(t, map[string]string{"mod1": "2.0", "mod2": "2.0"}, got, "each mod with the version its skip held")

	report := skipReport(t, svc, game)
	assert.Len(t, report.Updates, 2)
	assert.Empty(t, report.SkippedUpdates)

	again, err := svc.UnskipAllUpdates(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, again.Mods, "clearing nothing is not an error")
}

func TestListMods_MarksACollectionsLock(t *testing.T) {
	svc, game, _ := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	byID := map[string]core.ModListing{}
	for _, m := range list.Mods {
		byID[m.ID] = m
	}
	assert.True(t, byID["mod1"].Locked)
	assert.True(t, byID["mod1"].LockedByCollection, "the collection's lock is told apart (#667): unlocking leaves the collection")
	assert.True(t, byID["mod2"].Locked)
	assert.False(t, byID["mod2"].LockedByCollection, "a hand lock is the user's")
	assert.False(t, byID["mod3"].LockedByCollection)
}

// The web UI's Skip all sends the rows its card shows (#667 review): each is
// checked against the installed mods, the good ones written in one
// transaction, and a row that no longer applies is counted, not fatal.
func TestSkipUpdates_WritesTheRowsGivenAndCountsTheRest(t *testing.T) {
	svc, game, src := newSkipTestService(t)
	src.asked = nil

	result, err := svc.SkipUpdates(context.Background(), game.ID, "default", []core.UpdateSkipItem{
		{SourceID: "src", ModID: "mod1", NewVersion: "2.0"},
		{SourceID: "src", ModID: "gone", NewVersion: "2.0"}, // not installed
		{SourceID: "src", ModID: "mod2", NewVersion: "1.0"}, // the installed version: nothing to skip
		{SourceID: "src", ModID: "mod2", NewVersion: ""},    // no version
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Applied)
	assert.Equal(t, 3, result.NotApplied)
	require.Len(t, result.Mods, 1)
	assert.Equal(t, core.SkippedUpdateRef{SourceID: "src", ModID: "mod1", Name: "Mod One", Version: "2.0"}, result.Mods[0])
	assert.Empty(t, src.asked, "the rows are the caller's: no update check runs")

	row, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.SkippedVersion)
	row, err = svc.GetInstalledMod(context.Background(), "src", "mod2", game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, row.SkippedVersion)
}

func TestSkipAllUpdates_CountsWhatItApplied(t *testing.T) {
	svc, game, _ := newSkipTestService(t)

	result, err := svc.SkipAllUpdates(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Equal(t, 2, result.Applied)
	assert.Zero(t, result.NotApplied)

	cleared, err := svc.UnskipAllUpdates(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, 2, cleared.Applied)
}

func TestModDetail_MarksACollectionsLock(t *testing.T) {
	svc, game, _ := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed)
	assert.True(t, detail.Installed.LockedByCollection, "the full mod page reads it here before the listing loads")

	hand, err := svc.ModDetail(context.Background(), game, "default", "src", "mod2")
	require.NoError(t, err)
	assert.False(t, hand.Installed.LockedByCollection)
}

func TestCountGameUpdateSkips_MatchesTheReport(t *testing.T) {
	svc, game, _ := newCollectionLockedService(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123"})
	installed := installedFor(t, svc, game)

	skips, err := svc.CountGameUpdateSkips(context.Background(), game.ID, "default", installed)
	require.NoError(t, err)
	report := skipReport(t, svc, game)
	assert.Equal(t, report.Skipped.Total(), skips.Total(), "the count a frontend prints before the check is the one the check skips")
	assert.Equal(t, 1, skips.Locked)
	assert.Equal(t, "Night City", skips.Collection)
}
