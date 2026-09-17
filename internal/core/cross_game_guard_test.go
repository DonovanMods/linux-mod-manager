package core_test

// The #445 second gate's G2-1. With two games on one mod directory, the
// active profile's own purge, its uninstall and a deploy over a file
// ignored the other game's records: `lmm purge -g sky` deleted sky2's live,
// user-edited file (V5d, on v2 as well), following sky's mod_path refusal
// did the same (V5e), and the refusal routed a user there when sky2's
// active profile recorded a file sky's active profile lists (V5c). The
// recorded-only purge already kept such a file (review F7).
//
// Every removal and every overwrite now leaves a path another game records
// exactly as it is, and says so. A profile of this game that lists the
// path's mod cannot take the file over then, so when the other game keeps
// it for its own active profile the file is that game's, and the purged
// profile's record goes; when only another game's non-active profile
// records it, the refusal names that profile's purge first, which lets go.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bothActiveState is V5d: sky and sky2 share one mod directory and both
// active profiles record Data/a.esp - sky's mod k, sky2's mod j - and the
// live file is sky2's copy, which the user has since edited. sky's k also
// ships Data/k.esp, sky's alone.
func bothActiveState(t *testing.T, method domain.LinkMethod) (sky, sky2 *legacyFixture, live string) {
	t.Helper()
	root := t.TempDir()
	sky = newLegacyFixture(t, handoffGame(t, "sky", root, method))
	sky2 = &legacyFixture{svc: sky.svc, game: handoffGame(t, "sky2", root, method)}
	require.NoError(t, sky.svc.SaveGame(context.Background(), sky2.game))
	sky.profile(t, "default", true, "k")
	sky2.profile(t, "default", true, "j")
	sky.deployed(t, "default", "k", method, map[string]string{"Data/a.esp": "sky a", "Data/k.esp": "sky k"}, nil)
	var edited map[string]string
	if method == domain.LinkCopy {
		edited = map[string]string{"Data/a.esp": "sky2 a, USER-EDITED"}
	}
	sky2.deployed(t, "default", "j", method, map[string]string{"Data/a.esp": "sky2 a"}, edited)
	return sky, sky2, filepath.Join(root, "mods", "Data", "a.esp")
}

// liveBytes is what path holds, read through a link.
func liveBytes(t *testing.T, path string) string {
	t.Helper()
	return readLive(t, path)
}

func TestCrossGame_AnActivePurgeKeepsAFileAnotherGameRecords(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink} {
		t.Run(method.String(), func(t *testing.T) {
			sky, sky2, live := bothActiveState(t, method)
			before := liveBytes(t, live)

			plan, err := sky.svc.PlanPurge(ctx, sky.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			require.False(t, plan.RecordedOnly)
			result, err := sky.svc.ApplyPurge(ctx, sky.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)

			assert.Equal(t, before, liveBytes(t, live), "sky2's file is exactly as it was")
			assert.NoFileExists(t, filepath.Join(sky.game.ModPath, "Data", "k.esp"), "sky's own file goes")
			assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptOtherGame, Games: []string{"sky2"}}}, result.Kept)
			assert.Equal(t, 1, result.Purged)
			assert.Empty(t, sky.recorded(t, "default", "k"), "sky's records go: sky2 still tracks the file")
			assert.Equal(t, []string{"Data/a.esp"}, sky2.recorded(t, "default", "j"))

			// sky2's own purge then decides its file.
			plan, err = sky2.svc.PlanPurge(ctx, sky2.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			result, err = sky2.svc.ApplyPurge(ctx, sky2.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			assert.Empty(t, result.Kept)
			assert.NoFileExists(t, live)
		})
	}

	t.Run("under --uninstall", func(t *testing.T) {
		sky, _, live := bothActiveState(t, domain.LinkCopy)
		opts := core.PurgeOptions{Uninstall: true}
		plan, err := sky.svc.PlanPurge(ctx, sky.game, "default", opts)
		require.NoError(t, err)
		result, err := sky.svc.ApplyPurge(ctx, sky.game, plan, opts, nil)
		require.NoError(t, err)
		assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
		assert.Len(t, result.Kept, 1)
	})

	t.Run("deploy --purge", func(t *testing.T) {
		sky, _, live := bothActiveState(t, domain.LinkCopy)
		result, err := sky.svc.DeployProfile(ctx, sky.game, "default", core.DeployOptions{Purge: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
		assert.Contains(t, result.Warnings, "Data/a.esp was left in place: game sky2 records it too")
	})
}

func TestCrossGame_AnUninstallKeepsAFileAnotherGameRecords(t *testing.T) {
	sky, sky2, live := bothActiveState(t, domain.LinkCopy)

	result, err := sky.svc.UninstallMod(context.Background(), sky.game, "default", "local", "k", core.UninstallOptions{})
	require.NoError(t, err)

	assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
	assert.NoFileExists(t, filepath.Join(sky.game.ModPath, "Data", "k.esp"))
	assert.Equal(t, []string{"Data/a.esp was left in place: game sky2 records it too"}, result.Warnings)
	assert.Equal(t, []string{"Data/a.esp"}, sky2.recorded(t, "default", "j"))
}

func TestCrossGame_ADeployDoesNotReplaceAFileAnotherGameRecords(t *testing.T) {
	ctx := context.Background()
	sky, sky2, live := bothActiveState(t, domain.LinkCopy)

	result, err := sky.svc.DeployProfile(ctx, sky.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
	assert.Equal(t, "sky k", liveBytes(t, filepath.Join(sky.game.ModPath, "Data", "k.esp")))
	assert.Equal(t, 1, result.Deployed)
	assert.Contains(t, result.Warnings, "Data/a.esp was not replaced: game sky2 records it too, so lmm left that game's file there")
	assert.ElementsMatch(t, []string{"Data/a.esp", "Data/k.esp"}, sky.recorded(t, "default", "k"),
		"sky still records where its mod's file goes, so sky2's purge keeps the file for it")

	plan, err := sky2.svc.PlanPurge(ctx, sky2.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	purged, err := sky2.svc.ApplyPurge(ctx, sky2.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptOtherGame, Games: []string{"sky"}}}, purged.Kept)
	assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))

	t.Run("a profile switch", func(t *testing.T) {
		sky, _, live := bothActiveState(t, domain.LinkCopy)
		sky.profile(t, "alt", false, "k")
		plan, err := sky.svc.PlanProfileSwitch(ctx, sky.game, "alt")
		require.NoError(t, err)
		result, err := sky.svc.ApplyProfileSwitch(ctx, sky.game, plan, nil)
		require.NoError(t, err)
		assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
		assert.Contains(t, result.Warnings, "Data/a.esp was not replaced: game sky2 records it too, so lmm left that game's file there")
	})
}

// TestCrossGame_V5e_TheRefusalsCommandsKeepTheOtherGamesFile follows sky's
// own mod_path refusal in V5d's state: it names the full purge of sky's
// active profile, which used to delete sky2's edited file.
func TestCrossGame_V5e_TheRefusalsCommandsKeepTheOtherGamesFile(t *testing.T) {
	sky, sky2, live := bothActiveState(t, domain.LinkCopy)

	refusals := followModPathRefusal(t, sky.svc, "sky", filepath.Join(sky.game.InstallPath, "mods2"))

	require.Len(t, refusals, 1)
	assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
	assert.Equal(t, []string{"Data/a.esp"}, sky2.recorded(t, "default", "j"))
	requireActiveListedLive(t, sky.svc, "sky")
}

// sky2ActiveState is V5c: R2's state (crossGameState) with sky2's ACTIVE
// profile x recording Data/a.esp - live for sky2, a copy the user edited -
// instead of its non-active y. sky's default also records Data/b.esp, of a
// mod alt does not list.
func sky2ActiveState(t *testing.T) (sky, sky2 *legacyFixture, live string) {
	t.Helper()
	root := t.TempDir()
	sky = newLegacyFixture(t, handoffGame(t, "sky", root, domain.LinkCopy))
	sky2 = &legacyFixture{svc: sky.svc, game: handoffGame(t, "sky2", root, domain.LinkCopy)}
	require.NoError(t, sky.svc.SaveGame(context.Background(), sky2.game))
	sky.profile(t, "default", false, "k", "m")
	sky.profile(t, "alt", true, "k")
	sky2.profile(t, "x", true, "j")
	sky.deployed(t, "default", "k", domain.LinkCopy, map[string]string{"Data/a.esp": "mod a"}, nil)
	sky.deployed(t, "default", "m", domain.LinkCopy, map[string]string{"Data/b.esp": "mod b"}, nil)
	sky2.deployed(t, "x", "j", domain.LinkCopy, map[string]string{"Data/a.esp": "mod a"}, map[string]string{"Data/a.esp": "sky2's a, USER-EDITED"})
	return sky, sky2, filepath.Join(root, "mods", "Data", "a.esp")
}

func TestCrossGame_V5c_AListedFileTheOtherGamesActiveProfileRecordsIsThatGames(t *testing.T) {
	ctx := context.Background()

	t.Run("the recorded-only purge drops its record", func(t *testing.T) {
		sky, _, live := sky2ActiveState(t)

		plan, result := sky.purge(t, "default")

		assert.Equal(t, []string{"Data/b.esp"}, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptOtherGame, Games: []string{"sky2"}}}, plan.Kept)
		assert.Equal(t, 2, result.Purged)
		assert.Empty(t, sky.recorded(t, "default", "k"))
		assert.Equal(t, "sky2's a, USER-EDITED", liveBytes(t, live))
	})

	t.Run("the refusal's commands keep sky2's file", func(t *testing.T) {
		sky, sky2, live := sky2ActiveState(t)

		refusals := followModPathRefusal(t, sky.svc, "sky", filepath.Join(sky.game.InstallPath, "mods2"))

		require.Len(t, refusals, 1)
		assert.NotContains(t, refusals[0], "lmm profile apply", "alt cannot take the file over, so no apply is named")
		assert.NotContains(t, refusals[0], "--game sky2")
		assert.Equal(t, "sky2's a, USER-EDITED", liveBytes(t, live))
		assert.Equal(t, []string{"Data/a.esp"}, sky2.recorded(t, "x", "j"))

		// alt's apply deploys its mod into the new directory.
		moved, err := sky.svc.GetGame("sky")
		require.NoError(t, err)
		plan, err := sky.svc.PlanProfileApply(ctx, moved, "alt")
		require.NoError(t, err)
		result, err := sky.svc.ApplyProfileApply(ctx, moved, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		assert.Empty(t, result.Warnings)
		requireActiveListedLive(t, sky.svc, "sky")
		assert.Equal(t, "sky2's a, USER-EDITED", liveBytes(t, live))
	})

	// Following the refusal as it stood - an apply of alt, then its purge -
	// replaced and then deleted sky2's file.
	t.Run("an apply and a purge of alt keep it too", func(t *testing.T) {
		sky, _, live := sky2ActiveState(t)
		plan, err := sky.svc.PlanProfileApply(ctx, sky.game, "alt")
		require.NoError(t, err)
		result, err := sky.svc.ApplyProfileApply(ctx, sky.game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		assert.Contains(t, result.Warnings, "Data/a.esp was not replaced: game sky2 records it too, so lmm left that game's file there")
		assert.Equal(t, "sky2's a, USER-EDITED", liveBytes(t, live))

		pplan, err := sky.svc.PlanPurge(ctx, sky.game, "alt", core.PurgeOptions{})
		require.NoError(t, err)
		_, err = sky.svc.ApplyPurge(ctx, sky.game, pplan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, "sky2's a, USER-EDITED", liveBytes(t, live))
	})
}

// TestCrossGame_R2_TheRefusalNamesTheOtherGamesPurgeFirst: in R2 only
// sky2's non-active y records the file alt lists. sky's commands cannot
// replace it while y does, so the refusal names y's purge - which keeps the
// file sky records and lets go of it - before alt's apply.
func TestCrossGame_R2_TheRefusalNamesTheOtherGamesPurgeFirst(t *testing.T) {
	s := crossGameState(t)

	_, err := s.f.svc.SetGameModPath(context.Background(), "sky", filepath.Join(s.f.game.InstallPath, "mods2"))

	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, []core.OtherGameProfile{{GameID: "sky2", Profile: "y"}}, inUse.ReleaseFirst)
	text := err.Error()
	release := strings.Index(text, "`lmm purge --game sky2 --profile y`")
	apply := strings.Index(text, "`lmm profile apply alt --game sky`")
	require.GreaterOrEqual(t, release, 0, text)
	assert.Less(t, release, apply, text)
	assert.NotContains(t, text, "--game sky2 --profile x", "sky2's active profile is never named")
}

// TestCrossGame_AnOtherGameWhoseActiveProfileIsUnknownGetsNothing: sky2's
// active profile file cannot be read. sky's purge hands nothing to sky2,
// and sky's refusal cannot name sky2's purge - it could be sky2's whole
// deployment - so it says what blocks instead.
func TestCrossGame_AnOtherGameWhoseActiveProfileIsUnknownGetsNothing(t *testing.T) {
	ctx := context.Background()
	s := crossGameState(t)
	x := filepath.Join(s.f.svc.ConfigDir(), "games", "sky2", "profiles", "x.yaml")
	require.NoError(t, os.WriteFile(x, []byte("name: x\n  game_id: [broken\n"), 0o644))
	live := filepath.Join(s.f.game.ModPath, "Data", "a.esp")
	before := liveBytes(t, live)

	plan, err := s.f.svc.PlanPurge(ctx, s.f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}}}, plan.Kept)

	_, err = s.f.svc.SetGameModPath(ctx, "sky", filepath.Join(s.f.game.InstallPath, "mods2"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrActiveProfileUnknown), err.Error())
	assert.Contains(t, err.Error(), "game sky2, which shares the directory, records Data/a.esp too")
	assert.NotContains(t, err.Error(), "--game sky2 --profile")
	assert.Equal(t, before, liveBytes(t, live))
}
