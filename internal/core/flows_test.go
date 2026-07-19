package core_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// newFlowsTestService returns a *core.Service backed by fresh temp dirs
// (config/data/cache), matching the construction pattern used throughout
// service_test.go.
func newFlowsTestService(t *testing.T) *core.Service {
	t.Helper()
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})
	return svc
}

// seedInstalledMod stores the given files in the game's cache (when files is
// non-nil) and saves an InstalledMod DB record for source/mod/version with
// the requested Enabled state.
func seedInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     "Test Mod",
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// seedNamedInstalledMod is seedInstalledMod with a caller-supplied Name,
// needed whenever a test must tell mods apart by name (seedInstalledMod
// hardcodes "Test Mod" for every mod, which is fine for single-mod tests but
// useless for asserting deploy order or per-mod skip/progress identity).
func seedNamedInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     name,
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// --- EnableMod ---

func TestService_EnableMod_DeploysDisabledMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	changed, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	assert.True(t, changed)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "plugin.esp should be deployed to the game dir")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
}

func TestService_EnableMod_AlreadyEnabledIsNoop(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	changed, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	assert.False(t, changed)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "no-op enable must not deploy files")
}

func TestService_EnableMod_MissingCacheErrors(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// Installed-mod record exists, but nothing was ever stored in the cache.
	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, nil)

	changed, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "not found in cache")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "DB must remain untouched when cache is missing")
}

func TestService_EnableMod_DeployFailurePropagatesAndLeavesDBUntouched(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{
		"blocked/plugin.esp": []byte("data"),
	})

	// Block deployment deterministically (not permission-based, so this is
	// stable under both root and non-root test runners): "blocked" already
	// exists as a regular file, so the linker's os.MkdirAll(filepath.Dir(dst))
	// fails, exercising the same failure family as
	// TestInstaller_Install_DeployFailureRollsBackAndClearsDB.
	require.NoError(t, os.WriteFile(filepath.Join(gameDir, "blocked"), []byte("occupied"), 0644))

	changed, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.Error(t, err)
	assert.False(t, changed)
	assert.Contains(t, err.Error(), "failed to deploy mod")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "DB must remain untouched on deploy failure")
}

func TestService_EnableMod_UnknownModReturnsErrModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	changed, err := svc.EnableMod(context.Background(), game, "default", "src", "missing")
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// --- DisableMod ---

func TestService_DisableMod_UndeploysEnabledMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	// Deploy the files first so there's something to undeploy (mirrors an
	// install that happened earlier).
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	changed, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	assert.True(t, changed)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "plugin.esp should be removed from the game dir")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "1", "1.0"), "cache entry must be preserved")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)
}

func TestService_DisableMod_AlreadyDisabledIsNoop(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	changed, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestService_DisableMod_UnknownModReturnsErrModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	changed, err := svc.DisableMod(context.Background(), game, "default", "src", "missing")
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestService_DisableMod_UndeployFailureIsNonFatal guards a deliberate
// behavior-preservation decision (documented in the task report): the
// pre-extraction CLI (doModDisable) treated Uninstall failures as non-fatal
// ("warn but continue" under --verbose) because files may already have been
// removed manually. DisableMod preserves the *functional* outcome — DB still
// flips to disabled — even when undeploying fails.
func TestService_DisableMod_UndeployFailureIsNonFatal(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// Corrupt the deployed file into a plain file (not a symlink) so the
	// symlink linker's Undeploy fails deterministically ("not a symlink").
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	changed, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err, "undeploy failures must not fail DisableMod")
	assert.True(t, changed)

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "DB should still flip to disabled even when undeploy is best-effort")
}

// --- UninstallMod ---

// seedProfileWithMod creates profileName (if needed) and adds a reference to
// source/mod/version, mirroring the state left behind by a real install.
func seedProfileWithMod(t *testing.T, svc *core.Service, gameID, profileName, sourceID, modID, version string) {
	t.Helper()
	pm := svc.NewProfileManager()
	if _, err := pm.Get(gameID, profileName); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(gameID, profileName)
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(gameID, profileName, domain.ModReference{SourceID: sourceID, ModID: modID, Version: version}))
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

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should be removed")

	profile, err := svc.NewProfileManager().Get("g1", "default")
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

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should still be removed")

	profile, err := svc.NewProfileManager().Get("g1", "default")
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

	hooks := &core.ResolvedHooks{
		Uninstall: domain.HookConfig{
			BeforeAll:  beforeAllScript,
			BeforeEach: beforeEachScript,
			AfterEach:  afterEachScript,
			AfterAll:   afterAllScript,
		},
	}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: core.HookContext{GameID: game.ID, GamePath: game.InstallPath, ModPath: game.ModPath},
	})
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	t.Run("fatal without Force", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
			Hooks:      hooks,
			HookRunner: runner,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_each hook failed")
		require.NotNil(t, result, "the (empty) result must still be returned alongside a fatal error, not discarded")
		assert.Empty(t, result.Warnings)
		assert.Empty(t, result.Notes)

		// Nothing should have changed: mod still installed, file still deployed.
		_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
		assert.NoError(t, err, "deployed file must survive a fatal before_each failure")
		_, err = svc.GetInstalledMod("src", "1", "g1", "default")
		assert.NoError(t, err, "DB row must survive a fatal before_each failure")
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
			Hooks:      hooks,
			HookRunner: runner,
			Force:      true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_each hook failed")
		assert.Contains(t, result.Warnings[0], "forced")

		_, err = svc.GetInstalledMod("src", "1", "g1", "default")
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	t.Run("fatal without Force", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
			Hooks:      hooks,
			HookRunner: runner,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_all hook failed")
		require.NotNil(t, result, "the (empty) result must still be returned alongside a fatal error, not discarded")
		assert.Empty(t, result.Warnings)
		assert.Empty(t, result.Notes)

		// Nothing should have changed: mod still installed, file still deployed.
		_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
		assert.NoError(t, err, "deployed file must survive a fatal before_all failure")
		_, err = svc.GetInstalledMod("src", "1", "g1", "default")
		assert.NoError(t, err, "DB row must survive a fatal before_all failure")
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
			Hooks:      hooks,
			HookRunner: runner,
			Force:      true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_all hook failed")
		assert.Contains(t, result.Warnings[0], "forced")

		_, err = svc.GetInstalledMod("src", "1", "g1", "default")
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	dbPath := filepath.Join(dataDir, "lmm.db")
	locker, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer locker.Close()
	locker.SetMaxOpenConns(1)
	conn, err := locker.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	defer conn.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck // best-effort cleanup

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
		Hooks:      hooks,
		HookRunner: runner,
		Force:      true,
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

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should still be removed despite the profile note")
}

// TestService_UninstallMod_UndeployFailure_RecordedAsNoteWithHistoricalPrefix
// guards the exact text (including its historical "Warning: " prefix) of the
// undeploy-failure diagnostic. The mod is never actually cached, so
// Installer.Uninstall's cache.ListFiles call fails deterministically
// (directory does not exist) without relying on filesystem permissions. The
// profile is pre-seeded so profile removal succeeds silently, isolating this
// one diagnostic.
func TestService_UninstallMod_UndeployFailure_RecordedAsNoteWithHistoricalPrefix(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// No cache files stored (files: nil) - Installer.Uninstall's
	// cache.ListFiles call fails because the mod's cache directory was
	// never created.
	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{})
	require.NoError(t, err, "an undeploy failure must not fail the uninstall")
	require.NotNil(t, result)
	assert.Empty(t, result.Warnings, "the undeploy diagnostic must not leak into Warnings")
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to undeploy some files: "), "must carry its historical CLI prefix: %q", result.Notes[0])

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
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

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{AfterEach: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.UninstallMod(context.Background(), game, "default", "src", "1", core.UninstallOptions{
		Hooks:      hooks,
		HookRunner: runner,
	})
	require.NoError(t, err, "after_each failures must not fail UninstallMod")
	require.NotNil(t, result)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed")

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "DB row should already be removed by the time after_each runs")
}

// --- DeployProfile ---

// TestService_DeployProfile_MultiModDeploysInProfileOrder guards doDeploy's
// "no args" gathering step (GetInstalledModsInProfileOrder): deploy order
// must follow the profile's mod order, not DB insertion order or any other
// incidental ordering.
func TestService_DeployProfile_MultiModDeploysInProfileOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "c", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})

	// Profile order deliberately differs from DB insertion order (c, a, b).
	seedProfileWithMod(t, svc, "g1", "default", "src", "c", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.Deployed)
	assert.Empty(t, result.Skipped)

	var order []string
	for _, e := range events {
		if e.Phase == core.DeployDeployed {
			order = append(order, e.ModName)
		}
	}
	assert.Equal(t, []string{"Mod C", "Mod A", "Mod B"}, order, "deploy order must follow profile order")

	for _, f := range []string{"c.esp", "a.esp", "b.esp"} {
		_, err := os.Lstat(filepath.Join(gameDir, f))
		assert.NoError(t, err, "%s should be deployed", f)
	}
}

// TestService_DeployProfile_LinkMethodOverrideHonored guards the --method
// override: DeployOptions.LinkMethod (a *domain.LinkMethod, not a bare
// value - see the task report for why) must both change how files are
// linked and be persisted via SetModLinkMethod.
func TestService_DeployProfile_LinkMethodOverrideHonored(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	override := domain.LinkCopy
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{LinkMethod: &override}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	info, err := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "override to copy method must not leave a symlink")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkCopy, mod.LinkMethod, "SetModLinkMethod must record the override")
}

// TestService_DeployProfile_PurgeRemovesFilesFirstAndPreservesEnabledSet
// guards --purge's two documented behaviors. The disabled mod is the key
// witness for "removed first": it is excluded from the redeploy pass
// entirely (never reaches the main per-mod loop, which also happens to
// undeploy-then-install), so its file's removal can only be explained by
// the purge pass itself. The enabled mod is the witness for "enabled set
// preserved": only it redeploys after the purge.
func TestService_DeployProfile_PurgeRemovesFilesFirstAndPreservesEnabledSet(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "kept-mod", "1.0", true, map[string][]byte{"kept.esp": []byte("k")})
	seedInstalledMod(t, svc, game, "src", "purged-mod", "1.0", false, map[string][]byte{"purged.esp": []byte("p")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "kept-mod", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "purged-mod", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "purged-mod", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	purgedPath := filepath.Join(gameDir, "purged.esp")
	_, err := os.Lstat(purgedPath)
	require.NoError(t, err, "precondition: the disabled mod's file must be deployed before purge")

	var purgeTotal int
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{Purge: true}, func(p core.DeployProgress) {
		if p.Phase == core.DeployPurging {
			purgeTotal = p.Total
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, purgeTotal, "purge must consider every installed mod, enabled or not")

	assert.Equal(t, 1, result.Deployed, "only the mod enabled before the purge should redeploy")
	_, err = os.Lstat(filepath.Join(gameDir, "kept.esp"))
	assert.NoError(t, err, "the previously-enabled mod should be redeployed after purge")
	_, err = os.Lstat(purgedPath)
	assert.True(t, os.IsNotExist(err), "the disabled mod's file must be removed by purge and never redeployed - proof purge actually undeploys mods excluded from the redeploy pass")
}

// TestService_DeployProfile_MissingCacheModRedownloads guards doDeploy's
// cache-miss path: when a mod's cache entry is gone, DeployProfile re-fetches
// it from source (GetMod -> GetModFiles -> DownloadMod) and still deploys it
// - a missing cache is not fatal to the mod, matching the pre-extraction CLI.
func TestService_DeployProfile_MissingCacheModRedownloads(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)

	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"plugin.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent) // mockSource.GetModFiles always returns file ID "1"

	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Redownload Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)

	// InstalledMod record exists, but nothing was ever stored in the cache.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          *mockMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	var phases []core.DeployPhase
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		phases = append(phases, p.Phase)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	assert.Empty(t, result.Skipped)
	assert.Contains(t, phases, core.DeployRedownloading)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "redownloaded file should be deployed")
}

// TestService_DeployProfile_MissingCacheAndFetchFailure_SkipsMod guards the
// other half of the cache-miss path: when the redownload itself can't even
// start (GetMod fails - here because no source is registered for "src"),
// doDeploy skips that mod and continues rather than aborting.
func TestService_DeployProfile_MissingCacheAndFetchFailure_SkipsMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err, "a per-mod fetch failure must not fail the whole deploy")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Deployed)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "failed to fetch")
}

// TestService_DeployProfile_HookOrder proves install.before_all ->
// install.before_each -> (deploy) -> install.after_each -> install.after_all
// ordering, mirroring TestService_UninstallMod_HookOrder.
func TestService_DeployProfile_HookOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	deployedFile := filepath.Join(gameDir, "plugin.esp")
	callLog := filepath.Join(scriptsDir, "calls.log")

	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each" >> `+callLog+`
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
if [ -e `+deployedFile+` ]; then
  echo "after_each:deployed" >> `+callLog+`
else
  echo "after_each:missing" >> `+callLog+`
fi
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all" >> `+callLog+`
exit 0`)

	hooks := &core.ResolvedHooks{Install: domain.HookConfig{
		BeforeAll: beforeAllScript, BeforeEach: beforeEachScript,
		AfterEach: afterEachScript, AfterAll: afterAllScript,
	}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Hooks: hooks, HookRunner: runner,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "before_all\nbefore_each\nafter_each:deployed\nafter_all\n", string(logContent))
}

// TestService_DeployProfile_BeforeEachHookFailure_SkipsModAndContinues
// guards deploy's before_each semantics, which differ from uninstall's: a
// failing install.before_each hook skips only that mod (added to
// result.Skipped) and the loop continues with the rest, rather than
// aborting the whole operation.
func TestService_DeployProfile_BeforeEachHookFailure_SkipsModAndContinues(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "bad", "Bad Mod", "1.0", true, map[string][]byte{"bad.esp": []byte("b")})
	seedNamedInstalledMod(t, svc, game, "src", "good", "Good Mod", "1.0", true, map[string][]byte{"good.esp": []byte("g")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "bad", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "good", "1.0")

	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "bad" ]; then
  echo "boom" >&2
  exit 1
fi
exit 0`)
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: beforeEachScript}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Hooks: hooks, HookRunner: runner,
	}, nil)
	require.NoError(t, err, "a before_each hook failure must skip that mod, not fail the deploy")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "Bad Mod")
	assert.Contains(t, result.Skipped[0], "install.before_each hook failed")

	_, err = os.Lstat(filepath.Join(gameDir, "good.esp"))
	assert.NoError(t, err, "the other mod must still deploy")
}

// TestService_DeployProfile_BeforeAllHookFails_AbortsUnlessForce mirrors
// TestService_UninstallMod_BeforeAllHookFails_AbortsUnlessForce: a failing
// install.before_all hook aborts the whole deploy unless Force is set, in
// which case it becomes a Warning and the deploy proceeds.
func TestService_DeployProfile_BeforeAllHookFails_AbortsUnlessForce(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeAll: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	t.Run("fatal without Force", func(t *testing.T) {
		result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
			Hooks: hooks, HookRunner: runner,
		}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_all hook failed")
		require.NotNil(t, result)
		assert.Equal(t, 0, result.Deployed)

		_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
		assert.True(t, os.IsNotExist(err), "nothing should deploy on a fatal before_all failure")
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
			Hooks: hooks, HookRunner: runner, Force: true,
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.Deployed)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_all hook failed")
		assert.Contains(t, result.Warnings[0], "forced")
	})
}

// TestService_DeployProfile_AppliesProfileOverrides guards the final step of
// doDeploy: profile.Overrides (INI tweaks etc.) are written into the game's
// install directory via core.ApplyProfileOverrides after the deploy loop.
func TestService_DeployProfile_AppliesProfileOverrides(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	installDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, InstallPath: installDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	profile, err := svc.NewProfileManager().Get("g1", "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"tweaks.ini": []byte("[General]\nfoo=bar\n")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	content, err := os.ReadFile(filepath.Join(installDir, "tweaks.ini"))
	require.NoError(t, err)
	assert.Equal(t, "[General]\nfoo=bar\n", string(content))
}

// TestService_DeployProfile_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult
// guards the error-path convention from Task 2 (commit 45470e8): once the
// result struct exists, a later fatal error must still return it, not
// discard it. Here, a forced uninstall.before_all failure during --purge
// records a Warning, and the subsequent single-mod lookup (an unknown
// ModID) fails fatally - the Warning recorded during purge must still come
// back with the error.
func TestService_DeployProfile_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, ModID: "does-not-exist", SourceID: "src",
		Hooks: hooks, HookRunner: runner, Force: true,
	}, nil)
	require.Error(t, err, "an unknown ModID must fail the deploy")
	assert.Contains(t, err.Error(), "mod not found")
	require.NotNil(t, result, "diagnostics accumulated during purge must not be discarded")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "uninstall.before_all hook failed")
	assert.Contains(t, result.Warnings[0], "forced")
}

// TestService_DeployProfile_ProgressCallback_IndexTotalModNameSequence
// guards the Index/Total/ModName sequence a 3-mod deploy reports.
func TestService_DeployProfile_ProgressCallback_IndexTotalModNameSequence(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedNamedInstalledMod(t, svc, game, "src", "3", "Mod Three", "1.0", true, map[string][]byte{"three.esp": []byte("3")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "3", "1.0")

	var seen []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.DeployDeployed {
			seen = append(seen, p)
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, seen, 3)
	for i, p := range seen {
		assert.Equal(t, i+1, p.Index)
		assert.Equal(t, 3, p.Total)
	}
	assert.Equal(t, "Mod One", seen[0].ModName)
	assert.Equal(t, "Mod Two", seen[1].ModName)
	assert.Equal(t, "Mod Three", seen[2].ModName)
}

// TestService_DeployProfile_NilProgressCallbackIsSafe guards that progress
// may be nil per the required API.
func TestService_DeployProfile_NilProgressCallbackIsSafe(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	assert.NotPanics(t, func() {
		result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.Deployed)
	})
}

// TestService_DeployProfile_SingleModByID guards the `lmm deploy <mod-id>`
// path: DeployOptions.ModID/SourceID restrict the deploy to a single mod,
// bypassing profile-order gathering entirely (no profile needs to exist).
func TestService_DeployProfile_SingleModByID(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{ModID: "1", SourceID: "src"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	_, err = os.Lstat(filepath.Join(gameDir, "one.esp"))
	assert.NoError(t, err)
	_, err = os.Lstat(filepath.Join(gameDir, "two.esp"))
	assert.True(t, os.IsNotExist(err), "only the requested mod should deploy")
}

// TestService_DeployProfile_SingleModDisabled_RequiresAll guards doDeploy's
// disabled-single-mod guard: deploying a specific disabled ModID fails
// unless All is set.
func TestService_DeployProfile_SingleModDisabled_RequiresAll(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{ModID: "1", SourceID: "src"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Deployed)

	result, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{ModID: "1", SourceID: "src", All: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
}

// TestService_DeployProfile_ZeroModsToDeploy_NoHooksFired guards doDeploy's
// early return when nothing qualifies to deploy (here: a disabled mod with
// All unset): DeployProfile must return an empty result without firing any
// hooks at all, matching the pre-extraction CLI which returns before ever
// setting up hooks.
func TestService_DeployProfile_ZeroModsToDeploy_NoHooksFired(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeAll: beforeAllScript}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Hooks: hooks, HookRunner: runner,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Deployed)
	assert.Empty(t, result.Skipped)

	_, err = os.Stat(callLog)
	assert.True(t, os.IsNotExist(err), "install.before_all must not fire when there is nothing to deploy")
}
