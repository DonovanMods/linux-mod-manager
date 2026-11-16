package core_test

// handoff_plan_test.go pins #638: a plan whose Apply would hand a shared
// path to the next mod in load order (#633) names that mod per path, from
// the same load-order rule and the same functions Apply uses - and the
// plan's heirs are the final owners Apply's own ledger reports.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wantHandOff is the plan's hand-off of the fixture's shared path from
// mod from to mod to.
func (f *handOffFixture) wantHandOff(from, to string) core.HandOff {
	root := domain.RootModPath
	if f.c.root == "install_path" {
		root = domain.RootInstallPath
	}
	return core.HandOff{
		Path: f.c.rel, Root: root,
		From: core.HandOffMod{SourceID: "src", ModID: from, Name: handOffName(from)},
		To:   core.HandOffMod{SourceID: "src", ModID: to, Name: handOffName(to)},
	}
}

func TestPlanHandOffs_UninstallNamesTheNextModInLoadOrder(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second", "third")

		plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "src", "third", core.UninstallOptions{})
		require.NoError(t, err)
		assert.Equal(t, []core.HandOff{f.wantHandOff("third", "second")}, plan.Handoffs,
			"the winner's path passes to the next mod in load order, not the first")

		// A loser holds no deployed file there: nothing passes on.
		plan, err = f.svc.PlanUninstall(ctx, f.game, "default", "src", "first", core.UninstallOptions{})
		require.NoError(t, err)
		assert.Empty(t, plan.Handoffs, "a mod that does not hold the path hands nothing on")
	})
}

// The last provider hands nothing on: the original goes back, or nothing.
func TestPlanHandOffs_TheLastProviderHandsNothingOn(t *testing.T) {
	ctx := context.Background()
	f := newHandOffFixture(t, sharedOriginalCases[1], domain.LinkSymlink, true, "first")
	plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "src", "first", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Nil(t, plan.Handoffs)
}

// A disabled mod is not an heir in the plan either.
func TestPlanHandOffs_ADisabledModIsNotNamed(t *testing.T) {
	ctx := context.Background()
	f := newHandOffFixture(t, sharedOriginalCases[1], domain.LinkSymlink, false, "first", "second", "third")
	_, err := f.svc.DisableMod(ctx, f.game, "default", "src", "second")
	require.NoError(t, err)
	plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "src", "third", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, []core.HandOff{f.wantHandOff("third", "first")}, plan.Handoffs)
}

// A profile apply that turns the winner off (the profile's disable) names
// the heir; turning off the top two names the final owner, not the
// intermediate one.
func TestPlanHandOffs_ApplyDisablingTheWinner(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second", "third")
		pm := f.svc.NewProfileManager()
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", "third", true))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		assert.Equal(t, []core.HandOff{f.wantHandOff("third", "second")}, plan.Handoffs)

		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", "second", true))
		plan, err = f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		assert.Equal(t, []core.HandOff{f.wantHandOff("third", "first")}, plan.Handoffs,
			"a mod the apply turns off itself is never the heir")
	})
}

// A switch names the mod the target profile keeps.
func TestPlanHandOffs_SwitchDroppingTheWinner(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")
		first, err := f.svc.GetInstalledMod(ctx, "src", "first", f.game.ID, "default")
		require.NoError(t, err)
		row := *first
		row.ProfileName = "other"
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &row))
		seedProfileWithMod(t, f.svc, f.game.ID, "other", "src", "first", "1.0")

		plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
		require.NoError(t, err)
		assert.Equal(t, []core.HandOff{f.wantHandOff("second", "first")}, plan.Handoffs)
	})
}

// An update from an archive that no longer ships the path names the heir;
// one that still ships it hands nothing on.
func TestPlanHandOffs_UpdateFromArchive(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")

		archive := filepath.Join(t.TempDir(), "second-2.0.zip")
		createImportTestZip(t, archive, map[string]string{"own/second.txt": "second v2"})
		plan, err := f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "second", archive, core.UpdateFromArchiveOptions{Version: "2.0"})
		require.NoError(t, err)
		assert.Equal(t, []core.HandOff{f.wantHandOff("second", "first")}, plan.Handoffs)

		keeps := filepath.Join(t.TempDir(), "second-3.0.zip")
		createImportTestZip(t, keeps, map[string]string{"own/second.txt": "second v3", c.member: "v3"})
		plan, err = f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "second", keeps, core.UpdateFromArchiveOptions{Version: "3.0"})
		require.NoError(t, err)
		assert.Empty(t, plan.Handoffs, "a version that still ships the path keeps it")
	})
}

// A rollback to a version without the path names the heir.
func TestPlanHandOffs_Rollback(t *testing.T) {
	ctx := context.Background()
	c := sharedOriginalCases[1]
	f := newHandOffFixture(t, c, domain.LinkSymlink, false, "first", "second")

	// second 2.0 drops the path (first takes it), 3.0 ships it again.
	for _, step := range []struct {
		version string
		files   map[string]string
	}{
		{"2.0", map[string]string{"own/second.txt": "v2"}},
		{"3.0", map[string]string{"own/second.txt": "v3", c.member: "v3"}},
	} {
		archive := filepath.Join(t.TempDir(), "second-"+step.version+".zip")
		createImportTestZip(t, archive, step.files)
		plan, err := f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "second", archive, core.UpdateFromArchiveOptions{Version: step.version})
		require.NoError(t, err)
		_, err = f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{Version: step.version}, nil)
		require.NoError(t, err)
	}
	require.Equal(t, "v3", readFileString(t, f.dst))

	plan, err := f.svc.PlanRollback(ctx, f.game, "default", "src", "second")
	require.NoError(t, err)
	require.Equal(t, "2.0", plan.ToVersion)
	assert.Equal(t, []core.HandOff{f.wantHandOff("second", "first")}, plan.Handoffs)

	_, err = f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	f.assertHeld(t, "first", false, "the rollback hands the path to the heir the plan named")
}

// A copy replacing the winner with a version without the path names the
// heir on the entry it replaces.
func TestPlanHandOffs_CopyWithReplace(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second")
		require.NoError(t, f.svc.GetGameCache(f.game).Store(cache.Key{GameID: f.game.ID, SourceID: "src", ModID: "second", Version: "2.0"}, "own/second.txt", []byte("v2")))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "second", SourceID: "src", Name: handOffName("second"), Version: "2.0", GameID: f.game.ID},
			ProfileName: "other", UpdatePolicy: domain.UpdateNotify, Enabled: true,
		}))
		seedProfileWithMod(t, f.svc, f.game.ID, "other", "src", "second", "2.0")

		plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "other", To: "default", SourceID: "src", ModID: "second"})
		require.NoError(t, err)
		require.Len(t, plan.Mods, 1)
		assert.Equal(t, []core.HandOff{f.wantHandOff("second", "first")}, plan.Mods[0].Handoffs)
	})
}

// The plan reads nothing it does not need: an uninstall of a mod that
// shares no path lists no heir's files and names nothing.
func TestPlanHandOffs_NoSharedPathNoHandOff(t *testing.T) {
	ctx := context.Background()
	f := newHandOffFixture(t, sharedOriginalCases[1], domain.LinkSymlink, false, "first", "second")
	seedNamedInstalledMod(t, f.svc, f.game, "src", "solo", "Solo", "1.0", true, map[string][]byte{"solo/only.txt": []byte("x")})
	seedProfileWithMod(t, f.svc, f.game.ID, "default", "src", "solo", "1.0")
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "src", "solo", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Nil(t, plan.Handoffs)
}

// The plan's heirs are the final owners Apply's ledger reports: every
// planned path ends recorded as its planned heir's, and the ledger's notes
// are exactly the plan's, grouped by heir and removed mod.
func TestPlanHandOffs_PlanAgreesWithApply(t *testing.T) {
	forEachHandOffCase(t, func(t *testing.T, c sharedOriginalCase, method domain.LinkMethod, withOriginal bool) {
		ctx := context.Background()
		f := newHandOffFixture(t, c, method, withOriginal, "first", "second", "third", "fourth")
		pm := f.svc.NewProfileManager()
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", "fourth", true))
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", "third", true))

		plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		require.Len(t, plan.Handoffs, 1)
		res, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)

		assertPlanMatchesLedger(t, f, plan.Handoffs, res.Warnings)
	})
}

// assertPlanMatchesLedger asserts each planned path is its heir's now, and
// the flow's hand-off notes are one per planned heir and removed mod,
// naming both.
func assertPlanMatchesLedger(t *testing.T, f *handOffFixture, planned []core.HandOff, warnings []string) {
	t.Helper()
	ctx := context.Background()
	type pair struct{ to, from string }
	pairs := map[pair]bool{}
	for _, h := range planned {
		files, err := f.svc.GetDeployedFilesForMod(ctx, f.game.ID, "default", h.To.SourceID, h.To.ModID)
		require.NoError(t, err)
		assert.Contains(t, files, domain.DisplayDeployPath(h.Root, h.Path), "the planned heir holds the path after Apply")
		pairs[pair{h.To.Name, h.From.Name}] = true
	}
	var notes []string
	for _, w := range warnings {
		if strings.Contains(w, "as the next mod in load order") {
			notes = append(notes, w)
		}
	}
	require.Len(t, notes, len(pairs), "one ledger note per planned heir and removed mod: %q", warnings)
	for p := range pairs {
		var found bool
		for _, n := range notes {
			if strings.Contains(n, p.to+"'s file") && strings.Contains(n, p.from+"'s") {
				found = true
			}
		}
		assert.True(t, found, "the ledger names %s taking the path from %s: %q", p.to, p.from, notes)
	}
}

// The plan agrees with an uninstall's and a switch's Apply too.
func TestPlanHandOffs_PlanAgreesWithUninstallAndSwitch(t *testing.T) {
	ctx := context.Background()
	c := sharedOriginalCases[0]
	f := newHandOffFixture(t, c, domain.LinkCopy, true, "first", "second", "third")

	plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "src", "third", core.UninstallOptions{})
	require.NoError(t, err)
	res, err := f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Handoffs, 1)
	assertPlanMatchesLedger(t, f, plan.Handoffs, res.Warnings)

	// "other" keeps only first: the switch turns second off.
	first, err := f.svc.GetInstalledMod(ctx, "src", "first", f.game.ID, "default")
	require.NoError(t, err)
	row := *first
	row.ProfileName = "other"
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &row))
	seedProfileWithMod(t, f.svc, f.game.ID, "other", "src", "first", "1.0")
	sw, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
	require.NoError(t, err)
	require.Equal(t, []core.HandOff{f.wantHandOff("second", "first")}, sw.Handoffs)
	swRes, err := f.svc.ApplyProfileSwitch(ctx, f.game, sw, nil)
	require.NoError(t, err)
	owner, err := f.svc.GetDeployedFilesForMod(ctx, f.game.ID, "other", "src", "first")
	require.NoError(t, err)
	assert.Contains(t, owner, domain.DisplayDeployPath(domain.RootInstallPath, c.rel), "the switch's planned heir holds the path under the target")
	assert.True(t, anyContains(swRes.Warnings, handOffName("first")+"'s file"), "%q", swRes.Warnings)
}

// A plan lists each heir's cache entry once, however many mods it removes
// - as Apply's flow does (#633 review) - and never through the running
// mutation's listings.
func TestPlanHandOffs_APlanListsEachHeirOnce(t *testing.T) {
	ctx := context.Background()
	const n, m = 10, 5
	ids := make([]string, n)
	for j := range ids {
		ids[j] = string(rune('a' + j))
	}
	f := newHandOffFixture(t, sharedOriginalCases[1], domain.LinkSymlink, false, ids...)
	pm := f.svc.NewProfileManager()
	for _, id := range ids[n-m:] {
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", id, true))
	}
	var listings int
	defer core.CountHeirListingsForTest(func() { listings++ })()
	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	assert.LessOrEqual(t, listings, n-m)
	require.Len(t, plan.Handoffs, 1, "one path, by its final owner")
	assert.Equal(t, f.wantHandOff(ids[n-1], ids[n-m-1]), plan.Handoffs[0])
}

// A path the apply hands on and then deploys again with a mod it enables
// is that mod's in the end: the plan names no hand-off, as Apply's ledger
// reports none.
func TestPlanHandOffs_AnEnableLaterInTheFlowTakesThePathBack(t *testing.T) {
	ctx := context.Background()
	f := newHandOffFixture(t, sharedOriginalCases[1], domain.LinkSymlink, false, "first", "second", "third")
	_, err := f.svc.DisableMod(ctx, f.game, "default", "src", "third")
	require.NoError(t, err)
	require.Equal(t, "the second mod's file", readFileString(t, f.dst))
	pm := f.svc.NewProfileManager()
	require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", "third", false))
	require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "default", "src", "second", true))

	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToDisable, 1)
	require.Len(t, plan.ToEnable, 1)
	assert.Nil(t, plan.Handoffs, "third deploys the path again after second hands it to first")

	res, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "the third mod's file", readFileString(t, f.dst))
	assertPlanMatchesLedger(t, f, plan.Handoffs, res.Warnings)
}
