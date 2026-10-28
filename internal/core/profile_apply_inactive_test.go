package core_test

// #462, and the #445 final gate's F-B: `lmm profile apply <profile that is
// not active>` deployed that profile's mods into the game directory, which
// holds the active profile's - and since 75cc4c2a it did so offline, for
// any mod another profile has cached. An apply is a deploy-direction write,
// so it follows deploy's rule: a profile that is not active is refused,
// naming `lmm profile switch`, at the plan and again at the apply; a game
// whose profile files do not say which profile is active refuses every
// apply. `lmm profile apply <active>` - the mod_path refusal's remedy -
// keeps working.

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

func TestProfileApply_ANonActiveProfileIsRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("plan", func(t *testing.T) {
		f := liveDirFixture(t)
		before := treeOf(t, f.gameDir)

		_, err := f.svc.PlanProfileApply(ctx, f.game, "b")

		requireRefusedForB(t, err)
		assert.Contains(t, err.Error(), "cannot apply profile")
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("apply of a plan made while it was active", func(t *testing.T) {
		f := liveDirFixture(t)
		pm := f.svc.NewProfileManager()
		require.NoError(t, f.svc.SetModEnabledForTest(ctx, "src", "bonly", f.game.ID, "b", false))
		require.NoError(t, pm.SetDefault(ctx, f.game.ID, "b"))
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "b")
		require.NoError(t, err)
		require.False(t, plan.NoChanges, "b has a mod to enable")
		require.NoError(t, pm.SetDefault(ctx, f.game.ID, "a"))
		before := treeOf(t, f.gameDir)

		_, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)

		requireRefusedForB(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir), "nothing of b's reached the game directory")
		assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
	})

	// The gate's A1: another profile's cached mod, which the apply would
	// have deployed from the cache.
	t.Run("a mod another profile has cached", func(t *testing.T) {
		f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
		f.profile(t, "default", true, "mod-a")
		f.profile(t, "survival", false, "mod-s")
		f.profile(t, "alt", false, "mod-s")
		f.deployed(t, "default", "mod-a", domain.LinkSymlink, map[string]string{"Data/a.esp": "a"}, nil)
		require.NoError(t, f.svc.GetGameCache(f.game).Store("sky", "local", "mod-s", "unknown", "Data/s.esp", []byte("s")))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "mod-s", SourceID: "local", Name: "Mod s", Version: "unknown", GameID: "sky"},
			ProfileName: "survival", UpdatePolicy: domain.UpdateNotify,
		}))
		before := treeOf(t, f.game.ModPath)

		_, err := f.svc.PlanProfileApply(ctx, f.game, "alt")

		require.ErrorIs(t, err, core.ErrProfileNotActive)
		assert.Contains(t, err.Error(), "lmm profile switch alt")
		assert.Equal(t, before, treeOf(t, f.game.ModPath))
		rows, err := f.svc.GetInstalledMods(ctx, "sky", "alt")
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	t.Run("with no profile marked active", func(t *testing.T) {
		f := liveDirFixture(t)
		f.setFlag(t, "a", false)

		for _, profile := range []string{"a", "b"} {
			_, err := f.svc.PlanProfileApply(ctx, f.game, profile)
			requireActiveUnknown(t, err)
		}
	})

	t.Run("with a profile file unreadable at the apply", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
		require.NoError(t, err)
		f.breakProfile(t, "b")

		_, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)

		requireActiveUnknown(t, err, f.profilePath("b"))
	})

	t.Run("the active profile still applies", func(t *testing.T) {
		f := liveDirFixture(t)
		require.NoError(t, os.Remove(filepath.Join(f.gameDir, "aonly.esp")))
		require.NoError(t, f.svc.SetModEnabledForTest(ctx, "src", "aonly", f.game.ID, "a", false))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
		require.NoError(t, err)
		require.Len(t, plan.ToEnable, 1)
		result, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Enabled)
		assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	})
}
