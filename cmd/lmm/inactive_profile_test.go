package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoDeployAndDoPurge_RefuseANonActiveProfile is #445 at the CLI: `lmm
// deploy -p` naming a profile that is not active, and `lmm purge -p ...
// --uninstall` for one, are refused before anything is printed or changed,
// in every mode - including a dry run and a confirmed purge.
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

// mixedGameDir leaves the game directory holding the active profile's
// shared.esp and, deployed while alt was briefly active, alt's own
// altonly.esp and its record of shared.esp.
func mixedGameDir(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t) // "default" is active
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	for _, m := range []struct{ profile, id string }{{"default", "shared"}, {"alt", "shared"}, {"alt", "altonly"}} {
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", m.id, "1.0", m.id+".esp", []byte(m.id)))
		seedSyncInstalledMod(t, svc, game, "src", m.id, m.id, "1.0", m.profile, true, nil)
		require.NoError(t, pm.AddMod(ctx, game.ID, m.profile, domain.ModReference{SourceID: "src", ModID: m.id, Version: "1.0"}))
	}
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "alt"))
	_, err = svc.DeployProfile(ctx, game, "alt", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	require.FileExists(t, filepath.Join(game.ModPath, "altonly.esp"))
	return svc, game
}

// TestDoPurge_ANonActiveProfileClearsOnlyWhatItRecorded is the other half
// of #445 at the CLI: `lmm purge -p alt` is how a user clears files alt put
// into the live directory, and it says - in the dry run and the real run -
// that it removes only those, and which of them it leaves.
func TestDoPurge_ANonActiveProfileClearsOnlyWhatItRecorded(t *testing.T) {
	ctx := context.Background()
	svc, game := mixedGameDir(t)
	setFlag(t, &purgeProfile, "alt")
	setFlag(t, &purgeYes, true)
	header := "alt is not the active profile of Game (default is), so this purge only removes the files alt recorded as deployed that no other profile records:\n" +
		"  - altonly.esp\n" +
		"Left in place (also recorded by default): shared.esp\n" +
		"Mod records and the profile are kept, and no hooks run.\n"

	setFlag(t, &purgeDryRun, true)
	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, "Purge plan for profile \"alt\" (dry run)\n\n"+header+"\nWould remove: 1 file(s)\n", stdout)
	require.FileExists(t, filepath.Join(game.ModPath, "altonly.esp"))

	purgeDryRun = false
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, header+
		"\nPurging mods from Game...\n\n"+
		"  ✓ altonly\n"+
		"\nRemoved: 1 file(s); cleared: 1 mod(s)\n"+
		"Left in place (also recorded by default): shared.esp\n"+
		"\nRun 'lmm profile switch alt' to deploy alt again.\n", stdout)
	assert.NoFileExists(t, filepath.Join(game.ModPath, "altonly.esp"))
	assert.FileExists(t, filepath.Join(game.ModPath, "shared.esp"), "the active profile's file survives")

	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, strings.Replace(header, "  - altonly.esp\n", "  (none)\n", 1)+"\nNothing to remove.\n", stdout)

	setFlag(t, &jsonOutput, true)
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	var result core.PurgeResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	assert.Equal(t, []core.PurgeKeptPath{{Path: "shared.esp", Profiles: []string{"default"}}}, result.Kept)
}

// setFlag sets a command's package-level flag variable for one test.
func setFlag[T any](t *testing.T, flag *T, value T) {
	t.Helper()
	old := *flag
	*flag = value
	t.Cleanup(func() { *flag = old })
}
