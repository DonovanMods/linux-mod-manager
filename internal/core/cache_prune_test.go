package core_test

// #678: the cache keeps each mod's installed versions, the newest N
// previous versions (cache.keep_previous_versions, default 3), and every
// version a profile or a snapshot references; the rest can be pruned.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pruneEpoch is the first seeded entry's modification time; each later
// version is an hour newer.
var pruneEpoch = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// seedCacheEntry commits a complete-looking cache entry for the version:
// one member, a completion marker and the identity marker, last written
// at.
func seedCacheEntry(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version string, at time.Time) string {
	t.Helper()
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: sourceID, ModID: modID, Version: version}
	require.NoError(t, c.Store(k, "data/"+modID+".esp", []byte("payload of "+version)))
	dir := c.ModPath(k)
	require.NoError(t, cache.MarkFileComplete(dir, "f-"+version))
	require.NoError(t, cache.StampKey(dir, k))
	require.NoError(t, os.Chtimes(dir, at, at))
	return dir
}

// seedVersions seeds entries for versions, oldest first, an hour apart.
func seedVersions(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID string, versions ...string) {
	t.Helper()
	for i, v := range versions {
		seedCacheEntry(t, svc, game, sourceID, modID, v, pruneEpoch.Add(time.Duration(i)*time.Hour))
	}
}

// seedRow records an installed row of the mod in profile at version, with
// previous as its rollback target.
func seedRow(t *testing.T, svc *core.Service, game *domain.Game, profile, sourceID, modID, version, previous string) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:             domain.Mod{ID: modID, SourceID: sourceID, Name: "Mod " + modID, Version: version, GameID: game.ID},
		ProfileName:     profile,
		UpdatePolicy:    domain.UpdateNotify,
		Enabled:         true,
		PreviousVersion: previous,
	}))
}

// setKeepPrevious sets cache.keep_previous_versions, as the web UI does;
// "unlimited" is -1.
func setKeepPrevious(t *testing.T, svc *core.Service, value string) {
	t.Helper()
	n := -1
	if value != "unlimited" {
		var err error
		n, err = strconv.Atoi(value)
		require.NoError(t, err)
	}
	require.NoError(t, svc.SetCacheKeepPreviousVersions(context.Background(), n))
}

// prunedVersions is the versions a plan or result lists, in order.
func prunedVersions(entries []core.CachePruneEntry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Version)
	}
	return out
}

// cachedVersions is which of versions still have an entry.
func cachedVersions(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID string, versions ...string) []string {
	t.Helper()
	c := svc.GetGameCache(game)
	out := []string{}
	for _, v := range versions {
		if c.Exists(cache.Key{GameID: game.ID, SourceID: sourceID, ModID: modID, Version: v}) {
			out = append(out, v)
		}
	}
	return out
}

// pruneGame is a registered game for a prune test.
func pruneGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return game
}

// TestPlanCachePrune_KeepsInstalledAndTheNewestThreePrevious: installed
// 6.0 plus the three newest other versions stay; the two oldest go.
func TestPlanCachePrune_KeepsInstalledAndTheNewestThreePrevious(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	all := []string{"1.0", "2.0", "3.0", "4.0", "5.0", "6.0"}
	seedVersions(t, svc, game, "src", "m", all...)
	seedRow(t, svc, game, "default", "src", "m", "6.0", "")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Equal(t, 3, plan.KeepPreviousVersions)
	assert.Equal(t, []string{"2.0", "1.0"}, prunedVersions(plan.Entries))
	assert.Equal(t, 2, plan.Versions)
	assert.Positive(t, plan.Bytes)
	assert.Equal(t, "src", plan.Entries[0].SourceID)
	assert.Equal(t, "m", plan.Entries[0].ModID)
	assert.Equal(t, "Mod m", plan.Entries[0].ModName)
	assert.Equal(t, "g1", plan.Entries[0].GameID)

	// A plan is the dry run: nothing is gone yet.
	assert.Equal(t, all, cachedVersions(t, svc, game, "src", "m", all...))

	result, err := svc.ApplyCachePrune(context.Background(), plan, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"2.0", "1.0"}, prunedVersions(result.Entries))
	assert.Equal(t, plan.Bytes, result.Bytes)
	assert.Equal(t, []string{"3.0", "4.0", "5.0", "6.0"}, cachedVersions(t, svc, game, "src", "m", all...))
}

// TestPlanCachePrune_InstalledNeedNotBeNewest: the installed version is
// kept wherever it sits, and newer ones fill the previous slots.
func TestPlanCachePrune_InstalledNeedNotBeNewest(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0", "5.0", "6.0")
	seedRow(t, svc, game, "default", "src", "m", "1.0", "")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Equal(t, []string{"3.0", "2.0"}, prunedVersions(plan.Entries))
}

// TestPlanCachePrune_NeverPrunesAReferencedVersion: with no previous
// versions kept, everything a profile or a snapshot names still stays -
// another profile's row, a row's previous_version, a locked ref in a
// profile with no row, a snapshot's row and its rollback target.
func TestPlanCachePrune_NeverPrunesAReferencedVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	all := []string{"1.0", "2.0", "3.0", "4.0", "5.0", "6.0", "7.0", "8.0", "9.0"}
	seedVersions(t, svc, game, "src", "m", all...)
	setKeepPrevious(t, svc, "0")

	seedRow(t, svc, game, "default", "src", "m", "9.0", "8.0")
	seedRow(t, svc, game, "other", "src", "m", "2.0", "")
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "locked")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "locked", domain.ModReference{SourceID: "src", ModID: "m", Version: "4.0", Locked: true}))

	snapDir := svc.SnapshotsDirForTest(game.ID)
	require.NoError(t, os.MkdirAll(snapDir, 0o755))
	doc := core.Snapshot{
		Name: "before", GameID: game.ID, Profile: "default", CreatedAt: pruneEpoch,
		ProfileDocument: &domain.ExportedProfile{Name: "default", GameID: game.ID, Mods: []domain.ModReference{{SourceID: "src", ModID: "m", Version: "6.0"}}},
		Installed: []domain.InstalledMod{{
			Mod:             domain.Mod{ID: "m", SourceID: "src", Version: "5.0", GameID: game.ID},
			ProfileName:     "default",
			PreviousVersion: "1.0",
		}},
	}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(snapDir, "before.json"), data, 0o644))

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Equal(t, []string{"7.0", "3.0"}, prunedVersions(plan.Entries))
}

// TestPlanCachePrune_AnUnreadableReferenceKeepsTheGame: when lmm cannot
// read everything that could reference a version - here a snapshot - it
// prunes nothing of that game and says why.
func TestPlanCachePrune_AnUnreadableReferenceKeepsTheGame(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0", "5.0", "6.0")
	seedRow(t, svc, game, "default", "src", "m", "6.0", "")
	snapDir := svc.SnapshotsDirForTest(game.ID)
	require.NoError(t, os.MkdirAll(snapDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(snapDir, "broken.json"), []byte("{not json"), 0o644))

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Empty(t, plan.Entries)
	require.Len(t, plan.Skipped, 1)
	assert.Equal(t, "g1", plan.Skipped[0].GameID)
	assert.Contains(t, plan.Skipped[0].Reason, "broken.json")
}

// TestPlanCachePrune_SettingChangesTheCount: keep_previous_versions 1
// keeps one previous version.
func TestPlanCachePrune_SettingChangesTheCount(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0")
	seedRow(t, svc, game, "default", "src", "m", "4.0", "")
	setKeepPrevious(t, svc, "1")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Equal(t, 1, plan.KeepPreviousVersions)
	assert.Equal(t, []string{"2.0", "1.0"}, prunedVersions(plan.Entries))
}

// TestPlanCachePrune_UnlimitedPrunesNothing: unlimited (or a negative
// count) disables pruning, and the status reclaims nothing.
func TestPlanCachePrune_UnlimitedPrunesNothing(t *testing.T) {
	for _, value := range []string{"unlimited", "-1"} {
		svc := newFlowsTestService(t)
		game := pruneGame(t, svc)
		seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0", "5.0", "6.0")
		seedRow(t, svc, game, "default", "src", "m", "6.0", "")
		setKeepPrevious(t, svc, value)

		plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
		require.NoError(t, err, value)
		assert.Equal(t, -1, plan.KeepPreviousVersions, value)
		assert.Empty(t, plan.Entries, value)

		status, err := svc.CacheStatus(context.Background(), []*domain.Game{game})
		require.NoError(t, err)
		assert.Zero(t, status.ReclaimableBytes, value)
		assert.Positive(t, status.Bytes, value)
	}
}

// TestPlanCachePrune_LeavesWhatIsNoVersionAlone: the merged pak's entry
// and a directory no marker vouches for are never candidates, even with
// no previous versions kept.
func TestPlanCachePrune_LeavesWhatIsNoVersionAlone(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	setKeepPrevious(t, svc, "0")
	seedCacheEntry(t, svc, game, domain.SourceMerged, "merged-pak", "merged", pruneEpoch)
	c := svc.GetGameCache(game)
	stray := filepath.Join(c.Root(game.ID), "src-m", "hand-made")
	require.NoError(t, os.MkdirAll(stray, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stray, "x.txt"), []byte("x"), 0o644))

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Empty(t, plan.Entries)
	assert.DirExists(t, stray)
}

// TestApplyCachePrune_RefusesAStalePlan: a plan whose candidates changed
// before the apply - here a profile started referencing one - is refused
// whole, with nothing removed.
func TestApplyCachePrune_RefusesAStalePlan(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	all := []string{"1.0", "2.0", "3.0", "4.0", "5.0", "6.0"}
	seedVersions(t, svc, game, "src", "m", all...)
	seedRow(t, svc, game, "default", "src", "m", "6.0", "")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	require.Len(t, plan.Entries, 2)

	seedRow(t, svc, game, "other", "src", "m", "1.0", "")
	_, err = svc.ApplyCachePrune(context.Background(), plan, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
	assert.Equal(t, all, cachedVersions(t, svc, game, "src", "m", all...))
}

// TestCacheStatus_ReportsSizeAndWhatCanBeReclaimed.
func TestCacheStatus_ReportsSizeAndWhatCanBeReclaimed(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0", "5.0", "6.0")
	seedRow(t, svc, game, "default", "src", "m", "6.0", "")

	status, err := svc.CacheStatus(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 3, status.KeepPreviousVersions)
	require.Len(t, status.Games, 1)
	g := status.Games[0]
	assert.Equal(t, "g1", g.GameID)
	assert.Equal(t, "Game", g.GameName)
	assert.Equal(t, 1, g.Mods)
	assert.Equal(t, 6, g.Versions)
	assert.Equal(t, 2, g.ReclaimableVersions)
	assert.Positive(t, g.ReclaimableBytes)
	assert.Greater(t, g.Bytes, g.ReclaimableBytes)
	assert.Equal(t, g.Bytes, status.Bytes)
	assert.Equal(t, g.ReclaimableBytes, status.ReclaimableBytes)
}

// TestSetCacheKeepPreviousVersions_WritesTheSetting: the web UI's setting
// is config.yaml's, read back by the next plan.
func TestSetCacheKeepPreviousVersions_WritesTheSetting(t *testing.T) {
	svc := newFlowsTestService(t)
	require.NoError(t, svc.SetCacheKeepPreviousVersions(context.Background(), 5))
	assert.Equal(t, 5, svc.CacheKeepPreviousVersions())
	data, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "keep_previous_versions: 5")
}

// TestApplyUpdate_PrunesTheModAfterwards: a successful update prunes the
// updated mod's old versions, and its plan says beforehand what that
// frees.
func TestApplyUpdate_PrunesTheModAfterwards(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"9"},
		map[string][]byte{"mod1-old.esp": []byte("old-payload")})
	markEntry(t, svc, game, "src", "mod1", "1.0", "9")
	seedVersions(t, svc, game, "src", "mod1", "0.1", "0.2", "0.3", "0.4")
	// Another mod's old versions are not this update's to prune.
	seedVersions(t, svc, game, "src", "other", "0.1", "0.2", "0.3", "0.4", "0.5")
	svc.RegisterSource(newTwoVersionSource(t))

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.5"}
	plan, err := svc.PlanUpdateFrom(context.Background(), game, "default", upd)
	require.NoError(t, err)
	require.NotNil(t, plan.CachePrune, "the plan says the update prunes")
	assert.Equal(t, 2, plan.CachePrune.Versions)

	result, err := svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.CachePruned)
	assert.Equal(t, []string{"0.2", "0.1"}, prunedVersions(result.CachePruned.Entries))
	assert.Equal(t, []string{"0.3", "0.4"}, cachedVersions(t, svc, game, "src", "mod1", "0.1", "0.2", "0.3", "0.4"))
	assert.Len(t, cachedVersions(t, svc, game, "src", "other", "0.1", "0.2", "0.3", "0.4", "0.5"), 5)
}

// TestApplyUpdate_APruneFailureOnlyWarns: an old version the prune cannot
// remove leaves the update applied, with a warning.
func TestApplyUpdate_APruneFailureOnlyWarns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory this test relies on")
	}
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"9"},
		map[string][]byte{"mod1-old.esp": []byte("old-payload")})
	seedVersions(t, svc, game, "src", "mod1", "0.1", "0.2", "0.3", "0.4")
	svc.RegisterSource(newTwoVersionSource(t))

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.5"}
	plan, err := svc.PlanUpdateFrom(context.Background(), game, "default", upd)
	require.NoError(t, err)

	// The old entry's member directory is read-only, so the entry is moved
	// aside but its files cannot be deleted.
	oldDir := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "0.1"})
	require.NoError(t, os.Chmod(filepath.Join(oldDir, "data"), 0o555))
	t.Cleanup(func() { unlockTree(t, svc.GetGameCache(game).Root(game.ID)) })

	result, err := svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, core.UpdateUpdated, result.Status)
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "cache") && strings.Contains(w, "0.1") {
			found = true
		}
	}
	assert.True(t, found, "a prune failure is a warning: %v", result.Warnings)
}

// unlockTree makes every directory under root writable again, so the test's
// temp directory can be removed.
func unlockTree(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
}

// markEntry stamps the markers a real commit leaves on an entry a test
// seeded with Store alone.
func markEntry(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string) {
	t.Helper()
	k := cache.Key{GameID: game.ID, SourceID: sourceID, ModID: modID, Version: version}
	dir := svc.GetGameCache(game).ModPath(k)
	require.NoError(t, cache.MarkFileComplete(dir, fileID))
	require.NoError(t, cache.StampKey(dir, k))
}

// TestApplyRollback_PrunesTheModAfterwards: a rollback prunes too, and its
// plan says what it frees beforehand. The rolled-away-from version is the
// rollback target afterwards, so it stays.
func TestApplyRollback_PrunesTheModAfterwards(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"9"},
		map[string][]byte{"mod1-old.esp": []byte("old-payload")})
	markEntry(t, svc, game, "src", "mod1", "1.0", "9")
	seedVersions(t, svc, game, "src", "mod1", "0.3", "0.4")
	svc.RegisterSource(newTwoVersionSource(t))
	setKeepPrevious(t, svc, "unlimited")

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.5"}
	plan, err := svc.PlanUpdateFrom(context.Background(), game, "default", upd)
	require.NoError(t, err)
	assert.Nil(t, plan.CachePrune, "unlimited prunes nothing")
	result, err := svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Nil(t, result.CachePruned)

	setKeepPrevious(t, svc, "1")
	rb, err := svc.PlanRollback(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, rb.CachePrune)
	assert.Equal(t, 2, rb.CachePrune.Versions)

	var events []core.Event
	rolled, err := svc.ApplyRollback(context.Background(), game, rb, core.RollbackOptions{}, func(e core.Event) { events = append(events, e) })
	require.NoError(t, err)
	require.NotNil(t, rolled.CachePruned)
	assert.Equal(t, []string{"0.4", "0.3"}, prunedVersions(rolled.CachePruned.Entries))
	assert.Equal(t, []string{"1.0", "1.5"}, cachedVersions(t, svc, game, "src", "mod1", "0.3", "0.4", "1.0", "1.5"))
	done := false
	for _, e := range events {
		if st, ok := e.(core.StepEvent); ok && st.Phase == core.CachePruneDone {
			done = true
			assert.Contains(t, st.Detail, "freed")
			assert.Contains(t, st.Detail, "2 old versions")
		}
	}
	assert.True(t, done, "the flow reports what its prune freed")
}

// TestCacheKeepPreviousVersions_ReadsConfigAtOpen: the setting config.yaml
// holds when the Service opens is the one it prunes by.
func TestCacheKeepPreviousVersions_ReadsConfigAtOpen(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("cache:\n  keep_previous_versions: 7\n"), 0o644))
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	assert.Equal(t, 7, svc.CacheKeepPreviousVersions())
}
