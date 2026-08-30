package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/require"
)

// --- v2 Phase 3 Ruling 8: UninstallPlan/PurgePlan model the merged artifact ---
//
// Before this, both renderers printed their merged-artifact line for EVERY
// DeployCompile game, whether or not the flow would touch the artifact at
// all. The plans now carry MergedArtifact *MergedArtifactEffect - nil when
// the game does not deploy by compilation, and nil when nothing would
// change - and the renderers print only what the plan actually says.
//
// "Nothing would change" is judged against the merge INPUTS (the enabled
// retained merge sources the pending mutation leaves behind) plus the
// artifact's presence in the game directory, because that is exactly what
// syncMergedPak/purgeMergedPak themselves act on.

// mergedArtifactName is the fake compile source's artifact filename, which
// is also the game-dir-relative path MergedArtifactEffect.Path carries.
const mergedArtifactName = "zzz_LMM_Merged_P.pak"

// seedPlainMod installs an enabled mod that is NOT a merge source: a
// deployable file in the cache, no retained source, so it contributes
// nothing to the merged artifact.
func seedPlainMod(t *testing.T, svc *core.Service, game *domain.Game, modID, fileName string) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "fake-compiler", modID, "1.0", fileName, []byte("plain")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: modID, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, svc.NewProfileManager().UpsertMod(game.ID, "default",
		domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: "1.0"}))
}

// TestPlanUninstall_MergedArtifact_NilOnNonCompileGame: a game that deploys
// files directly has no merged artifact for the plan to describe.
func TestPlanUninstall_MergedArtifact_NilOnNonCompileGame(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.DeployMode = domain.DeployExtract
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedPlainMod(t, svc, game, "plain", "plain.esp")

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "plain", core.UninstallOptions{})
	require.NoError(t, err)
	require.Nil(t, plan.MergedArtifact)
}

// TestPlanUninstall_MergedArtifact_ResyncWhenSourcesRemain: uninstalling one
// of two contributing mods leaves the artifact in place but rebuilt.
func TestPlanUninstall_MergedArtifact_ResyncWhenSourcesRemain(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "bear-mount", core.UninstallOptions{})
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact)
	require.Equal(t, core.MergedArtifactResync, plan.MergedArtifact.Action)
	require.Equal(t, mergedArtifactName, plan.MergedArtifact.Path)
}

// TestPlanUninstall_MergedArtifact_RemoveWhenLastSourceGoes: syncMergedPak's
// uninstall-to-zero branch takes the artifact out of the game directory.
func TestPlanUninstall_MergedArtifact_RemoveWhenLastSourceGoes(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "bear-mount", core.UninstallOptions{})
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact)
	require.Equal(t, core.MergedArtifactRemove, plan.MergedArtifact.Action)
	require.Equal(t, mergedArtifactName, plan.MergedArtifact.Path)
}

// TestPlanUninstall_MergedArtifact_NilWhenModContributesNothing is Ruling
// 8's own case: a compile game whose uninstall target is not a merge source
// leaves the artifact byte-identical, so the plan says nothing about it.
func TestPlanUninstall_MergedArtifact_NilWhenModContributesNothing(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	seedPlainMod(t, svc, game, "plain", "plain.esp")
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "plain", core.UninstallOptions{})
	require.NoError(t, err)
	require.Nil(t, plan.MergedArtifact, "a non-merge-source uninstall changes no merge input")
}

// TestPlanUninstall_MergedArtifact_ResyncWhenFingerprintUndecidable pins the
// unit Q review's M6: when the "mod contributes nothing" branch cannot
// DECIDE - here currentMergedFingerprint fails because the base pak is gone
// - the answer must be a resync, not nil. nil renders as "the merged
// artifact is untouched", i.e. silence, which under-states; the sibling
// branch immediately above (stored fingerprint unreadable) already answers
// resync for the same class of "we can't tell", and the superseded decisions
// row was explicit that this modelling errs toward over-stating. Only
// mergeCompilerForGame failing still yields nil, since without it there is
// no artifact name to print.
func TestPlanUninstall_MergedArtifact_ResyncWhenFingerprintUndecidable(t *testing.T) {
	svc, game, basePak := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	seedPlainMod(t, svc, game, "plain", "plain.esp")
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	// Removing the base pak makes ResolveBaseArtifact - and so
	// currentMergedFingerprint - fail, without touching the merge INPUTS
	// (retained cache files) or the deployed artifact, so the plan reaches
	// the "mod contributes nothing" branch exactly as
	// TestPlanUninstall_MergedArtifact_NilWhenModContributesNothing does.
	require.NoError(t, os.Remove(basePak))

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "plain", core.UninstallOptions{})
	require.NoError(t, err, "an undecidable fingerprint is a modelling gap, not a planning failure")
	require.NotNil(t, plan.MergedArtifact, "M6: an undecidable comparison must not read as \"nothing happens\"")
	require.Equal(t, core.MergedArtifactResync, plan.MergedArtifact.Action)
	require.Equal(t, mergedArtifactName, plan.MergedArtifact.Path)
}

// TestPlanUninstall_MergedArtifact_NilWhenNoArtifactExists: nothing was ever
// merged, and removing the last source would not create one either.
func TestPlanUninstall_MergedArtifact_NilWhenNoArtifactExists(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "bear-mount", core.UninstallOptions{})
	require.NoError(t, err)
	require.Nil(t, plan.MergedArtifact)
}

// TestPlanUninstall_MergedArtifact_ResyncWhenArtifactMissing pins
// syncMergedPak's self-healing redeploy (#197 I5): sources remain but the
// artifact is gone from the game directory, so the sync puts it back.
func TestPlanUninstall_MergedArtifact_ResyncWhenArtifactMissing(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	seedPlainMod(t, svc, game, "plain", "plain.esp")
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(game.ModPath, mergedArtifactName)))

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "plain", core.UninstallOptions{})
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact)
	require.Equal(t, core.MergedArtifactResync, plan.MergedArtifact.Action)
}

// --- purge ---

func TestPlanPurge_MergedArtifact_NilOnNonCompileGame(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.DeployMode = domain.DeployExtract
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedPlainMod(t, svc, game, "plain", "plain.esp")

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.Nil(t, plan.MergedArtifact)
}

// TestPlanPurge_MergedArtifact_RemoveWhenDeployed: `lmm purge` always takes
// the artifact out (purgeMergedPak), so a deployed one is always removed.
func TestPlanPurge_MergedArtifact_RemoveWhenDeployed(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact)
	require.Equal(t, core.MergedArtifactRemove, plan.MergedArtifact.Action)
	require.Equal(t, mergedArtifactName, plan.MergedArtifact.Path)
}

// TestPlanPurge_MergedArtifact_NilWhenNothingDeployed is Ruling 8's purge
// case: a compile game with no merged artifact on disk has nothing to
// remove, so the plan says nothing about it.
func TestPlanPurge_MergedArtifact_NilWhenNothingDeployed(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.Nil(t, plan.MergedArtifact)
}

// --- the plans predict what the flows then do ---

// TestUninstallPlan_MergedArtifact_PredictsTheLiveUninstall proves the
// modelling is not just a second opinion: the artifact really is gone
// afterwards when the plan said "remove".
func TestUninstallPlan_MergedArtifact_PredictsTheLiveUninstall(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	opts := core.UninstallOptions{}
	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "bear-mount", opts)
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact)
	require.Equal(t, core.MergedArtifactRemove, plan.MergedArtifact.Action)

	_, err = svc.ApplyUninstall(context.Background(), game, plan, opts)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(game.ModPath, plan.MergedArtifact.Path))
	require.True(t, os.IsNotExist(statErr), "the live uninstall must remove what the plan said it would")
}

// TestUninstallPlan_MergedArtifact_PredictsTheLiveUninstall_ResyncOnStaleFingerprint
// is the resync half of the prediction pair (Important #1): a stale STORED
// fingerprint - a base-game patch since the last sync, #196's "Friday
// recompile" - must be caught even when the uninstalled mod is not itself a
// merge source, because syncMergedPak's own fast path compares the stored
// fingerprint against the current one, not just "did this mod contribute".
func TestUninstallPlan_MergedArtifact_PredictsTheLiveUninstall_ResyncOnStaleFingerprint(t *testing.T) {
	svc, game, basePak := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	seedPlainMod(t, svc, game, "plain", "plain.esp")
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 1, src.compileCalls, "fixture: the initial sync must have compiled once")

	// Simulate a base-game patch since the last sync - the stored
	// fingerprint is now stale, even though "plain" never contributed to
	// the merge.
	writeFakeBasePakWithTable(t, basePak, map[string][]byte{"AI/D_Other.json": []byte(`{"Rows":[{"Name":"x","V":1}]}`)})

	opts := core.UninstallOptions{}
	plan, err := svc.PlanUninstall(context.Background(), game, "default", "", "plain", opts)
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact, "a stale base pak must resync even though the uninstalled mod never merged")
	require.Equal(t, core.MergedArtifactResync, plan.MergedArtifact.Action)

	_, err = svc.ApplyUninstall(context.Background(), game, plan, opts)
	require.NoError(t, err)
	require.Equal(t, 2, src.compileCalls, "the live uninstall must really rebuild the artifact the plan said it would resync")
}

// TestPurgePlan_MergedArtifact_PredictsTheLivePurge is the purge half.
func TestPurgePlan_MergedArtifact_PredictsTheLivePurge(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	opts := core.PurgeOptions{}
	plan, err := svc.PlanPurge(context.Background(), game, "default", opts)
	require.NoError(t, err)
	require.NotNil(t, plan.MergedArtifact)

	_, err = svc.ApplyPurge(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(game.ModPath, plan.MergedArtifact.Path))
	require.True(t, os.IsNotExist(statErr), "the live purge must remove what the plan said it would")
}

func requireArtifactDeployed(t *testing.T, game *domain.Game) {
	t.Helper()
	_, err := os.Stat(filepath.Join(game.ModPath, mergedArtifactName))
	require.NoError(t, err, "fixture: the merged artifact must be deployed")
}
