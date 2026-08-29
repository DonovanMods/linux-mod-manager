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
