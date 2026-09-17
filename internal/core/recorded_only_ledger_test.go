package core_test

// #462 meets #466 and #451: a removal for a profile that is not the game's
// active one runs recorded-only (clearRecorded, and verify --fix's
// convergence), and each of those must still judge a file before it goes -
// a copy or hardlink the user changed is theirs (#466) - and must treat a
// record under an earlier mod_path as a purge does (#451).
//
// Each case below tries to make a recorded-only removal delete a file the
// user changed, and fails; the untouched case shows the same path still
// removes lmm's own file.

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

// demote makes ledgerState's default profile non-active, still listing k,
// with other active and listing nothing: every removal of k for default is
// then recorded-only.
func (f *legacyFixture) demote(t *testing.T) {
	t.Helper()
	f.profile(t, "default", false, "k")
	f.profile(t, "other", true)
}

// recordedOnlyRemovals are the recorded-only flows that remove mod k's
// deployed files for the demoted default profile; each returns every
// message it reported.
func recordedOnlyRemovals() []removalPath {
	ctx := context.Background()
	return []removalPath{
		{"uninstall", func(t *testing.T, f *legacyFixture) []string {
			result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
			require.NoError(t, err)
			require.True(t, result.RecordedOnly)
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"planned uninstall", func(t *testing.T, f *legacyFixture) []string {
			plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
			require.NoError(t, err)
			require.True(t, plan.RecordedOnly)
			result, err := f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
			require.NoError(t, err)
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"mod disable", func(t *testing.T, f *legacyFixture) []string {
			result, err := f.svc.DisableMod(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)
			require.True(t, result.RecordedOnly)
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"purge", func(t *testing.T, f *legacyFixture) []string {
			_, result := f.purge(t, "default")
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"verify --fix convergence", func(t *testing.T, f *legacyFixture) []string {
			// The mod no longer ships Data/k.esp, so default's row is stale.
			entry := f.svc.GetGameCache(f.game).ModPath(f.game.ID, "local", "k", "unknown")
			require.NoError(t, os.Remove(filepath.Join(entry, "Data", "k.esp")))
			report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
			require.NoError(t, err)
			var notes []string
			for _, fd := range report.Result.Findings {
				notes = append(notes, fd.Status+": "+fd.Note)
			}
			return notes
		}},
	}
}

func TestRecordedOnlyRemoval_KeepsAFileTheUserChanged(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		for _, edit := range userEdits(method) {
			for _, path := range recordedOnlyRemovals() {
				t.Run(method.String()+"/"+edit.name+"/"+path.name, func(t *testing.T) {
					f := ledgerState(t, method)
					f.demote(t)
					edit.apply(t, f.kPath())
					want := readLive(t, f.kPath())

					messages := path.run(t, f)

					assert.Equal(t, want, readLive(t, f.kPath()), "the user's file survives")
					assert.True(t, containsLine(messages, "Data/k.esp", "changed after lmm deployed it"),
						"the flow says it kept the file, and why: %q", messages)
				})
			}
		}
	}
}

func TestRecordedOnlyRemoval_StillRemovesLmmsOwnFile(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		for _, path := range recordedOnlyRemovals() {
			t.Run(method.String()+"/"+path.name, func(t *testing.T) {
				f := ledgerState(t, method)
				f.demote(t)

				messages := path.run(t, f)

				assert.NoFileExists(t, f.kPath())
				assert.False(t, containsLine(messages, "Data/k.esp", "left in place"), "%q", messages)
				assert.False(t, containsLine(messages, "unverified"), "a fingerprinted file is checked: %q", messages)
			})
		}
	}
}

// TestRecordedOnlyRemoval_AFileChangedAfterThePlanIsKept: the judge runs
// again at the moment of removal, so a file the user changes between a
// recorded-only plan and its apply is still kept.
func TestRecordedOnlyRemoval_AFileChangedAfterThePlanIsKept(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	f.demote(t)

	plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
	require.NoError(t, err)
	require.Contains(t, plan.Files, "Data/k.esp", "the plan removes lmm's own file")
	userEdits(domain.LinkCopy)[0].apply(t, f.kPath())

	result, err := f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{})
	require.NoError(t, err)

	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
	assert.True(t, containsLine(keptTexts(result.Kept), "user_file Data/k.esp", "changed after lmm deployed it"), "%+v", result.Kept)
}

// TestVerifyFix_ANonActiveProfileKeepsAUserFileOnlyItRecorded is the #466
// hazard the profile-scope review reproduced on a copy game: verify --fix
// for a profile that is not active, at a path only that profile recorded,
// deleted the file the user had put there.
func TestVerifyFix_ANonActiveProfileKeepsAUserFileOnlyItRecorded(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		t.Run(method.String(), func(t *testing.T) {
			f := ledgerState(t, method)
			f.demote(t)
			entry := f.svc.GetGameCache(f.game).ModPath(f.game.ID, "local", "k", "unknown")
			require.NoError(t, os.Remove(filepath.Join(entry, "Data", "k.esp")))
			userEdits(method)[0].apply(t, f.kPath())

			for run := 1; run <= 2; run++ {
				report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
				require.NoError(t, err)
				require.NotNil(t, report.Result)
				assert.Equal(t, "USER FILE", readLive(t, f.kPath()), "run %d", run)
			}
			assert.NotContains(t, f.recorded(t, "default", "k"), "Data/k.esp", "the user's file is no longer tracked")
		})
	}
}

// TestRecordedOnlyPurge_ClearsStrandedFilesFromWhereTheyWereDeployed: a
// recorded-only purge of a profile whose files are under an earlier
// mod_path removes them from there, names them in the plan, and keeps one
// the user changed, saying under which mod_path (#451, #466).
func TestRecordedOnlyPurge_ClearsStrandedFilesFromWhereTheyWereDeployed(t *testing.T) {
	f, oldPath, _ := movedState(t, domain.LinkCopy)
	f.demote(t)
	oldK := filepath.Join(oldPath, "Data", "k.esp")
	oldK2 := filepath.Join(oldPath, "Data", "k2.esp")
	userEdits(domain.LinkCopy)[0].apply(t, oldK)

	plan, result := f.purge(t, "default")

	assert.Equal(t, []core.PurgeStrandedPath{{Path: "Data/k2.esp", ModPath: oldPath}}, plan.Stranded,
		"the plan names the stranded file it removes")
	require.Len(t, plan.Kept, 1, "and the one it keeps: %+v", plan.Kept)
	assert.Equal(t, core.PurgeKeptPath{Path: "Data/k.esp", Reason: core.PurgeKeptUserFile, Note: plan.Kept[0].Note, ModPath: oldPath}, plan.Kept[0])
	assert.NoFileExists(t, oldK2, "lmm's own stranded file goes")
	assert.Equal(t, "USER FILE", readLive(t, oldK), "the user's stranded file stays")
	require.Len(t, result.Kept, 1, "%+v", result.Kept)
	assert.Equal(t, core.PurgeKeptUserFile, result.Kept[0].Reason)
	assert.Equal(t, oldPath, result.Kept[0].ModPath)
	assert.Contains(t, result.Kept[0].Note, "changed after lmm deployed it")
	assert.Empty(t, f.recorded(t, "default", "k"), "no record is left to strand")
}

// TestRecordedOnlyUninstall_LeavesStrandedFilesToAPurge: a one-mod
// recorded-only removal leaves a record under an earlier mod_path, and its
// file, to a purge - as the active profile's uninstall does (#451).
func TestRecordedOnlyUninstall_LeavesStrandedFilesToAPurge(t *testing.T) {
	ctx := context.Background()
	for _, path := range []string{"uninstall", "mod disable"} {
		t.Run(path, func(t *testing.T) {
			f, oldPath, _ := movedState(t, domain.LinkCopy)
			f.demote(t)
			oldK := filepath.Join(oldPath, "Data", "k.esp")

			switch path {
			case "uninstall":
				plan, err := f.svc.PlanUninstall(ctx, f.game, "default", "local", "k", core.UninstallOptions{KeepCache: true})
				require.NoError(t, err)
				require.True(t, plan.RecordedOnly)
				assert.Empty(t, plan.Files, "nothing under the current mod_path to remove")
				_, err = f.svc.ApplyUninstall(ctx, f.game, plan, core.UninstallOptions{KeepCache: true})
				require.NoError(t, err)
			case "mod disable":
				_, err := f.svc.DisableMod(ctx, f.game, "default", "local", "k")
				require.NoError(t, err)
			}

			assert.FileExists(t, oldK)
			assert.ElementsMatch(t, []string{"Data/k.esp", "Data/k2.esp"}, f.recorded(t, "default", "k"), "the stranded records stay for a purge")

			plan, _ := f.purge(t, "default")
			assert.Len(t, plan.Stranded, 2)
			assert.NoFileExists(t, oldK, "the purge clears them")
		})
	}
}
