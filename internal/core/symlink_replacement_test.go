package core_test

// #483: a deployed symlink's row has no content fingerprint. When the user
// replaced the link with a regular file, the deploy-side identity judge used
// to call that file unverified and SymlinkLinker.Deploy removed it. Every
// deploy-direction flow shares Installer, so these regressions exercise the
// public flow APIs and pin the warning as well as the surviving filesystem
// object.

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

type symlinkReplacement struct {
	name   string
	plant  func(t *testing.T, path string)
	assert func(t *testing.T, path string)
}

func symlinkReplacements() []symlinkReplacement {
	return []symlinkReplacement{
		{
			name: "regular file",
			plant: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, []byte("USER FILE"), 0o644))
			},
			assert: func(t *testing.T, path string) {
				assert.Equal(t, "USER FILE", readLive(t, path))
			},
		},
		{
			name: "directory",
			plant: func(t *testing.T, path string) {
				require.NoError(t, os.Mkdir(path, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(path, "mine.txt"), []byte("USER DIRECTORY"), 0o644))
			},
			assert: func(t *testing.T, path string) {
				assert.Equal(t, "USER DIRECTORY", readLive(t, filepath.Join(path, "mine.txt")))
			},
		},
	}
}

func replaceDeployedSymlink(t *testing.T, path string, replacement symlinkReplacement) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	replacement.plant(t, path)
}

func assertReplacementWarning(t *testing.T, warnings []string) {
	t.Helper()
	assert.True(t, containsLine(warnings, "Data/k.esp", "you replaced lmm's link", "left"), "%q", warnings)
}

func TestSymlinkReplacement_DeployAndDeployPurgeLeaveUserContent(t *testing.T) {
	ctx := context.Background()
	for _, replacement := range symlinkReplacements() {
		for _, purge := range []bool{false, true} {
			name := "deploy"
			if purge {
				name = "deploy --purge"
			}
			t.Run(replacement.name+"/"+name, func(t *testing.T) {
				f := ledgerState(t, domain.LinkSymlink)
				replaceDeployedSymlink(t, f.kPath(), replacement)

				result, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{Purge: purge}, nil)
				require.NoError(t, err)

				assert.Equal(t, 1, result.Deployed, "the deploy ran; only the contested path was skipped")
				replacement.assert(t, f.kPath())
				assertReplacementWarning(t, result.Warnings)
			})
		}
	}
}

func TestSymlinkReplacement_UpdateLeavesUsersFile(t *testing.T) {
	ctx := context.Background()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"Data/k.esp": []byte("old")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "new.zip", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod(game.ID, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: game.ID})
	archive := createTestZip(t, t.TempDir(), map[string]string{"Data/k.esp": "new"})
	archiveBytes, err := os.ReadFile(archive)
	require.NoError(t, err)
	mock.AddDownload("new-1", archiveBytes)

	path := filepath.Join(game.ModPath, "Data", "k.esp")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("USER FILE"), 0o644))

	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", domain.Update{InstalledMod: *old, NewVersion: "2.0"})
	require.NoError(t, err)
	result, err := svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, core.UpdateUpdated, result.Status)
	assert.Equal(t, "USER FILE", readLive(t, path))
	assert.True(t, containsLine(result.Warnings, "Data/k.esp", "you replaced lmm's link", "left"), "%q", result.Warnings)
}

func TestSymlinkReplacement_RollbackLeavesUsersFile(t *testing.T) {
	ctx := context.Background()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"Data/k.esp": []byte("old")},
		map[string][]byte{"Data/k.esp": []byte("new")})

	path := filepath.Join(game.ModPath, "Data", "k.esp")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("USER FILE"), 0o644))

	plan, err := svc.PlanRollback(ctx, game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	result, err := svc.ApplyRollback(ctx, game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, core.UpdateRolledBack, result.Status)
	assert.Equal(t, "USER FILE", readLive(t, path))
	assert.True(t, containsLine(result.Warnings, "Data/k.esp", "you replaced lmm's link", "left"), "%q", result.Warnings)
}

func TestSymlinkReplacement_ProfileApplyLeavesUsersFile(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkSymlink)
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))
	require.NoError(t, f.svc.SetModEnabledForTest(ctx, "local", "k", f.game.ID, "default", false))
	require.NoError(t, f.svc.SetModDeployed(ctx, "local", "k", f.game.ID, "default", false))

	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	result, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Enabled)
	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
	assertReplacementWarning(t, result.Warnings)
}

func TestSymlinkReplacement_ProfileSwitchLeavesUsersFile(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkSymlink)
	f.profile(t, "other", false, "k")
	f.cachedOnly(t, "other", "k", domain.LinkSymlink, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"})
	require.NoError(t, f.svc.SetModEnabledForTest(ctx, "local", "k", f.game.ID, "other", false))
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
	require.NoError(t, err)
	result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Enabled)
	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
	assertReplacementWarning(t, result.Warnings)
}

func TestSymlinkReplacement_VerifyFixReportsAndLeavesUserContent(t *testing.T) {
	ctx := context.Background()
	for _, replacement := range symlinkReplacements() {
		t.Run(replacement.name, func(t *testing.T) {
			f := ledgerState(t, domain.LinkSymlink)
			replaceDeployedSymlink(t, f.kPath(), replacement)

			report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
			require.NoError(t, err)

			replacement.assert(t, f.kPath())
			notes := findingNotes(report.Result)
			assert.Positive(t, report.Result.Warnings)
			assert.True(t, containsFindingStatus(report.Result.Findings, core.VerifyStatusDeployedModified), "%+v", report.Result.Findings)
			assert.Contains(t, notes, "Data/k.esp", "%+v", report.Result.Findings)
			assert.Contains(t, notes, "you replaced lmm's link", "%+v", report.Result.Findings)
		})
	}
}

func containsFindingStatus(findings []core.VerifyFinding, status string) bool {
	for _, finding := range findings {
		if finding.Status == status {
			return true
		}
	}
	return false
}
