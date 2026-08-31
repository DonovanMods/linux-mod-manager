package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPurgeCmd_Structure tests the purge command structure
func TestPurgeCmd_Structure(t *testing.T) {
	assert.Equal(t, "purge", purgeCmd.Use)
	assert.NotEmpty(t, purgeCmd.Short)
	assert.NotEmpty(t, purgeCmd.Long)

	// Check flags exist
	assert.NotNil(t, purgeCmd.Flags().Lookup("profile"))
	assert.NotNil(t, purgeCmd.Flags().Lookup("uninstall"))
	assert.NotNil(t, purgeCmd.Flags().Lookup("yes"))
}

// TestPurgeCmd_NoGame tests purge without game flag
func TestPurgeCmd_NoGame(t *testing.T) {
	// Reset global state
	gameID = ""
	configDir = t.TempDir() // Use temp dir to avoid loading real default game

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(purgeCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(purgeCmd); rootCmd.AddCommand(purgeCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"purge"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestPurgeCmd_DefaultFlags tests that default flag values are set
func TestPurgeCmd_DefaultFlags(t *testing.T) {
	profileFlag := purgeCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	uninstallFlag := purgeCmd.Flags().Lookup("uninstall")
	assert.Equal(t, "false", uninstallFlag.DefValue)

	yesFlag := purgeCmd.Flags().Lookup("yes")
	assert.Equal(t, "false", yesFlag.DefValue)
}

// TestPurgeCmd_GameNotFound tests purge with non-existent game
func TestPurgeCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"
	purgeYes = true

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(purgeCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(purgeCmd); rootCmd.AddCommand(purgeCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"purge"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}

// TestPurgeCmd_NoModsToPurge tests purge with no installed mods
func TestPurgeCmd_NoModsToPurge(t *testing.T) {
	// Set up temp directories
	tempDir := t.TempDir()
	cfgDir := filepath.Join(tempDir, "config")
	dtaDir := filepath.Join(tempDir, "data")

	require.NoError(t, os.MkdirAll(cfgDir, 0755))
	require.NoError(t, os.MkdirAll(dtaDir, 0755))

	// Create a minimal games.yaml
	gamesYaml := `games:
  test-game:
    name: Test Game
    install_path: /tmp/test-game
    mod_path: /tmp/test-game/mods
    sources:
      nexusmods: testgame
`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "games.yaml"), []byte(gamesYaml), 0644))

	// Set global state
	configDir = cfgDir
	dataDir = dtaDir
	gameID = "test-game"
	purgeYes = true
	purgeProfile = ""
	purgeUninstall = false

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(purgeCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(purgeCmd); rootCmd.AddCommand(purgeCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"purge"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// --- doPurge output-fidelity tests (#61) ---
//
// Written against the pre-refit doPurge and kept unedited through its refit
// onto core.PurgeProfile: they pin the command's byte-exact console output.

// setupDoPurgeTest builds a *core.Service plus a game and resets purge's
// package-level flag globals for calling doPurge directly, following
// setupDoDeployTest's pattern. purgeYes is forced on (no stdin prompt);
// verbose is left to the caller.
func setupDoPurgeTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	oldProfile, oldUninstall, oldYes, oldForce, oldDryRun, oldNoColor, oldNoHooks :=
		purgeProfile, purgeUninstall, purgeYes, purgeForce, purgeDryRun, noColor, noHooks
	purgeProfile = ""
	purgeUninstall = false
	purgeYes = true
	purgeForce = false
	purgeDryRun = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		purgeProfile, purgeUninstall, purgeYes, purgeForce, purgeDryRun, noColor, noHooks =
			oldProfile, oldUninstall, oldYes, oldForce, oldDryRun, oldNoColor, oldNoHooks
	})

	return svc, game
}

// seedPurgeableMod seeds modID/name as installed AND deploys its file into
// the game dir - the state `lmm purge` operates on. The installed_mods row's
// Deployed flag is reset to false afterwards: the historical fixture (a bare
// Installer.Install, which never touched that column) left it at
// SaveInstalledMod's zero value despite the file being genuinely on disk,
// and TestJSONGolden_Purge/dry_run_plan pins that exact "deployed": false in
// the recorded plan document, so deployInstalledMod's own Deployed:true
// write (Ruling 10) is reverted here rather than re-recording the golden.
func seedPurgeableMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileName string) {
	t.Helper()
	seedDeployableMod(t, svc, game, modID, name, fileName)
	deployInstalledMod(t, svc, game, &domain.Mod{ID: modID, SourceID: "src", Version: "1.0", GameID: game.ID}, "default")
	require.NoError(t, svc.SetModDeployed(context.Background(), "src", modID, game.ID, "default", false))
}

func TestDoPurge_HappyPath_ByteExactOutput(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedPurgeableMod(t, svc, game, "b", "Mod B", "b.esp")

	out := captureStdout(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	assert.Equal(t, "\nPurging mods from Game...\n\n"+
		"  ✓ Mod A\n"+
		"  ✓ Mod B\n"+
		"\nPurged: 2 mod(s)\n"+
		"\nMod records preserved. Use 'lmm deploy' to restore mods.\n", out)

	for _, f := range []string{"a.esp", "b.esp"} {
		_, err := os.Lstat(filepath.Join(game.ModPath, f))
		assert.True(t, os.IsNotExist(err), "%s must be undeployed", f)
	}
	mod, err := svc.GetInstalledMod(context.Background(), "src", "a", "g1", "default")
	require.NoError(t, err, "records must be preserved without --uninstall")
	assert.False(t, mod.Deployed)
}

// TestDoPurge_JSONOutputReturnsConfirmationRequired pins the non-interactive
// rule (v2 Phase 3 Ruling 2) at doPurge's "Continue?" prompt: under --json
// with no -y, the purge must fail with core.ErrConfirmationRequired before
// ever reading stdin, and nothing gets undeployed.
func TestDoPurge_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	purgeYes = false
	withJSONOutput(t)
	seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")

	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doPurge(context.Background(), svc, game)
		})
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	assert.Empty(t, stdout, "a refused prompt emits no result document")
	assert.Empty(t, stderr)
	_, err = os.Lstat(filepath.Join(game.ModPath, "a.esp"))
	assert.NoError(t, err, "must not undeploy anything")
	_, dbErr := svc.GetInstalledMod(context.Background(), "src", "a", "g1", "default")
	assert.NoError(t, dbErr, "the record must still exist, untouched")
}

func TestDoPurge_BeforeEachSkip_PrintsSkippedLineAndFailedCount(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	seedPurgeableMod(t, svc, game, "bad", "Bad Mod", "bad.esp")
	seedPurgeableMod(t, svc, game, "good", "Good Mod", "good.esp")

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_each.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\n"+
		`if [ "$LMM_MOD_ID" = "bad" ]; then echo boom >&2; exit 1; fi`+"\nexit 0\n"), 0755))
	game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}}

	out := captureStdout(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	assert.Contains(t, out, "  Skipped Bad Mod: uninstall.before_each hook failed: ")
	assert.Contains(t, out, "  ✓ Good Mod\n")
	assert.NotContains(t, out, "✓ Bad Mod")
	assert.Contains(t, out, "\nPurged: 1 mod(s), Failed: 1\n")

	_, err := os.Lstat(filepath.Join(game.ModPath, "bad.esp"))
	assert.NoError(t, err, "a before_each-skipped mod must stay deployed")
}

func TestDoPurge_Uninstall_OmitsPreservedTrailer_RemovesRecords(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	seedPurgeableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	purgeUninstall = true

	out := captureStdout(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	assert.Contains(t, out, "  ✓ Test Mod\n")
	assert.Contains(t, out, "\nPurged: 1 mod(s)\n")
	assert.NotContains(t, out, "Mod records preserved")

	_, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "--uninstall must delete the DB record")
	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "--uninstall must remove the profile YAML entry")
}

func TestDoPurge_NoMods_PrintsEarlyOut(t *testing.T) {
	svc, game := setupDoPurgeTest(t)

	out := captureStdout(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	assert.Equal(t, "No mods installed for Game (profile: default)\n", out)
}

func TestDoPurge_ForcedBeforeAll_WarningPrintsBeforeHeader(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	seedPurgeableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	purgeForce = true

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_all.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}

	out := captureCombined(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	warnIdx := strings.Index(out, "Warning: uninstall.before_all hook failed (forced): ")
	headerIdx := strings.Index(out, "\nPurging mods from Game...")
	require.NotEqual(t, -1, warnIdx, "expected the forced before_all warning, got: %q", out)
	require.NotEqual(t, -1, headerIdx)
	assert.Less(t, warnIdx, headerIdx, "the forced warning must print before the purge header")
	assert.Contains(t, out, "  ✓ Test Mod\n")
}

func TestDoPurge_AfterHookWarnings_StderrAfterModLines(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	seedPurgeableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "fail.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{AfterEach: failScript, AfterAll: failScript}}

	out := captureCombined(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	afterEachIdx := strings.Index(out, "Warning: uninstall.after_each hook failed for Test Mod: ")
	afterAllIdx := strings.Index(out, "Warning: uninstall.after_all hook failed: ")
	modLineIdx := strings.Index(out, "  ✓ Test Mod\n")
	require.NotEqual(t, -1, afterEachIdx, "expected the after_each warning (NAME attribution), got: %q", out)
	require.NotEqual(t, -1, afterAllIdx)
	require.NotEqual(t, -1, modLineIdx)
	assert.Greater(t, afterEachIdx, modLineIdx, "after-hook warnings are deferred until every mod line has printed")
	assert.Greater(t, afterAllIdx, afterEachIdx, "after_each warnings print before after_all's")
}

func TestDoPurge_Verbose_UndeployDiagnosticPrintsInline(t *testing.T) {
	svc, game := setupDoPurgeTest(t)
	seedPurgeableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	// Corrupt the deployed symlink into a plain file so Uninstall fails.
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	oldVerbose := verbose
	verbose = true
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doPurge(context.Background(), svc, game)
	})

	warnIdx := strings.Index(out, "  ⚠ Test Mod - ")
	modLineIdx := strings.Index(out, "  ✓ Test Mod\n")
	require.NotEqual(t, -1, warnIdx, "expected the --verbose undeploy diagnostic, got: %q", out)
	require.NotEqual(t, -1, modLineIdx)
	assert.Less(t, warnIdx, modLineIdx, "the diagnostic prints inline, before this mod's own ✓ line")
	assert.Contains(t, out, "\nPurged: 1 mod(s)\n", "an undeploy failure is best-effort; the mod still counts")

	// And without --verbose it must not appear at all.
	verbose = false
	seedPurgeableMod(t, svc, game, "2", "Other Mod", "other.esp")
	deployedPath2 := filepath.Join(game.ModPath, "other.esp")
	require.NoError(t, os.Remove(deployedPath2))
	require.NoError(t, os.WriteFile(deployedPath2, []byte("not a symlink"), 0644))
	out = captureStdout(t, func() error {
		return doPurge(context.Background(), svc, game)
	})
	assert.NotContains(t, out, "⚠")
}
