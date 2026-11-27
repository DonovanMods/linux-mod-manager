package core_test

// #678 review: the cases the first round left untested - a cache shared by
// two games (one through a symlink), a removed game's records, FOMOD and
// legacy variants, unreadable inputs, the commit stamp, leftovers, mods
// nothing references, the setting read afresh, and the update-from-file,
// batch update and collection update prunes.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedEntryKey commits a complete-looking entry for k, last written at.
func seedEntryKey(t *testing.T, svc *core.Service, game *domain.Game, k cache.Key, at time.Time) string {
	t.Helper()
	c := svc.GetGameCache(game)
	require.NoError(t, c.Store(k, "data/"+k.ModID+".esp", []byte("payload of "+k.Version+k.Variant)))
	dir := c.ModPath(k)
	require.NoError(t, cache.MarkFileComplete(dir, "f-"+k.Version+k.Variant))
	require.NoError(t, cache.StampKey(dir, k))
	require.NoError(t, os.Chtimes(dir, at, at))
	return dir
}

// keptVersions is which of versions of the mod still have a plain entry in
// game's cache.
func keptVersions(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID string, versions ...string) []string {
	return cachedVersions(t, svc, game, sourceID, modID, versions...)
}

// TestPlanCachePrune_SharedCacheReadsEveryGame (B1): two games whose
// cache_path is one directory - the second reaching it through a symlink -
// share it, so a prune of the first never removes what the second has
// installed or rolls back to.
func TestPlanCachePrune_SharedCacheReadsEveryGame(t *testing.T) {
	for _, viaSymlink := range []bool{false, true} {
		svc := newFlowsTestService(t)
		shared := t.TempDir()
		pathB := shared
		if viaSymlink {
			pathB = filepath.Join(t.TempDir(), "link")
			require.NoError(t, os.Symlink(shared, pathB))
		}
		a := &domain.Game{ID: "ga", Name: "A", ModPath: t.TempDir(), CachePath: shared, LinkMethod: domain.LinkSymlink}
		b := &domain.Game{ID: "gb", Name: "B", ModPath: t.TempDir(), CachePath: pathB, LinkMethod: domain.LinkSymlink}
		require.NoError(t, svc.SaveGame(context.Background(), a))
		require.NoError(t, svc.SaveGame(context.Background(), b))
		seedVersions(t, svc, a, "src", "m", "1.0", "2.0", "3.0", "4.0")
		setKeepPrevious(t, svc, "0")
		seedRow(t, svc, a, "default", "src", "m", "4.0", "")
		seedRow(t, svc, b, "default", "src", "m", "1.0", "2.0")

		plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{a}, core.CachePruneOptions{})
		require.NoError(t, err)
		assert.Equal(t, []string{"3.0"}, prunedVersions(plan.Entries), "symlink %v", viaSymlink)

		status, err := svc.CacheStatus(context.Background(), nil)
		require.NoError(t, err)
		require.Len(t, status.Games, 1, "one directory, one row (symlink %v)", viaSymlink)
		assert.Equal(t, []string{"gb"}, status.Games[0].SharedWith)
	}
}

// TestPlanCachePrune_ARemovedGamesRowsStillProtect (B1): a game games.yaml
// no longer lists, whose cache lmm cannot place, still has installed rows;
// their versions are kept.
func TestPlanCachePrune_ARemovedGamesRowsStillProtect(t *testing.T) {
	svc := newFlowsTestService(t)
	shared := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), CachePath: shared, LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0")
	setKeepPrevious(t, svc, "0")
	seedRow(t, svc, game, "default", "src", "m", "3.0", "")
	gone := &domain.Game{ID: "removed-game"}
	seedRow(t, svc, gone, "default", "src", "m", "1.0", "")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2.0"}, prunedVersions(plan.Entries))
}

// TestPlanCachePrune_VariantsAreOneVersion: a FOMOD variant and the plain
// tree of one version are one version - one slot, kept or pruned
// together - and so is a legacy <version>@<variant> directory.
func TestPlanCachePrune_VariantsAreOneVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	t0 := pruneEpoch
	seedEntryKey(t, svc, game, cache.Key{GameID: game.ID, SourceID: "src", ModID: "m", Version: "1.0"}, t0)
	seedEntryKey(t, svc, game, cache.Key{GameID: game.ID, SourceID: "src", ModID: "m", Version: "1.0", Variant: "abcdef123456"}, t0.Add(time.Minute))
	// A legacy variant directory: a completion marker, no identity marker.
	legacy := filepath.Join(svc.GetGameCache(game).Root(game.ID), "src-m", "1.0@0123456789ab")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "x.esp"), []byte("x"), 0o644))
	require.NoError(t, cache.MarkFileComplete(legacy, "f-legacy"))
	require.NoError(t, os.Chtimes(legacy, t0, t0))
	seedVersions(t, svc, game, "src", "m") // nothing more
	for i, v := range []string{"2.0", "3.0", "4.0"} {
		seedCacheEntry(t, svc, game, "src", "m", v, t0.Add(time.Duration(i+1)*time.Hour))
	}
	seedRow(t, svc, game, "default", "src", "m", "4.0", "")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan.Entries, "1.0 and its variants are the third previous version, not three")

	setKeepPrevious(t, svc, "2")
	plan, err = svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Len(t, plan.Entries, 3, "every tree of 1.0 goes together")
	assert.Equal(t, 1, plan.Versions)
}

// TestPlanCachePrune_AnUnreadableProfileOrDatabaseKeepsTheGame: when a
// profile, or the database, cannot be read, the game's cache is left
// whole, with the reason.
func TestPlanCachePrune_AnUnreadableProfileOrDatabaseKeepsTheGame(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := pruneGame(t, svc)
		seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0", "5.0", "6.0")
		seedRow(t, svc, game, "default", "src", "m", "6.0", "")
		dir := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("mods: [unclosed\n"), 0o644))

		plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
		require.NoError(t, err)
		assert.Empty(t, plan.Entries)
		require.Len(t, plan.Skipped, 1)
		assert.Contains(t, plan.Skipped[0].Reason, "broken")
	})
	t.Run("database", func(t *testing.T) {
		dataDir := t.TempDir()
		svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir()})
		require.NoError(t, err)
		t.Cleanup(func() { _ = svc.Close() })
		game := pruneGame(t, svc)
		seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0", "5.0", "6.0")
		seedRow(t, svc, game, "default", "src", "m", "6.0", "")
		conn, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		_, err = conn.Exec(`ALTER TABLE installed_mods RENAME TO installed_mods_hidden`)
		require.NoError(t, err)

		plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
		require.NoError(t, err)
		assert.Empty(t, plan.Entries)
		require.Len(t, plan.Skipped, 1)
		assert.Contains(t, plan.Skipped[0].Reason, "installed")
	})
}

// TestPlanCachePrune_RanksByCommitStampNotModTime (S2): an old version an
// identity re-stamp or a later marker write touched is still the oldest.
func TestPlanCachePrune_RanksByCommitStampNotModTime(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	var dirs []string
	for i, v := range []string{"1.0", "2.0", "3.0", "4.0", "5.0"} {
		dir := seedCacheEntry(t, svc, game, "src", "m", v, pruneEpoch.Add(time.Duration(i)*time.Hour))
		require.NoError(t, cache.MarkCommitted(dir, pruneEpoch.Add(time.Duration(i)*time.Hour)))
		dirs = append(dirs, dir)
	}
	seedRow(t, svc, game, "default", "src", "m", "5.0", "")
	// 1.0 is touched now: a re-stamp and a new marker, as verify --fix does.
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "m", Version: "1.0"}
	require.NoError(t, cache.StampKey(dirs[0], k))
	require.NoError(t, cache.MarkFileComplete(dirs[0], "f-again"))
	require.NoError(t, cache.MarkCommitted(dirs[0], time.Now()))
	now := time.Now()
	require.NoError(t, os.Chtimes(dirs[0], now, now))

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"1.0"}, prunedVersions(plan.Entries), "1.0 was committed first, whatever touched it since")
}

// TestApplyCachePrune_LeavesBackupsAndSweepsLeftovers: a commit's .backup
// is never a candidate, a scan leaves an interrupted removal's leftovers
// alone, and the apply - under the mutation slot - sweeps them.
func TestApplyCachePrune_LeavesBackupsAndSweepsLeftovers(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	setKeepPrevious(t, svc, "0")
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0")
	seedRow(t, svc, game, "default", "src", "m", "2.0", "")
	container := filepath.Join(svc.GetGameCache(game).Root(game.ID), "src-m")
	backup := filepath.Join(container, "2.0.backup")
	require.NoError(t, os.MkdirAll(backup, 0o755))
	require.NoError(t, cache.MarkFileComplete(backup, "f"))
	leftover := filepath.Join(container, ".lmm-prune-0.9-abc")
	require.NoError(t, os.MkdirAll(filepath.Join(leftover, "x"), 0o755))

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"1.0"}, prunedVersions(plan.Entries))
	assert.DirExists(t, leftover, "a plan only reads")

	_, err = svc.ApplyCachePrune(context.Background(), plan, nil)
	require.NoError(t, err)
	assert.DirExists(t, backup)
	assert.NoDirExists(t, leftover)
}

// TestPlanCachePrune_UninstalledModsNeedAnOptIn (S5): a mod nothing
// references is left whole and listed; only IncludeUninstalled removes it,
// every version.
func TestPlanCachePrune_UninstalledModsNeedAnOptIn(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	setKeepPrevious(t, svc, "0")
	all := []string{"1.0", "2.0", "3.0", "4.0", "5.0"}
	seedVersions(t, svc, game, "src", "kept-cache", all...)

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan.Entries)
	require.Len(t, plan.Uninstalled, 1)
	assert.Equal(t, "kept-cache", plan.Uninstalled[0].ModID)
	assert.Equal(t, 5, plan.Uninstalled[0].Versions)
	assert.Positive(t, plan.Uninstalled[0].Bytes)

	status, err := svc.CacheStatus(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	require.Len(t, status.Games[0].Uninstalled, 1)
	assert.Equal(t, plan.Uninstalled[0].Bytes, status.Games[0].UninstalledBytes)
	assert.Zero(t, status.Games[0].ReclaimableVersions)

	plan, err = svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{IncludeUninstalled: true})
	require.NoError(t, err)
	assert.True(t, plan.IncludeUninstalled)
	assert.Len(t, plan.Entries, 5)
	assert.True(t, plan.Entries[0].Uninstalled)
	_, err = svc.ApplyCachePrune(context.Background(), plan, nil)
	require.NoError(t, err)
	assert.Empty(t, keptVersions(t, svc, game, "src", "kept-cache", all...))
}

// TestPruneAfterFlow_ReadsTheSettingAfresh (S1): `lmm cache keep unlimited`
// from another process reaches a Service opened before it - the next
// flow prunes nothing.
func TestPruneAfterFlow_ReadsTheSettingAfresh(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"9"},
		map[string][]byte{"mod1-old.esp": []byte("old-payload")})
	markEntry(t, svc, game, "src", "mod1", "1.0", "9")
	seedVersions(t, svc, game, "src", "mod1", "0.1", "0.2", "0.3", "0.4")
	svc.RegisterSource(newTwoVersionSource(t))

	// Another process's `lmm cache keep unlimited`.
	cfg, err := config.Load(svc.ConfigDir())
	require.NoError(t, err)
	cfg.SetCacheKeepPreviousVersions(-1)
	require.NoError(t, cfg.Save(svc.ConfigDir()))

	plan, err := svc.PlanUpdateFrom(context.Background(), game, "default", domain.Update{InstalledMod: *old, NewVersion: "1.5"})
	require.NoError(t, err)
	assert.Nil(t, plan.CachePrune)
	result, err := svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Nil(t, result.CachePruned)
	assert.Len(t, keptVersions(t, svc, game, "src", "mod1", "0.1", "0.2", "0.3", "0.4"), 4)

	// And a config.yaml that cannot be read prunes nothing either.
	require.NoError(t, os.WriteFile(filepath.Join(svc.ConfigDir(), "config.yaml"), []byte("cache: [\n"), 0o644))
	_, err = svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.Error(t, err)
}

// TestApplyUpdateBatch_PrunesOnceAtTheEnd (S4): a batch prunes the mods it
// updated once, after the last of them.
func TestApplyUpdateBatch_PrunesOnceAtTheEnd(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"9"},
		map[string][]byte{"mod1-old.esp": []byte("old-payload")})
	markEntry(t, svc, game, "src", "mod1", "1.0", "9")
	seedVersions(t, svc, game, "src", "mod1", "0.1", "0.2", "0.3", "0.4")
	svc.RegisterSource(newTwoVersionSource(t))

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", []domain.Update{{InstalledMod: *old, NewVersion: "1.5"}}, nil)
	require.NoError(t, err)
	require.NotNil(t, plan.CachePrune)
	assert.Equal(t, 2, plan.CachePrune.Versions)

	var done int
	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, func(e core.Event) {
		if st, ok := e.(core.StepEvent); ok && st.Phase == core.CachePruneDone {
			done++
		}
	})
	require.NoError(t, err)
	require.Len(t, result.Applied, 1)
	assert.Nil(t, result.Applied[0].CachePruned, "the item leaves the prune to the batch")
	require.NotNil(t, result.CachePruned)
	assert.Equal(t, []string{"0.2", "0.1"}, prunedVersions(result.CachePruned.Entries))
	assert.Equal(t, 1, done)
}

// TestApplyUpdateFromArchive_Prunes: an update from a downloaded file
// prunes the mod afterwards, as its plan says.
func TestApplyUpdateFromArchive_Prunes(t *testing.T) {
	f := newFromFileFixture(t)
	markEntry(t, f.svc, f.game, "msrc", "42", "1.0", "100")
	seedVersions(t, f.svc, f.game, "msrc", "42", "0.1", "0.2", "0.3", "0.4")
	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	require.NotNil(t, plan.CachePrune)
	assert.Equal(t, 2, plan.CachePrune.Versions)

	result, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.CachePruned)
	assert.Equal(t, []string{"0.3", "0.4"}, keptVersions(t, f.svc, f.game, "msrc", "42", "0.1", "0.2", "0.3", "0.4"))
}

// TestApplyCollectionUpdate_PrunesTheChangedMembers: a collection update
// that moves a member to a new version prunes that member's old versions.
func TestApplyCollectionUpdate_PrunesTheChangedMembers(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	seedVersions(t, f.svc, f.game, "nexusmods", "10", "0.1", "0.2", "0.3", "0.4")

	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	plan := f.planUpdate(t)
	require.NotNil(t, plan.CachePrune)

	result, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{RemoveObsolete: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.CachePruned)
	assert.Equal(t, []string{"0.3", "0.4"}, keptVersions(t, f.svc, f.game, "nexusmods", "10", "0.1", "0.2", "0.3", "0.4"))
}

// TestPlanCachePrune_KeepsEveryCollectionPin (S3): the pin a substitute
// stands in for, and a collection's skipped pin, are references too.
func TestPlanCachePrune_KeepsEveryCollectionPin(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	setKeepPrevious(t, svc, "0")
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0", "4.0")
	seedVersions(t, svc, game, "src", "skipped", "1.0", "2.0")
	seedRow(t, svc, game, "default", "src", "m", "4.0", "")
	seedRow(t, svc, game, "default", "src", "skipped", "2.0", "")
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "coll", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "src", ModID: "m", Version: "4.0", SubstitutedFor: &domain.CollectionPin{Version: "2.0"}}},
		CollectionBaseline: &domain.CollectionBaseline{Skipped: []domain.CollectionSkip{
			{SourceID: "src", ModID: "skipped", Pin: domain.CollectionPin{Version: "1.0"}},
		}},
	}))
	loaded, err := config.LoadProfile(svc.ConfigDir(), game.ID, "coll")
	require.NoError(t, err)
	require.NotNil(t, loaded.Mods[0].SubstitutedFor, "the profile file keeps the substitute's pin")
	require.NotNil(t, loaded.CollectionBaseline)

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	got := map[string][]string{}
	for _, e := range plan.Entries {
		got[e.ModID] = append(got[e.ModID], e.Version)
	}
	assert.Equal(t, map[string][]string{"m": {"3.0", "1.0"}}, got, "2.0 of m and 1.0 of skipped are pinned")
}

// TestPlanCachePrune_KeepsWhatIsDeployed (#678 re-review): a version the
// game directory's symlinks point into is in use, even when no row
// records it - an update that failed after deploying, before its row was
// saved. A deployed file lmm cannot read leaves the cache whole.
func TestPlanCachePrune_KeepsWhatIsDeployed(t *testing.T) {
	svc := newFlowsTestService(t)
	game := pruneGame(t, svc)
	setKeepPrevious(t, svc, "0")
	seedVersions(t, svc, game, "src", "m", "1.0", "2.0", "3.0")
	// 1.0 is deployed (symlinks into its entry, deployed_files rows)...
	require.NoError(t, svc.GetInstallerForTest(game).Install(context.Background(), game,
		&domain.Mod{ID: "m", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
	// ...while the row says 3.0.
	seedRow(t, svc, game, "default", "src", "m", "3.0", "")
	deployed := filepath.Join(game.ModPath, "data", "m.esp")
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the fixture deploys by symlink")

	plan, err := svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2.0"}, prunedVersions(plan.Entries), "1.0 is deployed")

	if os.Geteuid() != 0 {
		dir := filepath.Join(game.ModPath, "data")
		require.NoError(t, os.Chmod(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		plan, err = svc.PlanCachePrune(context.Background(), []*domain.Game{game}, core.CachePruneOptions{})
		require.NoError(t, err)
		assert.Empty(t, plan.Entries)
		require.Len(t, plan.Skipped, 1)
		assert.Contains(t, plan.Skipped[0].Reason, "deployed")
	}
}
