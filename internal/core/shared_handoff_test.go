package core_test

// shared_handoff_test.go pins #633: when two enabled mods ship the same
// path and the load-order winner goes - uninstalled, disabled, updated to a
// version without the file, replaced by a copy without it, or turned off by
// a switch or an apply - the path passes to the next mod in load order that
// still ships it, in the same operation. The original the first deploy set
// aside (#632) goes back only when no enabled mod is left to ship the path.
//
// Every case runs under both deploy roots (#629) and all three link
// methods.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handOffFixture is a deployed profile whose mods share one path.
type handOffFixture struct {
	svc     *core.Service
	dataDir string
	game    *domain.Game
	c       sharedOriginalCase
	dst     string
}

// newHandOffFixture seeds mods (in load order) that all ship c.member,
// over an original when withOriginal, plus "own.txt" per mod so no mod is
// left with nothing deployed, and deploys the profile.
func newHandOffFixture(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool, mods ...string) *handOffFixture {
	t.Helper()
	svc, dataDir := newOriginalsService(t)
	game := newRootGame(t, svc, method)
	dst := c.dst(game)
	if withOriginal {
		require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
		require.NoError(t, os.WriteFile(dst, []byte("the game's own file"), 0o644))
	}
	for _, id := range mods {
		seedNamedInstalledMod(t, svc, game, "src", id, handOffName(id), "1.0", true, map[string][]byte{
			c.member:              []byte("the " + id + " mod's file"),
			"own/" + id + ".txt":  []byte(id),
			"own/" + id + "2.txt": []byte(id),
		})
		seedProfileWithMod(t, svc, game.ID, "default", "src", id, "1.0")
	}
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	last := mods[len(mods)-1]
	require.Equal(t, "the "+last+" mod's file", readFileString(t, dst), "the last mod in load order wins the path")
	return &handOffFixture{svc: svc, dataDir: dataDir, game: game, c: c, dst: dst}
}

func handOffName(id string) string { return "Mod " + id }

// assertHeld asserts the path now holds id's file, recorded as id's, with
// the original (when there was one) still set aside under it.
func (f *handOffFixture) assertHeld(t *testing.T, id string, withOriginal bool, what string) {
	t.Helper()
	assert.Equal(t, "the "+id+" mod's file", readFileString(t, f.dst), "%s: the next mod in load order takes the path, with no deploy", what)
	files, err := f.svc.GetDeployedFilesForMod(context.Background(), f.game.ID, "default", "src", id)
	require.NoError(t, err)
	want := f.c.rel
	if f.c.root == "install_path" {
		want = domain.DisplayDeployPath(domain.RootInstallPath, f.c.rel)
	}
	assert.Contains(t, files, want, "%s: the path is recorded as %s's", what, id)
	rows := readOriginalsManifest(t, f.dataDir, f.game.ID)
	if withOriginal {
		require.Len(t, rows, 1, "%s: the original stays set aside under the next mod's file", what)
		assert.Equal(t, "the game's own file", readStoredOriginal(t, f.dataDir, f.game.ID, f.c.root, f.c.rel))
	} else {
		assert.Empty(t, rows, what)
	}
}

// assertReleased asserts no mod holds the path any more: the original is
// back, or nothing is there.
func (f *handOffFixture) assertReleased(t *testing.T, withOriginal bool, what string) {
	t.Helper()
	if withOriginal {
		assert.Equal(t, "the game's own file", readFileString(t, f.dst), "%s: with no mod left to ship it, the original goes back", what)
	} else {
		assert.NoFileExists(t, f.dst, what)
	}
	assert.Empty(t, readOriginalsManifest(t, f.dataDir, f.game.ID), what)
}

func forEachHandOffCase(t *testing.T, run func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool)) {
	for _, c := range sharedOriginalCases {
		for _, method := range rootMethods {
			for _, withOriginal := range []bool{true, false} {
				name := c.name + "/" + method.String() + "/no_original"
				if withOriginal {
					name = c.name + "/" + method.String() + "/original"
				}
				t.Run(name, func(t *testing.T) { run(t, c, method, withOriginal) })
			}
		}
	}
}

func TestSharedPathHandOff_UninstallingTheWinnerDeploysTheNextMod(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "src", "second", core.UninstallOptions{})
		require.NoError(t, err)
		res, err := f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		assertNoReplacedLinkClaim(t, append(res.Warnings, res.Notes...), "uninstall")
		assert.True(t, anyContains(res.Warnings, handOffName("first")), "the uninstall says who has the path now: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "uninstall the winner")

		plan, err = f.svc.PlanUninstall(ctx, f.game, "default", "src", "first", core.UninstallOptions{})
		require.NoError(t, err)
		_, err = f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		f.assertReleased(t, withOriginal, "uninstall the last")
	})
}

func TestSharedPathHandOff_DisablingTheWinnerDeploysTheNextMod(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		res, err := f.svc.DisableMod(ctx, f.game, "default", "src", "second")
		require.NoError(t, err)
		assertNoReplacedLinkClaim(t, append(res.Warnings, res.Notes...), "disable")
		assert.True(t, anyContains(res.Warnings, handOffName("first")), "the disable says who has the path now: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "disable the winner")

		// Enabling it again takes the path back.
		_, err = f.svc.EnableMod(ctx, f.game, "default", "src", "second")
		require.NoError(t, err)
		assert.Equal(t, "the second mod's file", readFileString(t, f.dst))

		_, err = f.svc.DisableMod(ctx, f.game, "default", "src", "second")
		require.NoError(t, err)
		f.assertHeld(t, "first", withOriginal, "disable the winner again")
		_, err = f.svc.DisableMod(ctx, f.game, "default", "src", "first")
		require.NoError(t, err)
		f.assertReleased(t, withOriginal, "disable the last")
	})
}

// A disabled mod ships nothing: the path is not handed to one.
func TestSharedPathHandOff_ADisabledModDoesNotTakeThePath(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second", "third")
		_, err := f.svc.DisableMod(ctx, f.game, "default", "src", "first")
		require.NoError(t, err)
		_, err = f.svc.DisableMod(ctx, f.game, "default", "src", "second")
		require.NoError(t, err)
		assert.Equal(t, "the third mod's file", readFileString(t, f.dst), "the losers' removals leave the winner's file")

		_, err = f.svc.UninstallMod(ctx, f.game, "default", "src", "third", core.UninstallOptions{})
		require.NoError(t, err)
		f.assertReleased(t, withOriginal, "uninstall the only enabled provider")
	})
}

// `lmm conflicts` names the mod the path passed to as its owner, and it is
// the load-order winner of the providers left.
func TestSharedPathHandOff_ConflictsNameTheNewOwner(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second", "third")

		_, err := f.svc.UninstallMod(ctx, f.game, "default", "src", "third", core.UninstallOptions{})
		require.NoError(t, err)
		f.assertHeld(t, "second", withOriginal, "uninstall the winner of three")

		conflicts, err := f.svc.GetProfileConflicts(ctx, f.game, "default")
		require.NoError(t, err)
		var found bool
		for _, cf := range conflicts {
			if cf.Path != c.rel {
				continue
			}
			found = true
			assert.Equal(t, "src:second", cf.Owner.Key, "the new owner")
			assert.Equal(t, "src:second", cf.LoadOrderWinner.Key)
			assert.False(t, cf.Stale, "the deployed file is the load-order winner's")
		}
		assert.True(t, found, "the path is still a conflict between first and second: %+v", conflicts)
	})
}

// An update to a version that no longer ships the path hands it to the
// next mod in load order.
func TestSharedPathHandOff_AnUpdateThatDropsTheFile(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		archive := filepath.Join(t.TempDir(), "second-2.0.zip")
		createImportTestZip(t, archive, map[string]string{"own/second.txt": "second v2"})
		plan, err := f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "second", archive, core.UpdateFromArchiveOptions{Version: "2.0"})
		require.NoError(t, err)
		res, err := f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{Version: "2.0"}, nil)
		require.NoError(t, err)
		assert.True(t, anyContains(res.Warnings, handOffName("first")), "the update says who has the path now: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "update the winner without the file")
	})
}

// A copy that replaces the winner with a version without the file hands
// the path on too.
func TestSharedPathHandOff_ACopyWithReplaceThatDropsTheFile(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		// "other" holds second at 2.0, which ships only its own file.
		require.NoError(t, f.svc.GetGameCache(f.game).Store(cache.Key{GameID: f.game.ID, SourceID: "src", ModID: "second", Version: "2.0"}, "own/second.txt", []byte("v2")))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "second", SourceID: "src", Name: handOffName("second"), Version: "2.0", GameID: f.game.ID},
			ProfileName: "other", UpdatePolicy: domain.UpdateNotify, Enabled: true,
		}))
		seedProfileWithMod(t, f.svc, f.game.ID, "other", "src", "second", "2.0")

		plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "other", To: "default", SourceID: "src", ModID: "second"})
		require.NoError(t, err)
		res, err := f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{Replace: true}, nil)
		require.NoError(t, err)
		assert.True(t, anyContains(res.Warnings, handOffName("first")), "the copy says who has the path now: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "copy a version without the file over the winner")
	})
}

// A profile apply whose document turns the winner off hands the path on.
func TestSharedPathHandOff_AnApplyThatTurnsTheWinnerOff(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		require.NoError(t, f.svc.NewProfileManager().SetModDisabled(ctx, f.game.ID, "default", "src", "second", true))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		require.Len(t, plan.ToDisable, 1)
		res, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		assert.True(t, anyContains(res.Warnings, handOffName("first")), "the apply says who has the path now: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "apply turns the winner off")
	})
}

// A switch to a profile without the winner hands the path to the mod the
// target profile keeps.
func TestSharedPathHandOff_ASwitchThatDropsTheWinner(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		// "other" lists only first, with a row of its own that is live.
		first, err := f.svc.GetInstalledMod(ctx, "src", "first", f.game.ID, "default")
		require.NoError(t, err)
		row := *first
		row.ProfileName = "other"
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &row))
		seedProfileWithMod(t, f.svc, f.game.ID, "other", "src", "first", "1.0")

		plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
		require.NoError(t, err)
		require.Len(t, plan.ToDisable, 1, "the switch turns second off")
		require.Empty(t, plan.ToEnable, "first is live under other already")
		res, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
		require.NoError(t, err)
		assert.True(t, anyContains(res.Warnings, handOffName("first")), "the switch says who has the path now: %q", res.Warnings)
		assert.Equal(t, "the first mod's file", readFileString(t, f.dst), "the mod the target profile keeps takes the path, with no deploy")
		rows := readOriginalsManifest(t, f.dataDir, f.game.ID)
		if withOriginal {
			require.Len(t, rows, 1, "the original stays set aside")
		} else {
			assert.Empty(t, rows)
		}
		conflicts, err := f.svc.GetProfileConflicts(ctx, f.game, "other")
		require.NoError(t, err)
		assert.Empty(t, conflicts, "other has one provider of the path")
	})
}

// #630: a path the profile overrides passes on as the overridden copy - the
// override stays in place, now recorded as the next mod's.
func TestSharedPathHandOff_AnOverriddenPathStaysOverridden(t *testing.T) {
	ctx := context.Background()
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
			f.overriddenProfile(t, "default", true, map[string]string{overrideKey: "tweaked\n"}, "j", "k")
			f.cachedOnly(t, "default", "j", method, map[string]string{"Data/k.ini": "stock\n", "Data/j.esp": "j"})
			f.cachedOnly(t, "default", "k", method, map[string]string{"Data/k.ini": "stock\n", "Data/k.esp": "k"})
			f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")

			res, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{KeepCache: true})
			require.NoError(t, err)
			assert.True(t, anyContains(res.Warnings, "Mod j"), "the uninstall says who has the path now: %q", res.Warnings)
			info, err := os.Lstat(f.iniPath())
			require.NoError(t, err)
			require.True(t, info.Mode().IsRegular(), "still the overridden copy, not a link: %v", info.Mode())
			assert.Equal(t, "tweaked\n", readLive(t, f.iniPath()))
			files, err := f.svc.GetDeployedFilesForMod(ctx, f.game.ID, "default", "local", "j")
			require.NoError(t, err)
			assert.Contains(t, files, "Data/k.ini", "the overridden copy is j's now")

			// Removing j too removes the copy: no mod is left to ship it.
			_, err = f.svc.UninstallMod(ctx, f.game, "default", "local", "j", core.UninstallOptions{KeepCache: true})
			require.NoError(t, err)
			assert.NoFileExists(t, f.iniPath())
		})
	}
}

// #466/#469: a file the user put where the winner's was is the user's -
// the removal leaves it, and nothing is handed on over it.
func TestSharedPathHandOff_TheUsersFileIsNotReplaced(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")
		require.NoError(t, os.Remove(f.dst))
		require.NoError(t, os.WriteFile(f.dst, []byte("the user's own file"), 0o644))

		res, err := f.svc.UninstallMod(ctx, f.game, "default", "src", "second", core.UninstallOptions{})
		require.NoError(t, err)
		assert.Equal(t, "the user's own file", readFileString(t, f.dst), "the user's file is never replaced")
		assert.False(t, anyContains(res.Warnings, handOffName("first")), "nothing was handed on: %q", res.Warnings)
	})
}

// #633 review: the removals of one flow list each heir's cache entry once.
// An apply turning off m of n mods used to list every remaining mod's
// files once per removal - m×n listings for paths no other mod ships.
func TestSharedPathHandOff_AFlowListsEachHeirOnce(t *testing.T) {
	ctx := context.Background()
	const n, m = 12, 6
	ids := make([]string, n)
	for j := range ids {
		ids[j] = string(rune('a' + j))
	}
	f := newHandOffFixture(t, sharedOriginalCases[1], domain.LinkSymlink, false, ids...)
	pm := f.svc.NewProfileManager()
	for _, id := range ids[n-m:] {
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", id, true))
	}
	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToDisable, m)

	var listings int
	defer core.CountHeirListingsForTest(func() { listings++ })()
	res, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)

	last := ids[n-m-1]
	assert.Equal(t, "the "+last+" mod's file", readFileString(t, f.dst), "the last mod the apply keeps has the path")
	assert.LessOrEqual(t, listings, n-m, "each mod the apply keeps is listed once, however many mods it turns off")
	var notes []string
	for _, w := range res.Warnings {
		if strings.Contains(w, "deployed there now") {
			notes = append(notes, w)
		}
	}
	require.Len(t, notes, 1, "the path is reported once, by its final owner: %q", res.Warnings)
	assert.Contains(t, notes[0], handOffName(last)+"'s file")
}

// #633 review: the web UI's batch uninstall names the batch's other mods,
// so the path goes straight to a mod the batch leaves installed, and is
// reported once.
func TestSharedPathHandOff_ABatchUninstallReportsTheFinalOwner(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second", "third")

		res, err := f.svc.UninstallMod(ctx, f.game, "default", "src", "third", core.UninstallOptions{
			AlsoRemoving: []domain.ModReference{{SourceID: "src", ModID: "second"}},
		})
		require.NoError(t, err)
		assert.True(t, anyContains(res.Warnings, handOffName("first")+"'s file"), "the path goes to the mod the batch keeps: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "the batch's first uninstall")

		res, err = f.svc.UninstallMod(ctx, f.game, "default", "src", "second", core.UninstallOptions{})
		require.NoError(t, err)
		assert.False(t, anyContains(res.Warnings, "deployed there now"), "nothing is handed on twice: %q", res.Warnings)
		f.assertHeld(t, "first", withOriginal, "the batch's second uninstall")
	})
}

// #633 review: a mod the profile document marks off is not deployed by a
// deploy, so it does not take a path either, whatever its row says.
func TestSharedPathHandOff_AModTheDocumentMarksOffDoesNotTakeThePath(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")
		require.NoError(t, f.svc.NewProfileManager().SetModDisabled(ctx, f.game.ID, "default", "src", "first", true))

		_, err := f.svc.UninstallMod(ctx, f.game, "default", "src", "second", core.UninstallOptions{})
		require.NoError(t, err)
		f.assertReleased(t, withOriginal, "the only other provider is marked off")
	})
}
