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

// TestUninstallCmd_Structure tests the uninstall command structure
func TestUninstallCmd_Structure(t *testing.T) {
	assert.Equal(t, "uninstall <mod-id>", uninstallCmd.Use)
	assert.NotEmpty(t, uninstallCmd.Short)
	assert.NotEmpty(t, uninstallCmd.Long)

	// Check flags exist
	assert.NotNil(t, uninstallCmd.Flags().Lookup("source"))
	assert.NotNil(t, uninstallCmd.Flags().Lookup("profile"))
	assert.NotNil(t, uninstallCmd.Flags().Lookup("keep-cache"))
}

// TestUninstallCmd_NoGame tests uninstall without game flag
func TestUninstallCmd_NoGame(t *testing.T) {
	// Reset flags. configDir must point at an empty tempdir so requireGame
	// does not pick up a default-game from the user's real ~/.config/lmm.
	gameID = ""
	configDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(uninstallCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"uninstall", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestUninstallCmd_NoModID tests uninstall without mod-id argument
func TestUninstallCmd_NoModID(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(uninstallCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"uninstall"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

// TestUninstallCmd_DefaultFlags tests that default flag values are set
func TestUninstallCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := uninstallCmd.Flags().Lookup("source")
	assert.Equal(t, "", sourceFlag.DefValue) // empty = auto-detect from game config

	profileFlag := uninstallCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	keepCacheFlag := uninstallCmd.Flags().Lookup("keep-cache")
	assert.Equal(t, "false", keepCacheFlag.DefValue)
}

// TestUninstallCmd_GameNotFound tests uninstall with non-existent game
func TestUninstallCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(uninstallCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"uninstall", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}

// setupDoUninstallTest builds a *core.Service plus a mod that will fail to
// undeploy (its cache directory was never created, so
// Installer.Uninstall's cache.ListFiles call fails deterministically) and
// resets the uninstall command's package-level flag globals to sane
// defaults for calling doUninstall directly. Callers set globals.verbose
// themselves.
func setupDoUninstallTest(t *testing.T) (*core.Service, *domain.Game) {
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

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	_, err = pm.Create("g1", "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod("g1", "default", domain.ModReference{SourceID: "src", ModID: "1", Version: "1.0"}))

	oldSource, oldProfile, oldKeep, oldForce := uninstallSource, uninstallProfile, uninstallKeep, uninstallForce
	uninstallSource = ""
	uninstallProfile = ""
	uninstallKeep = false
	uninstallForce = false
	t.Cleanup(func() {
		uninstallSource, uninstallProfile, uninstallKeep, uninstallForce = oldSource, oldProfile, oldKeep, oldForce
	})

	return svc, game
}

// TestDoUninstall_Verbose_PrintsUndeployFailureNoteWithHistoricalPrefix
// guards FINDING 1 of the Task 2 review: the undeploy-failure diagnostic
// must be printed to stdout, under --verbose only, with its historical
// "  Warning: failed to undeploy some files: <err>\n" bytes - byte-identical
// to the pre-refactor CLI (git show 1c092df:cmd/lmm/uninstall.go).
func TestDoUninstall_Verbose_PrintsUndeployFailureNoteWithHistoricalPrefix(t *testing.T) {
	svc, game := setupDoUninstallTest(t)

	oldVerbose := verbose
	verbose = true
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "1")
	})

	assert.Contains(t, out, "  Warning: failed to undeploy some files: ")
	assert.Contains(t, out, "✓ Uninstalled: Test Mod")
}

// TestDoUninstall_NonVerbose_DoesNotPrintNotes guards the other half of
// FINDING 1: without --verbose, the Notes-derived diagnostics must not
// appear at all, matching the pre-extraction CLI's `if verbose { ... }`
// gating.
func TestDoUninstall_NonVerbose_DoesNotPrintNotes(t *testing.T) {
	svc, game := setupDoUninstallTest(t)

	oldVerbose := verbose
	verbose = false
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "1")
	})

	assert.NotContains(t, out, "Warning:")
	assert.NotContains(t, out, "Note:")
	assert.Contains(t, out, "✓ Uninstalled: Test Mod")
}

// TestDoUninstall_Verbose_PrintsAllThreeHistoricalNotesByteIdentically
// end-to-end verifies FINDING 1 for all three drifted diagnostics at once:
// undeploy failure, cache-delete failure (both via a blocked cache
// directory - see TestService_UninstallMod_UndeployAndCacheDeleteFailures_RecordedAsNotesWithHistoricalPrefixes
// in internal/core/flows_test.go for why one obstruction fails both), and
// profile-removal (no profile ever created). Asserts the printed lines are
// byte-identical to `git show 1c092df:cmd/lmm/uninstall.go`'s
// `fmt.Printf("  Warning: failed to undeploy some files: %v\n", err)`,
// `fmt.Printf("  Warning: failed to clean cache: %v\n", err)`, and
// `fmt.Printf("  Note: %v\n", err)`.
func TestDoUninstall_Verbose_PrintsAllThreeHistoricalNotesByteIdentically(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()
	cacheDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: cacheDir,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	// No profile created -> profile-removal note.

	// Block the mod's cache directory with a regular file -> undeploy and
	// cache-delete both fail deterministically (ENOTDIR, not permissions).
	modPath := svc.GetGameCache(game).ModPath("g1", "src", "1", "1.0")
	blockedParent := filepath.Dir(modPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(blockedParent), 0755))
	require.NoError(t, os.WriteFile(blockedParent, []byte("blocked"), 0644))

	oldSource, oldProfile, oldKeep, oldForce, oldVerbose := uninstallSource, uninstallProfile, uninstallKeep, uninstallForce, verbose
	uninstallSource = ""
	uninstallProfile = ""
	uninstallKeep = false
	uninstallForce = false
	verbose = true
	t.Cleanup(func() {
		uninstallSource, uninstallProfile, uninstallKeep, uninstallForce, verbose = oldSource, oldProfile, oldKeep, oldForce, oldVerbose
	})

	out := captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "1")
	})

	lines := strings.Split(out, "\n")
	foundUndeploy, foundCache, foundNote := false, false, false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "  Warning: failed to undeploy some files: "):
			foundUndeploy = true
		case strings.HasPrefix(line, "  Warning: failed to clean cache: "):
			foundCache = true
		case strings.HasPrefix(line, "  Note: "):
			foundNote = true
		}
	}

	assert.True(t, foundUndeploy, "missing byte-identical undeploy-failure note; got:\n%s", out)
	assert.True(t, foundCache, "missing byte-identical cache-delete-failure note; got:\n%s", out)
	assert.True(t, foundNote, "missing byte-identical profile-removal note; got:\n%s", out)
	assert.Contains(t, out, "✓ Uninstalled: Test Mod")
}
