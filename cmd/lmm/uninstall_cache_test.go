package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modCacheDir returns the per-mod cache container directory (the version
// directory's parent) that #190 item 4's cleanup must remove once empty.
func modCacheDir(svc *core.Service, game *domain.Game, sourceID, modID, version string) string {
	return filepath.Dir(svc.GetGameCache(game).ModPath(game.ID, sourceID, modID, version))
}

// resetUninstallFlags saves and resets uninstall's package-level flag
// globals for a test using setupDoDeployTest's fixture (which resets
// deploy's own globals, not uninstall's) to drive doUninstall directly.
func resetUninstallFlags(t *testing.T) {
	t.Helper()
	oldSource, oldProfile, oldKeep, oldForce := uninstallSource, uninstallProfile, uninstallKeep, uninstallForce
	uninstallSource = ""
	uninstallProfile = ""
	uninstallKeep = false
	uninstallForce = false
	t.Cleanup(func() {
		uninstallSource, uninstallProfile, uninstallKeep, uninstallForce = oldSource, oldProfile, oldKeep, oldForce
	})
}

// TestDoUninstall_RemovesNowEmptyModCacheDirectory guards #190 item 4:
// uninstalling a mod's only cached version left the empty per-mod cache
// directory (<source>-<modID>/) behind. It must be removed, but only
// because it's now empty - the underlying behavior lives in
// internal/storage/cache.Cache.Delete, exercised here end-to-end through
// the real CLI command.
func TestDoUninstall_RemovesNowEmptyModCacheDirectory(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetUninstallFlags(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	dir := modCacheDir(svc, game, "src", "a", "1.0")
	require.DirExists(t, dir)

	_ = captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "a")
	})

	assert.NoDirExists(t, dir, "the now-empty per-mod cache directory must not be left behind")
}

// TestDoUninstall_KeepCache_LeavesModCacheDirectoryIntact is the negative
// case: --keep-cache must never trigger the cleanup, matching its own
// contract of preserving cached files for reinstallation.
func TestDoUninstall_KeepCache_LeavesModCacheDirectoryIntact(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetUninstallFlags(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	uninstallKeep = true

	dir := modCacheDir(svc, game, "src", "a", "1.0")
	require.DirExists(t, dir)

	_ = captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "a")
	})

	assert.DirExists(t, dir, "--keep-cache must preserve the cache directory entirely")
}

// TestDoUninstall_SaysWhichRowKeepsTheCacheEntry is #445 gate 2's G2-2 at
// the CLI: a cache entry another profile's row still uses is kept, and both
// the dry run and the uninstall say so, naming that profile.
func TestDoUninstall_SaysWhichRowKeepsTheCacheEntry(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetUninstallFlags(t)
	oldDryRun := uninstallDryRun
	t.Cleanup(func() { uninstallDryRun = oldDryRun })
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedLiveRowUnderProfile(t, svc, game, "other", "a", "Mod A")
	dir := svc.GetGameCache(game).ModPath(game.ID, "src", "a", "1.0")

	uninstallDryRun = true
	out := captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "a")
	})
	assert.Contains(t, out, "  Cache entry kept: profile other still uses it\n")
	assert.NotContains(t, out, "Cache entry would be deleted")

	uninstallDryRun = false
	out = captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "a")
	})
	assert.Contains(t, out, "✓ Uninstalled: Mod A\n  Removed from profile: default\n  Cache files kept: profile other still uses them\n")
	assert.DirExists(t, dir)
}
