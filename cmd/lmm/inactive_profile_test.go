package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoDeployAndDoPurge_RefuseANonActiveProfile is #445 at the CLI: `lmm
// deploy -p` and `lmm purge -p` naming a profile that is not active are
// refused before anything is printed or changed, in every mode - including
// a dry run and a confirmed purge.
func TestDoDeployAndDoPurge_RefuseANonActiveProfile(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t) // "default" is active
	_, err := getProfileManager(svc).Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	seedSyncInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", "alt", true, nil)

	for _, dryRun := range []bool{false, true} {
		setFlag(t, &deployProfile, "alt")
		setFlag(t, &deployDryRun, dryRun)
		stdout, stderr, err := captureStdoutAndStderr(t, func() error {
			return doDeploy(ctx, svc, game, nil)
		})
		require.ErrorIs(t, err, core.ErrProfileNotActive, "deploy, dry run %v", dryRun)
		assert.Contains(t, err.Error(), "lmm profile switch alt")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)

		setFlag(t, &purgeProfile, "alt")
		setFlag(t, &purgeDryRun, dryRun)
		setFlag(t, &purgeYes, true)
		setFlag(t, &purgeUninstall, true)
		stdout, stderr, err = captureStdoutAndStderr(t, func() error {
			return doPurge(ctx, svc, game)
		})
		require.ErrorIs(t, err, core.ErrProfileNotActive, "purge, dry run %v", dryRun)
		assert.Contains(t, err.Error(), "lmm profile switch alt")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
	}

	rows, err := svc.GetInstalledMods(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.Len(t, rows, 1, "purge --uninstall removed nothing")
	assert.True(t, rows[0].Enabled)
}

// setFlag sets a command's package-level flag variable for one test.
func setFlag[T any](t *testing.T, flag *T, value T) {
	t.Helper()
	old := *flag
	*flag = value
	t.Cleanup(func() { *flag = old })
}
