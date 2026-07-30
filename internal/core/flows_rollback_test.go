package core_test

// Tests for Service.ApplyRollback - the behavior-preserving extraction of
// cmd/lmm/update.go's doUpdateRollback, per Phase 6b Task 5. See
// internal/core/flows.go's ApplyRollback/RollbackResult/RollbackOptions doc
// comments for the exact behavior being tested here, and
// .superpowers/sdd/task-5-report.md for the full mapping/decision log.
//
// These tests reuse newFlowsTestService/installBlockingTrigger/
// seedProfileWithMod (flows_test.go), seedUpdatableMod (flows_update_test.go)
// and createTestScript (installer_batch_test.go) - all in this same
// core_test package.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// seedRollbackReadyMod prepares an installed mod already updated once,
// ready to be passed to ApplyRollback: an OLD version is installed and
// cached first, then advanced to a NEW version via the same
// Replace + ApplyModUpdate + UpsertMod sequence ApplyUpdate itself performs
// - leaving PreviousVersion/PreviousFileIDs set and the OLD version's cache
// entry intact, exactly the precondition ApplyRollback's own guards check
// (mirroring TestService_ApplyUpdate_RollbackPreconditionPreserved's own
// finding: Installer.Replace never touches the cache, so the old entry
// always survives).
func seedRollbackReadyMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, oldVersion, newVersion string, oldFileIDs, newFileIDs []string, oldFiles, newFiles map[string][]byte) *domain.InstalledMod {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range oldFiles {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, oldVersion, path, content))
	}
	for path, content := range newFiles {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, newVersion, path, content))
	}

	oldMod := domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: oldVersion, GameID: game.ID}
	im := &domain.InstalledMod{
		Mod:          oldMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   domain.LinkSymlink,
		FileIDs:      oldFileIDs,
	}
	require.NoError(t, svc.SaveInstalledMod(im))

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &oldMod, "default"))

	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		_, cerr := pm.Create(game.ID, "default")
		require.NoError(t, cerr)
	}
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: oldVersion, FileIDs: oldFileIDs}))

	newMod := domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: newVersion, GameID: game.ID}
	require.NoError(t, installer.Replace(context.Background(), game, &oldMod, &newMod, "default"))
	require.NoError(t, svc.ApplyModUpdate(sourceID, modID, game.ID, "default", newVersion, newFileIDs))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: newVersion, FileIDs: newFileIDs}))

	updated, err := svc.GetInstalledMod(sourceID, modID, game.ID, "default")
	require.NoError(t, err)
	return updated
}

// TestApplyRollbackSwapsVersions covers ApplyRollback's base case: the DB
// row's version/previous_version are swapped back, FileIDs restored to the
// old version's, and the profile YAML upserted with the previous version -
// mirroring doUpdateRollback's own happy path exactly.
func TestApplyRollbackSwapsVersions(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})
	require.Equal(t, "2.0", mod.Version)
	require.Equal(t, "1.0", mod.PreviousVersion)

	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "Mod One", result.ModName)
	assert.Equal(t, "2.0", result.FromVersion)
	assert.Equal(t, "1.0", result.ToVersion)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version)
	assert.Equal(t, []string{"old-1"}, updated.FileIDs)
	assert.Equal(t, "2.0", updated.PreviousVersion, "the DB swap must record the rolled-back-FROM version as the new previous_version")
	assert.Equal(t, domain.LinkSymlink, updated.LinkMethod)

	pm := svc.NewProfileManager()
	profile, err := pm.Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "1.0", profile.Mods[0].Version)
	assert.Equal(t, []string{"old-1"}, profile.Mods[0].FileIDs)

	_, err = os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
	assert.NoError(t, err, "the previous version's file must be redeployed")
	_, err = os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
	assert.True(t, os.IsNotExist(err), "the current version's file must be undeployed")
}

// TestApplyRollbackNoPreviousVersion covers the first guard: a mod that has
// never been updated (or has already been rolled back once) has no
// PreviousVersion, and ApplyRollback must fail with the exact
// pre-extraction error text before touching hooks, Replace, or the DB.
func TestApplyRollbackNoPreviousVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	require.Empty(t, mod.PreviousVersion)

	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.Equal(t, "no previous version available for rollback", err.Error())
	require.NotNil(t, result)
	assert.Empty(t, result.ModName, "no identity fields should be populated before this guard")
}

// TestApplyRollbackMissingCache covers the second guard: PreviousVersion is
// set, but its cache entry has been removed (pruned, or manually deleted)
// since the update - ApplyRollback must fail with the exact pre-extraction
// error text, again before touching hooks, Replace, or the DB.
func TestApplyRollbackMissingCache(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	require.NoError(t, svc.GetGameCache(game).Delete("g1", "src", "mod1", "1.0"))

	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.Equal(t, "previous version 1.0 not found in cache", err.Error())
	require.NotNil(t, result)
	assert.Empty(t, result.ModName, "no identity fields should be populated before this guard")
}

// TestApplyRollback_LockedRefRefusesRollback covers #97's whole contract for
// ApplyRollback, mirroring TestApplyUpdate_LockedRefRefusesUpdate: a locked
// profile ref refuses the rollback entirely, before any side effect -
// nothing redeployed, nothing changed in the DB or profile YAML.
func TestApplyRollback_LockedRefRefusesRollback(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})
	require.Equal(t, "2.0", mod.Version)

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock("g1", "default", "src", "mod1", ""))

	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Contains(t, err.Error(), "locked at v")
	assert.Contains(t, err.Error(), "in profile default", "the remedy must name the profile actually holding the lock (#142 round 4)")
	assert.Contains(t, err.Error(), "lmm mod lock -s src -p default mod1", "the remedy must carry -s/-p so a copy-paste can never resolve against a different source/profile")
	assert.Contains(t, err.Error(), "lmm mod unlock -s src -p default mod1")
	require.NotNil(t, result)
	assert.Empty(t, result.ModName, "no identity fields should be populated before this guard")

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version, "the DB row must be unchanged")

	profile, err := pm.Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "2.0", profile.Mods[0].Version, "the profile ref must be unchanged")

	_, err = os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
	assert.NoError(t, err, "the current version's file must remain deployed - nothing redeployed")
}

// TestApplyRollback_UnlockedRefStillRollsBack is the explicit control for
// TestApplyRollback_LockedRefRefusesRollback: an unlocked ref must roll back
// exactly as before the #97 gate was added.
func TestApplyRollback_UnlockedRefStillRollsBack(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})
	require.Equal(t, "2.0", mod.Version)

	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version)
}

// TestApplyRollbackHookForceGate covers the Force-gate/fatal semantics for
// ApplyRollback's two before_each hooks (uninstall.before_each for the
// CURRENT version, install.before_each for the PREVIOUS version being
// redeployed) - mirroring doUpdateRollback's own two near-identical Force
// checks exactly: fatal without Force, a Warning (plus an
// UpdateBeforeEachForced event carrying the full message verbatim) and the
// rollback proceeds when Force is set.
func TestApplyRollbackHookForceGate(t *testing.T) {
	newSetup := func(t *testing.T) (*core.Service, *domain.Game, *domain.InstalledMod) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
		mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
			[]string{"old-1"}, []string{"new-1"},
			map[string][]byte{"mod1-old.esp": []byte("old-content")},
			map[string][]byte{"mod1-new.esp": []byte("new-content")})
		return svc, game, mod
	}
	failingScript := func(t *testing.T, dir, name string) string {
		return createTestScript(t, dir, name, "#!/bin/bash\necho boom >&2\nexit 1")
	}

	t.Run("uninstall.before_each fatal without Force", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{Hooks: hooks, HookRunner: runner}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_each hook failed")
		require.NotNil(t, result)
		assert.Empty(t, result.Warnings)

		updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "2.0", updated.Version, "a fatal before_each hook must leave the DB row untouched")
	})

	t.Run("uninstall.before_each forced warns and proceeds", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		var events []core.DeployProgress
		result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{Hooks: hooks, HookRunner: runner, Force: true}, func(p core.DeployProgress) {
			events = append(events, p)
		})
		require.NoError(t, err)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_each hook failed (forced):")

		var sawForced bool
		for _, e := range events {
			if e.Phase == core.UpdateBeforeEachForced {
				sawForced = true
				assert.Equal(t, result.Warnings[0], e.Detail)
			}
		}
		assert.True(t, sawForced, "an UpdateBeforeEachForced event must fire")

		updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "1.0", updated.Version, "the rollback must still apply despite the forced hook failure")
	})

	t.Run("install.before_each fatal without Force", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		_, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{Hooks: hooks, HookRunner: runner}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_each hook failed")

		updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "2.0", updated.Version, "a fatal before_each hook must leave the DB row untouched")
	})

	t.Run("install.before_each forced warns and proceeds", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{Hooks: hooks, HookRunner: runner, Force: true}, nil)
		require.NoError(t, err)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_each hook failed (forced):")

		updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "1.0", updated.Version)
	})
}

// TestApplyRollbackAfterEachWarnings covers the always-non-fatal semantics
// for ApplyRollback's two after_each hooks: both failures land in
// result.Warnings (and fire UpdateWarning events), in hook-run order
// (uninstall.after_each, then install.after_each), and the rollback still
// applies end to end regardless of Force.
func TestApplyRollbackAfterEachWarnings(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	scriptsDir := t.TempDir()
	hooks := &core.ResolvedHooks{
		Uninstall: domain.HookConfig{AfterEach: createTestScript(t, scriptsDir, "u_after.sh", "#!/bin/bash\necho boom >&2\nexit 1")},
		Install:   domain.HookConfig{AfterEach: createTestScript(t, scriptsDir, "i_after.sh", "#!/bin/bash\necho boom >&2\nexit 1")},
	}
	runner := core.NewHookRunner(5 * time.Second)

	var events []core.DeployProgress
	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{Hooks: hooks, HookRunner: runner}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "after_each hook failures must never fail the rollback")
	require.Len(t, result.Warnings, 2)
	assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed")
	assert.Contains(t, result.Warnings[1], "install.after_each hook failed")

	var warningCount int
	for _, e := range events {
		if e.Phase == core.UpdateWarning {
			warningCount++
		}
	}
	assert.Equal(t, 2, warningCount)

	updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, gerr)
	assert.Equal(t, "1.0", updated.Version, "the rollback itself must still have applied")
}

// TestApplyRollbackCompensatesOnDBFailure covers RollbackModVersion's
// compensating action: when the DB version-swap fails, ApplyRollback
// attempts a best-effort reverse Installer.Replace (redeploying the
// CURRENT version, undoing the Replace it just performed) before returning
// the error - matching doUpdateRollback's own compensation block exactly.
//
// The DB failure is forced deterministically via a real SQLite trigger that
// aborts any UPDATE touching installed_mods.version (installBlockingTrigger's
// sibling technique - see its own doc comment) - installed only AFTER
// seedRollbackReadyMod's own ApplyModUpdate call (which touches the same
// column) has already completed, so only ApplyRollback's own
// RollbackModVersion call is affected.
func TestApplyRollbackCompensatesOnDBFailure(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	installVersionBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	result, err := svc.ApplyRollback(context.Background(), game, "default", mod.SourceID, mod.ID, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating database:")
	require.NotNil(t, result, "a partial result must be returned alongside the error")

	updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, gerr)
	assert.Equal(t, "2.0", updated.Version, "the blocked transaction must have rolled back entirely - the DB row must be untouched")
	assert.Equal(t, "1.0", updated.PreviousVersion)

	// The compensating reverse-Replace must have restored the CURRENT
	// (2.0) version's deployment - undoing the Replace(current->previous)
	// this call had already performed before RollbackModVersion failed.
	_, err = os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
	assert.NoError(t, err, "the current version's file must be redeployed by the compensating Replace")
	_, err = os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
	assert.True(t, os.IsNotExist(err), "the previous version's file must be undeployed again")
}

// installVersionBlockingTrigger opens a second connection to the SQLite
// file at dbPath and installs a trigger that makes any UPDATE touching
// installed_mods.version fail - the same technique as installBlockingTrigger
// (flows_test.go), narrowed to the column RollbackModVersion/SwapModVersions
// itself writes, so it can be installed AFTER seeding (which also updates
// this column via ApplyModUpdate) without interfering with that seed step.
func installVersionBlockingTrigger(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_version_updates
		BEFORE UPDATE OF version ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}
