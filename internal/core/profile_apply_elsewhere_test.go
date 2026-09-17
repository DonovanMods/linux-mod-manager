package core_test

// `lmm profile apply <active>` is what the mod_path refusal names first
// (#427, #445 audit) when the active profile lists a mod whose live file
// only another profile records - the state a v1.30.1 switch between two
// profiles sharing a mod left (l1Fixture). The apply has to record that
// file under the active profile, and it used to fail instead: the mod has
// no row there, so it was fetched from its source, and a local mod has
// none ("source not found: local"). A mod another profile of the game has,
// at the listed version and with its bytes in the cache, is now deployed
// from that cache entry, and the enable mints this profile's row - as a
// profile switch (#60) and a profile import (#371) already do.
//
// #467 rides along: the enable loop now records deployed = 1, as a
// switch's does, so a later switch sees the files as live.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	cachepkg "github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// applyProfile plans and applies `lmm profile apply profile`.
func applyProfile(t *testing.T, svc *core.Service, game *domain.Game, profile string) (*core.ProfileApplyPlan, *core.ProfileApplyResult) {
	t.Helper()
	ctx := context.Background()
	plan, err := svc.PlanProfileApply(ctx, game, profile)
	require.NoError(t, err)
	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	return plan, result
}

func TestProfileApply_AModAnotherProfileHasIsDeployedFromItsCache(t *testing.T) {
	ctx := context.Background()

	t.Run("the state a v1.30.1 switch leaves", func(t *testing.T) {
		f := l1Fixture(t)
		// default's row names the archive it came from, complete in the
		// cache, and carries its checksum.
		cache := f.svc.GetGameCache(f.game)
		require.NoError(t, cachepkg.MarkFileComplete(cache.ModPath("sky", "local", "a", "unknown"), "a.zip"))
		row, err := f.svc.GetInstalledMod(ctx, "local", "a", "sky", "default")
		require.NoError(t, err)
		row.FileIDs = []string{"a.zip"}
		require.NoError(t, f.svc.SaveInstalledMod(ctx, row))
		require.NoError(t, f.svc.SaveFileChecksum(ctx, "local", "a", "sky", "default", "a.zip", "abc123"))

		plan, result := applyProfile(t, f.svc, f.game, "alt")

		assert.Empty(t, plan.ToInstall, "nothing to fetch: default has a's bytes")
		require.Len(t, plan.ToEnable, 1)
		assert.Equal(t, "a", plan.ToEnable[0].ID)
		assert.Empty(t, result.Failed)
		assert.Equal(t, 1, result.Enabled)
		assert.FileExists(t, filepath.Join(f.game.ModPath, "Data", "a.esp"))
		assert.Equal(t, []string{"Data/a.esp"}, f.recorded(t, "alt", "a"), "alt now records the file it lists")

		row, err = f.svc.GetInstalledMod(ctx, "local", "a", "sky", "alt")
		require.NoError(t, err)
		assert.True(t, row.Enabled)
		assert.True(t, row.Deployed)
		assert.Equal(t, "unknown", row.Version)
		assert.Equal(t, []string{"a.zip"}, row.FileIDs)
		checksum, err := f.svc.GetFileChecksumForTest(ctx, "local", "a", "sky", "alt", "a.zip")
		require.NoError(t, err)
		assert.Equal(t, "abc123", checksum, "the row carries default's checksums, as an import's copy does")

		other, err := f.svc.GetInstalledMod(ctx, "local", "a", "sky", "default")
		require.NoError(t, err)
		assert.True(t, other.Enabled, "default's row is its own")
		assert.Equal(t, []string{"Data/a.esp"}, f.recorded(t, "default", "a"))

		again, err := f.svc.PlanProfileApply(ctx, f.game, "alt")
		require.NoError(t, err)
		assert.True(t, again.NoChanges)
	})

	t.Run("another version is still fetched", func(t *testing.T) {
		f := l1Fixture(t)
		f.profile(t, "alt", true)
		path := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml")
		require.NoError(t, os.WriteFile(path, []byte("name: alt\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: a\n      version: \"2.0\"\nis_default: true\n"), 0o644))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "alt")
		require.NoError(t, err)
		assert.Empty(t, plan.ToEnable)
		require.Len(t, plan.ToInstall, 1)
		assert.Contains(t, plan.ToInstall[0].Error, "source not found")
	})

	t.Run("a mod whose bytes are gone is still fetched", func(t *testing.T) {
		f := l1Fixture(t)
		require.NoError(t, os.RemoveAll(f.svc.GetGameCache(f.game).ModPath("sky", "local", "a", "unknown")))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "alt")
		require.NoError(t, err)
		assert.Empty(t, plan.ToEnable)
		require.Len(t, plan.ToInstall, 1)
	})

	t.Run("a live deployment of another version is not installed beside", func(t *testing.T) {
		f := l1Fixture(t)
		f.profile(t, "survival", false, "a")
		require.NoError(t, f.svc.GetGameCache(f.game).Store("sky", "local", "a", "0.9", "Data/a.esp", []byte("old a")))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "a", SourceID: "local", Name: "Mod a", Version: "0.9", GameID: "sky"},
			ProfileName: "survival", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true, LinkMethod: domain.LinkSymlink,
		}))
		path := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml")
		require.NoError(t, os.WriteFile(path, []byte("name: alt\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: a\n      version: unknown\nis_default: true\n"), 0o644))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "alt")
		require.NoError(t, err)
		assert.Empty(t, plan.ToEnable, "an enable cannot replace the live 0.9")
		require.Len(t, plan.ToInstall, 1)
	})
}

// TestProfileApply_RecordsDeployedOnTheRowsItEnables is #467: an enabled
// row whose files the apply put live says so, and a switch away then sees
// them as live - it replaces them with the target's version rather than
// installing that beside them.
func TestProfileApply_RecordsDeployedOnTheRowsItEnables(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	writeProfileFile(t, f.svc, "sky", "default", "name: default\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: m\n      version: \"1.0\"\nis_default: true\n")
	f.profile(t, "alt", false)
	cache := f.svc.GetGameCache(f.game)
	require.NoError(t, cache.Store("sky", "local", "m", "1.0", "old.esp", []byte("m 1.0")))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "m", SourceID: "local", Name: "Mod m", Version: "1.0", GameID: "sky"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: false, LinkMethod: domain.LinkSymlink,
	}))

	plan, result := applyProfile(t, f.svc, f.game, "default")
	require.Len(t, plan.ToEnable, 1)
	require.Equal(t, 1, result.Enabled)
	row, err := f.svc.GetInstalledMod(ctx, "local", "m", "sky", "default")
	require.NoError(t, err)
	assert.True(t, row.Enabled)
	assert.True(t, row.Deployed, "the files are live, and the row says so")

	// alt lists m at 2.0, a different file.
	require.NoError(t, cache.Store("sky", "local", "m", "2.0", "new.esp", []byte("m 2.0")))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "m", SourceID: "local", Name: "Mod m", Version: "2.0", GameID: "sky"},
		ProfileName: "alt", UpdatePolicy: domain.UpdateNotify, Enabled: false, LinkMethod: domain.LinkSymlink,
	}))
	path := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: alt\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: m\n      version: \"2.0\"\n"), 0o644))

	switchPlan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	assert.Contains(t, switchPlan.PriorVersions, domain.ModKey("local", "m"), "the switch sees default's 1.0 as live")
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, switchPlan, nil)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.game.ModPath, "old.esp"), "1.0 is replaced, not left beside 2.0")
	assert.FileExists(t, filepath.Join(f.game.ModPath, "new.esp"))
}
