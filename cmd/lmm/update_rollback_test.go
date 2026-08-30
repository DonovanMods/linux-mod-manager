package main

// Capture tests for doUpdateRollback (Phase 6b Task 5), pinning the
// pre-extraction CLI's console output and error text BEFORE
// Service.ApplyRollback existed - see internal/core/rollback.go's
// ApplyRollback/RollbackResult/RollbackOptions doc comments for the
// extraction target, and .superpowers/sdd/task-5-report.md for the full
// before/after comparison. These reuse setupDoUpdateTest/
// seedInstalledForUpdate/captureStdout/captureStderrErr/
// captureStdoutOnlyErr (update_test.go) and installBlockingTrigger
// (deploy_test.go) - all in this same main package.
//
// TestDoUpdateRollback_Integration_AfterApplySingleUpdate (update_test.go)
// already pins the happy-path header/footer text; the tests below cover the
// guard errors and the hook/verbose-note diagnostics doUpdateRollback also
// produces.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRollbackReadyMod builds an installed, already-updated mod (mod1:
// 1.0 -> 2.0, PreviousVersion == "1.0", the 1.0 cache entry still intact)
// via a real applySingleUpdate call, ready to be rolled back - mirroring
// TestDoUpdateRollback_Integration_AfterApplySingleUpdate's own setup.
func setupRollbackReadyMod(t *testing.T) (*core.Service, *domain.Game, *domain.InstalledMod) {
	t.Helper()
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	require.NoError(t, captureStdoutOnlyErr(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	}))

	updated, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	require.Equal(t, "2.0", updated.Version)
	require.Equal(t, "1.0", updated.PreviousVersion)
	return svc, game, updated
}

// TestDoUpdateRollback_NotInstalled_ReturnsExactError guards doUpdateRollback's
// own GetInstalledMod lookup, ahead of every other guard: a mod ID absent
// from the profile must fail with the exact pre-extraction error text,
// before the header ever prints (v2 Phase 2 Unit I Task 10 characterization,
// #289 - this branch previously had no dedicated capture).
func TestDoUpdateRollback_NotInstalled_ReturnsExactError(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil
	})
	require.Error(t, callErr)
	assert.Equal(t, "mod not found: mod1", callErr.Error())
	assert.Empty(t, out, "the header must never print when this guard fails")
}

// TestDoUpdateRollback_NoPreviousVersion_ReturnsExactError guards the first
// guard doUpdateRollback checks: a mod with no PreviousVersion (never
// updated) must fail with the exact pre-extraction error text, before any
// hook, Replace, or DB write - and, crucially, BEFORE the "Rolling back..."
// header ever prints (the pre-extraction CLI checked both guards before its
// header fmt.Printf).
func TestDoUpdateRollback_NoPreviousVersion_ReturnsExactError(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	require.Empty(t, mod.PreviousVersion)

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil // capture only the printed output; assert callErr separately
	})
	require.Error(t, callErr)
	assert.Equal(t, "no previous version available for rollback", callErr.Error())
	assert.Empty(t, out, "the header must never print when this guard fails")
}

// TestDoUpdateRollback_MissingCache_ReturnsExactError guards the second
// guard: PreviousVersion is set, but its cache entry has since been
// removed - doUpdateRollback must fail with the exact pre-extraction error
// text, again before the header ever prints.
func TestDoUpdateRollback_MissingCache_ReturnsExactError(t *testing.T) {
	svc, game, _ := setupRollbackReadyMod(t)
	require.NoError(t, svc.GetGameCache(game).Delete("g1", "test-src", "mod1", "1.0"))

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil
	})
	require.Error(t, callErr)
	assert.Equal(t, "previous version 1.0 not found in cache", callErr.Error())
	assert.Empty(t, out, "the header must never print when this guard fails")
}

// TestDoUpdateRollback_Locked_RefusesBeforeHeader_Text (#143 polish): a
// locked mod's rollback must be refused by a CLI pre-check BEFORE the
// optimistic "Rolling back..." header prints (the core gate backstops this
// regardless, but fired only after the header), mirroring applySingleUpdate's
// locked pre-check: a skip with both remedy commands, not an error.
func TestDoUpdateRollback_Locked_RefusesBeforeHeader_Text(t *testing.T) {
	svc, game, _ := setupRollbackReadyMod(t)
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "2.0")

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil
	})
	require.NoError(t, callErr, "a locked rollback is a skip, like a locked single-mod update, not a failure")
	assert.NotContains(t, out, "Rolling back", "the optimistic header must never print for a refused rollback")
	// #294 (Ruling 5): the whole refused-rollback readout, byte-exact - see
	// lock_visibility_test.go's sibling capture.
	assert.Equal(t, "Rollback available: 2.0 → 1.0\nMod One is locked at v2.0 in profile default - unlock with 'lmm mod unlock -s test-src -p default mod1' first\n", out)

	updated, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version, "a locked mod must not roll back")
}

// TestDoUpdateRollback_Locked_JSON_SkippedDocument: the --json sibling — a
// locked rollback emits the single-mod document with status "skipped"/reason
// "locked" (parity with applySingleUpdate's locked skip), instead of the
// {"error": ...} shape the core gate's error used to produce.
func TestDoUpdateRollback_Locked_JSON_SkippedDocument(t *testing.T) {
	svc, game, _ := setupRollbackReadyMod(t)
	withJSONOutput(t)
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "2.0")

	out := captureStdout(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})

	var doc core.RollbackResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, "mod1", doc.Mod.ModID)
	assert.Equal(t, "test-src", doc.Mod.SourceID)
	assert.Equal(t, "1.0", doc.Mod.Version, "Mod.Version is the version rolled back TO, even on this refused-locked branch - not the lock's own version (final review, Important #4 / #302)")
	assert.Equal(t, "Mod One", doc.ModName)
	assert.Equal(t, "2.0", doc.FromVersion)
	assert.Equal(t, "1.0", doc.ToVersion)
	assert.Equal(t, "skipped", doc.Status.String())
	assert.Equal(t, "locked", doc.Reason)

	updated, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version)
}

// TestDoUpdateRollback_HookForceGate covers doUpdateRollback's Force-gated
// before_each hook checks: fatal without --force, a "Warning: ... (forced):
// ..." stderr line (and the rollback still applying) with --force.
func TestDoUpdateRollback_HookForceGate(t *testing.T) {
	t.Run("uninstall.before_each fatal without force", func(t *testing.T) {
		svc, game, _ := setupRollbackReadyMod(t)
		scriptsDir := t.TempDir()
		failScript := filepath.Join(scriptsDir, "before_each.sh")
		require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
		game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}}

		err := doUpdateRollback(context.Background(), svc, game, "mod1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_each hook failed")

		updated, gerr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "2.0", updated.Version, "a fatal before_each hook must leave the DB row untouched")
	})

	t.Run("uninstall.before_each forced prints warning and proceeds", func(t *testing.T) {
		svc, game, _ := setupRollbackReadyMod(t)
		updateForce = true
		scriptsDir := t.TempDir()
		failScript := filepath.Join(scriptsDir, "before_each.sh")
		require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
		game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}}

		stderr, err := captureStderrErr(t, func() error {
			return doUpdateRollback(context.Background(), svc, game, "mod1")
		})
		require.NoError(t, err, "a forced before_each hook failure must not abort the rollback")
		assert.Contains(t, stderr,
			"Warning: uninstall.before_each hook failed (forced): hook failed with exit code 1: "+failScript+"\n")

		updated, gerr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "1.0", updated.Version, "the rollback must still apply despite the forced hook failure")
	})

	t.Run("install.before_each fatal without force", func(t *testing.T) {
		svc, game, _ := setupRollbackReadyMod(t)
		scriptsDir := t.TempDir()
		failScript := filepath.Join(scriptsDir, "before_each.sh")
		require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
		game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeEach: failScript}}

		err := doUpdateRollback(context.Background(), svc, game, "mod1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_each hook failed")
	})

	t.Run("install.before_each forced prints warning and proceeds", func(t *testing.T) {
		svc, game, _ := setupRollbackReadyMod(t)
		updateForce = true
		scriptsDir := t.TempDir()
		failScript := filepath.Join(scriptsDir, "before_each.sh")
		require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
		game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeEach: failScript}}

		stderr, err := captureStderrErr(t, func() error {
			return doUpdateRollback(context.Background(), svc, game, "mod1")
		})
		require.NoError(t, err)
		assert.Contains(t, stderr,
			"Warning: install.before_each hook failed (forced): hook failed with exit code 1: "+failScript+"\n")
	})
}

// TestDoUpdateRollback_AfterEachHookFailures_PrintWarningsInOrderAndSucceed
// covers doUpdateRollback's two always-non-fatal after_each hooks: both
// failures print a "Warning: ... hook failed: ..." stderr line, in
// hook-run order (uninstall.after_each, then install.after_each), and the
// rollback still applies end to end.
func TestDoUpdateRollback_AfterEachHookFailures_PrintWarningsInOrderAndSucceed(t *testing.T) {
	svc, game, _ := setupRollbackReadyMod(t)
	updateForce = false // after_each hooks are non-fatal even without --force

	scriptsDir := t.TempDir()
	uninstallScript := filepath.Join(scriptsDir, "uninstall_after_each.sh")
	installScript := filepath.Join(scriptsDir, "install_after_each.sh")
	require.NoError(t, os.WriteFile(uninstallScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	require.NoError(t, os.WriteFile(installScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{
		Uninstall: domain.HookConfig{AfterEach: uninstallScript},
		Install:   domain.HookConfig{AfterEach: installScript},
	}

	stderr, err := captureStderrErr(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})
	require.NoError(t, err, "after_each hook failures must never fail the rollback")

	uIdx := strings.Index(stderr, "Warning: uninstall.after_each hook failed: hook failed with exit code 1: "+uninstallScript)
	iIdx := strings.Index(stderr, "Warning: install.after_each hook failed: hook failed with exit code 1: "+installScript)
	require.GreaterOrEqual(t, uIdx, 0, "uninstall.after_each warning must be present")
	require.GreaterOrEqual(t, iIdx, 0, "install.after_each warning must be present")
	assert.Less(t, uIdx, iIdx, "uninstall.after_each warning must print before install.after_each")

	updated, gerr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, gerr)
	assert.Equal(t, "1.0", updated.Version, "the rollback itself must still have applied")
}

// TestDoUpdateRollback_VerboseGatedLinkMethodNote covers the sole
// --verbose-gated diagnostic in doUpdateRollback: a failed
// SetModLinkMethod prints "  Warning: could not update link method: %v\n"
// to stdout ONLY under --verbose, and never fails the rollback either way.
// SetModLinkMethod is forced to fail deterministically via
// installBlockingTrigger (deploy_test.go), which only blocks the
// link_method/deployed columns - narrow enough that the rest of the
// rollback (including the version-swap DB write, a different column)
// still succeeds normally.
func TestDoUpdateRollback_VerboseGatedLinkMethodNote(t *testing.T) {
	t.Run("verbose prints the note", func(t *testing.T) {
		svc, game, _ := setupRollbackReadyMod(t)
		installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))
		verbose = true

		out := captureStdout(t, func() error {
			return doUpdateRollback(context.Background(), svc, game, "mod1")
		})
		assert.Contains(t, out, "\n✓ Rolled back: Mod One 2.0 → 1.0\n", "a non-fatal link-method failure must not abort the rollback")
		assert.Contains(t, out, "  Warning: could not update link method:")
	})

	t.Run("non-verbose omits the note", func(t *testing.T) {
		svc, game, _ := setupRollbackReadyMod(t)
		installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))
		verbose = false

		out := captureStdout(t, func() error {
			return doUpdateRollback(context.Background(), svc, game, "mod1")
		})
		assert.Contains(t, out, "\n✓ Rolled back: Mod One 2.0 → 1.0\n")
		assert.NotContains(t, out, "link method", "the note must be --verbose-gated")
	})
}
