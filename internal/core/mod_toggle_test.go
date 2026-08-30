package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- EnableMod ---

func TestService_EnableMod_DeploysDisabledMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "plugin.esp should be deployed to the game dir")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
	assert.True(t, mod.Deployed, "#183: enabling a mod must also record it as deployed")
}

func TestService_EnableMod_AlreadyEnabledIsNoop(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Changed)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "no-op enable must not deploy files")
}

func TestService_EnableMod_MissingCacheErrors(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// Installed-mod record exists, but nothing was ever stored in the cache.
	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, nil)

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not found in cache")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
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

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to deploy mod")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "DB must remain untouched on deploy failure")
}

func TestService_EnableMod_UnknownModReturnsErrModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "missing")
	require.Error(t, err)
	assert.Nil(t, result)
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
	// install that happened earlier), and record the DB's deployed flag to
	// match — seedInstalledMod doesn't set it, so this mirrors the real
	// precondition DisableMod actually sees for a genuinely-deployed mod.
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.SetModDeployed(context.Background(), "src", "1", "g1", "default", true))

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Empty(t, result.Notes, "a clean undeploy must not record a diagnostic")

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "plugin.esp should be removed from the game dir")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "1", "1.0"), "cache entry must be preserved")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)
	assert.False(t, mod.Deployed, "#183: disabling a mod must also clear the deployed flag")
}

func TestService_DisableMod_AlreadyDisabledIsNoop(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Changed)
}

// TestService_DisableMod_AlreadyDisabledSelfHealsStaleDeployedFlag pins
// #183's self-heal follow-up: a mod disabled before the #183 fix shipped
// (or otherwise drifted) can be stuck at enabled=false, deployed=true
// forever, since nothing else clears the flag once a mod is already
// disabled. Calling DisableMod again on it must converge deployed to
// false, still succeed, and still report Changed=false (the enabled
// status itself didn't change).
func TestService_DisableMod_AlreadyDisabledSelfHealsStaleDeployedFlag(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{
		"plugin.esp": []byte("data"),
	})
	require.NoError(t, svc.SetModDeployed(context.Background(), "src", "1", "g1", "default", true),
		"simulate a pre-#183 disable that left the stale deployed=true flag behind")

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err, "self-healing the stale flag must still report success")
	require.NotNil(t, result)
	assert.False(t, result.Changed, "enabled status itself didn't change")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Deployed, "#183: re-disabling an already-disabled mod must self-heal a stale deployed=true")
}

func TestService_DisableMod_UnknownModReturnsErrModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "missing")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestService_DisableMod_UndeployFailureIsNonFatal guards a deliberate
// behavior-preservation decision (documented in the task report): the
// pre-extraction CLI (doModDisable) treated Uninstall failures as non-fatal
// ("warn but continue" under --verbose) because files may already have been
// removed manually. DisableMod preserves the *functional* outcome — DB still
// flips to disabled — even when undeploying fails, and (Task 6 item a)
// records the historical diagnostic text in Notes rather than discarding it,
// so a caller (cmd/lmm's doModDisable) can restore the pre-5a --verbose
// warning byte-identically.
func TestService_DisableMod_UndeployFailureIsNonFatal(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{
		"plugin.esp": []byte("data"),
	})

	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.SetModDeployed(context.Background(), "src", "1", "g1", "default", true),
		"seed Deployed=true so the post-disable assertion below actually proves a transition")

	// Corrupt the deployed file into a plain file (not a symlink) so the
	// symlink linker's Undeploy fails deterministically ("not a symlink").
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err, "undeploy failures must not fail DisableMod")
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	require.Len(t, result.Notes, 1)
	assert.Contains(t, result.Notes[0], "Warning: failed to undeploy some files: ",
		"must carry the pre-5a historical prefix verbatim, matching UninstallResult's own convention")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "DB should still flip to disabled even when undeploy is best-effort")
	assert.False(t, mod.Deployed, "#183: the deployed flag must still clear to record intent, even when the undeploy itself was best-effort")
}

// TestService_DisableMod_SetModDeployedFailure_NonFatalNote pins the #183
// fix's own failure-handling decision: a SetModDeployed(false) failure is
// non-fatal, recorded as a Note, exactly like DisableMod's existing
// Uninstall-failure handling and like PurgeProfile's own SetModDeployed
// call (see TestService_PurgeProfile_SetModDeployedFailure_NonFatalNote) —
// not escalated to a hard error the way SetModEnabled's own failure still
// is. installBlockingTrigger blocks only the "deployed" column, so
// SetModEnabled (a different column) still succeeds.
func TestService_DisableMod_SetModDeployedFailure_NonFatalNote(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")
	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err, "a SetModDeployed failure must not fail DisableMod")
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, strings.Join(result.Notes, "\n"), "could not mark as not deployed",
		"Notes = %v, want one mentioning the SetModDeployed failure", result.Notes)

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "SetModEnabled touches a different column and must still succeed")
}

// TestService_EnableMod_SetModDeployedFailure_NonFatalNote is
// TestService_DisableMod_SetModDeployedFailure_NonFatalNote's mirror for
// the enable path.
func TestService_EnableMod_SetModDeployedFailure_NonFatalNote(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err, "a SetModDeployed failure must not fail EnableMod")
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, strings.Join(result.Notes, "\n"), "could not mark as deployed",
		"Notes = %v, want one mentioning the SetModDeployed failure", result.Notes)

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "SetModEnabled touches a different column and must still succeed")
}
