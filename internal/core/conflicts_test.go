package core_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTwinConflict seeds two named, enabled mods that both provide the same
// shared paths (plus any distinct extras), lists them in the "default"
// profile in the given order, and deploys the profile for real - so the DB's
// deployed_files owner reflects Task 1's deploy-order-wins rule (last in
// profile order deploys last and owns each shared path). Returns the game.
func seedTwinConflict(t *testing.T, svc *core.Service, firstFiles, secondFiles map[string][]byte) *domain.Game {
	t.Helper()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "modX", "Mod X", "1.0", true, firstFiles)
	seedNamedInstalledMod(t, svc, game, "src", "modY", "Mod Y", "1.0", true, secondFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "modX", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "modY", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return game
}

// TestGetProfileConflictsWinnerAndStale: with the profile order [modX, modY],
// modY deploys last, owns the shared path, and is also the load-order winner
// -> Stale false. Flipping the profile order WITHOUT redeploying moves the
// winner to modX while the DB owner stays modY -> Stale true, winner = new
// last provider.
func TestGetProfileConflictsWinnerAndStale(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedTwinConflict(t, svc,
		map[string][]byte{"shared.esp": []byte("X-content")},
		map[string][]byte{"shared.esp": []byte("Y-content")},
	)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 1)

	c := conflicts[0]
	assert.Equal(t, "shared.esp", c.Path)
	assert.Equal(t, core.ConflictModRef{Key: "src:modY", Name: "Mod Y"}, c.Owner)
	assert.Equal(t, core.ConflictModRef{Key: "src:modY", Name: "Mod Y"}, c.LoadOrderWinner)
	assert.Equal(t, []core.ConflictModRef{{Key: "src:modX", Name: "Mod X"}}, c.AlsoIn)
	assert.False(t, c.Stale)

	require.NoError(t, svc.NewProfileManager().ReorderMods(context.Background(), "g1", "default", []domain.ModReference{
		{SourceID: "src", ModID: "modY", Version: "1.0"},
		{SourceID: "src", ModID: "modX", Version: "1.0"},
	}))

	conflicts, err = svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 1)

	c = conflicts[0]
	assert.Equal(t, "src:modY", c.Owner.Key, "DB owner must be unchanged without a redeploy")
	assert.Equal(t, core.ConflictModRef{Key: "src:modX", Name: "Mod X"}, c.LoadOrderWinner,
		"after the reorder, modX is last in profile order and must be the winner")
	assert.True(t, c.Stale, "owner != load-order winner must flag the conflict stale")
}

// TestGetProfileConflictsSortedByPath: multiple conflicting paths must come
// back sorted by Path - a declared change from the pre-extraction CLI, whose
// conflict order was Go map-iteration random.
func TestGetProfileConflictsSortedByPath(t *testing.T) {
	svc := newFlowsTestService(t)
	shared := func(tag string) map[string][]byte {
		return map[string][]byte{
			"zebra.esp":  []byte(tag),
			"alpha.esp":  []byte(tag),
			"middle.esp": []byte(tag),
		}
	}
	game := seedTwinConflict(t, svc, shared("X"), shared("Y"))

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 3)

	var paths []string
	for _, c := range conflicts {
		paths = append(paths, c.Path)
	}
	assert.Equal(t, []string{"alpha.esp", "middle.esp", "zebra.esp"}, paths)
}

// TestGetProfileConflictsUnlistedProvider: a provider absent from
// profile.Mods sorts before every listed provider (mirroring OrderByProfile),
// so any listed provider beats it - the listed mod is the winner even though
// both provide the shared path.
func TestGetProfileConflictsUnlistedProvider(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "listed", "Listed Mod", "1.0", true, map[string][]byte{"shared.esp": []byte("L")})
	seedNamedInstalledMod(t, svc, game, "src", "unlisted", "Unlisted Mod", "1.0", true, map[string][]byte{"shared.esp": []byte("U")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "listed", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 1)

	c := conflicts[0]
	assert.Equal(t, "shared.esp", c.Path)
	assert.Equal(t, core.ConflictModRef{Key: "src:listed", Name: "Listed Mod"}, c.Owner)
	assert.Equal(t, core.ConflictModRef{Key: "src:listed", Name: "Listed Mod"}, c.LoadOrderWinner,
		"any listed provider must beat any unlisted one")
	assert.Equal(t, []core.ConflictModRef{{Key: "src:unlisted", Name: "Unlisted Mod"}}, c.AlsoIn)
	assert.False(t, c.Stale)
}

// TestGetProfileConflictsNone: mods with fully distinct file sets produce an
// empty result and a nil error.
func TestGetProfileConflictsNone(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedTwinConflict(t, svc,
		map[string][]byte{"x-only.esp": []byte("X")},
		map[string][]byte{"y-only.esp": []byte("Y")},
	)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	assert.NoError(t, err)
	assert.Empty(t, conflicts)
}

// TestGetProfileConflictsDisabledProviderIgnored guards the enabled-only
// rule: only enabled mods participate in deployment, so a disabled mod's
// provided files must not count as conflicting providers.
func TestGetProfileConflictsDisabledProviderIgnored(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "on", "Enabled Mod", "1.0", true, map[string][]byte{"shared.esp": []byte("on")})
	seedNamedInstalledMod(t, svc, game, "src", "off", "Disabled Mod", "1.0", false, map[string][]byte{"shared.esp": []byte("off")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "on", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "off", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	assert.NoError(t, err)
	assert.Empty(t, conflicts, "a disabled provider must not create a conflict")
}

// TestGetProfileConflictsMissingCacheSkipsMod guards graceful handling of a
// mod whose cache entry is gone (e.g. manually deleted): it contributes no
// provided files - the query must skip it, not error.
func TestGetProfileConflictsMissingCacheSkipsMod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedTwinConflict(t, svc,
		map[string][]byte{"shared.esp": []byte("X-content")},
		map[string][]byte{"shared.esp": []byte("Y-content")},
	)

	// Sanity: the conflict is visible while both caches exist.
	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 1)

	// Delete modX's cache entry: modY is now the only provider left, so the
	// conflict disappears rather than erroring the whole query.
	require.NoError(t, svc.GetGameCache(game).Delete("g1", "src", "modX", "1.0"))

	conflicts, err = svc.GetProfileConflicts(context.Background(), game, "default")
	assert.NoError(t, err)
	assert.Empty(t, conflicts)
}

// TestGetProfileConflictsCancelledContext pins the ctx.Err() check between
// per-mod cache walks (Copilot PR #73 round 2): a cancelled context returns
// promptly with the context's error instead of walking remaining caches.
func TestGetProfileConflictsCancelledContext(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedTwinConflict(t, svc,
		map[string][]byte{"shared.esp": []byte("X-content")},
		map[string][]byte{"shared.esp": []byte("Y-content")},
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conflicts, err := svc.GetProfileConflicts(ctx, game, "default")
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, conflicts)
}

// TestGetProfileConflictsCacheReadErrorPropagates pins the skip-vs-fail
// split (Copilot PR #73 round 3): a MISSING cache entry skips the mod
// (TestGetProfileConflictsMissingCacheSkipsMod), but any other cache read
// failure (here: permissions) aborts the query with the error instead of
// silently under-reporting conflicts.
func TestGetProfileConflictsCacheReadErrorPropagates(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	svc := newFlowsTestService(t)
	game := seedTwinConflict(t, svc,
		map[string][]byte{"shared.esp": []byte("X-content")},
		map[string][]byte{"shared.esp": []byte("Y-content")},
	)

	modXCache := svc.GetGameCache(game).ModPath("g1", "src", "modX", "1.0")
	require.NoError(t, os.Chmod(modXCache, 0o000))
	t.Cleanup(func() { _ = os.Chmod(modXCache, 0o755) })

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.Error(t, err)
	assert.ErrorContains(t, err, "listing cache files")
	assert.Nil(t, conflicts)
}

// TestGetProfileConflictsOwnerLookupErrorPropagates pins the storage-error
// split on GetFileOwner (Copilot PR #73 round 4): "no owner recorded" skips
// the path (pre-extraction behavior), but a genuine storage failure aborts
// the query instead of silently under-reporting conflicts. The failure is
// forced deterministically by dropping deployed_files via a second SQLite
// connection (the installVersionBlockingTrigger technique, flows_rollback_test.go).
func TestGetProfileConflictsOwnerLookupErrorPropagates(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := seedTwinConflict(t, svc,
		map[string][]byte{"shared.esp": []byte("X-content")},
		map[string][]byte{"shared.esp": []byte("Y-content")},
	)

	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.Exec(`DROP TABLE deployed_files`)
	require.NoError(t, err)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.Error(t, err)
	assert.ErrorContains(t, err, "file owner")
	assert.Nil(t, conflicts)
}

// TestProfileConflicts_IgnoresUnclaimedCacheFiles: a stale unclaimed file in
// mod A's cache entry that collides with a path mod B legitimately provides
// must NOT be reported - deploy will never link it (#210).
//
// Fixture: mod A's cache entry has a recorded, zero-member "exmodz" marker
// plus a retained compile source (the full manifest-aware narrowing gate),
// but "shared.pak" on disk is claimed by no manifest member. Mod B's entry
// claims "shared.pak" via its own recorded "exmodz" marker plus retained
// source. Expect: no conflict on "shared.pak" (A provides nothing).
func TestProfileConflicts_IgnoresUnclaimedCacheFiles(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "modA", "Mod A", "1.0", true, map[string][]byte{"shared.pak": []byte("A-content")})
	seedNamedInstalledMod(t, svc, game, "src", "modB", "Mod B", "1.0", true, map[string][]byte{"shared.pak": []byte("B-content")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "modA", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "modB", "1.0")

	gameCache := svc.GetGameCache(game)

	dirA := gameCache.ModPath("g1", "src", "modA", "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(dirA, "exmodz", nil))
	require.NoError(t, os.WriteFile(filepath.Join(dirA, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))

	dirB := gameCache.ModPath("g1", "src", "modB", "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(dirB, "exmodz", []string{"shared.pak"}))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Empty(t, conflicts, "mod A's unclaimed shared.pak must not be reported as a conflict provider")
}
