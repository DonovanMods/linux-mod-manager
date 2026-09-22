package core_test

// #462: #445 settled that a game has one game directory, owned by its
// ACTIVE profile - deploy and apply refuse another profile, and purge runs
// recorded-only for one. Every other mutating flow follows the same rule:
//
//   - a deploy-direction write (install, update, rollback, enable, archive
//     import, adopt, verify --fix's deploying repairs) for a profile that is
//     not active is refused - at the plan and again at the apply - naming
//     `lmm profile switch`, and leaves the game directory exactly as it was;
//   - a removal (uninstall, disable) for such a profile is recorded-only: it
//     takes out what that profile alone recorded putting there, and nothing
//     the active profile, another profile or another game still claims;
//   - a profile import into such a profile records the profile without
//     deploying anything.
//
// The active profile's flows are unchanged.

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

// installedRow reads profile's row for src:modID.
func (f *backfillFixture) installedRow(t *testing.T, profile, modID string) domain.InstalledMod {
	t.Helper()
	row, err := f.svc.GetInstalledMod(context.Background(), "src", modID, f.game.ID, profile)
	require.NoError(t, err)
	return *row
}

func TestDeployDirectionFlows_ANonActiveProfileIsRefused(t *testing.T) {
	ctx := context.Background()
	archive := filepath.Join(t.TempDir(), "e.zip")
	require.NoError(t, os.WriteFile(archive, []byte("not read: the refusal comes first"), 0o644))

	flows := []struct {
		name string
		// run runs the flow for profile; row is the fixture's row for the
		// mod "bonly" under profile b, or "aonly" under a.
		run func(f *backfillFixture, profile string, row domain.InstalledMod) error
	}{
		{"PlanInstall", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.PlanInstall(ctx, f.game, p, "src", "new", false)
			return err
		}},
		{"PlanInstallMany", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.PlanInstallMany(ctx, f.game, p, []*domain.Mod{{ID: "new", SourceID: "src", GameID: f.game.ID}}, false)
			return err
		}},
		{"ApplyInstall", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.ApplyInstall(ctx, f.game, &core.InstallPlan{GameID: f.game.ID, Profile: p}, core.InstallOptions{}, nil)
			return err
		}},
		{"PlanUpdate", func(f *backfillFixture, p string, row domain.InstalledMod) error {
			_, err := f.svc.PlanUpdate(ctx, f.game, p, "src", row.ID)
			return err
		}},
		{"PlanUpdateFrom", func(f *backfillFixture, p string, row domain.InstalledMod) error {
			_, err := f.svc.PlanUpdateFrom(ctx, f.game, p, domain.Update{InstalledMod: row, NewVersion: "2.0"})
			return err
		}},
		{"ApplyUpdate", func(f *backfillFixture, _ string, row domain.InstalledMod) error {
			_, err := f.svc.ApplyUpdate(ctx, f.game, &core.UpdatePlan{Mod: row}, core.UpdateOptions{}, nil)
			return err
		}},
		{"PlanUpdateBatch", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.PlanUpdateBatch(ctx, f.game, p, nil)
			return err
		}},
		{"PlanUpdateBatchFrom", func(f *backfillFixture, p string, row domain.InstalledMod) error {
			_, err := f.svc.PlanUpdateBatchFrom(ctx, f.game, p, []domain.Update{{InstalledMod: row, NewVersion: "2.0"}}, nil)
			return err
		}},
		{"ApplyUpdateBatch", func(f *backfillFixture, p string, row domain.InstalledMod) error {
			_, err := f.svc.ApplyUpdateBatch(ctx, f.game, &core.UpdateBatchPlan{GameID: f.game.ID, Profile: p,
				Updates: []domain.Update{{InstalledMod: row, NewVersion: "2.0"}}}, core.UpdateBatchOptions{}, nil)
			return err
		}},
		{"PlanRollback", func(f *backfillFixture, p string, row domain.InstalledMod) error {
			_, err := f.svc.PlanRollback(ctx, f.game, p, "src", row.ID)
			return err
		}},
		{"ApplyRollback", func(f *backfillFixture, _ string, row domain.InstalledMod) error {
			row.PreviousVersion = "0.9"
			_, err := f.svc.ApplyRollback(ctx, f.game, &core.RollbackPlan{Mod: row}, core.RollbackOptions{}, nil)
			return err
		}},
		{"EnableMod", func(f *backfillFixture, p string, row domain.InstalledMod) error {
			_, err := f.svc.EnableMod(ctx, f.game, p, "src", row.ID)
			return err
		}},
		{"PlanImportArchive", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.PlanImportArchive(ctx, f.game, p, archive, core.ImportArchiveOptions{})
			return err
		}},
		{"ImportArchive", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.ImportArchive(ctx, f.game, p, archive, core.ImportArchiveOptions{}, nil)
			return err
		}},
		{"ApplyImportArchive", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.ApplyImportArchive(ctx, f.game, p, &core.ImportArchivePlan{Archive: archive}, core.ImportArchiveOptions{}, nil)
			return err
		}},
		{"PlanAdopt", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.PlanAdopt(ctx, f.game, p, core.AdoptOptions{})
			return err
		}},
		{"ApplyAdopt", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.ApplyAdopt(ctx, f.game, &core.AdoptPlan{GameID: f.game.ID, Profile: p}, nil)
			return err
		}},
		{"ApplyAdoptBackfill", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.ApplyAdoptBackfill(ctx, f.game, &core.AdoptPlan{GameID: f.game.ID, Profile: p}, nil)
			return err
		}},
		{"ApplyMergedPakRegen", func(f *backfillFixture, p string, _ domain.InstalledMod) error {
			_, err := f.svc.ApplyMergedPakRegen(ctx, f.game, p, nil)
			return err
		}},
	}

	for _, flow := range flows {
		t.Run(flow.name, func(t *testing.T) {
			f := liveDirFixture(t)
			before := treeOf(t, f.gameDir)
			rowsBefore, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
			require.NoError(t, err)

			err = flow.run(f, "b", f.installedRow(t, "b", "bonly"))

			requireRefusedForB(t, err)
			assert.Equal(t, before, treeOf(t, f.gameDir), "the game directory is byte-identical")
			rowsAfter, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
			require.NoError(t, err)
			assert.Equal(t, rowsBefore, rowsAfter, "b's rows are as they were")
		})
		t.Run(flow.name+"/for the active profile", func(t *testing.T) {
			f := liveDirFixture(t)
			err := flow.run(f, "a", f.installedRow(t, "a", "aonly"))
			assert.NotErrorIs(t, err, core.ErrProfileNotActive, "the active profile is not refused for being inactive")
			assert.NotErrorIs(t, err, core.ErrActiveProfileUnknown)
		})
		t.Run(flow.name+"/with no profile marked active", func(t *testing.T) {
			f := liveDirFixture(t)
			f.setFlag(t, "a", false)
			err := flow.run(f, "b", f.installedRow(t, "b", "bonly"))
			requireActiveUnknown(t, err)
		})
	}
}

// TestEnableMod_TheActiveProfileStillDeploys: the refusal is for other
// profiles only.
func TestEnableMod_TheActiveProfileStillDeploys(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)
	_, err := f.svc.DisableMod(ctx, f.game, "a", "src", "aonly")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"))

	result, err := f.svc.EnableMod(ctx, f.game, "a", "src", "aonly")

	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
}

// TestVerifyFix_ANonActiveProfileRepairsNothingThatDeploys: the loader
// re-layout re-deploys the verified profile's own copy of the mod, so on a
// profile that is not active it is refused, naming `lmm profile switch`,
// and the game directory is left as it is.
func TestVerifyFix_ANonActiveProfileRepairsNothingThatDeploys(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := stalePreFixJotunn(t, "default", "second")
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, "default"))
	before := treeOf(t, game.InstallPath)

	report, err := svc.VerifyReport(ctx, game, "second", core.VerifyOptions{Fix: true, Force: true}, nil)

	require.NoError(t, err)
	assert.Equal(t, before, treeOf(t, game.InstallPath), "the game directory is byte-identical")
	f := findingWithStatus(report.Result, "loader_deployed_outside_loader")
	require.NotNil(t, f, "statuses were %v", findingStatuses(report.Result))
	assert.False(t, f.Fixable)
	assert.Contains(t, f.FixableReason, "lmm profile switch second")

	plain, err := svc.VerifyReport(ctx, game, "second", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f = findingWithStatus(plain.Result, "loader_deployed_outside_loader")
	require.NotNil(t, f)
	assert.False(t, f.Fixable, "a plain run does not offer a repair --fix will refuse")
	assert.Contains(t, f.FixableReason, "lmm profile switch second")
}

func TestUninstall_ANonActiveProfileIsRecordedOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("a mod the active profile has deployed stays", func(t *testing.T) {
		f := liveDirFixture(t)
		before := treeOf(t, f.gameDir)

		plan, err := f.svc.PlanUninstall(ctx, f.game, "b", "src", "shared", core.UninstallOptions{})
		require.NoError(t, err)
		assert.True(t, plan.RecordedOnly)
		assert.Equal(t, "a", plan.ActiveProfile)
		assert.Empty(t, plan.Files, "b recorded nothing in the game directory")
		assert.Empty(t, plan.Hooks)

		result, err := f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		assert.True(t, result.RecordedOnly)
		assert.Equal(t, before, treeOf(t, f.gameDir), "the game directory is byte-identical")
		assert.Equal(t, []string{"profile a"}, result.CacheUsedBy, "a's row still uses the cache entry")
		_, err = f.svc.GetInstalledMod(ctx, "src", "shared", f.game.ID, "b")
		require.ErrorIs(t, err, domain.ErrModNotFound, "b's row went")
		assert.Equal(t, 1, f.refCount(t, "b"), "and so did b's reference")
		assert.Equal(t, 2, f.refCount(t, "a"), "a's document is untouched")
	})

	t.Run("only the files it alone recorded go", func(t *testing.T) {
		f := mixedDirFixture(t)

		plan, err := f.svc.PlanUninstall(ctx, f.game, "b", "src", "shared", core.UninstallOptions{})
		require.NoError(t, err)
		require.True(t, plan.RecordedOnly)
		assert.Empty(t, plan.Files, "a records shared.esp too")
		require.Len(t, plan.Kept, 1)
		assert.Equal(t, core.PurgeKeptRecorded, plan.Kept[0].Reason)
		assert.Equal(t, []string{"a"}, plan.Kept[0].Profiles)
		_, err = f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(f.gameDir, "shared.esp"))

		plan, err = f.svc.PlanUninstall(ctx, f.game, "b", "src", "bonly", core.UninstallOptions{KeepCache: true})
		require.NoError(t, err)
		assert.Equal(t, []string{"bonly.esp"}, plan.Files)
		result, err := f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{KeepCache: true})
		require.NoError(t, err)
		assert.Equal(t, []string{"bonly.esp"}, result.Removed)
		assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
		assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
		assert.FileExists(t, filepath.Join(f.gameDir, "shared.esp"))
	})

	t.Run("UninstallMod follows the same rule", func(t *testing.T) {
		f := liveDirFixture(t)
		before := treeOf(t, f.gameDir)
		result, err := f.svc.UninstallMod(ctx, f.game, "b", "src", "shared", core.UninstallOptions{})
		require.NoError(t, err)
		assert.True(t, result.RecordedOnly)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("a plan made while the profile was active is stale", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanUninstall(ctx, f.game, "a", "src", "aonly", core.UninstallOptions{})
		require.NoError(t, err)
		require.False(t, plan.RecordedOnly)
		require.NoError(t, f.svc.NewProfileManager().SetDefault(ctx, f.game.ID, "b"))
		before := treeOf(t, f.gameDir)

		_, err = f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})

		require.ErrorIs(t, err, core.ErrStalePlan)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("with no profile marked active", func(t *testing.T) {
		f := liveDirFixture(t)
		f.setFlag(t, "a", false)
		_, err := f.svc.PlanUninstall(ctx, f.game, "b", "src", "shared", core.UninstallOptions{})
		requireActiveUnknown(t, err)
	})

	t.Run("the active profile still removes its files", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanUninstall(ctx, f.game, "a", "src", "aonly", core.UninstallOptions{})
		require.NoError(t, err)
		assert.False(t, plan.RecordedOnly)
		assert.Equal(t, []string{"aonly.esp"}, plan.Files)
		_, err = f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		assert.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	})
}

func TestDisable_ANonActiveProfileIsRecordedOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("the active profile's copy of a shared mod stays", func(t *testing.T) {
		f := liveDirFixture(t)
		before := treeOf(t, f.gameDir)

		result, err := f.svc.DisableMod(ctx, f.game, "b", "src", "shared")

		require.NoError(t, err)
		assert.True(t, result.RecordedOnly)
		assert.Equal(t, "a", result.ActiveProfile)
		assert.True(t, result.Changed)
		assert.Equal(t, before, treeOf(t, f.gameDir), "the game directory is byte-identical")
		assert.Equal(t, []string{"shared"}, f.disabledRefs(t, "b"), "b's document says off")
		assert.Empty(t, f.disabledRefs(t, "a"))
		assert.True(t, f.installedRow(t, "a", "shared").Enabled)
	})

	t.Run("what it alone recorded goes", func(t *testing.T) {
		f := mixedDirFixture(t)

		result, err := f.svc.DisableMod(ctx, f.game, "b", "src", "bonly")

		require.NoError(t, err)
		assert.Equal(t, []string{"bonly.esp"}, result.Removed)
		assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
		assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
		assert.FileExists(t, filepath.Join(f.gameDir, "shared.esp"))
		assert.False(t, f.installedRow(t, "b", "bonly").Deployed)
	})

	t.Run("with no profile marked active", func(t *testing.T) {
		f := liveDirFixture(t)
		f.setFlag(t, "a", false)
		_, err := f.svc.DisableMod(ctx, f.game, "b", "src", "shared")
		requireActiveUnknown(t, err)
	})

	t.Run("the active profile still undeploys", func(t *testing.T) {
		f := liveDirFixture(t)
		result, err := f.svc.DisableMod(ctx, f.game, "a", "src", "aonly")
		require.NoError(t, err)
		assert.False(t, result.RecordedOnly)
		assert.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	})
}

func TestProfileImport_ANonActiveProfileIsRecordedOnly(t *testing.T) {
	ctx := context.Background()
	doc := func(name string) []byte {
		return []byte("name: " + name + "\ngame_id: g1\nmods:\n  - source_id: src\n    mod_id: aonly\n    version: \"1.0\"\n")
	}

	for _, target := range []string{"b", "c"} {
		t.Run("into "+target, func(t *testing.T) {
			f := liveDirFixture(t)
			before := treeOf(t, f.gameDir)

			plan, err := f.svc.PlanImport(ctx, f.game, doc(target))
			require.NoError(t, err)
			assert.True(t, plan.RecordedOnly)
			assert.Equal(t, "a", plan.ActiveProfile)
			require.Len(t, plan.AlreadyCached, 1)

			result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Force: true, Install: true}, nil)

			require.NoError(t, err)
			assert.True(t, result.RecordedOnly)
			assert.Equal(t, 1, result.Recorded)
			assert.Zero(t, result.Installed)
			assert.Equal(t, before, treeOf(t, f.gameDir), "the game directory is byte-identical")
			row := f.installedRow(t, target, "aonly")
			assert.False(t, row.Deployed, "recorded, not deployed")
			assert.True(t, row.Enabled, "the document lists it on")
			files, err := f.svc.GetDeployedFilesForMod(ctx, f.game.ID, target, "src", "aonly")
			require.NoError(t, err)
			assert.Empty(t, files)
		})
	}

	t.Run("into the active profile it still deploys", func(t *testing.T) {
		f := liveDirFixture(t)
		require.NoError(t, os.Remove(filepath.Join(f.gameDir, "aonly.esp")))
		_, err := f.svc.UninstallMod(ctx, f.game, "a", "src", "aonly", core.UninstallOptions{KeepCache: true})
		require.NoError(t, err)
		require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "src", "aonly", "1.0", "aonly.esp", []byte("aonly")))
		f.row(t, "b", "aonly", true, false)

		plan, err := f.svc.PlanImport(ctx, f.game, doc("a"))
		require.NoError(t, err)
		assert.False(t, plan.RecordedOnly)
		result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Force: true, Install: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Installed)
		assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	})

	t.Run("a plan whose target became active since is stale", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanImport(ctx, f.game, doc("b"))
		require.NoError(t, err)
		require.NoError(t, f.svc.NewProfileManager().SetDefault(ctx, f.game.ID, "b"))
		_, err = f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Force: true, Install: true}, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
	})

	t.Run("with no profile marked active", func(t *testing.T) {
		f := liveDirFixture(t)
		f.setFlag(t, "a", false)
		_, err := f.svc.PlanImport(ctx, f.game, doc("c"))
		requireActiveUnknown(t, err)
	})
}

// TestSnapshotRestore_RefusesToGuessTheActiveProfile is #462's G2-4: with
// no profile marked active, the restore used GetDefault's first-profile
// guess to decide whose files to purge.
func TestSnapshotRestore_RefusesToGuessTheActiveProfile(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)
	_, err := f.svc.CreateSnapshot(ctx, f.game, "b", "snap")
	require.NoError(t, err)
	f.setFlag(t, "a", false)

	_, err = f.svc.PlanSnapshotRestore(ctx, f.game, "snap")

	requireActiveUnknown(t, err)
}

// TestDeployTarget_AGameWithNoProfileFileStillHasALiveProfile: a game with
// no profile file may be installed into under any name - the install
// creates its first, active profile - unless its DB still records another
// profile's deployment in the directory (the profile files were deleted by
// hand). The missing target cannot be switched to, so the refusal names the
// creation that makes it active and the recorded owner's terminating purge.
func TestDeployTarget_AGameWithNoProfileFileStillHasALiveProfile(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink})
	require.NoError(t, f.svc.SaveGame(ctx, &domain.Game{ID: "other", Name: "Other", ModPath: t.TempDir()}))
	require.NoError(t, f.svc.SetDefaultGame(ctx, "other"))

	require.NoError(t, f.svc.CheckDeployTarget(ctx, "sky", "foo", core.VerbInstall), "a fresh game takes any first profile")

	f.deployed(t, "default", "a", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)

	err := f.svc.CheckDeployTarget(ctx, "sky", "foo", core.VerbInstall)
	require.ErrorIs(t, err, core.ErrProfileNotActive)
	assert.Contains(t, err.Error(), "has no profile file")
	assert.Contains(t, err.Error(), "lmm profile create foo --game sky")
	assert.Contains(t, err.Error(), "lmm purge -p default --game sky")
	assert.NotContains(t, err.Error(), "--game other")
	assert.NotContains(t, err.Error(), "lmm profile switch foo")
	assert.NoError(t, f.svc.CheckDeployTarget(ctx, "sky", "default", core.VerbInstall), "the profile the records name is the live one")
}

func TestShellQuoteArg(t *testing.T) {
	assert.Equal(t, "plain-id", core.ShellQuoteArg("plain-id"))
	assert.Equal(t, "'a b'", core.ShellQuoteArg("a b"))
	assert.Equal(t, "'a'\"'\"'b'", core.ShellQuoteArg("a'b"))
}
