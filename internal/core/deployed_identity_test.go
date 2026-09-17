package core_test

// #466: under the copy and hardlink link methods a deployed file is a
// regular file, so a file the user put in its place looked exactly like
// lmm's own, and every removal deleted it. A deploy now records what it
// wrote, and every removal compares first: a mismatch is the user's file,
// which stays and is reported.
//
// Each case below tries to make a removal path delete a file the user
// replaced, and fails; the untouched case shows the same path still
// removes lmm's own file.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledgerState is sky with profile default active, listing mod k, which
// ships Data/k.esp and Data/k2.esp, deployed by method through a real
// deploy - so its rows carry fingerprints.
func ledgerState(t *testing.T, method domain.LinkMethod) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
	f.profile(t, "default", true, "k")
	f.deployed(t, "default", "k", method, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"}, nil)
	_, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return f
}

// kPath is where mod k's first file is deployed.
func (f *legacyFixture) kPath() string {
	return filepath.Join(f.game.ModPath, "Data", "k.esp")
}

// userEdit is how the user changed the deployed file.
type userEdit struct {
	name  string
	apply func(t *testing.T, path string)
}

// userEdits are the ways a user makes a deployed file theirs: replace it
// with a new file, or (under copy) rewrite it in place - including at the
// same size with the old mtime put back, which only the checksum catches.
func userEdits(method domain.LinkMethod) []userEdit {
	edits := []userEdit{{"replaced", func(t *testing.T, path string) {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.WriteFile(path, []byte("USER FILE"), 0o644))
	}}}
	if method == domain.LinkCopy {
		edits = append(edits, userEdit{"edited in place, same size and mtime", func(t *testing.T, path string) {
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte("USER K"), 0o644)) // "mod k" is 5 bytes
			require.NoError(t, os.Truncate(path, info.Size()))
			require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
		}})
	}
	return edits
}

// removalPath is one flow that removes mod k's deployed files, run on the
// ledgerState fixture; it returns every message the flow reported.
type removalPath struct {
	name string
	run  func(t *testing.T, f *legacyFixture) []string
}

func removalPaths() []removalPath {
	ctx := context.Background()
	return []removalPath{
		{"purge", func(t *testing.T, f *legacyFixture) []string {
			plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			require.False(t, plan.RecordedOnly)
			result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"recorded-only purge", func(t *testing.T, f *legacyFixture) []string {
			f.profile(t, "default", false, "k")
			f.profile(t, "other", true)
			_, result := f.purge(t, "default")
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"uninstall", func(t *testing.T, f *legacyFixture) []string {
			result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
			require.NoError(t, err)
			return result.Warnings
		}},
		{"mod disable", func(t *testing.T, f *legacyFixture) []string {
			result, err := f.svc.DisableMod(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)
			return result.Warnings
		}},
		{"rollback", func(t *testing.T, f *legacyFixture) []string {
			// The previous version ships only Data/k2.esp, so rolling back
			// removes Data/k.esp.
			require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "k", "old", "Data/k2.esp", []byte("mod k2")))
			require.NoError(t, f.svc.ExecForTest(ctx, `UPDATE installed_mods SET previous_version = 'old' WHERE mod_id = 'k'`))
			plan, err := f.svc.PlanRollback(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)
			result, err := f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
			require.NoError(t, err)
			require.Equal(t, core.UpdateRolledBack, result.Status, "%+v", result)
			return result.Warnings
		}},
		{"profile apply", func(t *testing.T, f *legacyFixture) []string {
			f.profile(t, "default", true)
			plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
			require.NoError(t, err)
			result, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
			require.NoError(t, err)
			return result.Warnings
		}},
		{"profile switch", func(t *testing.T, f *legacyFixture) []string {
			f.profile(t, "other", false)
			plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
			require.NoError(t, err)
			result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
			require.NoError(t, err)
			return result.Warnings
		}},
		{"verify --fix convergence", func(t *testing.T, f *legacyFixture) []string {
			// The mod no longer ships Data/k.esp, so its row is stale.
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

// keptTexts renders a purge's kept paths as one line each.
func keptTexts(kept []core.PurgeKeptPath) []string {
	var out []string
	for _, k := range kept {
		out = append(out, string(k.Reason)+" "+k.Path+": "+k.Note)
	}
	return out
}

func TestDeployedIdentity_EveryRemovalKeepsAFileTheUserReplaced(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		for _, edit := range userEdits(method) {
			for _, path := range removalPaths() {
				t.Run(method.String()+"/"+edit.name+"/"+path.name, func(t *testing.T) {
					f := ledgerState(t, method)
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

func TestDeployedIdentity_EveryRemovalStillRemovesLmmsOwnFile(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		for _, path := range removalPaths() {
			t.Run(method.String()+"/"+path.name, func(t *testing.T) {
				f := ledgerState(t, method)

				messages := path.run(t, f)

				assert.NoFileExists(t, f.kPath())
				assert.False(t, containsLine(messages, "Data/k.esp", "left in place"), "%q", messages)
				assert.False(t, containsLine(messages, "unverified"), "a fingerprinted file is checked: %q", messages)
			})
		}
	}
}

// TestDeployedIdentity_AnUnfingerprintedRowIsRemovedAndReportedUnverified:
// a row written before fingerprints were recorded keeps the old
// behaviour - the file goes - and the flow says it was not checked.
func TestDeployedIdentity_AnUnfingerprintedRowIsRemovedAndReportedUnverified(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkCopy))
	f.profile(t, "default", true, "k")
	f.deployed(t, "default", "k", domain.LinkCopy, map[string]string{"Data/k.esp": "mod k"}, map[string]string{"Data/k.esp": "edited before v18"})

	result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
	require.NoError(t, err)

	assert.NoFileExists(t, f.kPath())
	assert.Equal(t, []string{"1 copied or hard-linked file(s) were removed unverified (deployed before checksums were recorded): Data/k.esp"}, result.Warnings)
}

// TestDeployedIdentity_ADeployDoesNotOverwriteAFileTheUserReplaced: a
// redeploy's undeploy keeps the file, so its install must not write over
// it either - and the record stays, so the next deploy, and verify, still
// know the file is the user's.
func TestDeployedIdentity_ADeployDoesNotOverwriteAFileTheUserReplaced(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		t.Run(method.String(), func(t *testing.T) {
			f := ledgerState(t, method)
			require.NoError(t, os.Remove(f.kPath()))
			require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))

			for range 2 {
				result, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)
				assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
				assert.Equal(t, "mod k2", readLive(t, filepath.Join(f.game.ModPath, "Data", "k2.esp")))
				assert.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "delete it, then deploy again"), "%q", result.Warnings)
				assert.ElementsMatch(t, []string{"Data/k.esp", "Data/k2.esp"}, f.recorded(t, "default", "k"))
			}

			// Once the user deletes it, the mod's version goes back.
			require.NoError(t, os.Remove(f.kPath()))
			result, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, "mod k", readLive(t, f.kPath()))
			assert.False(t, containsLine(result.Warnings, "Data/k.esp"), "%q", result.Warnings)
		})
	}
}

// TestDeployedIdentity_VerifyReportsAChangedFileAndFixLeavesIt: verify
// reports the mismatch as its own finding, and --fix never writes over it.
func TestDeployedIdentity_VerifyReportsAChangedFileAndFixLeavesIt(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var found *core.VerifyFinding
	for i := range report.Result.Findings {
		if report.Result.Findings[i].Status == core.VerifyStatusDeployedModified {
			found = &report.Result.Findings[i]
		}
	}
	require.NotNil(t, found, "%+v", report.Result.Findings)
	assert.Equal(t, "Data/k.esp", found.FileID)
	assert.Equal(t, "k", found.ModID)
	assert.False(t, found.Fixable)
	assert.NotEmpty(t, found.FixableReason)
	assert.Positive(t, report.Result.Warnings)

	_, err = f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))

	// An untouched file is not a finding.
	g := ledgerState(t, domain.LinkCopy)
	report, err = g.svc.VerifyReport(ctx, g.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	for _, fd := range report.Result.Findings {
		assert.NotEqual(t, core.VerifyStatusDeployedModified, fd.Status)
	}
}

// TestDeployedIdentity_ACopiedTimestampDoesNotPassForLmmsFile: the
// size-and-mtime pre-check is not enough on its own - a same-size file
// written in lmm's file's place, with the old mtime copied back, is hashed.
func TestDeployedIdentity_ACopiedTimestampDoesNotPassForLmmsFile(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	info, err := os.Stat(f.kPath())
	require.NoError(t, err)
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER!"), 0o644))
	require.NoError(t, os.Chtimes(f.kPath(), info.ModTime(), info.ModTime()))

	result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, "USER!", readLive(t, f.kPath()))
	assert.True(t, containsLine(result.Warnings, "Data/k.esp was left in place"), "%q", result.Warnings)
	assert.NoFileExists(t, filepath.Join(f.game.ModPath, "Data", "k2.esp"), "an untouched file still goes")
}

// TestDeployedIdentity_ATouchedFileWithLmmsContentIsStillLmms: a changed
// ctime only sends the file to the hash; the same content is lmm's.
func TestDeployedIdentity_ATouchedFileWithLmmsContentIsStillLmms(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(f.kPath(), later, later))
	require.NoError(t, os.Chmod(f.kPath(), 0o600))

	result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
	require.NoError(t, err)
	assert.NoFileExists(t, f.kPath())
	assert.Empty(t, result.Warnings)
}

// TestDeployedIdentity_AnUnreadableFileIsKept: a file that cannot be read
// to compare is not taken for lmm's.
func TestDeployedIdentity_AnUnreadableFileIsKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(f.kPath(), 0o644) })

	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.FileExists(t, f.kPath())
	assert.True(t, containsLine(keptTexts(result.Kept), "Data/k.esp", "could not be read"), "%+v", result.Kept)
}

// TestDeployedIdentity_ADeployKeepsAnUntrackedFileItCannotPreserve: a
// deploy preserves foreign content before replacing it, but only the
// first original of a path is kept - so a second one, which lmm cannot
// preserve, is left alone rather than overwritten.
func TestDeployedIdentity_ADeployKeepsAnUntrackedFileItCannotPreserve(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkCopy))
	f.profile(t, "default", true, "k")
	// Stock content at the path, which the first deploy preserves.
	require.NoError(t, os.MkdirAll(filepath.Dir(f.kPath()), 0o755))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("STOCK"), 0o644))
	f.deployed(t, "default", "k", domain.LinkCopy, map[string]string{"Data/k2.esp": "mod k2"}, nil)
	require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "k", "unknown", "Data/k.esp", []byte("mod k")))
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, "mod k", readLive(t, f.kPath()))

	// The user replaces lmm's file; an uninstall keeps it, untracked, and
	// the stock original is still held.
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))
	_, err = f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
	require.NoError(t, err)
	require.Equal(t, "USER FILE", readLive(t, f.kPath()))

	// A later deploy of a mod shipping that path cannot preserve the
	// user's file, so it does not replace it.
	f.profile(t, "default", true, "k")
	f.deployed(t, "default", "k", domain.LinkCopy, map[string]string{"Data/k2.esp": "mod k2"}, nil)
	require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "k", "unknown", "Data/k.esp", []byte("mod k")))
	result, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
	assert.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "earlier original"), "%q %+v", result.Warnings, result)
}
