package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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

// installBlockingTrigger opens a second connection to the SQLite file at
// dbPath and installs a trigger that makes any UPDATE touching
// installed_mods.link_method or installed_mods.deployed fail - used to
// deterministically force SetModLinkMethod/SetModDeployed to error without
// affecting any other table or column. Must be called after the
// *core.Service that owns dbPath has already run its migrations (so the
// installed_mods table exists). Mirrors the identically-named helper in
// internal/core/flows_test.go.
func installBlockingTrigger(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_link_method_and_deployed_updates
		BEFORE UPDATE OF link_method, deployed ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
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
	assert.Equal(t, 1, strings.Count(out, "Warning: undeploy Test Mod:"), "must print exactly once, not double-printed via both an inline event and the end-of-run batch")
	assert.Less(t, strings.Index(out, "Warning: undeploy Test Mod:"), strings.Index(out, "✓ Test Mod"),
		"the undeploy warning must print inline, immediately before THIS mod's own success line - not batched at the end of the run (review finding 3)")
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

// --- Fix wave 1: console-output positioning (review findings) ---

// captureCombined redirects both os.Stdout and os.Stderr to the same pipe
// for the duration of fn, preserving the relative order writes to either
// stream occurred in - necessary to assert cross-stream ordering (e.g.
// "does the forced before_all warning on stderr print before the header on
// stdout"), which captureStdout/captureStderrErr can't do since each only
// captures one stream. fn is expected to succeed; use captureStdout/
// captureStderrErr directly for error-path assertions.
func captureCombined(t *testing.T, fn func() error) string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = w, w
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	fnErr := fn()
	require.NoError(t, w.Close(), "closing write end of the pipe")
	out, readErr := io.ReadAll(r)
	require.NoError(t, r.Close())

	require.NoError(t, fnErr)
	require.NoError(t, readErr)
	return string(out)
}

// allIndexes returns the start index of every non-overlapping occurrence of
// substr in s.
func allIndexes(s, substr string) []int {
	var idxs []int
	for offset := 0; ; {
		i := strings.Index(s[offset:], substr)
		if i == -1 {
			return idxs
		}
		idxs = append(idxs, offset+i)
		offset += i + len(substr)
	}
}

// TestDoDeploy_ForcedBeforeAllWarning_PrintsBeforeDeployHeader guards review
// finding 1 (deploy side): a forced install.before_all hook failure must be
// the FIRST thing doDeploy prints, before the "Deploying N mod(s)..."
// header - it was the first line of output in the pre-extraction CLI
// (git show 45470e8:cmd/lmm/deploy.go). Printed unconditionally (no
// --verbose needed), matching the Warnings display contract.
func TestDoDeploy_ForcedBeforeAllWarning_PrintsBeforeDeployHeader(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_all.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}}

	oldForce := deployForce
	deployForce = true
	t.Cleanup(func() { deployForce = oldForce })

	out := captureCombined(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	warnIdx := strings.Index(out, "Warning: install.before_all hook failed (forced): ")
	headerIdx := strings.Index(out, "Deploying 1 mod(s) using symlink...")
	require.NotEqual(t, -1, warnIdx, "missing forced before_all warning; got:\n%s", out)
	require.NotEqual(t, -1, headerIdx, "missing deploy header; got:\n%s", out)
	assert.Less(t, warnIdx, headerIdx, "the forced before_all warning must print before the deploy header")
}

// TestDoDeploy_ForcedPurgeBeforeAllWarning_PrintsBeforePurgeHeader guards
// review finding 1 (purge side): a forced uninstall.before_all hook failure
// during --purge must print before the "Purging N mod(s) before
// deploy..." header, mirroring the pre-extraction purgeDeployedMods
// (git show 45470e8:cmd/lmm/purge.go).
func TestDoDeploy_ForcedPurgeBeforeAllWarning_PrintsBeforePurgeHeader(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_all.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}

	oldPurge, oldForce := deployPurge, deployForce
	deployPurge = true
	deployForce = true
	t.Cleanup(func() { deployPurge, deployForce = oldPurge, oldForce })

	out := captureCombined(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	warnIdx := strings.Index(out, "Warning: uninstall.before_all hook failed (forced): ")
	purgeHeaderIdx := strings.Index(out, "Purging 1 mod(s) before deploy...")
	require.NotEqual(t, -1, warnIdx, "missing forced purge before_all warning; got:\n%s", out)
	require.NotEqual(t, -1, purgeHeaderIdx, "missing purge header; got:\n%s", out)
	assert.Less(t, warnIdx, purgeHeaderIdx, "the forced purge before_all warning must print before the purge header")
}

// TestDoDeploy_Verbose_LinkMethodAndMarkDeployedWarnings_PrintAdjacentToTheirMod
// guards review finding 3's other two deploy-loop diagnostics: a failed
// SetModLinkMethod and a failed SetModDeployed both produce text with NO
// mod identity in it ("Warning: could not update link method: ..."), so
// console position - printed immediately before THAT mod's own "✓" line -
// was the ONLY attribution mechanism pre-extraction. Two mods are seeded so
// a batched (mis-attributed) implementation is distinguishable from a
// correctly interleaved one.
//
// SetModLinkMethod/SetModDeployed are plain UPDATEs against installed_mods,
// but Install/Uninstall also write to the DB (deployed_files) - so a
// blanket write-lock would fail Install itself before ever reaching
// SetModLinkMethod/SetModDeployed, defeating the test. Instead, a second
// connection installs a real SQLite trigger that aborts ONLY updates to
// installed_mods' link_method/deployed columns (see installBlockingTrigger),
// leaving deployed_files and every other installed_mods column untouched -
// deterministic, and narrow enough that Install/Uninstall still succeed.
func TestDoDeploy_Verbose_LinkMethodAndMarkDeployedWarnings_PrintAdjacentToTheirMod(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")

	oldVerbose := verbose
	verbose = true
	t.Cleanup(func() { verbose = oldVerbose })

	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	modAIdx := strings.Index(out, "✓ Mod A")
	modBIdx := strings.Index(out, "✓ Mod B")
	require.NotEqual(t, -1, modAIdx, "missing Mod A success line; got:\n%s", out)
	require.NotEqual(t, -1, modBIdx, "missing Mod B success line; got:\n%s", out)

	linkWarnings := allIndexes(out, "Warning: could not update link method:")
	deployedWarnings := allIndexes(out, "Warning: could not mark as deployed:")
	require.Len(t, linkWarnings, 2, "one link-method warning per mod; got:\n%s", out)
	require.Len(t, deployedWarnings, 2, "one mark-deployed warning per mod; got:\n%s", out)

	// Mod A's pair must both precede "✓ Mod A"; Mod B's pair must both fall
	// strictly between "✓ Mod A" and "✓ Mod B" - proof they're interleaved
	// per-mod, not batched at the end (which would put all four warnings
	// after both ✓ lines, and be indistinguishable from each other).
	assert.Less(t, linkWarnings[0], modAIdx)
	assert.Less(t, deployedWarnings[0], modAIdx)
	assert.Greater(t, linkWarnings[1], modAIdx)
	assert.Less(t, linkWarnings[1], modBIdx)
	assert.Greater(t, deployedWarnings[1], modAIdx)
	assert.Less(t, deployedWarnings[1], modBIdx)
}

// TestDoDeploy_OverridesWarning_PrintsBeforeAfterEachAfterAllHookWarnings
// guards review finding 2: the pre-extraction CLI printed the
// profile-overrides warning before its batched after_each/after_all hook
// warnings, even though (both pre- and post-extraction) after_each/
// after_all are computed earlier in the function than the overrides check -
// the overrides warning was printed immediately when computed, while the
// hook warnings were accumulated and only printed afterward via
// printHookWarnings. All three land on stderr, unconditionally.
func TestDoDeploy_OverridesWarning_PrintsBeforeAfterEachAfterAllHookWarnings(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	pm := svc.NewProfileManager()
	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	// An absolute override path is rejected by ApplyProfileOverrides
	// deterministically - no filesystem trickery required.
	profile.Overrides = map[string][]byte{"/etc/passwd": []byte("x")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	scriptsDir := t.TempDir()
	afterEachScript := filepath.Join(scriptsDir, "after_each.sh")
	require.NoError(t, os.WriteFile(afterEachScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	afterAllScript := filepath.Join(scriptsDir, "after_all.sh")
	require.NoError(t, os.WriteFile(afterAllScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterEach: afterEachScript, AfterAll: afterAllScript}}

	stderr, cmdErr := captureStderrErr(t, func() error {
		captureStdout(t, func() error { //nolint:errcheck // stdout content unused; the deploy itself must still succeed
			return doDeploy(context.Background(), svc, game, nil)
		})
		return nil
	})
	require.NoError(t, cmdErr)

	overridesIdx := strings.Index(stderr, "Warning: applying profile overrides:")
	afterEachIdx := strings.Index(stderr, "Warning: install.after_each hook failed")
	afterAllIdx := strings.Index(stderr, "Warning: install.after_all hook failed")
	require.NotEqual(t, -1, overridesIdx, "missing overrides warning; got:\n%s", stderr)
	require.NotEqual(t, -1, afterEachIdx, "missing after_each warning; got:\n%s", stderr)
	require.NotEqual(t, -1, afterAllIdx, "missing after_all warning; got:\n%s", stderr)
	assert.Less(t, overridesIdx, afterEachIdx, "overrides warning must print before the after_each hook warning")
	assert.Less(t, overridesIdx, afterAllIdx, "overrides warning must print before the after_all hook warning")
}

// TestDoDeploy_Verbose_PurgeBlankLine_AppearsAfterInlineDiagnosticsNotImmediatelyAfterHeader
// guards review finding 4: the pre-extraction purgeDeployedMods printed its
// closing blank line at the END of the purge phase (after any inline
// purge diagnostics), not immediately after the "Purging N mod(s) before
// deploy..." header. Corrupts a previously-deployed symlink into a plain
// file so purge's own Uninstall call fails deterministically ("not a
// symlink"), producing an inline --verbose purge diagnostic to place the
// blank line after.
func TestDoDeploy_Verbose_PurgeBlankLine_AppearsAfterInlineDiagnosticsNotImmediatelyAfterHeader(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	require.NoError(t, doDeploy(context.Background(), svc, game, nil))
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	oldPurge, oldVerbose := deployPurge, verbose
	deployPurge = true
	verbose = true
	t.Cleanup(func() { deployPurge, verbose = oldPurge, oldVerbose })

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "Purging 1 mod(s) before deploy...\n\n",
		"no blank line may appear immediately after the purge header - it belongs after any inline purge diagnostics")

	purgeHeaderIdx := strings.Index(out, "Purging 1 mod(s) before deploy...")
	diagIdx := strings.Index(out, "⚠ Test Mod")
	deployHeaderIdx := strings.Index(out, "Deploying 1 mod(s) using symlink...")
	require.NotEqual(t, -1, purgeHeaderIdx, "missing purge header; got:\n%s", out)
	require.NotEqual(t, -1, diagIdx, "missing inline purge diagnostic; got:\n%s", out)
	require.NotEqual(t, -1, deployHeaderIdx, "missing deploy header; got:\n%s", out)
	require.Less(t, purgeHeaderIdx, diagIdx)
	require.Less(t, diagIdx, deployHeaderIdx)

	between := out[diagIdx:deployHeaderIdx]
	assert.Contains(t, between, "\n\n", "the blank line must appear after the inline purge diagnostic, before the deploy header")
}

// TestDoDeploy_MissingCacheRedownloadSuccess_PrintsBlankLineAfterDownload
// guards finding F2 (CLI parity regression): the pre-extraction CLI printed
// an unconditional `fmt.Println() // Clear progress line` immediately after
// a cache-miss mod's redownload loop, on the success path (git show
// b2ad559:cmd/lmm/deploy.go) - a blank line between "cache missing,
// re-downloading..." and the mod's own "✓" success line. The extracted
// flow (internal/core/flows.go's redeployFromSource) emits no event for
// this on success, so doDeploy has nothing to print it from. A real custom
// manifest source served over httptest provides a working redownload
// (mockSourceWithDownloads/createTestZip are internal/core-only test
// helpers, not available to cmd/lmm - mirrors
// TestDoProfileSwitch_ProceedAccepted_HappyPath_PrintsExpectedOutput's
// pattern in profile_test.go), so the mod actually reaches its "✓" line
// instead of being skipped.
func TestDoDeploy_MissingCacheRedownloadSuccess_PrintsBlankLineAfterDownload(t *testing.T) {
	svc, game := setupDoDeployTest(t)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: redl
    name: Redownload Mod
    version: "1.0"
    summary: A mod to redownload
    files:
      - id: main
        filename: redownload.dat
        version: "1.0"
        url: %s/files/redownload.dat
        primary: true
`, srv.URL)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("archive bytes")) })
	src, err := custom.New(custom.SourceDefinition{
		ID:        "e2e-repo",
		Name:      "E2E Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true,
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)

	// InstalledMod row exists (enabled, in the profile) but nothing was
	// ever stored in the cache - forces DeployProfile's redownload branch.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "redl", SourceID: "e2e-repo", Name: "Redownload Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "e2e-repo", ModID: "redl", Version: "1.0"}))

	oldVerbose := verbose
	verbose = false
	t.Cleanup(func() { verbose = oldVerbose })

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	redownloadIdx := strings.Index(out, "cache missing, re-downloading...")
	successIdx := strings.Index(out, "✓ Redownload Mod")
	require.NotEqual(t, -1, redownloadIdx, "missing redownload diagnostic; got:\n%s", out)
	require.NotEqual(t, -1, successIdx, "mod must actually redeploy successfully; got:\n%s", out)
	require.Less(t, redownloadIdx, successIdx)

	// The source reports Content-Length, so the download's progress readout
	// prints via a bare '\r' (no trailing newline - core.DeployDownloading's
	// handler above). The pre-extraction CLI's unconditional
	// `fmt.Println() // Clear progress line` after the download loop (git
	// show b2ad559:cmd/lmm/deploy.go) is what terminates that line with a
	// real '\n' before the mod's own "✓" success line prints - without it,
	// the percent readout and "✓ Redownload Mod" run together on one
	// physical line.
	assert.Contains(t, out, "\n  ✓ Redownload Mod", "the mod's success line must start on its own line, not run together with the redownload's progress readout")

	_, err = os.Lstat(filepath.Join(game.ModPath, "redownload.dat"))
	assert.NoError(t, err, "the redownloaded mod must still be deployed")
}
