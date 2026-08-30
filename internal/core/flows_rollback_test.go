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
	require.NoError(t, svc.SaveInstalledMod(context.Background(), im))

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
	require.NoError(t, svc.ApplyModUpdateForTest(context.Background(), sourceID, modID, game.ID, "default", newVersion, newFileIDs))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: newVersion, FileIDs: newFileIDs}))

	updated, err := svc.GetInstalledMod(context.Background(), sourceID, modID, game.ID, "default")
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

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "Mod One", result.ModName)
	assert.Equal(t, "2.0", result.FromVersion)
	assert.Equal(t, "1.0", result.ToVersion)
	// #301: Status is the "did it happen" signal (ModName is populated as
	// soon as the guards pass, so it cannot be one) and matches the
	// "rolled_back" string cmd/lmm's rollback --json has always emitted.
	assert.Equal(t, core.UpdateRolledBack, result.Status)
	assert.Empty(t, result.Reason)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

// TestApplyRollbackMissingCache covers the second guard: PreviousVersion is
// set, but its cache entry has been removed (pruned, or manually deleted)
// since the update - ApplyRollback must fail with the exact pre-extraction
// error text, again before touching hooks, Replace, or the DB. PlanRollback
// itself still succeeds here (v2 Phase 2 Unit I, #289): a missing cache is
// plan DATA (RollbackPlan.CacheMissing), not a PlanRollback error - see
// RollbackPlan's doc comment - so this now exercises ApplyRollback's own
// independent re-check of the same condition.
func TestApplyRollbackMissingCache(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedRollbackReadyMod(t, svc, game, "src", "mod1", "Mod One", "1.0", "2.0",
		[]string{"old-1"}, []string{"new-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")},
		map[string][]byte{"mod1-new.esp": []byte("new-content")})

	require.NoError(t, svc.GetGameCache(game).Delete("g1", "src", "mod1", "1.0"))

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	assert.True(t, plan.CacheMissing)

	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.Equal(t, "previous version 1.0 not found in cache", err.Error())
	require.NotNil(t, result)
	assert.Empty(t, result.ModName, "no identity fields should be populated before this guard")
	// #301: a rollback that never happened is reported as skipped, so an
	// error return can never read as a successful "rolled_back".
	assert.Equal(t, core.UpdateSkipped, result.Status)
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

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	assert.True(t, plan.Locked)

	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Contains(t, err.Error(), "locked at v")
	assert.Contains(t, err.Error(), "in profile default", "the remedy must name the profile actually holding the lock (#142 round 4)")
	assert.NotContains(t, err.Error(), "lmm mod lock", "unit Q review I1: this gate refuses on the lock alone, so moving the lock is not a remedy")
	assert.Contains(t, err.Error(), "lmm mod unlock -s src -p default mod1", "the remedy must carry -s/-p so a copy-paste can never resolve against a different source/profile")
	require.NotNil(t, result)
	assert.Empty(t, result.ModName, "no identity fields should be populated before this guard")
	// #301: the refusal is reported as the same skipped/locked pair
	// cmd/lmm's rollback --json emits for a locked ref, so a future JSON
	// frontend can render the result directly.
	assert.Equal(t, core.UpdateSkipped, result.Status)
	assert.Equal(t, "locked", result.Reason)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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
		seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}})

		plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
		require.NoError(t, err)
		result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_each hook failed")
		require.NotNil(t, result)
		assert.Empty(t, result.Warnings)

		updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "2.0", updated.Version, "a fatal before_each hook must leave the DB row untouched")
	})

	t.Run("uninstall.before_each forced warns and proceeds", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}})

		plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
		require.NoError(t, err)
		sink, seen := core.RecordEvents()
		result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{Force: true}, sink)
		require.NoError(t, err)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_each hook failed (forced):")

		var sawForced bool
		for _, e := range *seen {
			if hook, ok := e.(core.HookEvent); ok && hook.Phase == core.UpdateBeforeEachForced {
				sawForced = true
				assert.Equal(t, result.Warnings[0], hook.Detail)
				assert.Equal(t, "uninstall.before_each", hook.Stage)
			}
		}
		assert.True(t, sawForced, "an UpdateBeforeEachForced event must fire")

		updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "1.0", updated.Version, "the rollback must still apply despite the forced hook failure")
	})

	t.Run("install.before_each fatal without Force", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}})

		plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
		require.NoError(t, err)
		_, err = svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_each hook failed")

		updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "2.0", updated.Version, "a fatal before_each hook must leave the DB row untouched")
	})

	t.Run("install.before_each forced warns and proceeds", func(t *testing.T) {
		svc, game, mod := newSetup(t)
		scriptsDir := t.TempDir()
		seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}})

		plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
		require.NoError(t, err)
		result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{Force: true}, nil)
		require.NoError(t, err)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_each hook failed (forced):")

		updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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
	seedHooks(t, svc, game, "default", domain.GameHooks{
		Uninstall: domain.HookConfig{AfterEach: createTestScript(t, scriptsDir, "u_after.sh", "#!/bin/bash\necho boom >&2\nexit 1")},
		Install:   domain.HookConfig{AfterEach: createTestScript(t, scriptsDir, "i_after.sh", "#!/bin/bash\necho boom >&2\nexit 1")},
	})

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	sink, seen := core.RecordEvents()
	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, sink)
	require.NoError(t, err, "after_each hook failures must never fail the rollback")
	require.Len(t, result.Warnings, 2)
	assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed")
	assert.Contains(t, result.Warnings[1], "install.after_each hook failed")

	var warningCount int
	for _, e := range *seen {
		if w, ok := e.(core.WarningEvent); ok && w.Phase == core.UpdateWarning {
			warningCount++
		}
	}
	assert.Equal(t, 2, warningCount)

	updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)

	installVersionBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating database:")
	require.NotNil(t, result, "a partial result must be returned alongside the error")

	updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

// --- #150: same-version cache sharing on rollback ---

// seedSameVersionRollbackReadyMod prepares an installed mod that has been
// through a same-version file-only update (#144 item 4's degenerate shape:
// the old file's member and its replacement's share ONE version-keyed cache
// dir) and is rollback-ready: PreviousVersion == Version == "1.0",
// PreviousFileIDs == {fileA}, FileIDs == {fileB}. The update is applied via
// the same ReplaceForUpdate + ApplyModUpdate + UpsertMod sequence ApplyUpdate
// itself performs. The withManifests flag picks between the two on-disk
// shapes PR #149 distinguishes: per-file-ID member manifests (every install
// made since) or a legacy entry with no recorded manifests (this seed writes
// no markers at all; a pre-#149 entry's bare zero-byte completion markers
// parse as not-recorded and fall back identically) - in which case the
// seeding update itself already deployed the union, the exact pre-manifest
// starting state.
func seedSameVersionRollbackReadyMod(t *testing.T, svc *core.Service, game *domain.Game, withManifests bool) *domain.InstalledMod {
	t.Helper()

	const version = "1.0"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", version, "mod1-fileA.esp", []byte("A")))
	if withManifests {
		seedSameVersionManifest(t, svc, game, "src", "mod1", version, "fileA", []string{"mod1-fileA.esp"})
	}

	oldMod := domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: version, GameID: game.ID}
	im := &domain.InstalledMod{
		Mod:          oldMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   domain.LinkSymlink,
		FileIDs:      []string{"fileA"},
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), im))

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &oldMod, "default"))

	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		_, cerr := pm.Create(game.ID, "default")
		require.NoError(t, cerr)
	}
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: version, FileIDs: []string{"fileA"}}))

	// The same-version file-only update: fileB's member lands in the SAME
	// version dir, superseding fileA's.
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", version, "mod1-fileB.esp", []byte("B")))
	if withManifests {
		seedSameVersionManifest(t, svc, game, "src", "mod1", version, "fileB", []string{"mod1-fileB.esp"})
	}
	newMod := oldMod
	require.NoError(t, installer.ReplaceForUpdate(context.Background(), game, &oldMod, &newMod, "default", []string{"fileA"}, []string{"fileB"}))
	require.NoError(t, svc.ApplyModUpdateForTest(context.Background(), "src", "mod1", game.ID, "default", version, []string{"fileB"}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: version, FileIDs: []string{"fileB"}}))

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.Equal(t, version, updated.PreviousVersion, "seed must leave the row rollback-ready")
	require.Equal(t, []string{"fileA"}, updated.PreviousFileIDs)
	require.Equal(t, []string{"fileB"}, updated.FileIDs)
	return updated
}

// TestApplyRollback_SameVersionFileOnlyUpdate_UndeploysRolledBackFromMember
// is #150: rolling back a same-version file-only update lists old and new
// caches from the SAME version-keyed dir, so a plain-union Replace would
// leave the rolled-back-from file's member deployed alongside the restored
// one. With member manifests on both sides, the rollback must narrow exactly
// like the forward update does - transition current FileIDs ->
// PreviousFileIDs - undeploying fileB's member and restoring fileA's.
func TestApplyRollback_SameVersionFileOnlyUpdate_UndeploysRolledBackFromMember(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedSameVersionRollbackReadyMod(t, svc, game, true)

	// Seed sanity: the update itself narrowed - only fileB's member deployed.
	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	require.NoError(t, statErr)
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	require.True(t, os.IsNotExist(statErr))

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)
	assert.Equal(t, "1.0", result.FromVersion)
	assert.Equal(t, "1.0", result.ToVersion)

	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.NoError(t, statErr, "the restored file's member must be redeployed")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.True(t, os.IsNotExist(statErr),
		"the rolled-back-from file's member must be UNDEPLOYED despite the shared same-version cache dir (#150)")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version)
	assert.Equal(t, []string{"fileA"}, updated.FileIDs)
	assert.Equal(t, []string{"fileB"}, updated.PreviousFileIDs)

	profile, err := svc.NewProfileManager().Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, []string{"fileA"}, profile.Mods[0].FileIDs)
}

// TestApplyRollback_SameVersionFileOnlyUpdate_LegacyCacheFallsBackToUnion
// pins #149's hard backward-compat rule on the rollback path: a cache entry
// with no recorded manifests (seeded here with no markers at all; a pre-#149
// entry's bare zero-byte completion markers parse as not-recorded and fall
// back the same way) must keep the historical union behavior
// silently - nothing undeployed, nothing erroring, nothing warning.
func TestApplyRollback_SameVersionFileOnlyUpdate_LegacyCacheFallsBackToUnion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedSameVersionRollbackReadyMod(t, svc, game, false)

	// Seed sanity: without manifests the update already deployed the union.
	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	require.NoError(t, statErr)
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	require.NoError(t, statErr)

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)
	result, err := svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.NoError(t, statErr, "a legacy cache keeps the union deployed - never guess, never undeploy without positive provenance")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr, "a legacy cache keeps the union deployed - never guess, never undeploy without positive provenance")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileA"}, updated.FileIDs, "the DB swap must still restore the previous file IDs")
}

// TestApplyRollback_SameVersionFileOnlyUpdate_CompensationStaysNarrow: when
// the DB version-swap fails after the rollback's narrowed replace already
// ran, the compensating reverse replace must narrow too (the transition
// swapped: PreviousFileIDs -> FileIDs), restoring exactly the pre-rollback
// deployment - fileB's member alone, never the union. The symmetric
// set-inequality gate guarantees the forward call and this compensation
// answer the same way (see resolveSharedDirUpdate's doc comment).
func TestApplyRollback_SameVersionFileOnlyUpdate_CompensationStaysNarrow(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mod := seedSameVersionRollbackReadyMod(t, svc, game, true)

	plan, err := svc.PlanRollback(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)

	installVersionBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	_, err = svc.ApplyRollback(context.Background(), game, plan, core.RollbackOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating database:")

	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr, "the compensating replace must restore the current file's member")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.True(t, os.IsNotExist(statErr),
		"the compensation must narrow too - the previous file's member must not stay deployed beside the restored current one")

	updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, gerr)
	assert.Equal(t, []string{"fileB"}, updated.FileIDs, "the blocked transaction must have left the row untouched")
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
