package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestModCmd_Structure(t *testing.T) {
	assert.Equal(t, "mod", modCmd.Use)
	assert.NotEmpty(t, modCmd.Short)
	assert.NotNil(t, modCmd.Commands())
	// mod show subcommand exists
	var showFound bool
	for _, c := range modCmd.Commands() {
		if c.Name() == "show" {
			showFound = true
			assert.Equal(t, "show <mod-id>", c.Use)
			break
		}
	}
	assert.True(t, showFound, "mod show subcommand should exist")
}

func TestModSetUpdateCmd_Structure(t *testing.T) {
	assert.Equal(t, "set-update <mod-id>", modSetUpdateCmd.Use)
	assert.NotEmpty(t, modSetUpdateCmd.Short)
	assert.NotEmpty(t, modSetUpdateCmd.Long)

	// Check flags exist
	assert.NotNil(t, modSetUpdateCmd.Flags().Lookup("auto"))
	assert.NotNil(t, modSetUpdateCmd.Flags().Lookup("notify"))
	assert.NotNil(t, modSetUpdateCmd.Flags().Lookup("pin"))
}

func TestModSetUpdateCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	modCmdCopy.AddCommand(modSetUpdateCmd)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "set-update", "12345", "--auto"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestModSetUpdateCmd_NoPolicy(t *testing.T) {
	gameID = "test-game"
	modSetAuto = false
	modSetNotify = false
	modSetPin = false

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	setUpdateCmdCopy := &cobra.Command{
		Use:  "set-update <mod-id>",
		Args: cobra.ExactArgs(1),
		RunE: runModSetUpdate,
	}
	setUpdateCmdCopy.Flags().BoolVar(&modSetAuto, "auto", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetNotify, "notify", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetPin, "pin", false, "")
	modCmdCopy.AddCommand(setUpdateCmdCopy)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "set-update", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "specify a policy")
}

func TestModSetUpdateCmd_MultiplePolicies(t *testing.T) {
	gameID = "test-game"
	// Reset flags before test
	modSetAuto = false
	modSetPin = false
	modSetNotify = false

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	setUpdateCmdCopy := &cobra.Command{
		Use:  "set-update <mod-id>",
		Args: cobra.ExactArgs(1),
		RunE: runModSetUpdate,
	}
	setUpdateCmdCopy.Flags().BoolVar(&modSetAuto, "auto", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetNotify, "notify", false, "")
	setUpdateCmdCopy.Flags().BoolVar(&modSetPin, "pin", false, "")
	modCmdCopy.AddCommand(setUpdateCmdCopy)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	// Pass --auto and --pin flags via command line
	cmd.SetArgs([]string{"mod", "set-update", "12345", "--auto", "--pin"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "specify only one policy")

	// Reset flags after test
	modSetAuto = false
	modSetPin = false
}

func TestModEnableCmd_Structure(t *testing.T) {
	assert.Equal(t, "enable <mod-id>", modEnableCmd.Use)
	assert.NotEmpty(t, modEnableCmd.Short)
}

func TestModDisableCmd_Structure(t *testing.T) {
	assert.Equal(t, "disable <mod-id>", modDisableCmd.Use)
	assert.NotEmpty(t, modDisableCmd.Short)
}

func TestModEnableCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	modCmdCopy.AddCommand(modEnableCmd)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "enable", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestModDisableCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	modCmdCopy.AddCommand(modDisableCmd)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "disable", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestModEnableCmd_NoModID(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	modCmdCopy.AddCommand(modEnableCmd)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "enable"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestModDisableCmd_NoModID(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	modCmdCopy := &cobra.Command{Use: "mod"}
	modCmdCopy.AddCommand(modDisableCmd)
	cmd.AddCommand(modCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"mod", "disable"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

// TestDoModDisable_Verbose_RestoresHistoricalUndeployWarningByteIdentically
// guards Task 6 item a: EnableMod/DisableMod's (bool, error) -> result-struct
// convergence restores the pre-5a --verbose diagnostic doModDisable dropped
// in 5a Task 1 (adjudicated acceptable-until-convergence at the time - see
// DisableMod's doc comment in flows.go). Asserts the printed line is
// byte-identical to `git show v1.10.0:cmd/lmm/mod.go`'s
// `fmt.Printf("  Warning: failed to undeploy some files: %v\n", err)`.
func TestDoModDisable_Verbose_RestoresHistoricalUndeployWarningByteIdentically(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "g1"},
	}

	// Deploy for real first (mirrors internal/core/flows_test.go's
	// TestService_DisableMod_UndeployFailureIsNonFatal) so there is
	// something to undeploy.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "1", "1.0", "plugin.esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// Corrupt the deployed file into a plain file (not a symlink) so the
	// symlink linker's Undeploy fails deterministically ("not a symlink").
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	oldSource, oldProfile, oldVerbose := modSource, modProfile, verbose
	modSource = "src"
	modProfile = ""
	verbose = true
	t.Cleanup(func() {
		modSource, modProfile, verbose = oldSource, oldProfile, oldVerbose
	})

	out := captureStdout(t, func() error {
		return doModDisable(context.Background(), svc, game, "1")
	})

	assert.Contains(t, out, "  Warning: failed to undeploy some files: ",
		"missing byte-identical restored undeploy-failure diagnostic; got:\n%s", out)
	assert.Contains(t, out, "✓ Disabled: Test Mod (files removed from game, kept in cache)")
}

// blockEnabledColumnUpdates installs a SQLite trigger that aborts any UPDATE
// touching installed_mods.enabled, forcing SetModEnabled to fail
// deterministically. Reproduces internal/core/flows_test.go's
// installEnabledBlockingTrigger (unexported there, so not importable from
// this package) verbatim.
func blockEnabledColumnUpdates(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_enabled_updates
		BEFORE UPDATE OF enabled ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}

// TestDoModDisable_ErrorPath_PrintsAccumulatedNoteToStdout guards the same
// class of bug TestDoUninstall_ErrorPath_PrintsAccumulatedWarningsToStderr
// (uninstall_test.go) guards for doUninstall: printModNotes was called AFTER
// the `if err != nil` check, so a Note accumulated before a LATER fatal error
// was silently dropped, even though v1.10.0 printed it unconditionally.
// doModDisable must now print result.Notes (still --verbose-gated, per
// printModNotes' own contract) before returning the error.
//
// Reproduces the scenario: an undeploy failure is forced deterministically by
// corrupting the deployed symlink into a plain file (mirrors
// internal/core/flows_test.go's TestService_DisableMod_UndeployFailureIsNonFatal),
// recording a Note; a SUBSEQUENT SetModEnabled failure is then forced via
// blockEnabledColumnUpdates above, so DisableMod returns its accumulated
// *DisableResult alongside the fatal error (see DisableMod's doc comment in
// flows.go: undeploy failures are non-fatal, but a SetModEnabled failure is
// not).
func TestDoModDisable_ErrorPath_PrintsAccumulatedNoteToStdout(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "g1"},
	}

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "1", "1.0", "plugin.esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// Corrupt the deployed file into a plain file (not a symlink) so the
	// symlink linker's Undeploy fails deterministically ("not a symlink"),
	// recording a Note before the fatal SetModEnabled failure below.
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	blockEnabledColumnUpdates(t, filepath.Join(dataDir, "lmm.db"))

	oldSource, oldProfile, oldVerbose := modSource, modProfile, verbose
	modSource = "src"
	modProfile = ""
	verbose = true
	t.Cleanup(func() {
		modSource, modProfile, verbose = oldSource, oldProfile, oldVerbose
	})

	out, cmdErr := captureStdoutErr(t, func() error {
		return doModDisable(context.Background(), svc, game, "1")
	})
	require.Error(t, cmdErr, "SetModEnabled must fail while the trigger blocks the update")
	assert.Contains(t, cmdErr.Error(), "failed to update mod status")
	assert.Contains(t, out, "Warning: failed to undeploy some files: ",
		"the accumulated Note must still reach stdout despite the command failing; got:\n%s", out)
}

// TestDoModEnable_ErrorPath_NilResultDoesNotPanic guards the enable-side
// counterpart of doModDisable's fix above: printModNotes(result.Notes) must
// now be reachable before doModEnable returns its error too, for consistency
// and forward-compat with a future EnableMod diagnostic (see EnableResult's
// doc comment in flows.go: Notes is "kept for parity with DisableResult...
// so a future EnableMod diagnostic wouldn't need another signature change").
//
// Unlike DisableMod, EVERY error path EnableMod has today returns a nil
// result (verified by inspection: no EnableMod error branch allocates an
// *EnableResult before returning), so there is no diagnostic actually being
// lost here yet - this test instead guards against the nil-pointer panic a
// careless copy of doModDisable's fix (unconditionally dereferencing
// result.Notes without a nil check) would introduce. SetModEnabled is forced
// to fail the same deterministic way as the disable-side test above.
func TestDoModEnable_ErrorPath_NilResultDoesNotPanic(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "g1"},
	}

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "1", "1.0", "plugin.esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
	}))

	blockEnabledColumnUpdates(t, filepath.Join(dataDir, "lmm.db"))

	oldSource, oldProfile, oldVerbose := modSource, modProfile, verbose
	modSource = "src"
	modProfile = ""
	verbose = true
	t.Cleanup(func() {
		modSource, modProfile, verbose = oldSource, oldProfile, oldVerbose
	})

	out, cmdErr := captureStdoutErr(t, func() error {
		return doModEnable(context.Background(), svc, game, "1")
	})
	require.Error(t, cmdErr, "SetModEnabled must fail while the trigger blocks the update")
	assert.Contains(t, cmdErr.Error(), "failed to update mod status")
	assert.Empty(t, out, "EnableResult carries no Notes on any error path today, so nothing should print")
}
