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

// --- PurgeProfile (#61) ---

// installDeleteBlockingTrigger is installBlockingTrigger's DELETE-side
// sibling: it makes any DELETE on installed_mods fail, deterministically
// forcing DeleteInstalledMod to error (PurgeProfile's --uninstall
// record-delete failure path) without affecting reads or other writes.
func installDeleteBlockingTrigger(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_installed_mod_deletes
		BEFORE DELETE ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}

// installSeededMod deploys an already-seeded mod's files into the game dir
// (cache + DB record + deployed files - the state `lmm purge` starts from).
func installSeededMod(t *testing.T, svc *core.Service, game *domain.Game, modID string) {
	t.Helper()
	require.NoError(t, svc.GetInstaller(game).Install(context.Background(), game, &domain.Mod{ID: modID, SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
}

func TestService_PurgeProfile_PurgesAll_MarksNotDeployed_EmitsPerModEvents(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	installSeededMod(t, svc, game, "a")
	installSeededMod(t, svc, game, "b")

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 2)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.Purged)
	assert.Empty(t, result.Skipped)

	for _, f := range []string{"a.esp", "b.esp"} {
		_, err := os.Lstat(filepath.Join(gameDir, f))
		assert.True(t, os.IsNotExist(err), "%s must be undeployed", f)
	}
	for _, id := range []string{"a", "b"} {
		mod, err := svc.GetInstalledMod(context.Background(), "src", id, "g1", "default")
		require.NoError(t, err, "records must be preserved without Uninstall")
		assert.False(t, mod.Deployed, "mod %s must be marked not deployed", id)
	}

	require.NotEmpty(t, *seen)
	phases, flowEvents := phasesOf(*seen)
	assert.Equal(t, core.DeployPurging, phases[0])
	assert.Equal(t, 2, flowEvents[0].(core.FlowEvent).EventScope().Total)
	var purged []core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.PurgeModPurged {
			purged = append(purged, m)
		}
	}
	require.Len(t, purged, 2)
	names := map[string]bool{}
	for i, e := range purged {
		assert.Equal(t, i+1, e.Index)
		assert.Equal(t, 2, e.Total)
		names[e.ModName] = true
	}
	assert.True(t, names["Mod A"] && names["Mod B"], "each mod must get its own PurgeModPurged event")
	assert.Equal(t, core.PurgeComplete, phases[len(phases)-1])
}

func TestService_PurgeProfile_EmptyModList_NoEventsNoHooks(t *testing.T) {
	svc := newFlowsTestService(t)
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	// A failing before_all hook is the witness that no hooks run on an
	// empty purge: if it ran, the (Force-less) call would return its error.
	failScript := createTestScript(t, scriptsDir, "before_all.sh", "#!/bin/bash\nexit 1")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", nil, core.PurgeOptions{}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.Purged)
	assert.Empty(t, *seen)
}

func TestService_PurgeProfile_Uninstall_RemovesRecordsAndProfileEntries(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	installSeededMod(t, svc, game, "1")

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{Uninstall: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err))
	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "Uninstall must delete the DB record")
	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "Uninstall must remove the profile YAML entry")
}

func TestService_PurgeProfile_BeforeEachSkip_RecordsSkippedAndEmitsPurgeModSkipped(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "bad", "Bad Mod", "1.0", true, map[string][]byte{"bad.esp": []byte("b")})
	seedNamedInstalledMod(t, svc, game, "src", "good", "Good Mod", "1.0", true, map[string][]byte{"good.esp": []byte("g")})
	installSeededMod(t, svc, game, "bad")
	installSeededMod(t, svc, game, "good")

	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "bad" ]; then
  echo "boom" >&2
  exit 1
fi
exit 0`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: beforeEachScript}})

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	var found *core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.PurgeModSkipped {
			found = &m
			break
		}
	}
	require.NotNil(t, found, "expected a PurgeModSkipped event")
	assert.Equal(t, "Bad Mod", found.ModName)
	require.NotNil(t, found.Mod)
	assert.Equal(t, "bad", found.Mod.ModID)
	assert.True(t, strings.HasPrefix(found.Detail, "uninstall.before_each hook failed: "),
		"Detail must carry the baked prefix the CLI prints after \"Skipped <name>: \"")
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Bad Mod", result.Skipped[0].Name)
	assert.Equal(t, found.Detail, result.Skipped[0].Reason,
		"the Skipped entry's Reason must be the event's Detail verbatim")

	_, err = os.Lstat(filepath.Join(gameDir, "bad.esp"))
	assert.NoError(t, err, "a before_each-skipped mod must stay deployed")
	_, err = os.Lstat(filepath.Join(gameDir, "good.esp"))
	assert.True(t, os.IsNotExist(err))
}

func TestService_PurgeProfile_BeforeAllFailure_FatalWithoutForce(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}})

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, sink)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uninstall.before_all hook failed:")
	require.NotNil(t, result, "partial-result convention: the accumulated result comes back alongside the error")
	assert.Zero(t, result.Purged)
	assert.Empty(t, *seen)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "nothing may be purged when before_all fails without Force")
}

func TestService_PurgeProfile_BeforeAllFailure_ForcedWarnsBeforePurging(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}})

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Force: true,
	}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "uninstall.before_all hook failed (forced):")
	phases, events := phasesOf(*seen)
	require.GreaterOrEqual(t, len(events), 2)
	first, ok := events[0].(core.HookEvent)
	require.True(t, ok, "the forced warning must precede the purge header")
	assert.Equal(t, core.DeployBeforeAllForced, first.Phase, "the forced warning must precede the purge header")
	assert.Equal(t, "uninstall.before_all", first.Stage)
	assert.Equal(t, result.Warnings[0], first.Detail)
	assert.Equal(t, core.DeployPurging, phases[1])
}

func TestService_PurgeProfile_AfterHookFailures_WarningsUseModNameAndDeferEmission(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")

	failScript := createTestScript(t, scriptsDir, "fail.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{AfterEach: failScript, AfterAll: failScript}})

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	require.Len(t, result.Warnings, 2)
	assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed for Test Mod:",
		"lmm purge's after_each warning carries the mod NAME (unlike deploy --purge's mod ID)")
	assert.Contains(t, result.Warnings[1], "uninstall.after_all hook failed:")

	purgedIdx, firstWarnIdx := -1, -1
	phases, _ := phasesOf(*seen)
	for i, ph := range phases {
		if ph == core.PurgeModPurged {
			purgedIdx = i
		}
		if ph == core.PurgeWarning && firstWarnIdx == -1 {
			firstWarnIdx = i
		}
	}
	require.NotEqual(t, -1, purgedIdx)
	require.NotEqual(t, -1, firstWarnIdx)
	assert.Greater(t, firstWarnIdx, purgedIdx,
		"after-hook warnings are deferred: they must emit only after the last per-mod event")
}

func TestService_PurgeProfile_UndeployFailure_EmitsPurgeNoteAndStillCounts(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")

	// Corrupt the deployed symlink into a plain file so Uninstall fails.
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged, "an undeploy failure is best-effort: the mod still counts as purged")
	assert.Empty(t, result.Skipped)

	var found *core.StepEvent
	for _, e := range *seen {
		if step, ok := e.(core.StepEvent); ok && step.Phase == core.PurgeNote {
			found = &step
			break
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, "Test Mod", found.ModName)
	assert.True(t, strings.HasPrefix(found.Detail, "⚠ Test Mod - "))
	assert.Contains(t, result.Notes, found.Detail)
}

func TestService_PurgeProfile_SetModDeployedFailure_NonFatalNote(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")
	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, result.Notes[0], "⚠ Test Mod - failed to mark as not deployed:")
}

func TestService_PurgeProfile_Uninstall_DeleteRecordFailure_CountsFailedSkipsAfterEachAndSuccess(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	installSeededMod(t, svc, game, "1")
	installDeleteBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	// after_each leaves a marker file; a delete-record failure must skip it.
	marker := filepath.Join(scriptsDir, "after_each_ran")
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", "#!/bin/bash\ntouch "+marker+"\nexit 0")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{AfterEach: afterEachScript}})

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Uninstall: true,
	}, sink)
	require.NoError(t, err)

	assert.Zero(t, result.Purged)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Test Mod", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "failed to remove record:")
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, result.Notes[0], "⚠ Test Mod - failed to remove record:")

	phases, _ := phasesOf(*seen)
	for _, ph := range phases {
		assert.NotEqual(t, core.PurgeModPurged, ph, "a delete-record failure must not count as purged")
	}
	_, err = os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "after_each must be skipped when the record delete fails")
}

func TestService_PurgeProfile_Uninstall_ProfileRemoveFailure_RecordsNote(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")

	// Profile exists but does NOT reference the mod, so RemoveMod returns
	// domain.ErrModNotFound - the non-fatal "Note:" path.
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), "g1", "default")
	require.NoError(t, err)

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{Uninstall: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged, "a profile-YAML removal failure is note-only; the mod still purges")
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, result.Notes[0], "Note: Test Mod - ")

	_, err = svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "the DB record delete must still have happened")
}

func TestService_PurgeProfile_CtxCancelledBetweenMods_ReturnsPartialResult(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	installSeededMod(t, svc, game, "a")
	installSeededMod(t, svc, game, "b")

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 2)

	ctx, cancel := context.WithCancel(context.Background())
	sink, seen := core.RecordEvents()
	result, err := svc.PurgeProfile(ctx, game, "default", mods, core.PurgeOptions{}, func(e core.Event) {
		sink(e)
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.PurgeModPurged {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Purged, "partial result: exactly the first mod purged before cancellation")

	stillDeployed := 0
	for _, f := range []string{"a.esp", "b.esp"} {
		if _, err := os.Lstat(filepath.Join(gameDir, f)); err == nil {
			stillDeployed++
		}
	}
	assert.Equal(t, 1, stillDeployed, "the second mod must remain deployed")
	phases, _ := phasesOf(*seen)
	for _, ph := range phases {
		assert.NotEqual(t, core.PurgeComplete, ph, "a cancelled purge must not report completion")
	}
}
