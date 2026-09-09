package core_test

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

// The §2 flow table of docs/plans/2026-09-09-steam-workshop-design.md, one
// test per row. An EXTERNAL mod is one lmm tracks but never deploys: the
// Steam client owns its files where they sit. Every rule below is about
// lmm NOT touching those files, and saying so plainly.

// seedExternalMod records a Steam Workshop item the way ApplyWorkshopAdopt
// does - a DB row and a profile ref, no cache entry, nothing under
// mod_path - and creates the Steam-owned content directory it points at.
func seedExternalMod(t *testing.T, svc *core.Service, game *domain.Game, profileName, modID, name string) string {
	t.Helper()
	steamDir := filepath.Join(t.TempDir(), "workshop", "content", "1133870", modID)
	require.NoError(t, os.MkdirAll(steamDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(steamDir, "mod.pak"), []byte("steam owns this"), 0o644))

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: modID, SourceID: "steamworkshop", Name: name,
			Version: "7987119735124793734", GameID: game.ID,
		},
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		External:     true,
		ExternalPath: steamDir,
	}))
	seedProfileWithMod(t, svc, game.ID, profileName, "steamworkshop", modID, "7987119735124793734")
	return steamDir
}

func externalTestGame(t *testing.T) *domain.Game {
	t.Helper()
	return &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"steamworkshop": "1133870"},
	}
}

// --- Row: lmm status / GameStatus ---

func TestExternal_GameStatus_CountsExternalSeparately(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)

	seedNamedInstalledMod(t, svc, game, "src", "1", "Managed Mod", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	status, err := svc.GameStatus(context.Background(), game)
	require.NoError(t, err)
	assert.Equal(t, 2, status.InstalledModCount, "an external mod is installed")
	assert.Equal(t, 2, status.EnabledModCount, "and enabled")
	assert.Equal(t, 1, status.ExternalCount, `so the readout can say "2 installed (1 tracked from Steam)"`)
}

// --- Row: lmm list / ModList ---

func TestExternal_ListMods_CarriesTheExternalKeys(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 1)
	assert.True(t, list.Mods[0].External)
	assert.Equal(t, steamDir, list.Mods[0].ExternalPath)
}

// --- Row: deploy ---

func TestExternal_PlanDeploy_ClassesExternalAndLinksNothing(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Mods, 1)
	assert.Equal(t, core.DeployModExternal, plan.Mods[0].Class)
	assert.Empty(t, plan.Mods[0].Link, "lmm links nothing for a mod Steam already placed")
	assert.Empty(t, plan.Mods[0].Remove)
	assert.False(t, plan.Mods[0].Redownload, "there is nothing to re-download")
	assert.Empty(t, plan.Mods[0].Skipped, "it is listed as external, not as a failure")
}

func TestExternal_ApplyDeploy_TouchesNothingUnderModPath(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	plan, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.NoError(t, err)

	var phases []core.DeployPhase
	result, err := svc.ApplyDeploy(context.Background(), game, plan, core.DeployOptions{}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok {
			phases = append(phases, fe.FlowPhase())
		}
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Deployed, "an external mod is not a deployment")
	assert.Contains(t, phases, core.DeployExternalSkipped)

	entries, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "the game's mod directory must be untouched")
}

func TestExternal_DeployTargetingOneExternalMod_IsRefused(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	_, err := svc.DeployProfile(context.Background(), game, "default",
		core.DeployOptions{ModID: "3617086610", SourceID: "steamworkshop"}, nil)
	require.Error(t, err)
	assertExternalRefusal(t, err, "deploy")
}

// --- Row: purge ---

func TestExternal_PlanPurge_ExcludesExternalAndNamesIt(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedNamedInstalledMod(t, svc, game, "src", "1", "Managed Mod", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedProfileWithMod(t, svc, game.ID, "default", "src", "1", "1.0")
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Mods, 1, "only the managed mod is purgeable")
	assert.Equal(t, "Managed Mod", plan.Mods[0].Name)
	assert.Equal(t, []string{"Workshop Item"}, plan.External, "the preview says what it will not touch")
}

func TestExternal_ApplyPurge_LeavesSteamsFilesAndTheRowAlone(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{Uninstall: true})
	require.NoError(t, err)
	_, err = svc.ApplyPurge(context.Background(), game, plan, core.PurgeOptions{Uninstall: true}, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(steamDir, "mod.pak"), "Steam's own files are never removed")
	mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3617086610", game.ID, "default")
	require.NoError(t, err, "a purge --uninstall must not drop the tracking row either")
	assert.True(t, mod.Deployed, "Deployed stays true: the files are where the game reads them")
}

// --- Row: convergeDeployedFiles ---

func TestExternal_Converge_IsUnaffected(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	removed, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.NoError(t, err)
	assert.Empty(t, removed, "converge reconciles files under mod_path, and an external mod owns none")
	assert.FileExists(t, filepath.Join(steamDir, "mod.pak"))
}

// --- Row: verify ---

func TestExternal_Verify_PresenceTierOnly(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	assert.Zero(t, report.Result.Issues, "a present item is fine")

	// Steam no longer has it: the one thing verify CAN see.
	require.NoError(t, os.RemoveAll(steamDir))
	report, err = svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, report.Result.Findings)
	var found *core.VerifyFinding
	for i := range report.Result.Findings {
		if report.Result.Findings[i].ModID == "3617086610" {
			found = &report.Result.Findings[i]
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, "external_missing", found.Status)
	assert.Contains(t, found.Note, "unsubscribed")
	assert.False(t, found.Fixable, "redownload and checksum backfill both presuppose an lmm-owned cache entry")
	assert.NotEmpty(t, found.FixableReason)
}

func TestExternal_VerifyFix_OffersNoRepair(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")
	require.NoError(t, os.RemoveAll(steamDir))

	report, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Tier: core.VerifyFull, Fix: true}, nil)
	require.NoError(t, err)
	assert.NoDirExists(t, steamDir, "--fix must not try to recreate Steam's directory")
	require.NotEmpty(t, report.Result.Findings)
}

// --- Row: uninstall ---

func TestExternal_PlanUninstall_IsTrackingOnly(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "steamworkshop", "3617086610", core.UninstallOptions{})
	require.NoError(t, err)
	assert.True(t, plan.External)
	assert.Empty(t, plan.Files, "there are no lmm-deployed files to remove")
}

func TestExternal_UninstallMod_RemovesTrackingAndNothingElse(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	plan, err := svc.PlanUninstall(context.Background(), game, "default", "steamworkshop", "3617086610", core.UninstallOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyUninstall(context.Background(), game, plan, core.UninstallOptions{})
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(steamDir, "mod.pak"), "the item stays subscribed in Steam")
	_, err = svc.GetInstalledMod(context.Background(), "steamworkshop", "3617086610", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "lmm's tracking row is gone")
}

// --- Row: profile switch / apply / sync ---

func TestExternal_ProfileSwitch_LeavesSteamAloneAndSaysSo(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	steamDir := seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")
	_, cerr := svc.NewProfileManager().Create(context.Background(), game.ID, "other")
	require.NoError(t, cerr)

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "other")
	require.NoError(t, err)
	assert.Equal(t, 1, plan.ExternalUnchanged,
		"a Workshop item is game-global; lmm profiles are not")

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(steamDir, "mod.pak"))
	assertHasExternalNote(t, result.Notes)
}

// --- Row: conflicts ---

func TestExternal_Conflicts_NeverIncludeAnExternalMod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")
	seedExternalMod(t, svc, game, "default", "3512001122", "Other Workshop Item")

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Empty(t, conflicts,
		"conflict detection compares deployed paths under mod_path, and external mods have none")
}

// --- Row: mod enable / mod disable ---

func TestExternal_EnableAndDisable_AreRefused(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	_, err := svc.DisableMod(context.Background(), game, "default", "steamworkshop", "3617086610")
	require.Error(t, err)
	assertExternalRefusal(t, err, "disable")
	assert.Contains(t, err.Error(), "unsubscribe it in Steam")

	_, err = svc.EnableMod(context.Background(), game, "default", "steamworkshop", "3617086610")
	require.Error(t, err)
	assertExternalRefusal(t, err, "enable")
}

// --- Row: update (apply) / rollback / mod edit ---

func TestExternal_UpdateApplyRollbackAndRelink_AreRefused(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	// The workshop source must be registered for PlanUpdate's own check to
	// run at all: the refusal is the PLAN's answer, not a lookup failure.
	svc.RegisterSource(newAdoptTestSource("steamworkshop"))
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")
	ctx := context.Background()

	t.Run("PlanUpdate fills the existing Refusal field", func(t *testing.T) {
		plan, err := svc.PlanUpdate(ctx, game, "default", "steamworkshop", "3617086610")
		require.NoError(t, err, "planning is still allowed: the refusal is the plan's answer")
		assert.True(t, plan.External)
		assert.NotEmpty(t, plan.Refusal)
	})

	t.Run("ApplyUpdate refuses", func(t *testing.T) {
		plan, err := svc.PlanUpdate(ctx, game, "default", "steamworkshop", "3617086610")
		require.NoError(t, err)
		_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
		require.Error(t, err)
		assertExternalRefusal(t, err, "update")
	})

	t.Run("PlanRollback refuses", func(t *testing.T) {
		_, err := svc.PlanRollback(ctx, game, "default", "steamworkshop", "3617086610")
		require.Error(t, err)
		assertExternalRefusal(t, err, "rollback")
	})

	t.Run("PlanRelinkMod refuses", func(t *testing.T) {
		_, err := svc.PlanRelinkMod(ctx, game, "default", "steamworkshop", "3617086610", "nexusmods", "42")
		require.Error(t, err)
		assertExternalRefusal(t, err, "relink")
	})
}

// A Workshop update is a normal, expected, REPORTABLE state (design §3):
// `lmm update --all` must record it as a skip carrying the design's own
// refusal sentence, never as a red failure. Before the fix the item landed
// in result.Failed and the CLI printed "âœ— <name>: cannot update ...".
func TestExternal_UpdateBatch_ReportsAnExternalItemAsSkippedNotFailed(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	svc.RegisterSource(newAdoptTestSource("steamworkshop"))
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")
	ctx := context.Background()

	mod, err := svc.GetInstalledMod(ctx, "steamworkshop", "3617086610", game.ID, "default")
	require.NoError(t, err)
	upd := domain.Update{InstalledMod: *mod, NewVersion: "9987119735124793734"}

	plan, err := svc.PlanUpdateBatchFrom(ctx, game, "default", []domain.Update{upd},
		[]string{domain.ModKey("steamworkshop", "3617086610")})
	require.NoError(t, err)

	result, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Failed, "a Workshop update is expected, not a failure")
	assert.Empty(t, result.Applied)
	require.Len(t, result.Skipped, 1)

	skip := result.Skipped[0]
	assert.Equal(t, "Workshop Item", skip.Name)
	assert.Equal(t, core.UpdateSkipped, skip.Status)
	assert.Equal(t, core.ReasonExternalNoUpdate, skip.Reason,
		"the skip carries the design's sentence verbatim")
	assert.False(t, skip.Mod.Locked, "external is not locked - the CLI splits the two")
	assert.Equal(t, mod.Version, skip.Mod.Version, "nothing was written: the ref stands as it was")
	assert.Equal(t, "9987119735124793734", skip.ToVersion)
}

func TestExternal_SetUpdatePolicyAuto_IsRefusedButNotifyAndPinnedAreNot(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")
	ctx := context.Background()

	_, err := svc.SetModUpdatePolicy(ctx, "steamworkshop", "3617086610", game.ID, "default", domain.UpdateAuto)
	require.Error(t, err)
	assertExternalRefusal(t, err, "set update policy")

	_, err = svc.SetModUpdatePolicy(ctx, "steamworkshop", "3617086610", game.ID, "default", domain.UpdatePinned)
	require.NoError(t, err, "pinned is meaningful: it silences the notification")
	_, err = svc.SetModUpdatePolicy(ctx, "steamworkshop", "3617086610", game.ID, "default", domain.UpdateNotify)
	require.NoError(t, err)
}

// --- Row: install (Q2 exclusivity) ---

func TestExternal_InstallingAnAlreadyTrackedItem_IsRefused(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	err := svc.CheckExternalInstallExclusivity(context.Background(), game.ID, "default", "steamworkshop", "3617086610")
	require.Error(t, err)
	assertExternalRefusal(t, err, "install")
	assert.Contains(t, err.Error(), "uninstall it first")
}

// --- Row: profile reorder (Q1 omission) ---

func TestExternal_Reorder_OmitsExternalMods(t *testing.T) {
	svc := newFlowsTestService(t)
	game := externalTestGame(t)
	seedNamedInstalledMod(t, svc, game, "src", "1", "Managed Mod", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedProfileWithMod(t, svc, game.ID, "default", "src", "1", "1.0")
	seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	// The external mod is not ORDERABLE - naming it is the same error as
	// naming a mod that is not in the profile - but its ref still comes
	// back, appended after the ordered ones, because ReorderMods REPLACES
	// the profile's whole mod list: dropping it here would silently delete
	// the mod from the profile.
	refs, err := svc.ResolveReorder(context.Background(), game, "default", []string{"1"})
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, "1", refs[0].ModID, "the orderable mod leads")
	assert.Equal(t, "3617086610", refs[1].ModID, "the external ref survives, unmovable, at the end")

	_, err = svc.ResolveReorder(context.Background(), game, "default", []string{"3617086610"})
	require.ErrorIs(t, err, core.ErrModNotInProfile,
		"an external mod deploys nothing, so any position it held would be inert")
}

// --- Shared assertions ---

func assertExternalRefusal(t *testing.T, err error, op string) {
	t.Helper()
	require.ErrorIs(t, err, domain.ErrExternalMod)
	var ext *core.ExternalModError
	require.ErrorAs(t, err, &ext)
	assert.Equal(t, op, ext.Op)
	assert.NotEmpty(t, ext.Reason)
	assert.NotEmpty(t, ext.ModName)
}

func assertHasExternalNote(t *testing.T, notes []string) {
	t.Helper()
	for _, n := range notes {
		if assert.ObjectsAreEqual(true, len(n) > 0) && containsSteamAdvisory(n) {
			return
		}
	}
	t.Fatalf("expected a Steam Workshop advisory note, got %q", notes)
}

func containsSteamAdvisory(s string) bool {
	return len(s) > 0 && (indexOf(s, "Steam Workshop item") >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
