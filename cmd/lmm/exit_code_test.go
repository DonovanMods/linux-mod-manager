package main

// #382: `lmm --help`'s EXIT CODES block promises "0 success / 1 error /
// 2 cancelled by the user (e.g. declined a confirmation prompt)". It was not
// true: the same "no" exited 2 from `purge` and `snapshot restore`, 1 from
// `import`, and 0 from `profile switch` - so `lmm profile switch p && echo
// switched` printed "switched" over a switch that never happened. The
// contract is documented, so it gets a test.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExitCodeFor pins the mapping Execute applies, on its own.
func TestExitCodeFor(t *testing.T) {
	assert.Equal(t, exitOK, exitCodeFor(nil))
	assert.Equal(t, exitCancelled, exitCodeFor(ErrCancelled))
	assert.Equal(t, exitCancelled, exitCodeFor(context.Canceled))
	assert.Equal(t, exitError, exitCodeFor(ErrReported), "a reported failure is still a failure")
	assert.Equal(t, exitError, exitCodeFor(os.ErrNotExist))
}

// TestDeclinedConfirmationsAllExitTwo drives every confirming command with a
// declining stdin and asserts the exit code the user's shell would see.
// exitCodeFor is Execute's own mapping, so this measures the real contract
// without building a binary per case.
func TestDeclinedConfirmationsAllExitTwo(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"purge", func(t *testing.T) error {
			svc, game := setupDoPurgeTest(t)
			purgeYes = false
			seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")
			return doPurge(context.Background(), svc, game)
		}},
		{"snapshot restore", func(t *testing.T) error {
			svc, game := setupSnapshotTest(t)
			ctx := context.Background()
			_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
			require.NoError(t, err)
			seedSnapshotMod(t, svc, game, "wrecker", "Wrecker", "Data/shipped.esp")
			_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			snapshotYes = false
			return doSnapshotRestore(ctx, svc, game, "known-good")
		}},
		{"profile switch", func(t *testing.T) error {
			svc, game := setupDoProfileSwitchTest(t)
			_, err := getProfileManager(svc).Create(context.Background(), game.ID, "target")
			require.NoError(t, err)
			seedDeployableMod(t, svc, game, "disable-me", "Disable Me", "disable.esp")
			return doProfileSwitch(context.Background(), svc, game, "target")
		}},
		{"profile apply", func(t *testing.T) error {
			svc, game := setupDoProfileSwitchTest(t)
			require.NoError(t, getProfileManager(svc).AddMod(context.Background(), game.ID, "default",
				domain.ModReference{SourceID: "src", ModID: "ins1", Version: "1.0"}))
			return doProfileApply(context.Background(), svc, game, nil)
		}},
		{"profile sync", func(t *testing.T) error {
			svc, game := setupDoProfileSwitchTest(t)
			seedSyncInstalledMod(t, svc, game, "src", "dec1", "Decline Me", "1.0", "default", true, nil)
			return doProfileSync(context.Background(), svc, game, nil)
		}},
		{"import archive conflict", func(t *testing.T) error {
			svc, game, archivePath := setupImportConflictTest(t)
			importForce = false
			return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
		}},
		{"install with dependencies declined", func(t *testing.T) error {
			svc, game, src := setupDoInstallTest(t)
			installYes = false
			src.AddMod(&domain.Mod{ID: "dep1", SourceID: "test-src", Name: "Dep One", Version: "1.0", GameID: "g1"},
				[]domain.DownloadableFile{{ID: "dep-file", FileName: "dep1.esp", IsPrimary: true}})
			src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1",
				Dependencies: []domain.ModReference{{SourceID: "test-src", ModID: "dep1"}}},
				[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
			return doInstall(context.Background(), svc, game, nil)
		}},
		{"install with a conflict declined", func(t *testing.T) error {
			svc, game, src := setupDoInstallTest(t)
			installYes = false
			seedConflictingMod(t, svc, game)
			src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
				[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
			src.AddDownload("main", []byte("mod1 content"))
			return doInstall(context.Background(), svc, game, nil)
		}},
		{"import scan", func(t *testing.T) error {
			svc, game := setupDoImportTest(t)
			game.DeployMode = domain.DeployCopy
			require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "LooseMod-1.0.zip"), []byte("loose-payload"), 0o644))
			importSkipMatch = true
			importForce = false
			importDryRun = false
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			return runImportScan(cmd, game, svc, "default")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			withStdin(t, "n\n", func() {
				_ = captureStdout(t, func() error {
					err = tc.run(t)
					return nil
				})
			})
			assert.Equal(t, exitCancelled, exitCodeFor(err),
				"declining %s must exit 2, not %d (err: %v)", tc.name, exitCodeFor(err), err)
		})
	}
}
