package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeployProfile_SelfHealsStaleUnclaimedPak is #210's acceptance test:
// a pre-fix deploy linked a stale unclaimed pak into the game dir; one
// deploy cycle must remove that link (Uninstall's union direction) and not
// re-create it (Install's manifest-aware direction).
//
// Fixture: installed mod m1@1.4, cache entry = stale "Stale_P.pak" on disk
// plus a recorded "exmodz" marker with zero members AND a retained source
// file (the amended resolver semantics: deployableFiles only narrows when
// every marker is recorded AND the entry holds a retained source). Game dir
// already contains a symlink Stale_P.pak -> the cached stale pak (simulating
// the pre-fix deployment).
//
// After DeployProfile: game dir does NOT contain Stale_P.pak; result has
// Deployed == 1 (the mod "deploys" successfully with zero files of its
// own); no Skipped entries.
//
// This would fail against pre-#210 code: Install re-linked the full cache
// union (deployableFiles narrowing didn't exist), so Stale_P.pak would be
// re-created immediately after Uninstall removed it, and the Lstat below
// would find the stale symlink still present.
func TestDeployProfile_SelfHealsStaleUnclaimedPak(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "m1", "1.4", true, map[string][]byte{
		"Stale_P.pak": []byte("stale-pak-content"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.4")

	gameCache := svc.GetGameCache(game)
	versionDir := gameCache.ModPath(game.ID, "src", "m1", "1.4")
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "exmodz", nil))
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))

	// Simulate the pre-fix deployment: a symlink in the game dir pointing at
	// the now-stale cached pak.
	stalePath := filepath.Join(gameDir, "Stale_P.pak")
	require.NoError(t, os.Symlink(filepath.Join(versionDir, "Stale_P.pak"), stalePath))

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	_, err = os.Lstat(stalePath)
	assert.True(t, os.IsNotExist(err), "stale pak symlink must be removed, not re-linked")
	assert.Equal(t, 1, result.Deployed)
	assert.Empty(t, result.Skipped)
}
