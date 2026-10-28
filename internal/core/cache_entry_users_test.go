package core_test

// The #445 second gate's G2-2. `lmm uninstall` deleted the uninstalled
// version's cache entry even while another installed row still used it.
// For a local mod that entry is the only copy, so the other profile was
// left with a mod it could never deploy again - and `lmm profile apply`
// borrowing another profile's cache (cachedRowElsewhere) makes two rows on
// one entry the ordinary state. A cache entry is now removed only when no
// other row - of the game's other profiles, or of another game whose cache
// is the same directory - names that version, as its current one or as the
// one it rolls back to; the plan and the result say which rows kept it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cachedRow seeds modID@version in game's cache and an installed row for it
// under profile, enabled and not deployed - what a profile the user
// switched away from holds.
func cachedRow(t *testing.T, svc *core.Service, game *domain.Game, profile, modID, version string, files map[string]string) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	for rel, content := range files {
		require.NoError(t, gameCache.Store(game.ID, "local", modID, version, rel, []byte(content)))
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "local", Name: "Mod " + modID, Version: version, GameID: game.ID},
		ProfileName:  profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		LinkMethod:   game.LinkMethod,
	}))
}

// uninstall plans and applies an uninstall of local:modID from profile.
func uninstall(t *testing.T, svc *core.Service, game *domain.Game, profile, modID string) (*core.UninstallPlan, *core.UninstallResult) {
	t.Helper()
	ctx := context.Background()
	plan, err := svc.PlanUninstall(ctx, game, profile, "local", modID, core.UninstallOptions{})
	require.NoError(t, err)
	result, err := svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
	require.NoError(t, err)
	return plan, result
}

func TestUninstall_KeepsACacheEntryAnotherProfileUses(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
	live := filepath.Join(f.game.ModPath, "Data", "a.esp")
	// default imported the local mod and was switched away from; alt, now
	// active, lists it and has no row, so its apply borrows default's cache.
	f.profile(t, "default", false, "k")
	f.profile(t, "alt", true, "k")
	cachedRow(t, f.svc, f.game, "default", "k", "unknown", map[string]string{"Data/a.esp": "the only copy"})
	plan, err := f.svc.PlanProfileApply(ctx, f.game, "alt")
	require.NoError(t, err)
	applied, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, "default", applied.Outcomes[0].FromProfile)
	require.Equal(t, "the only copy", readLive(t, live))

	uplan, result := uninstall(t, f.svc, f.game, "alt", "k")

	assert.Equal(t, []string{"profile default"}, uplan.CacheUsedBy, "the plan says the cache entry stays, and why")
	assert.Equal(t, []string{"profile default"}, result.CacheUsedBy, "and so does the result")
	assert.NoFileExists(t, live, "alt's deployment is gone")
	assert.True(t, f.svc.GetGameCache(f.game).Exists("sky", "local", "k", "unknown"), "default's only copy of the mod is still cached")

	// default can still deploy it: switching to it, then applying it.
	splan, err := f.svc.PlanProfileSwitch(ctx, f.game, "default")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, splan, nil)
	require.NoError(t, err)
	assert.Equal(t, "the only copy", readLive(t, live))
	aplan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileApply(ctx, f.game, aplan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	result2, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result2.Skipped)
	assert.Equal(t, "the only copy", readLive(t, live))

	t.Run("the last row's uninstall removes it", func(t *testing.T) {
		_, result := uninstall(t, f.svc, f.game, "default", "k")
		assert.Empty(t, result.CacheUsedBy)
		assert.False(t, f.svc.GetGameCache(f.game).Exists("sky", "local", "k", "unknown"))
	})
}

func TestUninstall_KeepsACacheEntryARowRollsBackTo(t *testing.T) {
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "default", true, "k")
	f.profile(t, "p", false, "k")
	cachedRow(t, f.svc, f.game, "default", "k", "1.0", map[string]string{"Data/a.esp": "one"})
	cachedRow(t, f.svc, f.game, "p", "k", "2.0", map[string]string{"Data/a.esp": "two"})
	require.NoError(t, f.svc.ExecForTest(context.Background(),
		`UPDATE installed_mods SET previous_version = '1.0' WHERE profile_name = 'p'`))

	plan, result := uninstall(t, f.svc, f.game, "default", "k")

	assert.Equal(t, []string{"profile p (to roll back to)"}, plan.CacheUsedBy)
	assert.Equal(t, []string{"profile p (to roll back to)"}, result.CacheUsedBy)
	assert.True(t, f.svc.GetGameCache(f.game).Exists("sky", "local", "k", "1.0"), "p's rollback still has its version")
}

func TestUninstall_KeepsACacheEntryAnotherGameUses(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		sameCache bool
		usedBy    []string
	}{
		{name: "the same cache_path", sameCache: true, usedBy: []string{"game sky2's profile default"}},
		{name: "another cache_path", sameCache: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared := t.TempDir()
			svc := newFlowsTestService(t)
			sky := &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), CachePath: shared, LinkMethod: domain.LinkSymlink}
			sky2 := &domain.Game{ID: "sky2", Name: "Sky 2", ModPath: t.TempDir(), CachePath: shared, LinkMethod: domain.LinkSymlink}
			if !tc.sameCache {
				sky2.CachePath = t.TempDir()
			}
			require.NoError(t, svc.SaveGame(ctx, sky))
			require.NoError(t, svc.SaveGame(ctx, sky2))
			cachedRow(t, svc, sky, "default", "k", "unknown", map[string]string{"a.esp": "a"})
			cachedRow(t, svc, sky2, "default", "k", "unknown", map[string]string{"a.esp": "a"})

			plan, result := uninstall(t, svc, sky, "default", "k")

			assert.Equal(t, tc.usedBy, plan.CacheUsedBy)
			assert.Equal(t, tc.usedBy, result.CacheUsedBy)
			assert.Equal(t, tc.sameCache, svc.GetGameCache(sky).Exists("sky", "local", "k", "unknown"))
			assert.True(t, svc.GetGameCache(sky2).Exists("sky2", "local", "k", "unknown"))
		})
	}

	// A row of a game lmm no longer has configured names no cache path lmm
	// can compare, so it keeps the entry.
	t.Run("a game that is not configured", func(t *testing.T) {
		svc := newFlowsTestService(t)
		sky := &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
		require.NoError(t, svc.SaveGame(ctx, sky))
		cachedRow(t, svc, sky, "default", "k", "unknown", map[string]string{"a.esp": "a"})
		require.NoError(t, svc.ExecForTest(ctx,
			`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, update_policy, enabled, deployed)
			 SELECT source_id, mod_id, 'gone', profile_name, name, version, update_policy, enabled, deployed FROM installed_mods WHERE game_id = 'sky'`))

		_, result := uninstall(t, svc, sky, "default", "k")

		assert.Equal(t, []string{"game gone's profile default (gone is not configured)"}, result.CacheUsedBy)
		assert.True(t, svc.GetGameCache(sky).Exists("sky", "local", "k", "unknown"))
	})
}

// TestInstall_KeepsAReplacedVersionAnotherProfileUses: an install that
// replaces a version clears that version's cache entry only when no other
// row uses it.
func TestInstall_KeepsAReplacedVersionAnotherProfileUses(t *testing.T) {
	ctx := context.Background()
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))

	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	require.NoError(t, svc.GetInstallerForTest(game).Install(ctx, game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.ExecForTest(ctx,
		`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, update_policy, enabled, deployed)
		 SELECT source_id, mod_id, game_id, 'other', name, version, update_policy, enabled, 0 FROM installed_mods WHERE profile_name = 'default'`))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"}, "mod1-new.esp", "new-content")

	plan, err := svc.PlanInstall(ctx, game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces)
	result, err := svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "profile other still uses 1.0")
	assert.Contains(t, result.Notes, "Kept the cache entry of src:mod1 at 1.0: profile other still uses it")
	_, err = os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
	assert.NoError(t, err)
}
