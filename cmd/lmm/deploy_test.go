package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployCmd_Structure(t *testing.T) {
	assert.Equal(t, "deploy [mod-id]", deployCmd.Use)
	assert.NotEmpty(t, deployCmd.Short)
	assert.NotEmpty(t, deployCmd.Long)

	// Check flags exist
	assert.NotNil(t, deployCmd.Flags().Lookup("source"))
	assert.NotNil(t, deployCmd.Flags().Lookup("profile"))
	assert.NotNil(t, deployCmd.Flags().Lookup("method"))
	assert.NotNil(t, deployCmd.Flags().Lookup("purge"))
}

func TestDeployCmd_PurgeFlag(t *testing.T) {
	purgeFlag := deployCmd.Flags().Lookup("purge")
	assert.NotNil(t, purgeFlag)
	assert.Equal(t, "false", purgeFlag.DefValue)
	assert.Equal(t, "bool", purgeFlag.Value.Type())
}

func TestDeployCmd_NoGame(t *testing.T) {
	gameID = ""
	configDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(deployCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"deploy"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// setupDoDeployTest builds a *core.Service plus a game and resets deploy's
// package-level flag globals to sane defaults for calling doDeploy directly,
// following setupDoUninstallTest's pattern. noColor is forced on so
// assertions don't have to match ANSI escapes; verbose is left to the
// caller. Callers seed their own installed mods/profile.
func setupDoDeployTest(t *testing.T) (*core.Service, *domain.Game) {
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

	oldSource, oldProfile, oldMethod, oldPurge, oldAll, oldForce, oldNoColor, oldNoHooks :=
		deploySource, deployProfile, deployMethod, deployPurge, deployAll, deployForce, noColor, noHooks
	deploySource = "src"
	deployProfile = ""
	deployMethod = ""
	deployPurge = false
	deployAll = false
	deployForce = false
	noColor = true // avoid asserting against ANSI escapes
	noHooks = false
	t.Cleanup(func() {
		deploySource, deployProfile, deployMethod, deployPurge, deployAll, deployForce, noColor, noHooks =
			oldSource, oldProfile, oldMethod, oldPurge, oldAll, oldForce, oldNoColor, oldNoHooks
	})

	return svc, game
}

// seedDeployableMod installs modID/name as enabled, stores fileName in its
// cache, and adds it to the "default" profile so doDeploy will pick it up.
func seedDeployableMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileName string) {
	t.Helper()

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", modID, "1.0", fileName, []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
}

// TestDoDeploy_Verbose_HappyPath_PrintsExpectedOutput guards doDeploy's
// normal multi-mod console output end to end: the "Deploying N mod(s)
// using METHOD..." header, one "  ✓ Name" line per mod in profile order,
// and the "Deployed: N" summary - byte-identical (modulo color, disabled
// here) to the pre-refactor CLI (git show 21db551~1:cmd/lmm/deploy.go).
func TestDoDeploy_Verbose_HappyPath_PrintsExpectedOutput(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")

	oldVerbose := verbose
	verbose = true
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "Deploying 2 mod(s) using symlink...\n\n")
	assert.Contains(t, out, "  ✓ Mod A\n")
	assert.Contains(t, out, "  ✓ Mod B\n")
	assert.Contains(t, out, "\nDeployed: 2\n")
	assert.Less(t, strings.Index(out, "Mod A"), strings.Index(out, "Mod B"), "deploy order must follow profile order")

	for _, f := range []string{"a.esp", "b.esp"} {
		_, err := os.Lstat(filepath.Join(game.ModPath, f))
		assert.NoError(t, err, "%s should be deployed", f)
	}
}

// TestDoDeploy_AfterEachHookFailure_PrintsWarningToStderrUnconditionally
// guards the Warnings display contract: an install.after_each hook failure
// must reach stderr as "Warning: ..." even without --verbose, and must not
// stop the mod from being reported as deployed (after_each is non-fatal).
func TestDoDeploy_AfterEachHookFailure_PrintsWarningToStderrUnconditionally(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "after_each.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterEach: failScript}}

	oldVerbose := verbose
	verbose = false
	t.Cleanup(func() { verbose = oldVerbose })

	var stdout string
	stderr, cmdErr := captureStderrErr(t, func() error {
		stdout = captureStdout(t, func() error {
			return doDeploy(context.Background(), svc, game, nil)
		})
		return nil
	})
	require.NoError(t, cmdErr)

	assert.Contains(t, stderr, "Warning: install.after_each hook failed for 1: ")
	assert.Contains(t, stdout, "  ✓ Test Mod\n")
	assert.Contains(t, stdout, "\nDeployed: 1\n")
	assert.NotContains(t, stdout, "Warning:", "Warnings must go to stderr, not stdout")
}

// TestDoDeploy_Verbose_PrintsUndeployWarningNoteWithHistoricalPrefix guards
// the Notes display contract for deploy's per-mod bookkeeping diagnostics:
// a failed "undeploy old files before redeploy" step is recorded with its
// historical "Warning: undeploy <name>: <err>" text and only shown under
// --verbose, without stopping the mod from redeploying successfully.
func TestDoDeploy_Verbose_PrintsUndeployWarningNoteWithHistoricalPrefix(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	// Deploy once for real, then corrupt the deployed symlink into a plain
	// file so the symlink linker's Undeploy fails deterministically on the
	// second pass ("not a symlink") - mirrors
	// TestService_DisableMod_UndeployFailureIsNonFatal. The cache itself is
	// untouched, so the subsequent Install still succeeds.
	require.NoError(t, doDeploy(context.Background(), svc, game, nil))
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	oldVerbose := verbose
	verbose = true
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "  Warning: undeploy Test Mod: ")
	assert.Contains(t, out, "  ✓ Test Mod\n")
	assert.Contains(t, out, "\nDeployed: 1\n")
}

// TestDoDeploy_NonVerbose_DoesNotPrintNotes guards the other half of the
// Notes contract: without --verbose, Notes-derived diagnostics must not
// appear at all.
func TestDoDeploy_NonVerbose_DoesNotPrintNotes(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	require.NoError(t, doDeploy(context.Background(), svc, game, nil))
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	oldVerbose := verbose
	verbose = false
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "Warning: undeploy")
	assert.Contains(t, out, "  ✓ Test Mod\n")
}
