package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- doVerify wires deploy convergence (#168/#212) ---
//
// setupDoVerifyConvergeTest builds a fixture with the two shapes
// ConvergeDeployedFiles detects (internal/core/converge_test.go exercises the
// same shapes at the core seam: TestConverge_RowDrivenStaleRemoved and
// TestConverge_DanglingCacheLinkSwept), driven here through the real doVerify
// CLI path instead:
//
//  1. A stale deployed_files row: mod1's cache holds "a.esp" and "gone.esp",
//     both get deployed by a real Install, then "gone.esp" is removed from
//     the cache afterward - its deployed_files row (and game-dir symlink)
//     survive, but the file it names is no longer provided by anything.
//  2. A dangling sweep candidate: a symlink placed directly into the game
//     dir, pointing at a path under the game's cache root that was never
//     created, with no deployed_files row at all.
//
// mod1 is installed under domain.SourceLocal (setupDoInstallTest's
// fakeInstallSource is registered but unused here) so doVerify's
// version-record pre-pass skips it entirely (verify.go's `mod.SourceID ==
// domain.SourceLocal` guard) - isolating the new convergence wiring from
// that pre-existing check. A single checksummed file ("a.esp") keeps
// GetFilesWithChecksums non-empty (avoiding doVerify's early "No installed
// mods to verify" return, which runs before convergence would ever get a
// chance to) while leaving every pre-existing check clean (no incidental
// issues/warnings to disentangle from the convergence assertions).
func setupDoVerifyConvergeTest(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	svc, game, _ := setupDoInstallTest(t)
	gameCache := svc.GetGameCache(game)

	require.NoError(t, gameCache.Store(game.ID, domain.SourceLocal, "mod1", "1.0", "a.esp", []byte("a")))
	require.NoError(t, gameCache.Store(game.ID, domain.SourceLocal, "mod1", "1.0", "gone.esp", []byte("g")))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: domain.SourceLocal, Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName: "default",
		Enabled:     true,
		FileIDs:     []string{"a.esp"},
	}))
	require.NoError(t, svc.SaveFileChecksum(domain.SourceLocal, "mod1", game.ID, "default", "a.esp", "deadbeef"))

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{
		SourceID: domain.SourceLocal, ModID: "mod1", Version: "1.0", FileIDs: []string{"a.esp"},
	}))

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: domain.SourceLocal, Version: "1.0", GameID: game.ID}, "default"))

	// gone.esp is now stale: its deployed_files row and game-dir symlink
	// survive, but the file itself is gone from the cache, so no installed
	// mod provides it any longer.
	require.NoError(t, os.Remove(gameCache.GetFilePath(game.ID, domain.SourceLocal, "mod1", "1.0", "gone.esp")))

	// A dangling symlink into the game's cache root with no DB row at all.
	cacheRoot := svc.GetGameCachePath(game)
	strayTarget := filepath.Join(cacheRoot, game.ID, "stray-src", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(strayTarget, filepath.Join(game.ModPath, "stray.pak")))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd, svc, game
}

// TestDoVerify_StaleDeployment_ReportedAsWarning guards plain `verify`
// (verifyFix=false): both convergence candidates are reported as warnings
// (STALE DEPLOYMENT in text, "stale_deployment" in --json) and counted into
// the warnings tally, and nothing on disk or in the DB is mutated - a dry
// run only reports.
func TestDoVerify_StaleDeployment_ReportedAsWarning(t *testing.T) {
	cmd, svc, game := setupDoVerifyConvergeTest(t)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "gone.esp - STALE DEPLOYMENT")
	assert.Contains(t, out, "stray.pak - STALE DEPLOYMENT")
	assert.Contains(t, out, "2 warning(s)")

	jsonOutput = true
	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues)
	assert.Equal(t, 2, result.Warnings, "both convergence candidates must be counted as warnings")

	var staleFiles []string
	for _, f := range result.Files {
		if f.Status == "stale_deployment" {
			staleFiles = append(staleFiles, f.FileID)
		}
	}
	assert.ElementsMatch(t, []string{"gone.esp", "stray.pak"}, staleFiles)

	// Dry run: nothing mutated.
	_, err := os.Lstat(filepath.Join(game.ModPath, "gone.esp"))
	assert.NoError(t, err, "plain verify must not remove the stale deployment")
	_, err = os.Lstat(filepath.Join(game.ModPath, "stray.pak"))
	assert.NoError(t, err, "plain verify must not sweep the dangling link")

	rows, err := svc.GetDeployedFilesForMod(game.ID, "default", domain.SourceLocal, "mod1")
	require.NoError(t, err)
	assert.Contains(t, rows, "gone.esp", "plain verify must not delete the stale row")
}

// TestDoVerify_Fix_StaleDeployment_RemovesAndReports guards `verify --fix`:
// both convergence candidates are actually removed (game-dir file gone,
// DB row deleted for the row case) and each removal is printed/reported as
// fixed, distinct from the plain-verify warning path above.
func TestDoVerify_Fix_StaleDeployment_RemovesAndReports(t *testing.T) {
	cmd, svc, game := setupDoVerifyConvergeTest(t)
	verifyFix = true
	t.Cleanup(func() { verifyFix = false })

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "Fixed: removed gone.esp")
	assert.Contains(t, out, "Fixed: removed stray.pak")

	_, err := os.Lstat(filepath.Join(game.ModPath, "gone.esp"))
	assert.True(t, os.IsNotExist(err), "--fix must remove the stale deployment from the game dir")
	_, err = os.Lstat(filepath.Join(game.ModPath, "stray.pak"))
	assert.True(t, os.IsNotExist(err), "--fix must sweep the dangling link")

	rows, err := svc.GetDeployedFilesForMod(game.ID, "default", domain.SourceLocal, "mod1")
	require.NoError(t, err)
	assert.NotContains(t, rows, "gone.esp", "--fix must delete the stale row")
	assert.Contains(t, rows, "a.esp", "the still-valid row must survive")
}

// TestDoVerify_Fix_StaleDeployment_JSONReportsFixed covers the --json half
// of --fix: fixed rows carry the "fixed_stale_deployment" status, and a
// clean run afterward reports zero warnings.
func TestDoVerify_Fix_StaleDeployment_JSONReportsFixed(t *testing.T) {
	cmd, svc, game := setupDoVerifyConvergeTest(t)
	verifyFix = true
	t.Cleanup(func() { verifyFix = false })

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Warnings, "a successfully fixed candidate must not be counted as an outstanding warning")

	var fixedFiles []string
	for _, f := range result.Files {
		if f.Status == "fixed_stale_deployment" {
			fixedFiles = append(fixedFiles, f.FileID)
		}
	}
	assert.ElementsMatch(t, []string{"gone.esp", "stray.pak"}, fixedFiles)
}

// TestDoVerify_Fix_StaleDeployment_SecondRunClean guards the required
// end-to-end shape from the task brief: after --fix removes both
// candidates, a second --fix run must report a clean state - nothing left
// to converge.
func TestDoVerify_Fix_StaleDeployment_SecondRunClean(t *testing.T) {
	cmd, svc, game := setupDoVerifyConvergeTest(t)
	verifyFix = true
	t.Cleanup(func() { verifyFix = false })

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	out2 := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.NotContains(t, out2, "STALE DEPLOYMENT", "a second --fix run must not re-report anything")
	assert.Contains(t, out2, "All files verified OK.", "a second --fix run must be genuinely clean")
}
