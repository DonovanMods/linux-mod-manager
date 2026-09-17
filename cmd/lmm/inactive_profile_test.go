package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

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

// TestDoProfileApply_RefusesANonActiveProfile is #462 for `lmm profile
// apply` (#445 final gate F-B): an apply deploys, so a profile that is not
// active is refused before anything is printed or changed, dry run or not,
// and the active profile - what the mod_path refusal names - still applies.
func TestDoProfileApply_RefusesANonActiveProfile(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t) // "default" is active
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "m1", "1.0", "m1.esp", []byte("m1")))
	seedSyncInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", "alt", false, nil)
	require.NoError(t, pm.AddMod(ctx, game.ID, "alt", domain.ModReference{SourceID: "src", ModID: "m1", Version: "1.0"}))
	setFlag(t, &profileApplyYes, true)

	for _, dryRun := range []bool{false, true} {
		for _, asJSON := range []bool{false, true} {
			setFlag(t, &profileApplyDryRun, dryRun)
			setFlag(t, &jsonOutput, asJSON)
			stdout, stderr, err := captureStdoutAndStderr(t, func() error {
				return doProfileApply(ctx, svc, game, []string{"alt"})
			})
			require.ErrorIs(t, err, core.ErrProfileNotActive, "dry run %v, json %v", dryRun, asJSON)
			assert.Contains(t, err.Error(), "lmm profile switch alt")
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
		}
	}
	assert.NoFileExists(t, filepath.Join(game.ModPath, "m1.esp"))
	rows, err := svc.GetInstalledMods(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].Enabled, "alt's row is untouched")

	setFlag(t, &profileApplyDryRun, false)
	setFlag(t, &jsonOutput, false)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "alt"))
	_, _, err = captureStdoutAndStderr(t, func() error {
		return doProfileApply(ctx, svc, game, []string{"alt"})
	})
	require.NoError(t, err, "the active profile applies")
	assert.FileExists(t, filepath.Join(game.ModPath, "m1.esp"))
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
	header := "alt is not the active profile of Game (default is), so this purge only removes the files alt recorded as deployed that nothing else still claims:\n" +
		"  - altonly.esp\n" +
		"Left in place (still recorded by default): shared.esp\n" +
		"Mod records and the profile are kept, and no hooks run.\n"

	setFlag(t, &purgeDryRun, true)
	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, "Purge plan for profile \"alt\" (dry run)\n\n"+header+"\nWould remove: 1 file(s), and drop alt's record of 1 file(s) it leaves\n", stdout)
	require.FileExists(t, filepath.Join(game.ModPath, "altonly.esp"))

	purgeDryRun = false
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, header+
		"\nPurging mods from Game...\n\n"+
		"  ✓ shared\n"+
		"  ✓ altonly\n"+
		"\nRemoved: 1 file(s); cleared: 2 mod(s)\n"+
		"Left in place (still recorded by default): shared.esp\n"+
		"\nRun 'lmm profile switch alt' to deploy alt again.\n", stdout)
	assert.NoFileExists(t, filepath.Join(game.ModPath, "altonly.esp"))
	assert.FileExists(t, filepath.Join(game.ModPath, "shared.esp"), "the active profile's file survives")

	// alt recorded nothing else, so a second purge has nothing to do.
	nothing := "alt is not the active profile of Game (default is), so this purge only removes the files alt recorded as deployed that nothing else still claims:\n" +
		"  (none)\n" +
		"Mod records and the profile are kept, and no hooks run.\n" +
		"\nNothing to remove.\n"
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, nothing, stdout)

	setFlag(t, &jsonOutput, true)
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	var result core.PurgeResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	assert.Empty(t, result.Kept)
}

// TestDoPurge_JSONListsTheKeptPaths: the --json document of a
// recorded-only purge names each path it keeps, with why.
func TestDoPurge_JSONListsTheKeptPaths(t *testing.T) {
	ctx := context.Background()
	svc, game := mixedGameDir(t)
	setFlag(t, &purgeProfile, "alt")
	setFlag(t, &purgeYes, true)
	setFlag(t, &jsonOutput, true)

	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, game) })

	var result core.PurgeResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	assert.Equal(t, []core.PurgeKeptPath{{Path: "shared.esp", Reason: core.PurgeKeptRecorded, Profiles: []string{"default"}}}, result.Kept)
	assert.Equal(t, 1, result.RemovedPaths)
}

// setFlag sets a command's package-level flag variable for one test.
func setFlag[T any](t *testing.T, flag *T, value T) {
	t.Helper()
	old := *flag
	*flag = value
	t.Cleanup(func() { *flag = old })
}

// TestPrintKeptPaths_SaysWhyEachPathIsLeft pins the line a recorded-only
// purge prints for each reason core.PurgeKeptReason names (#445 review F1,
// F3, F7).
func TestPrintKeptPaths_SaysWhyEachPathIsLeft(t *testing.T) {
	stdout := captureStdout(t, func() error {
		printKeptPaths([]core.PurgeKeptPath{
			{Path: "Data/shared.esp", Reason: core.PurgeKeptRecorded, Profiles: []string{"default", "survival"}},
			{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}},
			{Path: "Data/b.esp", Reason: core.PurgeKeptOtherGame, Games: []string{"sky"}},
			{Path: "BepInEx/config/m.cfg", Reason: core.PurgeKeptUserFile},
		})
		return nil
	})
	assert.Equal(t, "Left in place (still recorded by default, survival): Data/shared.esp\n"+
		"Left in place (its mod is in the active profile alt): Data/a.esp\n"+
		"Left in place (still recorded by game sky): Data/b.esp\n"+
		"Kept your file; lmm no longer tracks it (the game hands it to you after its first deploy): BepInEx/config/m.cfg\n", stdout)
}

// TestDoPurge_AConfigOnlyProfileStopsTrackingIt: a non-active profile
// whose only deployed-file record is a legacy BepInEx config (v2 never
// records one) has nothing to remove, and the purge still runs - it drops
// that record and keeps the file, as an ordinary purge does. Stopping at
// "Nothing to remove." would leave the record, and with it the game's
// mod_path locked (#427).
func TestDoPurge_AConfigOnlyProfileStopsTrackingIt(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t) // "default" is active
	app.RegisterAdapters(svc)
	game.InstallPath = game.ModPath // the bepinex adapter is derived
	require.NoError(t, os.MkdirAll(filepath.Join(game.ModPath, "BepInEx", "core"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "BepInEx", "core", "BepInEx.Preloader.dll"), []byte("x"), 0o644))
	cfg := filepath.Join(game.ModPath, "BepInEx", "config", "m.cfg")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
	require.NoError(t, os.WriteFile(cfg, []byte("setting=USER-TUNED"), 0o644))
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	seedSyncInstalledMod(t, svc, game, "src", "m", "Mod M", "1.0", "alt", true, nil)
	require.NoError(t, svc.SetModDeployed(ctx, "src", "m", game.ID, "alt", true))
	legacy, err := db.New(filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	require.NoError(t, legacy.SaveDeployedFile(ctx, game.ID, "alt", "BepInEx/config/m.cfg", "src", "m"))
	require.NoError(t, legacy.Close())
	setFlag(t, &purgeProfile, "alt")
	setFlag(t, &purgeYes, true)
	header := "alt is not the active profile of Game (default is), so this purge only removes the files alt recorded as deployed that nothing else still claims:\n" +
		"  (none)\n" +
		"Kept your file; lmm no longer tracks it (the game hands it to you after its first deploy): BepInEx/config/m.cfg\n" +
		"Mod records and the profile are kept, and no hooks run.\n"

	setFlag(t, &purgeDryRun, true)
	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, "Purge plan for profile \"alt\" (dry run)\n\n"+header+"\nWould remove: 0 file(s), and drop alt's record of 1 file(s) it leaves\n", stdout)
	recorded, err := svc.GetDeployedFilesForMod(ctx, game.ID, "alt", "src", "m")
	require.NoError(t, err)
	require.Len(t, recorded, 1, "a dry run changes nothing")

	purgeDryRun = false
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, header+
		"\nPurging mods from Game...\n\n"+
		"  ✓ Mod M\n"+
		"\nRemoved: 0 file(s); cleared: 1 mod(s)\n"+
		"Kept your file; lmm no longer tracks it (the game hands it to you after its first deploy): BepInEx/config/m.cfg\n"+
		"\nRun 'lmm profile switch alt' to deploy alt again.\n", stdout)
	recorded, err = svc.GetDeployedFilesForMod(ctx, game.ID, "alt", "src", "m")
	require.NoError(t, err)
	assert.Empty(t, recorded)
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	assert.Equal(t, "setting=USER-TUNED", string(data))

	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Equal(t, strings.Replace(header, "Kept your file; lmm no longer tracks it (the game hands it to you after its first deploy): BepInEx/config/m.cfg\n", "", 1)+"\nNothing to remove.\n", stdout)
}
