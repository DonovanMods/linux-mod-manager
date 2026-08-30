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
	"bytes"
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

// TestDoGameDetect_JSON_PartialApplyFailure_EnvelopeNamesPersistedGames
// covers Task 11 review Minor 5: when ApplyGameDetect saves one game and
// then fails converting a second, doGameDetect's --json path used to return
// the bare applyErr, so the error envelope never said which game(s) were
// already persisted (the plain-text "Added:" loop, by contrast, always
// prints every game result.Profiles names before returning applyErr - see
// its own comment). The second game here has neither Sources nor a NexusID,
// so GameFromDetected fails it deterministically.
func TestDoGameDetect_JSON_PartialApplyFailure_EnvelopeNamesPersistedGames(t *testing.T) {
	configDir = t.TempDir()
	svc := newGameDetectTestService(t)
	withJSONOutput(t)
	oldAll := gameDetectAll
	gameDetectAll = true
	t.Cleanup(func() { gameDetectAll = oldAll })
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "no-source", Name: "No Source Game", InstallPath: "/games/no-source"},
	}

	var callErr error
	stdout := captureStdout(t, func() error {
		callErr = doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc, games, []string{"steam library at /nowhere is unreadable"})
		return nil
	})

	require.Error(t, callErr)
	assert.Empty(t, stdout, "the --json partial-failure path must never emit a Result document")
	assert.Empty(t, buf.String(), "no listing may be printed under --json")

	var partialErr *gameDetectPartialError
	require.ErrorAs(t, callErr, &partialErr, "the error must carry the partial result, not just applyErr")
	assert.Equal(t, []string{"skyrim-se"}, partialErr.result.Saved)
	assert.Equal(t, []string{"skyrim-se/default"}, partialErr.result.Profiles)
	assert.Equal(t, []string{"steam library at /nowhere is unreadable"}, partialErr.result.Warnings,
		"the scan warnings that preceded the apply must still reach the envelope's details")

	envelope := captureStdout(t, func() error { reportError(callErr); return nil })
	assert.Contains(t, envelope, "\"details\"")
	assert.Contains(t, envelope, "\"saved\": [\n      \"skyrim-se\"\n    ]", "the envelope's details must name the persisted game")

	saved, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "Skyrim Special Edition", saved.Name, "the successfully-persisted game must still be on disk")
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

	// `profile reorder` with no arguments is the load-order READOUT, and
	// profile.Mods is that order - so it emits the same document a reorder
	// does, rather than a second shape for the same data.
	t.Run("reorder_listing", func(t *testing.T) {
		svc, game := setupDoProfileSwitchTest(t)
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

		out := runJSONCommand(t, func() error {
			return doProfileReorder(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "profile_reorder_listing", out)
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

// TestDoProfileSwitch_DryRun_NoChanges_DoesNotSwitchDefault covers
// doProfileSwitch's plan.NoChanges branch under --dry-run (Task 11 review,
// Minor 4): the two dry-run tests above both exercise a plan WITH changes,
// so neither ever reaches the `if profileSwitchDryRun { return nil }` guard
// that skips the bare SetDefault a no-op switch would otherwise still
// perform. Mirrors TestDoProfileSwitch_NoChanges_SwitchesDefaultWithoutPrompting's
// setup (a target profile whose mod set already matches "default"'s), minus
// the mutation.
func TestDoProfileSwitch_DryRun_NoChanges_DoesNotSwitchDefault(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "other")
	require.NoError(t, err)

	seedDeployableMod(t, svc, game, "shared", "Shared Mod", "shared.esp")
	require.NoError(t, pm.AddMod(game.ID, "other", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))
	withProfileDryRun(t, &profileSwitchDryRun)

	out := captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "other")
	})

	assert.Equal(t, "Switch plan for profile \"other\" (dry run)\n\n", out)

	active, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", active.Name, "a dry run must not switch the active profile, even when the plan has no mod changes")
}

// TestDoProfileSync_DryRun_MissingProfile_DoesNotCreateProfile covers
// doProfileSync's plan.NoChanges/plan.Missing branch under --dry-run (Task
// 11 review, Minor 4): TestDoProfileSync_MissingProfile_EmptyDiff_StillCreatesProfile
// pins that a plain sync onto a nonexistent profile still writes its
// profile.yaml (ApplyProfileSync's create-on-missing side effect, reached
// even with an empty diff); this is that same setup under --dry-run, where
// the `plan.Missing && !profileSyncDryRun` guard must skip that write
// entirely.
func TestDoProfileSync_DryRun_MissingProfile_DoesNotCreateProfile(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	withProfileDryRun(t, &profileSyncDryRun)

	pm := getProfileManager(svc)
	_, err := pm.Get(game.ID, "newprof")
	require.Error(t, err, "precondition: newprof must not exist yet")

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, []string{"newprof"})
	})

	assert.Equal(t, "Profile newprof is already in sync.\n", out)

	_, err = pm.Get(game.ID, "newprof")
	assert.Error(t, err, "a dry run must not create profile.yaml, even for a missing profile with nothing to sync into it")
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

// --- install (single / deps / multi) ---

// seedConflictingOwner installs "other", which owns shared.esp in the game
// dir, so a later install of a mod carrying the same file conflicts.
// Mirrors install_test.go's own local seedOtherOwningSharedFile closure.
func seedConflictingOwner(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "other", "1.0", "shared.esp", []byte("original-other-content")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "other", SourceID: "test-src", Name: "Other Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	require.NoError(t, svc.GetInstaller(game).Install(context.Background(), game,
		&domain.Mod{ID: "other", SourceID: "test-src", Version: "1.0", GameID: game.ID}, "default"))
}

func TestJSONGolden_Install(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		svc, game, src := setupDoInstallTest(t)
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "main", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"}})
		src.AddDownload("main", []byte("plugin content"))

		out := runJSONCommand(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "install_single_result", out)
	})

	t.Run("deps", func(t *testing.T) {
		svc, game, src := setupDoInstallTest(t)
		src.AddMod(&domain.Mod{ID: "dep1", SourceID: "test-src", Name: "Dep One", Version: "1.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "dep-main", FileName: "dep1.esp", IsPrimary: true}})
		src.AddDownload("dep-main", []byte("dep content"))
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1",
			Dependencies: []domain.ModReference{{SourceID: "test-src", ModID: "dep1"}}},
			[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
		src.AddDownload("main", []byte("plugin content"))

		out := runJSONCommand(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})
		assertJSONCLIGolden(t, "install_deps_result", out)
	})

	t.Run("multi", func(t *testing.T) {
		svc, game, src := setupDoInstallTest(t)
		for _, id := range []string{"mod1", "mod2"} {
			src.AddMod(&domain.Mod{ID: id, SourceID: "test-src", Name: "Mod " + id, Version: "1.0", GameID: "g1"},
				[]domain.DownloadableFile{{ID: id + "-main", FileName: id + ".esp", IsPrimary: true}})
			src.AddDownload(id+"-main", []byte("content "+id))
		}
		mods := []*domain.Mod{
			{ID: "mod1", SourceID: "test-src", Name: "Mod mod1", Version: "1.0", GameID: "g1"},
			{ID: "mod2", SourceID: "test-src", Name: "Mod mod2", Version: "1.0", GameID: "g1"},
		}

		out := runJSONCommand(t, func() error {
			return installMultipleMods(context.Background(), svc, game, mods, "default")
		})
		assertJSONCLIGolden(t, "install_multi_result", out)
	})
}

// TestDoInstall_JSON_ConflictWithoutForce_SurfacesEnvelopeWithDetails pins
// the install-specific half of Ruling 15: under --json an unaccepted file
// conflict never reaches the confirmation prompt at all - the
// *core.ConflictError comes straight back, so reportError renders it as the
// envelope with details.conflicts, and nothing is installed.
func TestDoInstall_JSON_ConflictWithoutForce_SurfacesEnvelopeWithDetails(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = false
	withJSONOutput(t)
	seedConflictingOwner(t, svc, game)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "shared.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("new-mod1-content"))

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})
	})

	var conflictErr *core.ConflictError
	require.ErrorAs(t, err, &conflictErr)
	require.NotEmpty(t, conflictErr.Conflicts)
	assert.Empty(t, stdout, "the conflict path emits no result document")
	assert.Empty(t, stderr)

	_, dbErr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	assert.Error(t, dbErr, "nothing may be installed when the conflict is unresolved")

	// The envelope reportError would print for that error carries the
	// conflicts as data.
	envelope := captureStdout(t, func() error { reportError(err); return nil })
	assert.Contains(t, envelope, "\"details\"")
	assert.Contains(t, envelope, "\"conflicts\"")
}

// TestDoInstall_JSON_ForceAcceptsConflicts is the other side: --force
// implies AcceptConflicts in core, so a --json install never reaches the
// prompt and completes with its Result document.
func TestDoInstall_JSON_ForceAcceptsConflicts(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = false
	installForce = true
	seedConflictingOwner(t, svc, game)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "shared.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("new-mod1-content"))

	out := runJSONCommand(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})
	})

	var doc core.InstallResult
	decodeSingleDoc(t, out, &doc)
	assert.Len(t, doc.Installed, 1)
}

// TestLogLevel_UnderJSON_StillWritesToStderr pins the single carve-out in
// Ruling 15's "stderr stays empty" rule: --log-level diagnostics are not
// output, and --json does not silence them. newCLILogger is the one place
// the CLI builds that logger (initServiceWith hands it os.Stderr), and it
// consults nothing but the level - so a --json run still gets its
// diagnostics on stderr, alongside the one document on stdout.
func TestLogLevel_UnderJSON_StillWritesToStderr(t *testing.T) {
	withJSONOutput(t)

	var buf bytes.Buffer
	logger, err := newCLILogger("warn", &buf)
	require.NoError(t, err)
	logger.Warn("diagnostic", "k", "v")

	assert.Contains(t, buf.String(), "level=WARN msg=diagnostic k=v")
}

// --- update / rollback (Task 11 review, Important 3) ---
//
// update/update rollback were left out of the Ruling 15 sweep despite being
// a row of the coverage table: doUpdate's verbose bulk-check print/sink
// (update.go's `if verbose {...}` before CheckGameUpdates) and applyUpdate's/
// doUpdateRollback's UpdateBeforeEachForced/UpdateWarning progress cases both
// wrote straight to stdout/stderr with no jsonOutput gate. These three pin
// the fix: the verbose bulk print no longer leaks onto stdout ahead of the
// document, and a warning that would otherwise print via the progress
// closure instead reaches the Result/RollbackResult document with nothing on
// stderr - the same framing every other mutating command gets via
// quietSink.

// TestDoUpdate_JSON_Verbose_BulkCheckStaysOffStdout covers doUpdate's bulk
// (no mod-id) path: with --verbose, the "Checking %d mod(s)..." print and
// the per-mod UpdateCheckEvent sink it installs must both stay off under
// --json, or runJSONCommand's single-document check fails.
func TestDoUpdate_JSON_Verbose_BulkCheckStaysOffStdout(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	verbose = true
	seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	// No AddMod: the check still runs (and would still fire per-mod
	// UpdateCheckEvents under plain --verbose) even though it finds nothing.

	out := runJSONCommand(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	var doc core.UpdateCheckReport
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, game.ID, doc.GameID)
}

// TestApplySingleUpdate_JSON_AfterEachHookWarnings_ReachResultWithNoStderr
// covers applyUpdate's progress closure (update.go ~:706): a forced-nonfatal
// after_each hook failure emits core.UpdateWarning, which used to print
// straight to stderr regardless of --json. quietSink now keeps the closure
// unregistered under --json, so the warnings must show up in
// UpdateApplyResult.Warnings instead, with stderr empty.
func TestApplySingleUpdate_JSON_AfterEachHookWarnings_ReachResultWithNoStderr(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)

	scriptsDir := t.TempDir()
	uninstallScript := filepath.Join(scriptsDir, "uninstall_after_each.sh")
	installScript := filepath.Join(scriptsDir, "install_after_each.sh")
	require.NoError(t, os.WriteFile(uninstallScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	require.NoError(t, os.WriteFile(installScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{
		Uninstall: domain.HookConfig{AfterEach: uninstallScript},
		Install:   domain.HookConfig{AfterEach: installScript},
	}

	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	out := runJSONCommand(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	var doc core.UpdateApplyResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, "updated", doc.Status.String())
	assert.Contains(t, doc.Warnings, "uninstall.after_each hook failed: hook failed with exit code 1: "+uninstallScript)
	assert.Contains(t, doc.Warnings, "install.after_each hook failed: hook failed with exit code 1: "+installScript)
}

// TestDoUpdateRollback_JSON_AfterEachHookWarnings_ReachResultWithNoStderr is
// the rollback-side twin: doUpdateRollback's own progress closure
// (update.go ~:848) has the identical UpdateWarning case, now also routed
// through quietSink.
func TestDoUpdateRollback_JSON_AfterEachHookWarnings_ReachResultWithNoStderr(t *testing.T) {
	svc, game, _ := setupRollbackReadyMod(t)

	scriptsDir := t.TempDir()
	uninstallScript := filepath.Join(scriptsDir, "uninstall_after_each.sh")
	installScript := filepath.Join(scriptsDir, "install_after_each.sh")
	require.NoError(t, os.WriteFile(uninstallScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	require.NoError(t, os.WriteFile(installScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{
		Uninstall: domain.HookConfig{AfterEach: uninstallScript},
		Install:   domain.HookConfig{AfterEach: installScript},
	}

	out := runJSONCommand(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})

	var doc core.RollbackResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, "rolled_back", doc.Status.String())
	assert.Contains(t, doc.Warnings, "uninstall.after_each hook failed: hook failed with exit code 1: "+uninstallScript)
	assert.Contains(t, doc.Warnings, "install.after_each hook failed: hook failed with exit code 1: "+installScript)
}

// TestDoUpdate_JSON_BulkCheckError_NoStderrLeak pins Task 11 re-review round
// 2, New Finding 1: a bulk `lmm update --json` whose source check fails with
// a non-auth error used to print an unconditional "Warning: ..." line to
// stderr even though the same message already reaches
// UpdateCheckReport.ErrorMessage in the document - the exact "stderr stays
// empty under --json" violation Important 3 fixed at its three named sites,
// left standing here because this fourth site was outside the ruling's
// scope.
func TestDoUpdate_JSON_BulkCheckError_NoStderrLeak(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	// "unregistered-src" is never passed to svc.RegisterSource, so
	// CheckGameUpdates' source lookup fails with a plain (non-auth) error -
	// the ordinary "a source is unreachable" case, not an auth prompt.
	seedInstalledForUpdate(t, svc, game, "unregistered-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})

	withJSONOutput(t)
	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	require.ErrorIs(t, err, ErrReported, "a failed check must still report non-zero")
	assert.Empty(t, stderr, "the check-error warning must not leak to stderr under --json (Ruling 15)")

	var doc core.UpdateCheckReport
	decodeSingleDoc(t, stdout, &doc)
	assert.Contains(t, doc.ErrorMessage, "unregistered-src", "the message must still reach the document")
}

// TestDoImport_JSON_ConflictWithoutForce_SurfacesEnvelopeWithDetails is the
// archive-import twin of
// TestDoInstall_JSON_ConflictWithoutForce_SurfacesEnvelopeWithDetails (unit
// P review, Important 1): doImport and doInstall share
// confirmInstallConflicts, so import's caller needs install's `&&
// !jsonOutput` guard too. Without it the conflict block prints to stdout
// ahead of the envelope and the *core.ConflictError collapses into
// ErrConfirmationRequired at the refused stdin read, losing
// details.conflicts entirely.
func TestDoImport_JSON_ConflictWithoutForce_SurfacesEnvelopeWithDetails(t *testing.T) {
	svc, game, archiveBPath := setupImportConflictTest(t)
	importForce = false
	withJSONOutput(t)

	before := snapshotDryRunState(t, svc, game, "default")

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archiveBPath})
		})
	})

	var conflictErr *core.ConflictError
	require.ErrorAs(t, err, &conflictErr)
	require.NotEmpty(t, conflictErr.Conflicts)
	assert.Equal(t, "shared.txt", conflictErr.Conflicts[0].RelativePath)
	assert.Equal(t, "A1", conflictErr.Conflicts[0].CurrentModID)
	assert.Empty(t, stdout, "the conflict path emits no document and no prompt text")
	assert.Empty(t, stderr)

	assert.Equal(t, before, snapshotDryRunState(t, svc, game, "default"),
		"a refused conflict must leave the DB, the profile, the cache and the game dir exactly as it found them")

	// The envelope reportError would print for that error carries the
	// conflicts as data, exactly as install's does.
	envelope := captureStdout(t, func() error { reportError(err); return nil })
	assert.Contains(t, envelope, "\"details\"")
	assert.Contains(t, envelope, "\"conflicts\"")
}
