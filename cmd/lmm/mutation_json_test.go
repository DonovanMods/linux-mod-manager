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
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"

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
