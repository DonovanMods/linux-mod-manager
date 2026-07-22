package main

import (
	"bytes"
	"context"
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
