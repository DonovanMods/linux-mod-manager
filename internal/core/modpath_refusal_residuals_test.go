package core_test

// #469: states the mod_path in-use refusal (#427) held with nothing the
// user could run to clear it. Each runs the refusal's own commands
// (followModPathRefusal), then checks the move went through and the user's
// file is exactly as it was.

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

// userFileAt writes the user's own regular file at rel under f's mod_path,
// replacing whatever is there.
func userFileAt(t *testing.T, f *legacyFixture, rel, content string) string {
	t.Helper()
	path := filepath.Join(f.game.ModPath, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	if err := os.Remove(path); err != nil {
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// TestModPathRefusal_AnOrphanedActiveRecordIsClearable is state (a): the
// active profile records a file for a mod it has no installed row for.
func TestModPathRefusal_AnOrphanedActiveRecordIsClearable(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	f.profile(t, "default", true, "a")
	f.deployed(t, "default", "a", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	user := userFileAt(t, f, "Data/ghost.esp", "the user's")
	require.NoError(t, f.svc.ExecForTest(ctx,
		`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES ('sky', 'default', 'Data/ghost.esp', 'local', 'ghost')`))

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	orphan := findingWithStatus(report.Result, "orphaned_record")
	require.NotNil(t, orphan, "statuses were %v", findingStatuses(report.Result))
	assert.True(t, orphan.Fixable)
	assert.Contains(t, orphan.Note, "Data/ghost.esp")

	refusals := followModPathRefusal(t, f.svc, "sky", t.TempDir())

	require.NotEmpty(t, refusals)
	assert.Contains(t, refusals[0], "lmm verify --fix --game sky --profile default")
	assert.Equal(t, "the user's", readLive(t, user), "verify --fix removed nothing")
	assert.Empty(t, f.recorded(t, "default", "ghost"))
}

// TestVerify_AnOrphanedRecordOfAnotherProfileNamesItsPurge: --fix drops
// orphaned records only for the active profile; another profile's are its
// recorded-only purge's to judge.
func TestVerify_AnOrphanedRecordOfAnotherProfileNamesItsPurge(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	f.profile(t, "default", true)
	f.profile(t, "alt", false)
	require.NoError(t, f.svc.ExecForTest(ctx,
		`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES ('sky', 'alt', 'Data/ghost.esp', 'local', 'ghost')`))

	report, err := f.svc.VerifyReport(ctx, f.game, "alt", core.VerifyOptions{Fix: true, Force: true}, nil)

	require.NoError(t, err)
	orphan := findingWithStatus(report.Result, "orphaned_record")
	require.NotNil(t, orphan, "statuses were %v", findingStatuses(report.Result))
	assert.False(t, orphan.Fixable)
	assert.Contains(t, orphan.FixableReason, "lmm purge --game sky --profile alt")
	assert.Equal(t, []string{"Data/ghost.esp"}, f.recorded(t, "alt", "ghost"), "the record stays for the purge")
}

// TestModPathRefusal_ALinkReplacedByAUserFileIsClearable is state (b): a
// symlink profile's recorded path now holds the user's regular file.
func TestModPathRefusal_ALinkReplacedByAUserFileIsClearable(t *testing.T) {
	for _, profile := range []string{"alt", "default"} {
		t.Run("recorded by "+profile, func(t *testing.T) {
			f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
			f.profile(t, "default", true, "x")
			f.profile(t, "alt", false, "b")
			f.deployed(t, "default", "x", domain.LinkSymlink, map[string]string{"Data/x.esp": "mod x"}, nil)
			f.deployed(t, profile, "b", domain.LinkSymlink, map[string]string{"Data/b.esp": "mod b"}, nil)
			user := userFileAt(t, f, "Data/b.esp", "the user's own b")

			refusals := followModPathRefusal(t, f.svc, "sky", t.TempDir())

			require.NotEmpty(t, refusals)
			assert.Equal(t, "the user's own b", readLive(t, user), "the user's file is untouched")
			assert.Empty(t, f.recorded(t, profile, "b"), "and lmm no longer records it")
		})
	}

	t.Run("the recorded-only purge says why it kept it", func(t *testing.T) {
		f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
		f.profile(t, "default", true)
		f.profile(t, "alt", false, "b")
		f.deployed(t, "alt", "b", domain.LinkSymlink, map[string]string{"Data/b.esp": "mod b"}, nil)
		userFileAt(t, f, "Data/b.esp", "the user's own b")

		plan := f.purgePlan(t, "alt")

		assert.Empty(t, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/b.esp", Reason: core.PurgeKeptUserFile, Note: "you replaced lmm's link with your own file"}}, plan.Kept)
	})
}

// TestModPathRefusal_AListedVersionThatNoLongerShipsThePathIsClearable is
// state (c): another profile recorded a file of a remote mod the active
// profile lists at a version that no longer ships it, so no apply or
// deploy the refusal named could record it, and the purge kept it for the
// active profile forever.
func TestModPathRefusal_AListedVersionThatNoLongerShipsThePathIsClearable(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	gameCache := f.svc.GetGameCache(f.game)
	require.NoError(t, gameCache.Store("sky", "nexus", "m", "1.0", "Data/old.esp", []byte("m 1.0")))
	require.NoError(t, gameCache.Store("sky", "nexus", "m", "2.0", "Data/new.esp", []byte("m 2.0")))
	dir := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alt.yaml"), []byte("name: alt\ngame_id: sky\nmods:\n    - source_id: nexus\n      mod_id: m\n      version: \"2.0\"\nis_default: true\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "default.yaml"), []byte("name: default\ngame_id: sky\nmods:\n    - source_id: nexus\n      mod_id: m\n      version: \"1.0\"\n"), 0o644))
	for _, v := range []struct{ profile, version, rel string }{{"alt", "2.0", "Data/new.esp"}, {"default", "1.0", "Data/old.esp"}} {
		dst := filepath.Join(f.game.ModPath, filepath.FromSlash(v.rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
		require.NoError(t, os.Symlink(filepath.Join(gameCache.ModPath("sky", "nexus", "m", v.version), filepath.FromSlash(v.rel)), dst))
		require.NoError(t, f.svc.ExecForTest(ctx,
			`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES ('sky', ?, ?, 'nexus', 'm')`, v.profile, v.rel))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "m", SourceID: "nexus", Name: "Mod m", Version: v.version, GameID: "sky"},
			ProfileName: v.profile, UpdatePolicy: domain.UpdateNotify, Enabled: v.profile == "alt", Deployed: true, LinkMethod: domain.LinkSymlink,
		}))
	}

	plan := f.purgePlan(t, "default")
	assert.Equal(t, []string{"Data/old.esp"}, plan.Remove, "2.0 does not ship it, so it is not kept for alt")
	require.Len(t, plan.Unshipped, 1)
	assert.Contains(t, plan.Unshipped[0].String(), "nexus:m at 2.0")

	refusals := followModPathRefusal(t, f.svc, "sky", t.TempDir())

	require.NotEmpty(t, refusals)
	assert.Empty(t, f.recorded(t, "default", "m"))
}

// TestVerify_ARowWhoseCacheEntryIsGoneIsAFinding is #469's D2: a mod with no
// file record was never checked against the cache at all. The row models a
// downloaded mod - a source other than local, a version, not adopted in
// place - since only such a row ever had a cache entry to lose.
func TestVerify_ARowWhoseCacheEntryIsGoneIsAFinding(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	f.profile(t, "default", true)
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "a", SourceID: "acme", Name: "A", Version: "1.0", GameID: "sky"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
	}))

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)

	require.NoError(t, err)
	gone := findingWithStatus(report.Result, "missing_cache")
	require.NotNil(t, gone, "statuses were %v", findingStatuses(report.Result))
	assert.Equal(t, "a", gone.ModID)
	assert.Equal(t, "version 1.0 is not in the cache any more", gone.Note)
	assert.False(t, gone.Fixable)
	assert.Contains(t, gone.FixableReason, "--source acme")
	assert.Contains(t, gone.FixableReason, "uninstall it")
	assert.Positive(t, report.Result.Issues)
}

// TestVerify_AModAdoptedInPlaceIsNotMissingItsCache: `lmm import` adopts a
// mod on a non-copy game in place and writes no cache entry by design
// (adoptScannedMod), so the D2 pass above must not call that entry "gone" -
// nor name `lmm install --source local`, which can install nothing.
func TestVerify_AModAdoptedInPlaceIsNotMissingItsCache(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []domain.DeployMode{domain.DeployExtract, domain.DeployCompile} {
		t.Run(mode.String(), func(t *testing.T) {
			svc, game := newAdoptTestService(t)
			game.DeployMode = mode
			require.NoError(t, svc.SaveGame(ctx, game))
			require.NoError(t, os.MkdirAll(filepath.Join(game.ModPath, "CoolMod"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "CoolMod", "cool.esp"), []byte("cool"), 0o644))

			plan, err := svc.PlanAdopt(ctx, game, "default", core.AdoptOptions{SkipMatch: true})
			require.NoError(t, err)
			require.NotEmpty(t, plan.Scan.Untracked, "the scan must see the folder")
			_, err = svc.ApplyAdopt(ctx, game, plan, nil)
			require.NoError(t, err)
			installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
			require.NoError(t, err)
			require.NotEmpty(t, installed, "the adoption must record the mod")
			// A source-matched adoption is in place too: ManualDownload with
			// a real source and version, still no cache entry.
			require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
				Mod:         domain.Mod{ID: "42", SourceID: "acme", Name: "Matched", Version: "1.0", GameID: game.ID},
				ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
				ManualDownload: true,
			}))

			report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)

			require.NoError(t, err)
			assert.Nil(t, findingWithStatus(report.Result, "missing_cache"), "statuses were %v", findingStatuses(report.Result))
			assert.Zero(t, report.Result.Issues, "statuses were %v", findingStatuses(report.Result))
		})
	}
}

// TestSaveGame_RefusesOnlyAnEditThatMovesTheModPath is #451's comment:
// Service.SaveGame was the one exported way to write a game that skipped
// the in-use check every frontend path runs - and an edit that keeps the
// mod_path is still allowed.
func TestSaveGame_RefusesOnlyAnEditThatMovesTheModPath(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	f.profile(t, "default", true, "a")
	f.deployed(t, "default", "a", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	moved := *f.game
	moved.ModPath = t.TempDir()

	err := f.svc.SaveGame(ctx, &moved)

	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	saved, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	assert.Equal(t, f.game.ModPath, saved.ModPath)

	renamed := *f.game
	renamed.Name = "Sky, renamed"
	require.NoError(t, f.svc.SaveGame(ctx, &renamed), "an edit that keeps the mod_path is not refused")
}
