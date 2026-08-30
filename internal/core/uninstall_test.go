package core_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// --- UninstallMod ---

// seedProfileWithMod creates profileName (if needed) and adds a reference to
// source/mod/version, mirroring the state left behind by a real install.
func seedProfileWithMod(t *testing.T, svc *core.Service, gameID, profileName, sourceID, modID, version string) {
	t.Helper()
	pm := svc.NewProfileManager()
	if _, err := pm.Get(context.Background(), gameID, profileName); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(context.Background(), gameID, profileName)
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(context.Background(), gameID, profileName, domain.ModReference{SourceID: sourceID, ModID: modID, Version: version}))
}

// seedHooks sets game's hooks and persists them (mirroring a real games.yaml
// edit) so a flow's internal hook resolution (Service.resolvedHooks/
// hookRunner, hooks_resolve.go) picks them up - the Task 2 (#286)
// replacement for setting core.XOptions.Hooks/HookRunner/HookContext
// directly, now that those fields no longer exist. The blanked profileName
// param is intentionally VESTIGIAL (#286 review Minor 1) - kept only so
// every call site symmetrically names the profile the caller is about to
// exercise; profile-level hook overrides are exercised directly by
// TestResolvedHooks_MergesGameAndProfile (hooks_resolve_test.go) and
// ResolveHooks's own tests (hooks_test.go), not by these flow tests.
func seedHooks(t *testing.T, svc *core.Service, game *domain.Game, _ string, hooks domain.GameHooks) {
	t.Helper()
	game.Hooks = hooks
	require.NoError(t, svc.SaveGame(context.Background(), game))
}

// installBlockingTrigger opens a second connection to the SQLite file at
// dbPath and installs a trigger that makes any UPDATE touching
// installed_mods.link_method or installed_mods.deployed fail - used to
// deterministically force SetModLinkMethod/SetModDeployed to error without
// affecting any other table or column (see the technique note on
// TestService_DeployProfile_PerModNoteDiagnostics_CarryModAttributionAndPrecedeSuccessEvent).
// Must be called after the *core.Service that owns dbPath has already run
// its migrations (so the installed_mods table exists).
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

func TestService_UninstallMod_FullUninstall(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "deployed file should be undeployed")

	assert.False(t, svc.GetGameCache(game).Exists("g1", "src", "1", "1.0"), "cache entry should be deleted")

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should be removed")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "profile should no longer list the mod")
}

func TestService_UninstallMod_KeepCache(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "deployed file should still be undeployed")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "1", "1.0"), "cache entry must survive with KeepCache")

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should still be removed")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "profile should still no longer list the mod")
}

// TestService_UninstallMod_HookOrder proves before_each runs before the
// mod's files are undeployed, and after_each runs after the mod has been
// removed from the profile (mirroring doUninstall's step ordering:
// hooks -> undeploy -> cache delete -> DB delete -> profile remove -> after
// hooks).
func TestService_UninstallMod_HookOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	deployedFile := filepath.Join(gameDir, "plugin.esp")
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	callLog := filepath.Join(scriptsDir, "calls.log")

	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ -e `+deployedFile+` ]; then
  echo "before_each:deployed" >> `+callLog+`
else
  echo "before_each:undeployed" >> `+callLog+`
fi
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
if grep -q mod_id `+profilePath+` 2>/dev/null; then
  echo "after_each:still_in_profile" >> `+callLog+`
else
  echo "after_each:removed_from_profile" >> `+callLog+`
fi
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all" >> `+callLog+`
exit 0`)

	seedHooks(t, svc, game, "default", domain.GameHooks{
		Uninstall: domain.HookConfig{
			BeforeAll:  beforeAllScript,
			BeforeEach: beforeEachScript,
			AfterEach:  afterEachScript,
			AfterAll:   afterAllScript,
		},
	})

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	expectedLog := "before_all\nbefore_each:deployed\nafter_each:removed_from_profile\nafter_all\n"
	assert.Equal(t, expectedLog, string(logContent))
}

// TestService_UninstallMod_BeforeEachHookFails_AbortsUnlessForce guards the
// fatal-by-default hook semantics of the pre-extraction doUninstall: a
// failing uninstall.before_* hook aborts the whole operation (nothing is
// undeployed, uninstalled, or removed from the DB/profile) unless Force is
// set, in which case the failure becomes a warning and the uninstall
// proceeds.
func TestService_UninstallMod_BeforeEachHookFails_AbortsUnlessForce(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	failScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}})

	t.Run("fatal without Force", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_each hook failed")
		require.NotNil(t, result, "the (empty) result must still be returned alongside a fatal error, not discarded")
		assert.Empty(t, result.Warnings)
		assert.Empty(t, result.Notes)

		// Nothing should have changed: mod still installed, file still deployed.
		_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
		assert.NoError(t, err, "deployed file must survive a fatal before_each failure")
		_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
		assert.NoError(t, err, "DB row must survive a fatal before_each failure")
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
			Force: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_each hook failed")
		assert.Contains(t, result.Warnings[0], "forced")

		_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
		assert.ErrorIs(t, err, domain.ErrModNotFound, "forced uninstall must still remove the DB row")
	})
}

// TestService_UninstallMod_BeforeAllHookFails_AbortsUnlessForce mirrors
// TestService_UninstallMod_BeforeEachHookFails_AbortsUnlessForce for the
// uninstall.before_all branch, which is a separate hand-duplicated code path
// in UninstallMod (see the review that flagged it as untested).
func TestService_UninstallMod_BeforeAllHookFails_AbortsUnlessForce(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}})

	t.Run("fatal without Force", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_all hook failed")
		require.NotNil(t, result, "the (empty) result must still be returned alongside a fatal error, not discarded")
		assert.Empty(t, result.Warnings)
		assert.Empty(t, result.Notes)

		// Nothing should have changed: mod still installed, file still deployed.
		_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
		assert.NoError(t, err, "deployed file must survive a fatal before_all failure")
		_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
		assert.NoError(t, err, "DB row must survive a fatal before_all failure")
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
			Force: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_all hook failed")
		assert.Contains(t, result.Warnings[0], "forced")

		_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
		assert.ErrorIs(t, err, domain.ErrModNotFound, "forced uninstall must still remove the DB row")
	})
}

// TestService_UninstallMod_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult
// guards the error-path convention amendment flagged by the Task 2 review:
// once the result struct exists, every fatal return must carry the
// partially-populated result instead of discarding it (see
// UninstallResult's doc comment), so the CLI can still surface diagnostics
// that already "happened" before the fatal error hit.
//
// DeleteInstalledMod is the only fatal step in UninstallMod that can be
// reached *after* a diagnostic has already been recorded: before_all/
// before_each are fatal-by-default (nothing accumulated yet when they
// abort) unless Force is set, in which case their failures become Warnings
// and execution continues - and DeleteInstalledMod is the sole remaining
// fatal step downstream of that. This test forces it to fail by holding a
// real write lock on the same SQLite file - a dedicated second connection
// issues "BEGIN IMMEDIATE" directly (a plain sql.Tx's default deferred
// BEGIN does NOT take a lock until its first statement runs, so it doesn't
// work here) and never commits, for the call's duration. WAL-mode readers
// (GetInstalledMod) proceed unaffected, but the writer (DeleteInstalledMod)
// deterministically gets SQLITE_BUSY (busy_timeout defaults to 0, so it's
// an immediate error, not a timing-dependent race).
func TestService_UninstallMod_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	failScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}})

	dbPath := filepath.Join(dataDir, "lmm.db")
	locker, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer locker.Close() //nolint:errcheck // best-effort cleanup
	locker.SetMaxOpenConns(1)
	conn, err := locker.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck // best-effort cleanup
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	defer conn.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck // best-effort cleanup

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
		Force: true,
	})
	require.Error(t, err, "DeleteInstalledMod must fail while another writer holds the file lock")
	assert.Contains(t, err.Error(), "failed to remove mod record")
	require.NotNil(t, result, "the result accumulated before the fatal error must not be discarded")
	require.Len(t, result.Warnings, 1, "the forced before_each hook failure must have been recorded before the later fatal error")
	assert.Contains(t, result.Warnings[0], "uninstall.before_each hook failed")
	assert.Contains(t, result.Warnings[0], "forced")
}

func TestService_UninstallMod_UnknownModReturnsErrModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "missing", core.UninstallOptions{})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestService_UninstallMod_ProfileDesyncWarnsAndContinues guards the
// "don't fail if not in profile" partial-failure path from the
// pre-extraction doUninstall: if the DB row exists but the profile can't be
// updated (e.g. no profile file, or the mod isn't listed in it), that is
// recorded as a Note (not a Warning, and not a fatal error) - the DB row is
// still removed. UninstallMod records this unconditionally: there is no
// verbosity concept in core (UninstallOptions has no Verbose field) - the
// CLI is solely responsible for deciding whether to display it, under
// --verbose, matching the pre-extraction CLI's "Note: %v" (gated) print.
func TestService_UninstallMod_ProfileDesyncWarnsAndContinues(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// DB row + cache exist, but no profile was ever created for "default".
	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err, "a profile-removal failure must not fail the uninstall")
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings, "the profile-removal diagnostic must not leak into Warnings")
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Note: "), "must carry its historical CLI prefix: %q", result.Notes[0])
	assert.Contains(t, result.Notes[0], domain.ErrProfileNotFound.Error())

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should still be removed despite the profile note")
}

// TestService_UninstallMod_UndeployFailure_RecordedAsNoteWithHistoricalPrefix
// guards the exact text (including its historical "Warning: " prefix) of the
// undeploy-failure diagnostic. A regular file sits where the symlink linker
// expects its own link, so linker.Undeploy fails deterministically ("not a
// symlink") without relying on filesystem permissions. (An absent cache
// entry no longer works as the failure fixture here: since #260 that is a
// documented no-op, not an error.) The profile is pre-seeded so profile
// removal succeeds silently, isolating this one diagnostic.
func TestService_UninstallMod_UndeployFailure_RecordedAsNoteWithHistoricalPrefix(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	// A foreign regular file at the deploy destination: Undeploy refuses to
	// remove anything that is not its own symlink.
	require.NoError(t, os.WriteFile(filepath.Join(gameDir, "plugin.esp"), []byte("not a symlink"), 0644))

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err, "an undeploy failure must not fail the uninstall")
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings, "the undeploy diagnostic must not leak into Warnings")
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to undeploy some files: "), "must carry its historical CLI prefix: %q", result.Notes[0])

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should still be removed despite the undeploy note")
}

// TestService_UninstallMod_UndeployAndCacheDeleteFailures_RecordedAsNotesWithHistoricalPrefixes
// guards the exact text (including historical "Warning: " prefixes) of the
// undeploy-failure and cache-delete-failure diagnostics together. Both
// Installer.Uninstall (via cache.ListFiles) and Cache.Delete (via
// os.RemoveAll) resolve the identical on-disk mod path, so a single
// structural obstruction - a regular file in place of the mod's cache
// directory - deterministically fails both without relying on filesystem
// permissions (unlike a read-only directory, this also fails when tests run
// as root).
func TestService_UninstallMod_UndeployAndCacheDeleteFailures_RecordedAsNotesWithHistoricalPrefixes(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// No cache files stored (files: nil); the mod's cache directory is
	// created below only as a blocking regular file, never a real directory.
	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	modPath := svc.GetGameCache(game).ModPath("g1", "src", "1", "1.0")
	blockedParent := filepath.Dir(modPath) // .../g1/src-1, normally a directory
	require.NoError(t, os.MkdirAll(filepath.Dir(blockedParent), 0755))
	require.NoError(t, os.WriteFile(blockedParent, []byte("blocked"), 0644))

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err, "undeploy and cache-delete failures must not fail the uninstall")
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings, "operational diagnostics must not leak into Warnings")
	require.Len(t, result.Notes, 2)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to undeploy some files: "), "note[0] must carry its historical CLI prefix: %q", result.Notes[0])
	assert.True(t, strings.HasPrefix(result.Notes[1], "Warning: failed to clean cache: "), "note[1] must carry its historical CLI prefix: %q", result.Notes[1])

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should still be removed despite the failures")
}

// TestService_UninstallMod_AfterEachHookFailure_IsNonFatalWarning guards
// that after_each/after_all hook failures never fail the uninstall (they
// run after every other step has already committed) and are always
// recorded in Warnings, matching the pre-extraction CLI's unconditional
// printHookWarnings behavior. Unlike Notes, Warnings entries are printed by
// the CLI unconditionally (regardless of --verbose).
func TestService_UninstallMod_AfterEachHookFailure_IsNonFatalWarning(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	failScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{AfterEach: failScript}})

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err, "after_each failures must not fail UninstallMod")
	require.NotNil(t, result)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed")

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should already be removed by the time after_each runs")
}
