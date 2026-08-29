package core_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- v2 Phase 2 Unit M Task 24: PlanDeploy / ApplyDeploy ---

// deployedTree returns every game-dir-relative path currently present under
// root (regular files AND symlinks, dangling ones included - Lstat
// semantics, since a deploy's product is usually a symlink). Directories are
// excluded: the plan speaks in the same file-path vocabulary the linker
// does.
func deployedTree(t *testing.T, root string) map[string]bool {
	t.Helper()
	tree := make(map[string]bool)
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		tree[rel] = true
		return nil
	}))
	return tree
}

// planModByName indexes a plan's mods by name for per-mod assertions.
func planModByName(t *testing.T, plan *core.DeployPlan, name string) core.DeployPlanMod {
	t.Helper()
	for _, m := range plan.Mods {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("plan has no mod named %q (mods: %+v)", name, plan.Mods)
	return core.DeployPlanMod{}
}

// seedNarrowedStaleMod seeds #210's self-heal shape: a cache entry holding a
// stale unclaimed pak on disk plus a recorded zero-member marker AND a
// retained source, so deployableFiles narrows to nothing while the removal
// direction (ListFiles union) still covers the stale pak. A symlink for it
// is planted in the game dir, exactly as a pre-#210 deploy would have left
// it - the one fixture where a plan's Remove set is non-empty.
func seedNarrowedStaleMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, stalePath string) {
	t.Helper()
	seedNamedInstalledMod(t, svc, game, "src", modID, name, "1.0", true, map[string][]byte{
		stalePath: []byte("stale-pak-content"),
	})
	seedProfileWithMod(t, svc, game.ID, "default", "src", modID, "1.0")

	versionDir := svc.GetGameCache(game).ModPath(game.ID, "src", modID, "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "exmodz", nil))
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(versionDir, stalePath), filepath.Join(game.ModPath, stalePath)))
}

// TestPlanDeploy_ListsModsInLoadOrderWithTheirDeployableFiles is the plan's
// baseline shape: one entry per mod that would deploy, in the same
// profile/load order the deploy loop walks, each carrying the relative paths
// that would be linked.
func TestPlanDeploy_ListsModsInLoadOrderWithTheirDeployableFiles(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, "default", plan.Profile)
	assert.False(t, plan.NoChanges)
	require.Len(t, plan.Mods, 2)
	assert.Equal(t, "Mod One", plan.Mods[0].Name, "load order, not key order")
	assert.Equal(t, "Mod Two", plan.Mods[1].Name)
	assert.Equal(t, domain.ModReference{SourceID: "src", ModID: "1"}, plan.Mods[0].Ref)
	assert.Equal(t, []string{"one.esp"}, plan.Mods[0].Link)
	assert.Equal(t, []string{"two.esp"}, plan.Mods[1].Link)
	assert.Empty(t, plan.Mods[0].Remove)
	assert.Empty(t, plan.Purge, "no --purge means no purge set")
	assert.Nil(t, plan.Merged, "a non-compile game has no merge plan")
}

// TestPlanDeploy_LinkAndRemove_MatchWhatApplyDeployThenDid is the task's
// central contract: the file sets PlanDeploy lists are exactly the ones
// ApplyDeploy goes on to produce and remove, proven by diffing the deployed
// tree across the Apply rather than by re-deriving them the same way twice.
// The fixture deliberately mixes an ordinary mod (Link non-empty, Remove
// empty) with #210's narrowed self-heal shape (Link empty, Remove naming the
// stale link that must not come back).
func TestPlanDeploy_LinkAndRemove_MatchWhatApplyDeployThenDid(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNarrowedStaleMod(t, svc, game, "stale", "Stale Mod", "Stale_P.pak")

	before := deployedTree(t, game.ModPath)
	require.True(t, before["Stale_P.pak"], "fixture: the stale link must be deployed before the plan")

	ctx := context.Background()
	plan, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
	require.NoError(t, err)

	one := planModByName(t, plan, "Mod One")
	stale := planModByName(t, plan, "Stale Mod")
	assert.Equal(t, []string{"one.esp"}, one.Link)
	assert.Empty(t, one.Remove)
	assert.Empty(t, stale.Link, "a fully-narrowed entry links nothing")
	assert.Equal(t, []string{"Stale_P.pak"}, stale.Remove, "the stale unclaimed pak is removed and not re-linked")

	result, err := svc.ApplyDeploy(ctx, game, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Deployed)

	after := deployedTree(t, game.ModPath)

	link, remove := map[string]bool{}, map[string]bool{}
	for _, m := range plan.Mods {
		for _, f := range m.Link {
			link[f] = true
		}
		for _, f := range m.Remove {
			remove[f] = true
		}
	}

	for f := range link {
		assert.True(t, after[f], "plan listed %q as linked but the deploy did not produce it", f)
	}
	for f := range remove {
		assert.False(t, after[f], "plan listed %q as removed but the deploy left it in place", f)
	}
	for f := range after {
		if !before[f] {
			assert.True(t, link[f], "deploy created %q, which the plan did not list under Link", f)
		}
	}
	for f := range before {
		if !after[f] {
			assert.True(t, remove[f], "deploy removed %q, which the plan did not list under Remove", f)
		}
	}
}

// TestPlanDeploy_Purge_ListsEveryDeployedPathAndBothHookFamilies covers the
// --purge pass: Purge names every path the purge would undeploy - the
// removal direction's full cache union, narrowed to paths actually deployed
// right now (Minor #1), in purge order - and Hooks lists the uninstall.*
// family ahead of the install.* one, matching run order.
func TestPlanDeploy_Purge_ListsEveryDeployedPathAndBothHookFamilies(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")

	ctx := context.Background()
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err, "fixture: Purge only lists paths that are actually on disk")

	scripts := t.TempDir()
	ok := createTestScript(t, scripts, "ok.sh", "#!/bin/bash\nexit 0")
	seedHooks(t, svc, game, "default", domain.GameHooks{
		Install:   domain.HookConfig{BeforeAll: ok, AfterEach: ok},
		Uninstall: domain.HookConfig{BeforeEach: ok, AfterAll: ok},
	})

	plan, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{Purge: true})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"one.esp", "two.esp"}, plan.Purge)
	assert.Equal(t, []string{
		"uninstall.before_each", "uninstall.after_all",
		"install.before_all", "install.after_each",
	}, plan.Hooks, "configured hooks only, uninstall family first (purge runs first)")
}

// TestPlanDeploy_SkipHooks_ListsNoHooks pins the other half of the hook
// readout: --no-hooks means none of them run, so none are listed.
func TestPlanDeploy_SkipHooks_ListsNoHooks(t *testing.T) {
	svc, game := newDeployableService(t)
	scripts := t.TempDir()
	ok := createTestScript(t, scripts, "ok.sh", "#!/bin/bash\nexit 0")
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: ok}})

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{SkipHooks: true})
	require.NoError(t, err)
	assert.Empty(t, plan.Hooks)
}

// TestPlanDeploy_DisabledSingleMod_RecordsSkippedRatherThanFailing pins
// Ruling: a selection problem the historical flow only reported AFTER its
// --purge pass is plan DATA here, never a plan error - otherwise Plan+Apply
// would skip a purge the pre-lift flow ran (pinned by
// TestService_DeployProfile_PurgeThenUnknownModID_ReturnsPurgeDiagnostics).
func TestPlanDeploy_DisabledSingleMod_RecordsSkippedRatherThanFailing(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "src", "off", "Disabled Mod", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "off", "1.0")

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{ModID: "off", SourceID: "src"})
	require.NoError(t, err, "a disabled mod is a skip in the plan, not a planning failure")
	require.Len(t, plan.Mods, 1)
	assert.Equal(t, "Disabled Mod", plan.Mods[0].Name)
	assert.Contains(t, plan.Mods[0].Skipped, "is disabled")
	assert.Empty(t, plan.Mods[0].Link)

	// ...and ApplyDeploy still fails exactly as the pre-lift flow did.
	_, err = svc.ApplyDeploy(context.Background(), game, plan, core.DeployOptions{ModID: "off", SourceID: "src"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is disabled")
}

// TestPlanDeploy_UnknownModID_RecordsSkippedRatherThanFailing is the same
// ruling for an ID that resolves to nothing at all.
func TestPlanDeploy_UnknownModID_RecordsSkippedRatherThanFailing(t *testing.T) {
	svc, game := newDeployableService(t)

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{ModID: "nope", SourceID: "src"})
	require.NoError(t, err)
	require.Len(t, plan.Mods, 1)
	assert.Equal(t, domain.ModReference{SourceID: "src", ModID: "nope"}, plan.Mods[0].Ref)
	assert.Equal(t, "mod not found", plan.Mods[0].Skipped)

	_, err = svc.ApplyDeploy(context.Background(), game, plan, core.DeployOptions{ModID: "nope", SourceID: "src"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod not found: nope")
}

// TestPlanDeploy_EmptyProfile_ReportsNoChanges pins the "nothing to do"
// readout the CLI's "No mods to deploy." line is driven from.
func TestPlanDeploy_EmptyProfile_ReportsNoChanges(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan.Mods)
	assert.Empty(t, plan.Purge)
	assert.True(t, plan.NoChanges)
}

// TestApplyDeploy_StalePlan_ReturnsErrStalePlan pins Ruling 5 for deploy:
// an installed-mod set that changed since the plan was computed is refused,
// not silently applied.
func TestApplyDeploy_StalePlan_ReturnsErrStalePlan(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()

	plan, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")

	_, err = svc.ApplyDeploy(ctx, game, plan, core.DeployOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// TestApplyDeploy_NilPlan_IsRefused guards the one shape no caller should
// reach: Apply without a precondition to check.
func TestApplyDeploy_NilPlan_IsRefused(t *testing.T) {
	svc, game := newDeployableService(t)
	_, err := svc.ApplyDeploy(context.Background(), game, nil, core.DeployOptions{}, nil)
	require.Error(t, err)
}

// TestPlanDeploy_Compile_MergePlanNamesArtifactSourcesAndRawFallbacks covers
// the DeployCompile readout (#255) in plan form: the merged artifact's name
// comes from the game's compile source, Sources names the mods whose content
// rides it, and RawFallbacks names the ConvertPaks-opted-out paks that
// deploy raw instead.
func TestPlanDeploy_Compile_MergePlanNamesArtifactSourcesAndRawFallbacks(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&fakeCompilerSource{})
	game := setupCompileReadoutGame(t, svc)

	seedExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	seedEnabledPakMod(t, svc, game, "fake-compiler", "raw-pak", "1.0", "raw.pak", []byte("raw-pak-bytes"))
	require.NoError(t, svc.SetModConvertPaks(context.Background(), "fake-compiler", "raw-pak", game.ID, "default", false))
	seedLooseMod(t, svc, game, "loose", "Loose Mod", "loose.esp")

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.NoError(t, err)

	require.NotNil(t, plan.Merged)
	assert.Equal(t, "zzz_LMM_Merged_P.pak", plan.Merged.Artifact)
	assert.Equal(t, []string{"Bear Mount"}, plan.Merged.Sources)
	assert.Equal(t, []string{"raw-pak"}, plan.Merged.RawFallbacks)

	classes := map[string]core.DeployModClass{}
	for _, m := range plan.Mods {
		classes[m.Name] = m.Class
	}
	assert.Equal(t, core.DeployModMerged, classes["Bear Mount"])
	assert.Equal(t, core.DeployModRaw, classes["raw-pak"])
	assert.Equal(t, core.DeployModIndividual, classes["Loose Mod"])
}

// TestPlanDeploy_CacheMissingMod_RecordsRedownload pins the one shape a
// side-effect-free plan cannot enumerate files for: an absent cache entry,
// which the deploy heals by re-downloading from source. Task 24 review,
// Important #2: this is NOT a skip - the live deploy re-downloads and then
// deploys this mod, so the plan must say so (Redownload=true), not render it
// as a mod that would fail (Skipped).
func TestPlanDeploy_CacheMissingMod_RecordsRedownload(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "src", "gone", "Gone Mod", "1.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "gone", "1.0")

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.NoError(t, err)

	gone := planModByName(t, plan, "Gone Mod")
	assert.Empty(t, gone.Link)
	assert.Empty(t, gone.Remove)
	assert.True(t, gone.Redownload, "a cache-missing mod is a redownload-then-deploy, not a skip")
	assert.Empty(t, gone.Skipped, "Redownload replaces Skipped for this case - see DeployPlanMod's doc comment")
}

// TestPlanDeploy_InstalledModsReadFailure_PreservesHistoricalErrorText pins
// Important #1 from the Task 24 review: pre-lift, a failing installed-mods
// read reached the user from deployProfile as "getting installed mods: …";
// planDeploy must surface the SAME text, not currentInstalledSnapshot's
// "loading installed mods: …" (used by every other Plan, where there is no
// historical wording to preserve).
func TestPlanDeploy_InstalledModsReadFailure_PreservesHistoricalErrorText(t *testing.T) {
	svc, game := newDeployableService(t)
	require.NoError(t, svc.Close(), "closing the DB early forces the read planDeploy opens with to fail")

	_, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "getting installed mods:"),
		"want error prefixed %q (the pre-lift text), got %q", "getting installed mods:", err.Error())
}

// TestPlanDeploy_Purge_NothingDeployed_ListsNoPaths pins Minor #1 from the
// Task 24 review: a --purge pass lists only paths that are ACTUALLY deployed
// right now, not every installed mod's whole cache/DB removal-direction
// union. Mod One here is cached and installed (newDeployableService's
// fixture) but never linked into game.ModPath, so a purge of it undeploys
// nothing.
func TestPlanDeploy_Purge_NothingDeployed_ListsNoPaths(t *testing.T) {
	svc, game := newDeployableService(t)

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{Purge: true})
	require.NoError(t, err)
	assert.Empty(t, plan.Purge, "Mod One is cached and installed but never deployed to game.ModPath")
}

// TestDeployProfile_MatchesPlanPlusApply pins the convenience wrapper's
// equivalence: the same profile deployed through DeployProfile and through
// PlanDeploy+ApplyDeploy produces the same result and the same tree.
func TestDeployProfile_MatchesPlanPlusApply(t *testing.T) {
	ctx := context.Background()

	svcA, gameA := newDeployableService(t)
	resultA, err := svcA.DeployProfile(ctx, gameA, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	svcB, gameB := newDeployableService(t)
	plan, err := svcB.PlanDeploy(ctx, gameB, "default", core.DeployOptions{})
	require.NoError(t, err)
	resultB, err := svcB.ApplyDeploy(ctx, gameB, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, resultA, resultB)

	treeA, treeB := deployedTree(t, gameA.ModPath), deployedTree(t, gameB.ModPath)
	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, keys(treeA), keys(treeB))
}
