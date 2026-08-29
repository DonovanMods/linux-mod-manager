package main

// Tests for v2 Phase 3 Ruling 15: every MUTATING command honours --json.
// A --dry-run --json run emits the flow's Plan, an applying run emits its
// Result, and a declined/undecidable prompt emits the error envelope
// (Ruling 2) - always exactly one document on stdout, with nothing on
// stderr, because the console event closure is not installed at all under
// --json (quietSink).
//
// The read-only commands' --json documents live in json_golden_test.go;
// the goldens both files compare against share
// cmd/lmm/testdata/json_golden/ and the -update-json-cli flag.

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runJSONCommand runs fn with --json active and returns its stdout, after
// proving the two framing invariants Ruling 15 gives every mutating
// command: exactly one JSON document on stdout, and NOTHING on stderr (no
// progress line, no "Warning:" line - the event closure is never
// installed). The decoded document is discarded; callers compare the raw
// text against a golden.
func runJSONCommand(t *testing.T, fn func() error) string {
	t.Helper()
	withJSONOutput(t)

	stdout, stderr, err := captureStdoutStderrErr(t, fn)
	require.NoError(t, err)
	assert.Empty(t, stderr, "--json must leave stderr empty: events are suppressed (Ruling 15)")

	var doc any
	decodeSingleDoc(t, stdout, &doc)
	return stdout
}

// --- deploy ---

func TestJSONGolden_Deploy(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")

		out := runJSONCommand(t, func() error {
			return doDeploy(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "deploy_result", out)
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		oldDryRun := deployDryRun
		deployDryRun = true
		t.Cleanup(func() { deployDryRun = oldDryRun })

		out := runJSONCommand(t, func() error {
			return doDeploy(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "deploy_dry_run", out)
	})
}

// --- uninstall ---

func TestJSONGolden_Uninstall(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoUninstallTest(t)

		out := runJSONCommand(t, func() error {
			return doUninstall(context.Background(), svc, game, "1")
		})
		assertJSONCLIGolden(t, "uninstall_result", out, game.ModPath, "<GAME-DIR>")
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoUninstallTest(t)
		uninstallDryRun = true

		out := runJSONCommand(t, func() error {
			return doUninstall(context.Background(), svc, game, "1")
		})
		assertJSONCLIGolden(t, "uninstall_dry_run", out, game.ModPath, "<GAME-DIR>")
	})
}

// --- purge ---

func TestJSONGolden_Purge(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoPurgeTest(t)
		seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")

		out := runJSONCommand(t, func() error {
			return doPurge(context.Background(), svc, game)
		})
		assertJSONCLIGolden(t, "purge_result", out)
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoPurgeTest(t)
		seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")
		purgeDryRun = true

		out := runJSONCommand(t, func() error {
			return doPurge(context.Background(), svc, game)
		})
		assertJSONCLIGolden(t, "purge_dry_run", out, game.ModPath, "<GAME-DIR>")
	})

	// Nothing installed is not an error and still owes the caller a
	// document: the Result a purge of nothing produces.
	t.Run("nothing_installed", func(t *testing.T) {
		svc, game := setupDoPurgeTest(t)

		out := runJSONCommand(t, func() error {
			return doPurge(context.Background(), svc, game)
		})
		assertJSONCLIGolden(t, "purge_nothing_installed", out)
	})
}

// TestDoDeploy_JSON_PlainTextPathUnaffected is the other half of every
// --json test here: with jsonOutput false the command still prints its
// historical lines, so the --json branch cannot have been implemented by
// changing the plain path.
func TestDoDeploy_JSON_PlainTextPathUnaffected(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Deploying 1 mod(s) using symlink...\n\n  ✓ Mod A\n\nDeployed: 1\n", out)
	assert.False(t, jsonOutput, "the plain path must not have flipped the global")
}

// TestDoPurge_JSON_ConfirmationRequiredEmitsNoDocument pins Ruling 2's half
// of Ruling 15: the undecidable prompt fails BEFORE mutating and prints no
// document of its own - the envelope is reportError's job, off the error
// return, so the command's own stdout stays empty.
func TestDoPurge_JSON_ConfirmationRequiredEmitsNoDocument(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	purgeYes = false
	withJSONOutput(t)
	seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	assert.Empty(t, stdout, "a refused prompt emits no result document")
	assert.Empty(t, stderr)
}

// --- mod enable / disable ---

// withModSourceFlags points `lmm mod`'s -s/-p globals at the fixture's
// source and default profile, restoring them afterwards -
// setupDoModLockTest does this itself, but the enable/disable fixtures
// build on setupDoDeployTest, which does not.
func withModSourceFlags(t *testing.T) {
	t.Helper()
	oldSource, oldProfile := modSource, modProfile
	modSource, modProfile = "src", ""
	t.Cleanup(func() { modSource, modProfile = oldSource, oldProfile })
}

func TestJSONGolden_ModToggle(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: "a", SourceID: "src", Name: "Mod A", Version: "1.0", GameID: game.ID},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      false,
		}))
		game.SourceIDs = map[string]string{"src": game.ID}
		withModSourceFlags(t)

		out := runJSONCommand(t, func() error {
			return doModEnable(context.Background(), svc, game, "a")
		})
		assertJSONCLIGolden(t, "mod_enable_result", out)
	})

	t.Run("disable", func(t *testing.T) {
		svc, game := setupDoDeployTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		game.SourceIDs = map[string]string{"src": game.ID}
		withModSourceFlags(t)

		out := runJSONCommand(t, func() error {
			return doModDisable(context.Background(), svc, game, "a")
		})
		assertJSONCLIGolden(t, "mod_disable_result", out)
	})
}

// --- mod lock / unlock / set-update / convert ---

func TestJSONGolden_ModSettings(t *testing.T) {
	t.Run("lock", func(t *testing.T) {
		svc, game, _ := setupDoModLockTest(t)
		seedLockableMod(t, svc, game, "a", "Mod A", "1.5")

		out := runJSONCommand(t, func() error {
			return doModLock(context.Background(), svc, game, "a", "")
		})
		assertJSONCLIGolden(t, "mod_lock_result", out)
	})

	t.Run("unlock", func(t *testing.T) {
		svc, game, _ := setupDoModLockTest(t)
		seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
		require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", "src", "a", ""))

		out := runJSONCommand(t, func() error {
			return doModUnlock(context.Background(), svc, game, "a")
		})
		assertJSONCLIGolden(t, "mod_unlock_result", out)
	})

	t.Run("set_update", func(t *testing.T) {
		svc, game, _ := setupDoModLockTest(t)
		seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
		oldAuto, oldPin := modSetAuto, modSetPin
		modSetAuto, modSetPin = true, false
		t.Cleanup(func() { modSetAuto, modSetPin = oldAuto, oldPin })

		out := runJSONCommand(t, func() error {
			return doModSetUpdate(context.Background(), svc, game, "a")
		})
		assertJSONCLIGolden(t, "mod_set_update_result", out)
	})

	t.Run("convert", func(t *testing.T) {
		svc, game, _ := setupDoModConvertTest(t)
		// Saved so modSettingResult's own GetGame finds the compile-mode
		// game and populates ModSettingResult.ConvertPaks - the datum this
		// command's plain output states as "pak conversion: on|off".
		require.NoError(t, svc.SaveGame(context.Background(), game))
		seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")

		out := runJSONCommand(t, func() error {
			return doModConvert(context.Background(), svc, game, "a", false)
		})
		assertJSONCLIGolden(t, "mod_convert_result", out)
	})
}

// --- mod edit ---

func TestJSONGolden_ModEdit(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	editName = "Renamed Mod"

	out := runJSONCommand(t, func() error {
		return doModEdit(context.Background(), svc, game, "a")
	})
	assertJSONCLIGolden(t, "mod_edit_result", out)
}

// --- game set-default / clear-default / detect ---

func TestJSONGolden_GameSettings(t *testing.T) {
	t.Run("set_default", func(t *testing.T) {
		svc := setupGameAddTest(t)
		require.NoError(t, svc.SaveGame(context.Background(), goldenStatusGame("skyrim-se", "Skyrim SE")))
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := runJSONCommand(t, func() error {
			return doGameSetDefault(cmd, svc, "skyrim-se")
		})
		assertJSONCLIGolden(t, "game_set_default_result", out)
	})

	t.Run("clear_default", func(t *testing.T) {
		svc := setupGameAddTest(t)
		require.NoError(t, svc.SaveGame(context.Background(), goldenStatusGame("skyrim-se", "Skyrim SE")))
		require.NoError(t, svc.SetDefaultGame(context.Background(), "skyrim-se"))
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := runJSONCommand(t, func() error { return runGameClearDefault(cmd, nil) })
		assertJSONCLIGolden(t, "game_clear_default_result", out)
	})
}

func TestJSONGolden_GameDetect(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		configDir = t.TempDir()
		svc := newGameDetectTestService(t)
		oldAll := gameDetectAll
		gameDetectAll = true
		t.Cleanup(func() { gameDetectAll = oldAll })
		cmd := &cobra.Command{}
		cmd.SetOut(&strings.Builder{})

		out := runJSONCommand(t, func() error {
			return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc,
				[]steam.DetectedGame{{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"}},
				[]string{"steam library at /nowhere is unreadable"})
		})
		assertJSONCLIGolden(t, "game_detect_all", out)
	})

	// Ruling 2 + Ruling 15: with neither --all nor --select the selection
	// prompt is unanswerable under --json, so the command must fail before
	// saving anything AND print no listing of its own - the listing is
	// console text that would sit beside the error envelope.
	t.Run("no_flag_is_confirmation_required", func(t *testing.T) {
		configDir = t.TempDir()
		svc := newGameDetectTestService(t)
		withJSONOutput(t)
		// Both deciding flags explicitly off: this scenario is precisely
		// "neither --all nor --select", and the package's other detect
		// tests set them.
		oldAll, oldSelect := gameDetectAll, gameDetectSelect
		gameDetectAll, gameDetectSelect = false, ""
		t.Cleanup(func() { gameDetectAll, gameDetectSelect = oldAll, oldSelect })
		var buf strings.Builder
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)

		stdout, stderr, err := captureStdoutStderrErr(t, func() error {
			return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc,
				[]steam.DetectedGame{{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture"}}, nil)
		})

		require.ErrorIs(t, err, core.ErrConfirmationRequired)
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.Empty(t, buf.String(), "no listing may be printed under --json")

		games, loadErr := config.LoadGames(configDir)
		require.NoError(t, loadErr)
		assert.Empty(t, games, "nothing may be saved when the prompt is refused")
	})
}

// --- profile create / delete / reorder ---

// withProfileDryRun turns one of the three new profile --dry-run flags on
// for the duration of a test.
func withProfileDryRun(t *testing.T, flag *bool) {
	t.Helper()
	old := *flag
	*flag = true
	t.Cleanup(func() { *flag = old })
}

// withProfileSwitchYes / withProfileSyncYes answer switch's and sync's
// "Proceed?" prompts from the flag, which is the only way to reach their
// apply step under --json (Ruling 2).
func withProfileSwitchYes(t *testing.T) {
	t.Helper()
	old := profileSwitchYes
	profileSwitchYes = true
	t.Cleanup(func() { profileSwitchYes = old })
}

func withProfileSyncYes(t *testing.T) {
	t.Helper()
	old := profileSyncYes
	profileSyncYes = true
	t.Cleanup(func() { profileSyncYes = old })
}

func TestJSONGolden_ProfileManagement(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)

		out := runJSONCommand(t, func() error {
			return doProfileCreate(svc, game, "survival")
		})
		assertJSONCLIGolden(t, "profile_create_result", out)
	})

	t.Run("delete", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		_, err := getProfileManager(svc).Create(game.ID, "survival")
		require.NoError(t, err)

		out := runJSONCommand(t, func() error {
			return doProfileDelete(svc, game, "survival")
		})
		assertJSONCLIGolden(t, "profile_delete_result", out)
	})

	t.Run("reorder", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")

		out := runJSONCommand(t, func() error {
			return doProfileReorder(context.Background(), svc, game, []string{"b", "a"})
		})
		assertJSONCLIGolden(t, "profile_reorder_result", out)
	})
}

// --- profile apply / switch / sync / import ---

func TestJSONGolden_ProfileApply(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		pm := getProfileManager(svc)
		seedApplyCandidateMod(t, svc, game, "src", "dis1", "Dis One", "1.0", true, map[string][]byte{"dis1.esp": []byte("dis")})
		seedApplyCandidateMod(t, svc, game, "src", "en1", "En One", "1.0", false, map[string][]byte{"en1.esp": []byte("en")})
		require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "en1", Version: "1.0"}))
		applyYes(t)

		out := runJSONCommand(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "profile_apply_result", out)
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		pm := getProfileManager(svc)
		seedApplyCandidateMod(t, svc, game, "src", "dis1", "Dis One", "1.0", true, map[string][]byte{"dis1.esp": []byte("dis")})
		seedApplyCandidateMod(t, svc, game, "src", "en1", "En One", "1.0", false, map[string][]byte{"en1.esp": []byte("en")})
		require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "en1", Version: "1.0"}))
		withProfileDryRun(t, &profileApplyDryRun)

		out := runJSONCommand(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "profile_apply_dry_run", out)
	})
}

func TestJSONGolden_ProfileSwitch(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		pm := getProfileManager(svc)
		_, err := pm.Create(game.ID, "other")
		require.NoError(t, err)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		withProfileSwitchYes(t)

		out := runJSONCommand(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "other")
		})
		assertJSONCLIGolden(t, "profile_switch_result", out)
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		pm := getProfileManager(svc)
		_, err := pm.Create(game.ID, "other")
		require.NoError(t, err)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		withProfileDryRun(t, &profileSwitchDryRun)

		out := runJSONCommand(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "other")
		})
		assertJSONCLIGolden(t, "profile_switch_dry_run", out)
	})
}

func TestJSONGolden_ProfileSync(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		seedSyncInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", "default", true, nil)
		withProfileSyncYes(t)

		out := runJSONCommand(t, func() error {
			return doProfileSync(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "profile_sync_result", out)
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		seedSyncInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", "default", true, nil)
		withProfileDryRun(t, &profileSyncDryRun)

		out := runJSONCommand(t, func() error {
			return doProfileSync(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "profile_sync_dry_run", out)
	})
}

func TestJSONGolden_ProfileImport(t *testing.T) {
	svc, game, _ := setupDoProfileImportTest(t)
	data := buildImportProfileData(t, game.ID, "imported", nil)

	out := runJSONCommand(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})
	assertJSONCLIGolden(t, "profile_import_result", out)
}

// --- the new profile --dry-run flags, plain text ---
//
// apply/switch/sync gained --dry-run in this task (the flag `--dry-run
// --json` needs in order to emit a Plan). All of it is new output behind a
// new flag, so no existing invocation changes; these pin the new lines and,
// more importantly, that a dry run writes nothing.

func TestDoProfileApply_DryRun_PrintsPlanAndChangesNothing(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedApplyCandidateMod(t, svc, game, "src", "dis1", "Dis One", "1.0", true, map[string][]byte{"dis1.esp": []byte("dis")})
	withProfileDryRun(t, &profileApplyDryRun)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Apply plan for profile \"default\" (dry run)\n\n"+
		"Will disable 1 mod(s):\n"+
		"  - Dis One (dis1)\n", out)

	mod, err := svc.GetInstalledMod(context.Background(), "src", "dis1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "a dry run must not disable anything")
}

func TestDoProfileSwitch_DryRun_PrintsPlanAndChangesNothing(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	seedDeployableMod(t, svc, game, "disable-me", "Disable Me", "disable.esp")
	withProfileDryRun(t, &profileSwitchDryRun)

	out := captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "target")
	})

	assert.Equal(t, "Switch plan for profile \"target\" (dry run)\n\n"+
		"Will disable 1 mod(s):\n"+
		"  - Disable Me (disable-me)\n", out)

	active, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", active.Name, "a dry run must not switch the active profile")
}

func TestDoProfileSync_DryRun_PrintsPlanAndChangesNothing(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	withProfileDryRun(t, &profileSyncDryRun)

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Sync plan for profile \"default\" (dry run)\n\n"+
		"Will add to profile:\n"+
		"  + Add One (src:add1)\n", out)

	profile, err := getProfileManager(svc).Get(game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "a dry run must not write the profile")
}

// --- import (archive / scan) ---

func TestJSONGolden_ImportArchive(t *testing.T) {
	svc, game := setupDoImportTest(t)
	// --id/--source pin the imported mod's identity: an unlinked import
	// mints a random local UUID, which no golden can pin.
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: game.ID}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": game.ID}
	importModID, importSource = "999", "acme-source"

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})

	out := runJSONCommand(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	assertJSONCLIGolden(t, "import_archive_result", out)
}

func TestJSONGolden_ImportScan(t *testing.T) {
	t.Run("result", func(t *testing.T) {
		svc, game := setupDoImportTest(t)
		game.DeployMode = domain.DeployCopy
		require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "LooseMod-1.0.zip"), []byte("payload"), 0o644))
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := runJSONCommand(t, func() error {
			return runImportScan(cmd, game, svc, "default")
		})
		assertJSONCLIGolden(t, "import_scan_result", out)
	})

	t.Run("dry_run_plan", func(t *testing.T) {
		svc, game := setupDoImportTest(t)
		game.DeployMode = domain.DeployCopy
		importDryRun = true
		require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "LooseMod-1.0.zip"), []byte("payload"), 0o644))
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := runJSONCommand(t, func() error {
			return runImportScan(cmd, game, svc, "default")
		})
		assertJSONCLIGolden(t, "import_scan_dry_run", out, game.ModPath, "<GAME-DIR>")
	})
}
