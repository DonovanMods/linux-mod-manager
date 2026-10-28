package core_test

// The #445 review's P1 findings on the recorded-only purge (`lmm purge -p
// <profile that is not active>`). It removes a path only on positive proof
// that the path is the purged profile's and nothing else the user wants
// live; each test below is a way that proof was missing.
//
//   - F1: a file the game's adapter hands to the user after its first deploy
//     (BepInEx/config/**, adapter.RouteCopyOnce) was removed. The ordinary
//     purge never removes one; the recorded-only purge skipped that guard.
//     Like the ordinary purge, it drops the record and keeps the file: v2
//     never records such a file, so the row is a legacy one, and a kept row
//     would keep the game's mod_path locked (#427) with nothing to clear it.
//   - F3: a v1.30.1 switch between two profiles that share a mod left the
//     new profile with no row for it while its files stayed live, so the
//     purge of the old profile saw "no other profile records this" and
//     deleted the active profile's live files. A path whose mod the active
//     profile's document lists, and does not mark off, is kept.
//   - F4: a path whose Lstat failed for any reason but "not there" was
//     treated as gone: its record was deleted and it was reported removed.
//   - F7: a path another game records, in a mod directory both games
//     share, was removed.
//
// Each fixture writes the state an older lmm leaves - rows and files - by
// hand, since no current flow can produce it.
//
// Every purge the mod_path refusal (#427) names has to be able to clear the
// rows it counts, so each path is decided in this order (#445 audit):
//
//  1. its file is already gone: the purged profile's record goes;
//  2. the game hands the file to the user: the file stays, the record goes;
//  3. the active profile's document lists its mod: the file and the record
//     stay, because the record is the file's only claim to be lmm's -
//     unless another record of the path is the active profile's, or is for
//     a mod it lists, which keeps the file in turn
//     (recorded_purge_handoff_test.go);
//  4. another profile, or another game, records it: the file stays - that
//     claimant still tracks it, and its own purge decides it - and the
//     purged profile's record goes;
//  5. otherwise it is the purged profile's alone: file and record go.

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

// legacyFixture is a Service over one game whose profiles and rows are
// written by hand.
type legacyFixture struct {
	svc  *core.Service
	game *domain.Game
}

func newLegacyFixture(t *testing.T, game *domain.Game) *legacyFixture {
	t.Helper()
	svc := newFlowsTestService(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &legacyFixture{svc: svc, game: game}
}

// profile writes a profile file the way v1.30.1 wrote one.
func (f *legacyFixture) profile(t *testing.T, name string, active bool, refs ...string) {
	t.Helper()
	text := "name: " + name + "\ngame_id: " + f.game.ID + "\nmods:\n"
	if len(refs) == 0 {
		text = "name: " + name + "\ngame_id: " + f.game.ID + "\nmods: []\n"
	}
	for _, ref := range refs {
		text += "    - source_id: local\n      mod_id: " + ref + "\n      version: unknown\n"
	}
	if active {
		text += "is_default: true\n"
	}
	dir := filepath.Join(f.svc.ConfigDir(), "games", f.game.ID, "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(text), 0o644))
}

// deployed seeds modID as installed and deployed under profile with method,
// its files in the cache and - as the deploy that recorded them left them -
// in the game directory, each with a deployed_files row. content overrides
// a file's live bytes (a user's edit since).
func (f *legacyFixture) deployed(t *testing.T, profile, modID string, method domain.LinkMethod, files map[string]string, live map[string]string) {
	t.Helper()
	ctx := context.Background()
	gameCache := f.svc.GetGameCache(f.game)
	for rel, content := range files {
		require.NoError(t, gameCache.Store(f.game.ID, "local", modID, "unknown", rel, []byte(content)))
		dst := filepath.Join(f.game.ModPath, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
		// A later deploy of a path replaces what an earlier one left.
		if err := os.Remove(dst); err != nil {
			require.ErrorIs(t, err, os.ErrNotExist)
		}
		if text, edited := live[rel]; edited {
			require.NoError(t, os.WriteFile(dst, []byte(text), 0o644))
		} else if method == domain.LinkSymlink {
			require.NoError(t, os.Symlink(filepath.Join(gameCache.ModPath(f.game.ID, "local", modID, "unknown"), filepath.FromSlash(rel)), dst))
		} else {
			require.NoError(t, os.WriteFile(dst, []byte(content), 0o644))
		}
		require.NoError(t, f.svc.ExecForTest(ctx,
			`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, ?, ?, 'local', ?)`,
			f.game.ID, profile, rel, modID))
	}
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "local", Name: "Mod " + modID, Version: "unknown", GameID: f.game.ID},
		ProfileName:  profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   method,
	}))
}

// recorded is profile's deployed_files paths for modID.
func (f *legacyFixture) recorded(t *testing.T, profile, modID string) []string {
	t.Helper()
	rows, err := f.svc.GetDeployedFilesForMod(context.Background(), f.game.ID, profile, "local", modID)
	require.NoError(t, err)
	return rows
}

// purge plans and applies a purge of profile.
func (f *legacyFixture) purge(t *testing.T, profile string) (*core.PurgePlan, *core.PurgeResult) {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanPurge(ctx, f.game, profile, core.PurgeOptions{})
	require.NoError(t, err)
	require.True(t, plan.RecordedOnly)
	result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	return plan, result
}

// purgePlan plans a purge of profile, without applying it.
func (f *legacyFixture) purgePlan(t *testing.T, profile string) *core.PurgePlan {
	t.Helper()
	plan, err := f.svc.PlanPurge(context.Background(), f.game, profile, core.PurgeOptions{})
	require.NoError(t, err)
	return plan
}

func readLive(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// bepinexGame is a BepInEx game laid out at its root - the preloader is
// there, so the bepinex adapter is derived - deploying by method.
func bepinexGame(t *testing.T, method domain.LinkMethod) *domain.Game {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "BepInEx", "core"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "BepInEx", "core", "BepInEx.Preloader.dll"), []byte("preloader"), 0o644))
	return &domain.Game{
		ID: "val", Name: "Val", InstallPath: root, ModPath: root,
		LinkMethod: method, LinkMethodExplicit: true,
	}
}

var bepinexMod = map[string]string{
	"BepInEx/plugins/m.dll": "dll",
	"BepInEx/config/m.cfg":  "setting=default",
}

func TestRecordedPurge_AFileTheGameHandsToTheUserIsKept(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink, domain.LinkSymlink} {
		t.Run(method.String(), func(t *testing.T) {
			f := newLegacyFixture(t, bepinexGame(t, method))
			f.profile(t, "default", false, "m")
			f.profile(t, "alt", true) // the active profile does not list m
			live := map[string]string{}
			if method != domain.LinkSymlink {
				live["BepInEx/config/m.cfg"] = "setting=USER-TUNED"
			}
			f.deployed(t, "default", "m", method, bepinexMod, live)
			cfg := filepath.Join(f.game.ModPath, "BepInEx", "config", "m.cfg")
			before := readLive(t, cfg)

			plan, result := f.purge(t, "default")

			assert.Equal(t, []string{"BepInEx/plugins/m.dll"}, plan.Remove)
			assert.Equal(t, []core.PurgeKeptPath{{Path: "BepInEx/config/m.cfg", Reason: core.PurgeKeptUserFile}}, plan.Kept)
			assert.NoFileExists(t, filepath.Join(f.game.ModPath, "BepInEx", "plugins", "m.dll"))
			assert.Equal(t, before, readLive(t, cfg), "the user's config is exactly as it was")
			assert.Equal(t, 1, result.RemovedPaths)
			assert.Equal(t, plan.Kept, result.Kept)
			assert.Empty(t, f.recorded(t, "default", "m"), "the config's record goes, as an ordinary purge's does")
			assert.Equal(t, 1, result.Purged, "nothing of m is left recorded")
			assert.Empty(t, result.Skipped)
			row, err := f.svc.GetInstalledMod(context.Background(), "local", "m", f.game.ID, "default")
			require.NoError(t, err)
			assert.False(t, row.Deployed)
		})
	}

	// The state the review reproduced it from: a v1.30.1 switch default ->
	// alt, both listing m, then the user tunes the live config. The plugin
	// is alt's live file (F3), so it stays with its record; the config is
	// the user's whoever lists its mod, so only its record goes.
	t.Run("the state a v1.30.1 switch leaves", func(t *testing.T) {
		f := newLegacyFixture(t, bepinexGame(t, domain.LinkCopy))
		f.profile(t, "default", false, "m")
		f.profile(t, "alt", true, "m")
		f.deployed(t, "default", "m", domain.LinkCopy, bepinexMod, map[string]string{"BepInEx/config/m.cfg": "setting=USER-TUNED"})

		plan, result := f.purge(t, "default")

		assert.Empty(t, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{
			{Path: "BepInEx/config/m.cfg", Reason: core.PurgeKeptUserFile},
			{Path: "BepInEx/plugins/m.dll", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}},
		}, plan.Kept)
		assert.Zero(t, result.RemovedPaths)
		assert.Equal(t, "setting=USER-TUNED", readLive(t, filepath.Join(f.game.ModPath, "BepInEx", "config", "m.cfg")))
		assert.Equal(t, "dll", readLive(t, filepath.Join(f.game.ModPath, "BepInEx", "plugins", "m.dll")), "alt's live plugin stays too (F3)")
		assert.Equal(t, []string{"BepInEx/plugins/m.dll"}, f.recorded(t, "default", "m"))
		assert.Zero(t, result.Purged, "m still has a recorded file")
		require.Len(t, result.Skipped, 1)
	})

	// A config is the user's whoever else records it, so the purged
	// profile's record goes even while another profile's stays - each
	// profile's own purge clears its own.
	t.Run("another profile's record of the config is no reason to keep this one", func(t *testing.T) {
		f := newLegacyFixture(t, bepinexGame(t, domain.LinkCopy))
		f.profile(t, "default", false, "m")
		f.profile(t, "survival", false, "m")
		f.profile(t, "alt", true)
		f.deployed(t, "default", "m", domain.LinkCopy, map[string]string{"BepInEx/config/m.cfg": "setting=default"}, nil)
		f.deployed(t, "survival", "m", domain.LinkCopy, map[string]string{"BepInEx/config/m.cfg": "setting=default"}, nil)

		plan, _ := f.purge(t, "default")

		assert.Equal(t, []core.PurgeKeptPath{{Path: "BepInEx/config/m.cfg", Reason: core.PurgeKeptUserFile}}, plan.Kept)
		assert.Empty(t, f.recorded(t, "default", "m"))
		assert.Equal(t, []string{"BepInEx/config/m.cfg"}, f.recorded(t, "survival", "m"), "only the purged profile's record goes")

		f.purge(t, "survival")
		assert.Empty(t, f.recorded(t, "survival", "m"))
		assert.FileExists(t, filepath.Join(f.game.ModPath, "BepInEx", "config", "m.cfg"))
	})

	// A profile whose only record is a config has nothing to remove and a
	// record to drop: the plan names its mod, and applying it clears it.
	t.Run("a profile whose only record is a config", func(t *testing.T) {
		f := newLegacyFixture(t, bepinexGame(t, domain.LinkCopy))
		f.profile(t, "default", false, "m")
		f.profile(t, "alt", true)
		f.deployed(t, "default", "m", domain.LinkCopy, map[string]string{"BepInEx/config/m.cfg": "setting=default"}, nil)

		plan, result := f.purge(t, "default")

		assert.Empty(t, plan.Remove)
		require.Len(t, plan.Mods, 1, "the plan names the mod it clears")
		assert.Equal(t, "m", plan.Mods[0].ID)
		assert.Equal(t, 1, result.Purged)
		assert.Empty(t, f.recorded(t, "default", "m"))
		assert.FileExists(t, filepath.Join(f.game.ModPath, "BepInEx", "config", "m.cfg"))
	})

	// The plan is the consent: a path it did not list as the user's keeps
	// its record, even when the game's adapter says so by the time it runs.
	t.Run("a config the plan did not list keeps its record", func(t *testing.T) {
		f := newLegacyFixture(t, bepinexGame(t, domain.LinkCopy))
		f.profile(t, "default", false, "m")
		f.profile(t, "alt", true)
		f.deployed(t, "default", "m", domain.LinkCopy, map[string]string{"BepInEx/config/m.cfg": "setting=default"}, nil)
		ctx := context.Background()
		plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
		require.NoError(t, err)
		require.Len(t, plan.Kept, 1)
		plan.Kept = nil

		result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"BepInEx/config/m.cfg"}, f.recorded(t, "default", "m"))
		assert.Zero(t, result.Purged)
	})
}

// TestPurgeKeptReason_OnlyAListedPathKeepsItsRecord pins which kept paths
// lose the purged profile's record: all but a path the active profile
// lists, whose record is its only claim to be lmm's.
func TestPurgeKeptReason_OnlyAListedPathKeepsItsRecord(t *testing.T) {
	assert.False(t, core.PurgeKeptListed.DropsRecord())
	for _, reason := range []core.PurgeKeptReason{core.PurgeKeptUserFile, core.PurgeKeptRecorded, core.PurgeKeptOtherGame} {
		assert.True(t, reason.DropsRecord(), reason)
	}
}

// sharedFixture is a plain game whose active profile default lists what
// activeLists names, and whose non-active profiles alt and survival both
// record x.esp for mod x - live, as a pre-upgrade switch leaves it.
func sharedFixture(t *testing.T, activeLists ...string) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy, LinkMethodExplicit: true})
	f.profile(t, "default", true, activeLists...)
	f.profile(t, "alt", false, "x")
	f.profile(t, "survival", false, "x")
	f.deployed(t, "alt", "x", domain.LinkCopy, map[string]string{"x.esp": "x"}, nil)
	f.deployed(t, "survival", "x", domain.LinkCopy, map[string]string{"x.esp": "x"}, nil)
	return f
}

// TestRecordedPurge_APathAnotherProfileRecordsKeepsItsFileNotThisRecord is
// rule 3: two profiles recording one file used to keep it for each other
// forever, so neither purge could clear its record - and the mod_path
// refusal counting those records never lifted. The first purge leaves the
// file to the other claimant; the last one decides it.
func TestRecordedPurge_APathAnotherProfileRecordsKeepsItsFileNotThisRecord(t *testing.T) {
	x := func(f *legacyFixture) string { return filepath.Join(f.game.ModPath, "x.esp") }

	t.Run("the first claimant's purge", func(t *testing.T) {
		f := sharedFixture(t)

		plan, result := f.purge(t, "alt")

		assert.Empty(t, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "x.esp", Reason: core.PurgeKeptRecorded, Profiles: []string{"survival"}}}, plan.Kept)
		require.Len(t, plan.Mods, 1, "the plan names the mod whose record goes")
		assert.FileExists(t, x(f))
		assert.Zero(t, result.RemovedPaths)
		assert.Equal(t, 1, result.Purged)
		assert.Empty(t, f.recorded(t, "alt", "x"))
		assert.Equal(t, []string{"x.esp"}, f.recorded(t, "survival", "x"), "the other claimant still tracks it")
	})

	t.Run("the last claimant's purge removes it", func(t *testing.T) {
		f := sharedFixture(t)
		f.purge(t, "alt")

		plan, result := f.purge(t, "survival")

		assert.Equal(t, []string{"x.esp"}, plan.Remove)
		assert.NoFileExists(t, x(f))
		assert.Equal(t, 1, result.RemovedPaths)
		assert.Empty(t, f.recorded(t, "survival", "x"))
	})

	t.Run("the last claimant's purge keeps a file the active profile lists", func(t *testing.T) {
		f := sharedFixture(t, "x")
		f.purge(t, "alt")

		plan, result := f.purge(t, "survival")

		assert.Equal(t, []core.PurgeKeptPath{{Path: "x.esp", Reason: core.PurgeKeptListed, Profiles: []string{"default"}}}, plan.Kept)
		assert.FileExists(t, x(f))
		assert.Zero(t, result.RemovedPaths)
		assert.Equal(t, []string{"x.esp"}, f.recorded(t, "survival", "x"), "its only record stays")
	})

	t.Run("the active profile recording it too", func(t *testing.T) {
		f := sharedFixture(t, "x")
		f.deployed(t, "default", "x", domain.LinkCopy, map[string]string{"x.esp": "x"}, nil)

		f.purge(t, "alt")
		plan, _ := f.purge(t, "survival")

		assert.Equal(t, []core.PurgeKeptPath{{Path: "x.esp", Reason: core.PurgeKeptRecorded, Profiles: []string{"default"}}}, plan.Kept)
		assert.FileExists(t, x(f))
		assert.Empty(t, f.recorded(t, "survival", "x"))
		assert.Equal(t, []string{"x.esp"}, f.recorded(t, "default", "x"))
	})

	t.Run("a record the plan did not list as going stays", func(t *testing.T) {
		f := sharedFixture(t)
		ctx := context.Background()
		plan, err := f.svc.PlanPurge(ctx, f.game, "alt", core.PurgeOptions{})
		require.NoError(t, err)
		plan.Kept = nil

		result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"x.esp"}, f.recorded(t, "alt", "x"))
		assert.Zero(t, result.Purged)
	})
}

// TestRecordedPurge_AGoneFileLosesItsRecordWhateverClaimsIt is rule 1: a
// record of a file that is not there protects nothing, so it goes even when
// the active profile lists the mod or records the path too - the state an
// active profile's own purge leaves for every path it shared.
func TestRecordedPurge_AGoneFileLosesItsRecordWhateverClaimsIt(t *testing.T) {
	for name, activeRecords := range map[string]bool{"listed by the active profile": false, "recorded by the active profile too": true} {
		t.Run(name, func(t *testing.T) {
			f := sharedFixture(t, "x")
			if activeRecords {
				f.deployed(t, "default", "x", domain.LinkCopy, map[string]string{"x.esp": "x"}, nil)
			}
			require.NoError(t, os.Remove(filepath.Join(f.game.ModPath, "x.esp")))

			plan, result := f.purge(t, "alt")

			assert.Equal(t, []string{"x.esp"}, plan.Remove)
			assert.Empty(t, plan.Kept)
			assert.Equal(t, 1, result.Purged)
			assert.Empty(t, f.recorded(t, "alt", "x"))
			assert.Equal(t, []string{"x.esp"}, f.recorded(t, "survival", "x"), "only the purged profile's record goes")
			if activeRecords {
				assert.Equal(t, []string{"x.esp"}, f.recorded(t, "default", "x"))
			}
		})
	}
}

// l1Fixture is exactly what v1.30.1 leaves after `lmm profile switch alt`
// from default, which had a and b deployed, where alt lists a: default keeps
// a's row and record (a.esp is live - it is alt's now) and b's row at
// enabled 0, its file removed; alt, now active, has no row at all.
func l1Fixture(t *testing.T) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	f.profile(t, "default", false, "a", "b")
	f.profile(t, "alt", true, "a")
	f.deployed(t, "default", "a", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "b", "unknown", "Data/b.esp", []byte("mod b")))
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "b", SourceID: "local", Name: "Mod b", Version: "unknown", GameID: f.game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: false, Deployed: true, LinkMethod: domain.LinkSymlink,
	}))
	return f
}

func TestRecordedPurge_AModTheActiveProfileListsIsKept(t *testing.T) {
	aLive := func(f *legacyFixture) string { return filepath.Join(f.game.ModPath, "Data", "a.esp") }

	t.Run("the state a v1.30.1 switch leaves", func(t *testing.T) {
		f := l1Fixture(t)
		before := treeOf(t, f.game.ModPath)

		plan, result := f.purge(t, "default")

		assert.Empty(t, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}}}, plan.Kept)
		assert.Equal(t, before, treeOf(t, f.game.ModPath), "alt's live file is still there")
		assert.FileExists(t, aLive(f))
		assert.Zero(t, result.RemovedPaths)
		assert.Equal(t, []string{"Data/a.esp"}, f.recorded(t, "default", "a"))
	})

	t.Run("a mod the active profile marks off is removed", func(t *testing.T) {
		f := l1Fixture(t)
		path := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml")
		require.NoError(t, os.WriteFile(path, []byte("name: alt\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: a\n      version: unknown\n      disabled: true\nis_default: true\n"), 0o644))

		plan, result := f.purge(t, "default")

		assert.Equal(t, []string{"Data/a.esp"}, plan.Remove)
		assert.NoFileExists(t, aLive(f))
		assert.Equal(t, 1, result.RemovedPaths)
	})

	t.Run("a mod the active profile does not list is removed", func(t *testing.T) {
		f := l1Fixture(t)
		f.profile(t, "alt", true)

		plan, _ := f.purge(t, "default")

		assert.Equal(t, []string{"Data/a.esp"}, plan.Remove)
		assert.NoFileExists(t, aLive(f))
	})

	t.Run("its first reference decides", func(t *testing.T) {
		f := l1Fixture(t)
		path := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml")
		require.NoError(t, os.WriteFile(path, []byte("name: alt\ngame_id: sky\nmods:\n    - {source_id: local, mod_id: a}\n    - {source_id: local, mod_id: a, disabled: true}\nis_default: true\n"), 0o644))

		plan, _ := f.purge(t, "default")

		assert.Empty(t, plan.Remove)
		assert.FileExists(t, aLive(f))
	})

	t.Run("a path listed after the plan is still kept", func(t *testing.T) {
		f := l1Fixture(t)
		f.profile(t, "alt", true)
		ctx := context.Background()
		plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
		require.NoError(t, err)
		require.Equal(t, []string{"Data/a.esp"}, plan.Remove)
		f.profile(t, "alt", true, "a")

		result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.FileExists(t, aLive(f))
		assert.Zero(t, result.RemovedPaths)
		assert.Contains(t, result.Kept, core.PurgeKeptPath{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}})
	})
}

func TestRecordedPurge_APathThatCannotBeCheckedKeepsItsRecord(t *testing.T) {
	skipAsRoot(t)
	f := l1Fixture(t)
	f.profile(t, "alt", true) // alt no longer lists a, so a.esp is default's to remove
	ctx := context.Background()
	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"Data/a.esp"}, plan.Remove)

	data := filepath.Join(f.game.ModPath, "Data")
	require.NoError(t, os.Chmod(data, 0o000))
	t.Cleanup(func() { _ = os.Chmod(data, 0o755) })
	result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, os.Chmod(data, 0o755))
	require.NoError(t, err)

	assert.Zero(t, result.RemovedPaths, "nothing was removed")
	assert.Zero(t, result.Purged)
	require.Len(t, result.Skipped, 1)
	require.Len(t, result.Warnings, 1, "said unconditionally, not as a --verbose note")
	assert.Contains(t, result.Warnings[0], "Data/a.esp")
	assert.Contains(t, result.Warnings[0], "permission denied")
	assert.Equal(t, []string{"Data/a.esp"}, f.recorded(t, "default", "a"), "the record stays with the file")
	assert.FileExists(t, filepath.Join(data, "a.esp"))
	row, err := f.svc.GetInstalledMod(ctx, "local", "a", "sky", "default")
	require.NoError(t, err)
	assert.True(t, row.Deployed, "a mod with a file left is not marked undeployed")

	t.Run("a path that is already gone loses its record", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(data, "a.esp")))
		_, result := f.purge(t, "default")
		assert.Equal(t, 1, result.RemovedPaths)
		assert.Empty(t, result.Warnings)
		assert.Empty(t, f.recorded(t, "default", "a"))
	})
}

func TestRecordedPurge_APathAnotherGameRecordsIsKept(t *testing.T) {
	ctx := context.Background()

	// setup registers games sky and sky2, whose mod directories layout
	// places relative to one shared directory, with sky's default profile
	// recording shared/Data/a.esp - live, sky's own - under skyRel, and
	// sky2's non-active alt recording the same file as Data/a.esp.
	setup := func(t *testing.T, layout func(shared string) (skyModPath, sky2ModPath, skyRel string)) (*core.Service, *domain.Game, string) {
		t.Helper()
		shared := t.TempDir()
		skyModPath, sky2ModPath, skyRel := layout(shared)
		svc := newFlowsTestService(t)
		// Copy deployments: a purge that removed the path would delete the
		// file, not just refuse a link it did not find.
		sky := &domain.Game{ID: "sky", Name: "Sky", ModPath: skyModPath, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
		sky2 := &domain.Game{ID: "sky2", Name: "Sky 2", ModPath: sky2ModPath, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
		require.NoError(t, svc.SaveGame(ctx, sky))
		require.NoError(t, svc.SaveGame(ctx, sky2))
		live := filepath.Join(shared, "Data", "a.esp")
		require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
		require.NoError(t, os.WriteFile(live, []byte("sky's"), 0o644))
		for _, row := range []struct{ game, profile, rel string }{{"sky", "default", skyRel}, {"sky2", "alt", "Data/a.esp"}} {
			require.NoError(t, svc.ExecForTest(ctx,
				`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, ?, ?, 'local', 'a')`,
				row.game, row.profile, row.rel))
		}
		for _, p := range []struct {
			game, name string
			active     bool
		}{{"sky", "default", true}, {"sky2", "default", true}, {"sky2", "alt", false}} {
			dir := filepath.Join(svc.ConfigDir(), "games", p.game, "profiles")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			text := "name: " + p.name + "\ngame_id: " + p.game + "\nmods: []\n"
			if p.active {
				text += "is_default: true\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, p.name+".yaml"), []byte(text), 0o644))
		}
		return svc, sky2, live
	}
	requireKept := func(t *testing.T, svc *core.Service, sky2 *domain.Game, live string) {
		t.Helper()
		plan, err := svc.PlanPurge(ctx, sky2, "alt", core.PurgeOptions{})
		require.NoError(t, err)
		assert.Empty(t, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptOtherGame, Games: []string{"sky"}}}, plan.Kept)
		result, err := svc.ApplyPurge(ctx, sky2, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
		assert.Zero(t, result.RemovedPaths)
		assert.Equal(t, "sky's", readLive(t, live))
		alt, err := svc.GetDeployedFilesForMod(ctx, "sky2", "alt", "local", "a")
		require.NoError(t, err)
		assert.Empty(t, alt, "sky2's record goes: sky still tracks the file")
		skys, err := svc.GetDeployedFilesForMod(ctx, "sky", "default", "local", "a")
		require.NoError(t, err)
		assert.Len(t, skys, 1, "and sky's record stays")
	}

	t.Run("the same mod directory", func(t *testing.T) {
		svc, sky2, live := setup(t, func(shared string) (string, string, string) {
			return shared, shared, "Data/a.esp"
		})
		requireKept(t, svc, sky2, live)
	})

	t.Run("one mod directory inside the other", func(t *testing.T) {
		svc, sky2, live := setup(t, func(shared string) (string, string, string) {
			return filepath.Join(shared, "Data"), shared, "a.esp"
		})
		requireKept(t, svc, sky2, live)
	})

	t.Run("a mod directory reached through a link", func(t *testing.T) {
		svc, sky2, live := setup(t, func(shared string) (string, string, string) {
			link := filepath.Join(t.TempDir(), "link")
			require.NoError(t, os.Symlink(shared, link))
			return link, shared, "Data/a.esp"
		})
		requireKept(t, svc, sky2, live)
	})

	t.Run("a record for another path is no reason", func(t *testing.T) {
		svc, sky2, _ := setup(t, func(shared string) (string, string, string) {
			return shared, shared, "Data/other.esp"
		})
		plan, err := svc.PlanPurge(ctx, sky2, "alt", core.PurgeOptions{})
		require.NoError(t, err)
		assert.Equal(t, []string{"Data/a.esp"}, plan.Remove)
	})

	t.Run("an unrelated directory is no reason", func(t *testing.T) {
		svc, sky2, _ := setup(t, func(shared string) (string, string, string) {
			return t.TempDir(), shared, "Data/a.esp"
		})
		plan, err := svc.PlanPurge(ctx, sky2, "alt", core.PurgeOptions{})
		require.NoError(t, err)
		assert.Equal(t, []string{"Data/a.esp"}, plan.Remove)
	})
}
