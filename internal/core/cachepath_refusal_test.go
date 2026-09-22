package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func purgeCachePathFixture(t *testing.T, f *legacyFixture) {
	t.Helper()
	plan, err := f.svc.PlanPurge(t.Context(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(t.Context(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
}

func TestSaveGame_CachePathMoveRequiresPurgeBeforeRedeploy(t *testing.T) {
	ctx := context.Background()
	oldCache, newCache := t.TempDir(), t.TempDir()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(),
		CachePath: oldCache, LinkMethod: domain.LinkSymlink})
	f.profile(t, "default", true, "a")
	f.profile(t, "alt", false, "b")
	f.deployed(t, "default", "a", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	f.deployed(t, "alt", "b", domain.LinkSymlink, map[string]string{"Data/b.esp": "mod b"}, nil)
	link := filepath.Join(f.game.ModPath, "Data", "a.esp")
	altLink := filepath.Join(f.game.ModPath, "Data", "b.esp")
	user := userFileAt(t, f, "Data/user.esp", "user bytes")

	moved, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	moved.CachePath = newCache
	err = f.svc.SaveGame(ctx, moved)
	var inUse *core.GameCachePathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Contains(t, err.Error(), "lmm purge --game sky --profile default")
	assert.Contains(t, err.Error(), "lmm purge --game sky --profile alt")
	assert.Equal(t, 2, inUse.DeployedFiles)
	saved, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	assert.Equal(t, oldCache, saved.CachePath)
	assert.Equal(t, "mod a", readLive(t, link))
	assert.Equal(t, "user bytes", readLive(t, user))

	altPlan, err := f.svc.PlanPurge(ctx, f.game, "alt", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(ctx, f.game, altPlan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	_, err = os.Lstat(altLink)
	require.ErrorIs(t, err, os.ErrNotExist)
	// A single remaining profile still blocks the path change.
	require.ErrorAs(t, f.svc.SaveGame(ctx, moved), &inUse)
	purgeCachePathFixture(t, f)
	_, err = os.Lstat(link)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, "user bytes", readLive(t, user))
	require.NoError(t, f.svc.SaveGame(ctx, moved))
	require.NoError(t, f.svc.GetGameCache(moved).Store("sky", "local", "a", "unknown", "Data/a.esp", []byte("mod a from new cache")))
	_, err = f.svc.DeployProfile(ctx, moved, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	assert.Equal(t, "mod a from new cache", readLive(t, link))
	assert.Equal(t, "user bytes", readLive(t, user))
}

func TestSaveGame_CachePathChangeWithoutDeploymentAndManualEditRecovery(t *testing.T) {
	ctx := context.Background()
	oldCache, newCache := t.TempDir(), t.TempDir()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(),
		CachePath: oldCache, LinkMethod: domain.LinkSymlink})
	clean, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	clean.CachePath = newCache
	require.NoError(t, f.svc.SaveGame(ctx, clean), "an undeployed game can change cache_path")
	clean.CachePath = oldCache
	require.NoError(t, f.svc.SaveGame(ctx, clean))

	f.profile(t, "default", true, "a")
	f.deployed(t, "default", "a", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	// Simulate a manual games.yaml edit that bypassed SaveGame's guard.
	manual := *f.game
	manual.CachePath = newCache
	require.NoError(t, config.SaveGame(f.svc.ConfigDir(), &manual))
	changed, err := f.svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, changed)
	loaded, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	assert.Equal(t, newCache, loaded.CachePath)
	// Restore the cache root the live link uses before running purge.
	require.NoError(t, config.SaveGame(f.svc.ConfigDir(), f.game))
	changed, err = f.svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, changed)
	f.game, err = f.svc.GetGame("sky")
	require.NoError(t, err)
	assert.Equal(t, oldCache, f.game.CachePath)
	purgeCachePathFixture(t, f)
	require.NoError(t, f.svc.SaveGame(ctx, &manual))
}
