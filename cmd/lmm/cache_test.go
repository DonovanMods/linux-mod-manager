package main

// cache_test.go covers `lmm cache` (#678): the status table, the prune's
// dry run (which removes nothing), the prune itself (asserted on the
// cache's end state), the setting, and their --json documents.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cacheTestVersions are the cached versions setupCacheTest seeds, oldest
// first; 6.0 is installed.
var cacheTestVersions = []string{"1.0", "2.0", "3.0", "4.0", "5.0", "6.0"}

// setupCacheTest builds a sandboxed service with one game whose mod has
// six cached versions, the newest installed, and resets the cache flags.
func setupCacheTest(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	configDir = t.TempDir()
	dataDir = t.TempDir()
	cacheDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: cacheDir})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))
	c := svc.GetGameCache(game)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i, v := range cacheTestVersions {
		k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "m", Version: v}
		require.NoError(t, c.Store(k, "data/m.esp", []byte("payload "+v)))
		dir := c.ModPath(k)
		require.NoError(t, cache.MarkFileComplete(dir, "f"+v))
		require.NoError(t, cache.StampKey(dir, k))
		when := at.Add(time.Duration(i) * time.Hour)
		require.NoError(t, os.Chtimes(dir, when, when))
	}
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "m", SourceID: "src", Name: "Mod M", Version: "6.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))

	oldDry, oldYes, oldJSON, oldVerbose, oldGame := cachePruneDryRun, cachePruneYes, jsonOutput, verbose, gameID
	cachePruneDryRun, cachePruneYes, jsonOutput, verbose, gameID = false, true, false, false, ""
	t.Cleanup(func() {
		cachePruneDryRun, cachePruneYes, jsonOutput, verbose, gameID = oldDry, oldYes, oldJSON, oldVerbose, oldGame
	})
	return svc, game, cacheDir
}

// stillCached is which of cacheTestVersions still have an entry.
func stillCached(t *testing.T, svc *core.Service, game *domain.Game) []string {
	t.Helper()
	c := svc.GetGameCache(game)
	var out []string
	for _, v := range cacheTestVersions {
		if c.Exists(cache.Key{GameID: game.ID, SourceID: "src", ModID: "m", Version: v}) {
			out = append(out, v)
		}
	}
	return out
}

func TestCachePrune_DryRunListsAndRemovesNothing(t *testing.T) {
	svc, game, _ := setupCacheTest(t)
	cachePruneDryRun = true

	out := captureStdout(t, func() error { return doCachePrune(context.Background(), svc, nil) })
	assert.Contains(t, out, "Mod M (src:m)")
	assert.Contains(t, out, "- 2.0")
	assert.Contains(t, out, "- 1.0")
	assert.NotContains(t, out, "- 3.0")
	assert.Contains(t, out, "Would free")
	assert.Contains(t, out, "(2 old versions)")
	assert.Contains(t, out, "dry-run: nothing removed")
	assert.Equal(t, cacheTestVersions, stillCached(t, svc, game))
}

func TestCachePrune_RemovesTheOldVersions(t *testing.T) {
	svc, game, _ := setupCacheTest(t)

	out := captureStdout(t, func() error { return doCachePrune(context.Background(), svc, []*domain.Game{game}) })
	assert.Contains(t, out, "✓ Freed")
	assert.Equal(t, []string{"3.0", "4.0", "5.0", "6.0"}, stillCached(t, svc, game))

	out = captureStdout(t, func() error { return doCachePrune(context.Background(), svc, nil) })
	assert.Contains(t, out, "Nothing to prune.")
}

func TestCachePrune_UnderJSONWithoutYesIsRefused(t *testing.T) {
	svc, game, _ := setupCacheTest(t)
	cachePruneYes = false
	withJSONOutput(t)

	err := doCachePrune(context.Background(), svc, nil)
	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	assert.Equal(t, cacheTestVersions, stillCached(t, svc, game))
}

func TestCacheStatus_Table(t *testing.T) {
	svc, _, _ := setupCacheTest(t)

	out := captureStdout(t, func() error { return doCacheStatus(context.Background(), svc, nil) })
	assert.Contains(t, out, "GAME")
	assert.Contains(t, out, "RECLAIMABLE")
	assert.Contains(t, out, "g1")
	assert.Contains(t, out, "(2 versions)")
	assert.Contains(t, out, "run 'lmm cache prune'")
	assert.Contains(t, out, "plus 3 previous versions")
}

func TestCacheKeep_ShowsAndSetsTheSetting(t *testing.T) {
	svc, game, _ := setupCacheTest(t)
	ctx := context.Background()

	out := captureStdout(t, func() error { return doCacheKeep(ctx, svc, nil) })
	assert.Contains(t, out, "plus 3 previous versions")

	out = captureStdout(t, func() error { return doCacheKeep(ctx, svc, []string{"unlimited"}) })
	assert.Contains(t, out, "unlimited")
	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "keep_previous_versions: unlimited")
	captureStdout(t, func() error { return doCachePrune(ctx, svc, nil) })
	assert.Equal(t, cacheTestVersions, stillCached(t, svc, game), "unlimited prunes nothing")

	captureStdout(t, func() error { return doCacheKeep(ctx, svc, []string{"1"}) })
	captureStdout(t, func() error { return doCachePrune(ctx, svc, nil) })
	assert.Equal(t, []string{"5.0", "6.0"}, stillCached(t, svc, game))

	require.Error(t, doCacheKeep(ctx, svc, []string{"lots"}))
}

// TestJSONGolden_Cache pins the `lmm cache` documents. The types are
// goldened at the core level too; these pin that the commands emit them.
func TestJSONGolden_Cache(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		svc, _, cacheDir := setupCacheTest(t)
		withJSONOutput(t)
		out := captureStdout(t, func() error { return doCacheStatus(context.Background(), svc, nil) })
		assertJSONCLIGolden(t, "cache_status", out, cacheDir, "/GOLDEN/cache")
	})
	t.Run("prune_dry_run", func(t *testing.T) {
		svc, _, _ := setupCacheTest(t)
		cachePruneDryRun = true
		withJSONOutput(t)
		out := captureStdout(t, func() error { return doCachePrune(context.Background(), svc, nil) })
		assertJSONCLIGolden(t, "cache_prune_dry_run", out)
	})
	t.Run("prune", func(t *testing.T) {
		svc, game, _ := setupCacheTest(t)
		withJSONOutput(t)
		out := captureStdout(t, func() error { return doCachePrune(context.Background(), svc, nil) })
		assertJSONCLIGolden(t, "cache_prune", out)
		assert.Equal(t, []string{"3.0", "4.0", "5.0", "6.0"}, stillCached(t, svc, game))
	})
	t.Run("keep", func(t *testing.T) {
		svc, _, _ := setupCacheTest(t)
		withJSONOutput(t)
		out := captureStdout(t, func() error { return doCacheKeep(context.Background(), svc, []string{"5"}) })
		assertJSONCLIGolden(t, "cache_keep", out)
	})
}
