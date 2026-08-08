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
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// versionedFileSource serves a single file under mockSource's default ID
// ("1" - the same ID both ApplyProfileSwitch and ApplyImport's install
// loops select via stored FileIDs), but with an explicit Version ("1.0")
// distinct from the mod-level Version a test passes to AddMod - mirroring
// flows_install_test.go's oldFileSource/versionOverrideFileSource fixtures
// for the #94 stamp, applied to the two remaining flows (Task A4). See
// TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion and
// TestApplyImport_InstallLoop_RecordsFileVersion.
type versionedFileSource struct {
	*mockSourceWithDownloads
}

func (s *versionedFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "1", Name: "Main File", FileName: mod.ID + ".zip", Version: "1.0", IsPrimary: true},
	}, nil
}

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

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "plugin.esp should be deployed to the game dir")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
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

	result, err := svc.EnableMod(context.Background(), game, "default", "src", "1")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to deploy mod")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
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
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.SetModDeployed("src", "1", "g1", "default", true))

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Changed)
	assert.Empty(t, result.Notes, "a clean undeploy must not record a diagnostic")

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "plugin.esp should be removed from the game dir")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "1", "1.0"), "cache entry must be preserved")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
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
	require.NoError(t, svc.SetModDeployed("src", "1", "g1", "default", true),
		"simulate a pre-#183 disable that left the stale deployed=true flag behind")

	result, err := svc.DisableMod(context.Background(), game, "default", "src", "1")
	require.NoError(t, err, "self-healing the stale flag must still report success")
	require.NotNil(t, result)
	assert.False(t, result.Changed, "enabled status itself didn't change")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
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

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.SetModDeployed("src", "1", "g1", "default", true),
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

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
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

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
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

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "SetModEnabled touches a different column and must still succeed")
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

// --- OrderByProfile (Task 1) ---

// TestOrderByProfile table-drives core.OrderByProfile's contract: mods
// absent from profile.Mods come first (sorted by "SourceID:ID" key), then
// mods present in profile.Mods in that profile's own order - regardless of
// the input mods slice's order, and with duplicate keys collapsed to one
// occurrence.
func TestOrderByProfile(t *testing.T) {
	im := func(sourceID, id string) domain.InstalledMod {
		return domain.InstalledMod{Mod: domain.Mod{SourceID: sourceID, ID: id}}
	}
	ref := func(sourceID, id string) domain.ModReference {
		return domain.ModReference{SourceID: sourceID, ModID: id}
	}
	keysOf := func(mods []domain.InstalledMod) []string {
		keys := make([]string, len(mods))
		for i, m := range mods {
			keys[i] = domain.ModKey(m.SourceID, m.ID)
		}
		return keys
	}

	tests := []struct {
		name     string
		profile  *domain.Profile
		mods     []domain.InstalledMod
		expected []string
	}{
		{
			name:     "listed mods follow profile order regardless of input order",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "c"), ref("src", "a"), ref("src", "b")}},
			mods:     []domain.InstalledMod{im("src", "a"), im("src", "b"), im("src", "c")},
			expected: []string{"src:c", "src:a", "src:b"},
		},
		{
			name:     "unlisted mods sort first by key, then listed mods follow profile order",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "b")}},
			mods:     []domain.InstalledMod{im("src", "z"), im("src", "b"), im("src", "a")},
			expected: []string{"src:a", "src:z", "src:b"},
		},
		{
			name:     "empty profile sorts every mod by key",
			profile:  &domain.Profile{},
			mods:     []domain.InstalledMod{im("src", "c"), im("src", "a"), im("src", "b")},
			expected: []string{"src:a", "src:b", "src:c"},
		},
		{
			name:     "nil profile is treated as empty - sorts every mod by key",
			profile:  nil,
			mods:     []domain.InstalledMod{im("src", "c"), im("src", "a")},
			expected: []string{"src:a", "src:c"},
		},
		{
			name:     "a mod repeated in profile.Mods is not duplicated in the output",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "a"), ref("src", "a"), ref("src", "b")}},
			mods:     []domain.InstalledMod{im("src", "a"), im("src", "b")},
			expected: []string{"src:a", "src:b"},
		},
		{
			name:     "empty mods with a non-empty profile returns empty",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "a")}},
			mods:     nil,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := core.OrderByProfile(tt.profile, tt.mods)
			assert.Equal(t, tt.expected, keysOf(got))
		})
	}
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

// TestService_DeployProfile_DeployOrderWinsFileConflicts guards Task 1's
// core motivation: two mods that each own a file at the same relative game
// path race for "who deploys last, wins" - and before this task, that race
// was decided by Go's randomized map iteration, not profile.Mods' documented
// load order (first = lowest priority). Deploying twice - once per profile
// order - proves the winner tracks profile order, not insertion order or
// chance: modY (deployed second, both times) wins the first pass, and after
// ReorderMods flips it to deploy first, modX wins instead.
func TestService_DeployProfile_DeployOrderWinsFileConflicts(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "modX", "Mod X", "1.0", true, map[string][]byte{"shared.esp": []byte("X-content")})
	seedNamedInstalledMod(t, svc, game, "src", "modY", "Mod Y", "1.0", true, map[string][]byte{"shared.esp": []byte("Y-content")})

	seedProfileWithMod(t, svc, "g1", "default", "src", "modX", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "modY", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Deployed)

	content, err := os.ReadFile(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, "Y-content", string(content), "modY deploys later (last in profile order) and must win the shared file")

	pm := svc.NewProfileManager()
	require.NoError(t, pm.ReorderMods(game.ID, "default", []domain.ModReference{
		{SourceID: "src", ModID: "modY", Version: "1.0"},
		{SourceID: "src", ModID: "modX", Version: "1.0"},
	}))

	result, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Deployed)

	content, err = os.ReadFile(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, "X-content", string(content), "after reordering the profile, modX now deploys later and must win the shared file")
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

// setProfileLinkMethod stamps an explicit link_method onto an existing profile
// file, as if the user had set it in the profile YAML by hand.
func setProfileLinkMethod(t *testing.T, svc *core.Service, gameID, profileName string, method domain.LinkMethod) {
	t.Helper()
	p, err := config.LoadProfile(svc.ConfigDir(), gameID, profileName)
	require.NoError(t, err)
	p.LinkMethod = method
	p.LinkMethodExplicit = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
}

// TestService_GetEffectiveLinkMethod_Precedence pins #81's documented
// resolution order: profile-explicit > game-explicit > global default
// (symlink in a fresh test service). A missing profile file must degrade to
// the game-level resolution, never error.
func TestService_GetEffectiveLinkMethod_Precedence(t *testing.T) {
	tests := map[string]struct {
		gameMethod     domain.LinkMethod
		gameExplicit   bool
		profileExists  bool
		profileMethod  *domain.LinkMethod
		expectedMethod domain.LinkMethod
	}{
		"profile beats game": {
			gameMethod: domain.LinkHardlink, gameExplicit: true,
			profileExists: true, profileMethod: linkMethodPtr(domain.LinkCopy),
			expectedMethod: domain.LinkCopy,
		},
		"explicit profile symlink beats non-symlink game": {
			gameMethod: domain.LinkCopy, gameExplicit: true,
			profileExists: true, profileMethod: linkMethodPtr(domain.LinkSymlink),
			expectedMethod: domain.LinkSymlink,
		},
		"game wins when profile is silent": {
			gameMethod: domain.LinkHardlink, gameExplicit: true,
			profileExists:  true,
			expectedMethod: domain.LinkHardlink,
		},
		"game wins when profile file is missing": {
			gameMethod: domain.LinkHardlink, gameExplicit: true,
			expectedMethod: domain.LinkHardlink,
		},
		"global default when neither is explicit": {
			gameMethod:     domain.LinkSymlink,
			profileExists:  true,
			expectedMethod: domain.LinkSymlink,
		},
	}

	for label, tc := range tests {
		t.Run(label, func(t *testing.T) {
			svc := newFlowsTestService(t)
			game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: tc.gameMethod, LinkMethodExplicit: tc.gameExplicit}

			if tc.profileExists {
				_, err := svc.NewProfileManager().Create("g1", "default")
				require.NoError(t, err)
				if tc.profileMethod != nil {
					setProfileLinkMethod(t, svc, "g1", "default", *tc.profileMethod)
				}
			}

			method, err := svc.GetEffectiveLinkMethod(game, "default")
			require.NoError(t, err)
			assert.Equal(t, tc.expectedMethod, method)
		})
	}
}

// TestService_GetEffectiveLinkMethod_InvalidLinkMethodSurfaces pins #189: a
// hand-edited profile with an unrecognized link_method must surface as an
// error naming domain.ErrInvalidLinkMethod, not silently degrade to the
// game/global default the way a missing profile file does (the precedence
// test above). Without the fix, GetEffectiveLinkMethod would return the
// game's LinkHardlink with no error - the exact silent-misbehavior #172's
// fail-loud contract exists to prevent, one layer deeper on the deploy path.
func TestService_GetEffectiveLinkMethod_InvalidLinkMethodSurfaces(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkHardlink, LinkMethodExplicit: true}

	_, err := svc.NewProfileManager().Create("g1", "default")
	require.NoError(t, err)
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilePath, append(data, []byte("link_method: bogus\n")...), 0644))

	_, err = svc.GetEffectiveLinkMethod(game, "default")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
}

func linkMethodPtr(m domain.LinkMethod) *domain.LinkMethod {
	return &m
}

// TestService_DeployProfile_ProfileLinkMethodOverridesGame is #81's headline
// failing case: the game explicitly says symlink, the profile explicitly says
// copy, and the profile must win - the deployed file is a real copy, and
// SetModLinkMethod records the effective (profile) method.
func TestService_DeployProfile_ProfileLinkMethodOverridesGame(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	setProfileLinkMethod(t, svc, "g1", "default", domain.LinkCopy)

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	info, err := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "profile-level copy must beat the game's explicit symlink")

	mod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkCopy, mod.LinkMethod, "SetModLinkMethod must record the profile's effective method")
}

// TestService_DeployProfile_CLIMethodOverrideBeatsProfileLinkMethod pins the
// top of the precedence chain: the --method override (DeployOptions.LinkMethod)
// still beats a profile-level link_method.
func TestService_DeployProfile_CLIMethodOverrideBeatsProfileLinkMethod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	setProfileLinkMethod(t, svc, "g1", "default", domain.LinkCopy)

	override := domain.LinkSymlink
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{LinkMethod: &override}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deployed)

	info, err := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	require.NoError(t, err)
	assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "--method symlink must beat the profile's copy")
}

// TestService_DeployProfile_InvalidProfileLinkMethodFailsLoud is #189's
// deploy-path-level proof, one layer up from
// TestService_GetEffectiveLinkMethod_InvalidLinkMethodSurfaces: a profile
// with a hand-edited, unrecognized link_method must VISIBLY fail the deploy
// rather than silently deploying with the game's method. Before the fix,
// this would have deployed 1 mod with a real symlink at gameDir/plugin.esp;
// after, DeployProfile errors and nothing is written.
func TestService_DeployProfile_InvalidProfileLinkMethodFailsLoud(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilePath, append(data, []byte("link_method: bogus\n")...), 0644))

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
	if result != nil {
		assert.Zero(t, result.Deployed, "an invalid link_method must deploy nothing, not fall back to the game's method")
	}
	_, statErr := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(statErr), "no file may be deployed when the effective link method can't be resolved")
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

// TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent
// guards finding D1: when the redownload itself starts (GetMod/GetModFiles
// succeed) but the actual file download fails, redeployFromSource must emit
// a DeployDownloadFailed event - not DeploySkipped - so cmd/lmm's dedicated
// DeployDownloadFailed handler (blank line / "✗ <mod> - <detail>" / blank
// line, matching the pre-extraction CLI) actually fires instead of
// DeploySkipped's bare, unpadded "✗" line. result.Skipped accounting must be
// unchanged (the mod still gets exactly one "<mod>: <reason>" entry), and
// DeploySkipped must NOT also fire for this mod - that would double-print
// under cmd/lmm's handler. Uses mockSourceWithDownloads without ever calling
// AddDownload, so its httptest server 404s the file request deterministically.
func TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	// Deliberately no AddDownload call - the mock's httptest server 404s any
	// file request, making the download fail deterministically.

	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Download Fail Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          *mockMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "a per-mod download failure must not fail the whole deploy")
	require.NotNil(t, result)

	assert.Equal(t, 0, result.Deployed)
	require.Len(t, result.Skipped, 1, "accounting must be unchanged: exactly one Skipped entry")
	assert.Contains(t, result.Skipped[0], "Download Fail Mod: download failed:")

	var failEvt *core.DeployProgress
	for i := range events {
		assert.NotEqual(t, core.DeploySkipped, events[i].Phase,
			"DeploySkipped must not also fire for a download failure - see DeploySkipped's doc comment ('a reason other than a hook or download failure') and cmd/lmm/deploy.go's DeploySkipped handler, which would double-print alongside DeployDownloadFailed's")
		if events[i].Phase == core.DeployDownloadFailed {
			failEvt = &events[i]
		}
	}
	require.NotNil(t, failEvt, "DeployDownloadFailed event must fire on download failure")
	assert.Equal(t, "Download Fail Mod", failEvt.ModName)
	assert.Equal(t, "1", failEvt.ModID)
	assert.Contains(t, failEvt.Detail, "download failed:")
}

// TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError guards
// #95: when a mod's stored file IDs (mod.FileIDs) no longer match any file
// the source currently offers, redeployFromSource must fail that mod
// (result.Skipped + DeploySkipped, via selectDeployFiles's allowFallback=
// false) with a clear, actionable error - never silently substitute the
// primary file (the old DeployFallbackUsed phase, removed entirely in the
// renderer-cleanup task B2). Mirrors
// TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent's
// negative-assertion idiom.
func TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	// Deliberately no AddDownload call needed - selectDeployFiles must fail
	// before any download is attempted.

	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Stale File Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)
	// mockSource.GetModFiles always returns a single file with ID "1" -
	// "stale-id" never matches it.

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          *mockMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"stale-id"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "a per-mod stored-file-gone failure must not fail the whole deploy")
	require.NotNil(t, result)

	assert.Equal(t, 0, result.Deployed, "the mod must not be deployed via fallback substitution")
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "no longer available upstream")
	assert.Contains(t, result.Skipped[0], "stale-id")

	var sawSkipped bool
	for i := range events {
		if events[i].Phase == core.DeploySkipped {
			sawSkipped = true
		}
	}
	assert.True(t, sawSkipped, "expected a DeploySkipped event")
}

// TestService_DeployProfile_StoredIDsGone_HealsToRecordedVersion is #96's
// DeployProfile-flavored healing guard (issue #96 names DeployProfile
// explicitly): the installed mod's stored FileIDs ("999") no longer match
// anything upstream, but its recorded Version ("1.0") still resolves to the
// source's archived file "9". With the cache dir missing (forcing
// redeployFromSource), the deploy must succeed using the recorded version's
// file rather than the #95 skip - never silently substituting the source's
// current primary file (1.5/"10").
func TestService_DeployProfile_StoredIDsGone_HealsToRecordedVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// DB row is pinned at 1.0 with stale FileIDs; nothing stored in the
	// cache, forcing redeployFromSource.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"999"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	var phases []core.DeployPhase
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		phases = append(phases, p.Phase)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed, "must heal to the recorded version, not skip")
	assert.Empty(t, result.Skipped)
	assert.Contains(t, phases, core.DeployRedownloading)

	_, err = os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
	assert.NoError(t, err, "the recorded 1.0 file's payload should be deployed, not the source's current primary")
	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the source's current primary (1.5) file must not be deployed via fallback")
}

// TestService_DeployProfile_StoredIDsGone_HealPersistsFileIDs is #139 item 1:
// a successful redeployFromSource heal must persist the healed FileIDs onto
// the DB row (via the targeted SetModFileIDs setter - never a full-row save),
// so `profile export` emits the live IDs instead of the dead ones and later
// cache misses resolve from the stored IDs' fast path instead of re-healing.
func TestService_DeployProfile_StoredIDsGone_HealPersistsFileIDs(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// DB row pinned at 1.0 with dead FileIDs; nothing in the cache, forcing
	// redeployFromSource to heal by version match (file "9").
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"999"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Deployed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"9"}, installed.FileIDs,
		"a successful heal must persist the healed FileIDs, not keep the dead ones")

	data, err := svc.NewProfileManager().Export("g1", "default")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "999",
		"profile export must no longer emit the dead FileIDs after a heal")
	assert.Contains(t, string(data), "9",
		"profile export must emit the healed FileID")
}

// TestService_DeployProfile_CacheMissRedownload_SameIDsPreserveChecksums is
// #139 item 1's checksum guard (the PR #128 SaveInstalledMod lesson applied
// to SetModFileIDs): when a cache-miss redownload resolves to the SAME stored
// FileIDs (no heal happened), the persist step must not rewrite the
// installed_mod_files rows - a blind delete+reinsert would silently drop
// their recorded checksums.
func TestService_DeployProfile_CacheMissRedownload_SameIDsPreserveChecksums(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// DB row whose stored FileIDs still match the source's 1.0 file ("9"),
	// with a recorded checksum; nothing in the cache, forcing a redownload
	// that resolves to the very same IDs.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"9"},
	}))
	require.NoError(t, svc.SaveFileChecksum("src", "mod1", "g1", "default", "9", "abc123"))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Deployed)

	files, err := svc.GetFilesWithChecksums("g1", "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "9", files[0].FileID)
	assert.Equal(t, "abc123", files[0].Checksum,
		"an unchanged FileIDs set must keep its recorded checksum through a cache-miss redownload")
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

// --- Fix wave 1: progress-event positioning (review findings) ---
//
// The tests below guard DeployProgress events added to restore the
// pre-extraction CLI's console positioning for diagnostics that Task 3
// correctly accumulated into DeployResult.Warnings/Notes but only surfaced
// via progress events for a subset of cases (DeployBeforeEachSkipped/
// DeployDownloadFailed/DeploySkipped). See the task-3-report.md "Fix wave 1"
// entry for the full mapping.

// TestService_DeployProfile_ForcedBeforeAllWarning_EmitsEventBeforeAnythingElse
// guards finding 1 (deploy side): a forced install.before_all failure must
// be reported via a DeployBeforeAllForced event before any other event -
// the pre-extraction CLI printed this warning as the very first line of
// output, before the "Deploying N mod(s)..." header.
func TestService_DeployProfile_ForcedBeforeAllWarning_EmitsEventBeforeAnythingElse(t *testing.T) {
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

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Hooks: hooks, HookRunner: runner, Force: true,
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotEmpty(t, events)
	assert.Equal(t, core.DeployBeforeAllForced, events[0].Phase, "the forced before_all warning must be the first event emitted")
	assert.Contains(t, events[0].Detail, "install.before_all hook failed")
	assert.Contains(t, events[0].Detail, "forced")
	assert.Equal(t, events[0].Detail, result.Warnings[0], "the event's Detail must match the recorded Warning text verbatim")

	require.Greater(t, len(events), 1, "at least one later event (the mod itself deploying) must exist")
	assert.NotEqual(t, core.DeployBeforeAllForced, events[1].Phase)
}

// TestService_DeployProfile_PurgeForcedBeforeAllWarning_EmitsEventBeforePurgingEvent
// guards finding 1 (purge side): a forced uninstall.before_all failure
// during --purge must be reported before the DeployPurging event (which the
// CLI uses to print the "Purging N mod(s) before deploy..." header).
func TestService_DeployProfile_PurgeForcedBeforeAllWarning_EmitsEventBeforePurgingEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, Hooks: hooks, HookRunner: runner, Force: true,
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	require.NotNil(t, result)

	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, core.DeployBeforeAllForced, events[0].Phase)
	assert.Contains(t, events[0].Detail, "uninstall.before_all hook failed")
	assert.Contains(t, events[0].Detail, "forced")
	assert.Equal(t, core.DeployPurging, events[1].Phase, "the purge header event must come right after the forced warning")
}

// TestService_DeployProfile_PerModNoteDiagnostics_CarryModAttributionAndPrecedeSuccessEvent
// guards finding 3 (deploy loop): a failed SetModLinkMethod and a failed
// SetModDeployed both produce text with NO mod identity in it
// ("Warning: could not update link method: ..."), so position (via the
// event's ModName/ModID and its place in the event stream, before that same
// mod's DeployDeployed event) is the ONLY way to attribute either
// diagnostic to a mod. Two mods are seeded so a batched/misattributed
// implementation is distinguishable from a correctly-interleaved one: both
// mods' Note events must appear before THEIR OWN DeployDeployed event, not
// after both mods have already "succeeded".
//
// SetModLinkMethod/SetModDeployed are both plain UPDATEs against
// installed_mods, but Install/Uninstall also write to the DB (deployed_files,
// via SaveDeployedFile/DeleteDeployedFiles) - so a blanket write-lock
// (the BEGIN IMMEDIATE technique
// TestService_UninstallMod_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult
// uses) would fail Install itself before ever reaching SetModLinkMethod/
// SetModDeployed, defeating the test. Instead, a second connection installs
// a real SQLite trigger that aborts ONLY updates to installed_mods'
// link_method/deployed columns, leaving every other table (deployed_files)
// and every other installed_mods column untouched - deterministic, and
// narrow enough that Install/Uninstall still succeed normally.
func TestService_DeployProfile_PerModNoteDiagnostics_CarryModAttributionAndPrecedeSuccessEvent(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")

	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "SetModLinkMethod/SetModDeployed failures must not fail the deploy")
	require.NotNil(t, result)
	assert.Equal(t, 2, result.Deployed, "both mods must still deploy despite the bookkeeping failures")
	require.Len(t, result.Notes, 4, "2 mods x (link-method + mark-deployed) failures")

	// Find each mod's DeployDeployed index and confirm its two DeployNote
	// events (with matching ModName/ModID) both appear before it.
	for _, modName := range []string{"Mod A", "Mod B"} {
		var noteIdxs []int
		var deployedIdx = -1
		for i, e := range events {
			if e.ModName != modName {
				continue
			}
			switch e.Phase {
			case core.DeployNote:
				noteIdxs = append(noteIdxs, i)
			case core.DeployDeployed:
				deployedIdx = i
			}
		}
		require.Len(t, noteIdxs, 2, "%s must have exactly 2 DeployNote events (link-method + mark-deployed)", modName)
		require.NotEqual(t, -1, deployedIdx, "%s must have a DeployDeployed event", modName)
		for _, ni := range noteIdxs {
			assert.Less(t, ni, deployedIdx, "%s's Note events must precede its own DeployDeployed event", modName)
		}
	}
}

// TestService_DeployProfile_UndeployFailureEmitsNoteEventBeforeSuccessEvent
// guards finding 3's third deploy-loop diagnostic (undeploy-before-redeploy
// failure, flows.go's "Warning: undeploy %s: %v" - the only one of the
// three whose text DOES carry a mod name already), corrupting a previously
// deployed symlink into a plain file so the redeploy's own undeploy step
// fails deterministically, mirroring
// TestService_DisableMod_UndeployFailureIsNonFatal.
func TestService_DeployProfile_UndeployFailureEmitsNoteEventBeforeSuccessEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: undeploy Test Mod: "))

	require.Len(t, events, 2)
	assert.Equal(t, core.DeployNote, events[0].Phase)
	assert.Equal(t, "Test Mod", events[0].ModName)
	assert.Equal(t, "1", events[0].ModID)
	assert.Equal(t, result.Notes[0], events[0].Detail)
	assert.Equal(t, core.DeployDeployed, events[1].Phase, "the Note event must precede the success event")
}

// TestService_DeployProfile_PurgeBeforeEachSkip_EmitsWarningEventWithModAttribution
// guards finding 3's purge-side case: purgeForDeploy's before_each-skip
// diagnostic must fire a PurgeWarning event with the skipped mod's
// attribution, at the point it happens (inline with that mod), not batched.
func TestService_DeployProfile_PurgeBeforeEachSkip_EmitsWarningEventWithModAttribution(t *testing.T) {
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: beforeEachScript}}
	runner := core.NewHookRunner(5 * time.Second)

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, All: true, Hooks: hooks, HookRunner: runner,
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	require.NotNil(t, result)

	var found *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.PurgeWarning && events[i].ModName == "Bad Mod" {
			found = &events[i]
			break
		}
	}
	require.NotNil(t, found, "expected a PurgeWarning event attributed to Bad Mod")
	assert.Equal(t, "bad", found.ModID)
	assert.Contains(t, found.Detail, "uninstall.before_each hook failed")
	assert.Contains(t, result.Warnings, found.Detail, "the event's Detail must match the recorded Warning text verbatim")
}

// TestService_DeployProfile_PurgeUndeployFailureEmitsNoteEvent guards
// finding 3's "finish the pattern" scope: purgeForDeploy's own per-mod ⚠
// undeploy-failure Note (previously batched, same as the deploy loop's
// equivalent) must fire inline via a PurgeNote event. Reuses the
// symlink-corruption technique, then triggers a --purge deploy so purge's
// own Uninstall call hits the same "not a symlink" failure.
func TestService_DeployProfile_PurgeUndeployFailureEmitsNoteEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{Purge: true}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var found *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.PurgeNote {
			found = &events[i]
			break
		}
	}
	require.NotNil(t, found, "expected a PurgeNote event for the purge-phase undeploy failure")
	assert.Equal(t, "Test Mod", found.ModName)
	assert.True(t, strings.HasPrefix(found.Detail, "⚠ Test Mod - "))
	assert.Contains(t, result.Notes, found.Detail)

	// PurgeNote must be emitted before DeployPurging's redeploy-phase
	// events (it belongs to the purge phase).
	purgingIdx, noteIdx := -1, -1
	for i, e := range events {
		if e.Phase == core.DeployPurging {
			purgingIdx = i
		}
		if e.Phase == core.PurgeNote && noteIdx == -1 {
			noteIdx = i
		}
	}
	require.NotEqual(t, -1, purgingIdx)
	assert.Greater(t, noteIdx, purgingIdx, "the purge-phase note must come after the DeployPurging header event, still within the purge phase")
}

// TestService_DeployProfile_PurgeBeforeEachSkip_WarningTextExact pins the
// deploy --purge before_each-skip Warning's exact wording (in particular
// its "during purge (not purged)" tail and NAME attribution), which
// deliberately differs from `lmm purge`'s treatment of the same skip
// (PurgeResult.Skipped + PurgeModSkipped) - a #61 divergence preserved
// through the shared-loop convergence.
func TestService_DeployProfile_PurgeBeforeEachSkip_WarningTextExact(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "bad", "Bad Mod", "1.0", true, map[string][]byte{"bad.esp": []byte("b")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "bad", "1.0")

	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: beforeEachScript}}

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, All: true, Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, nil)
	require.NoError(t, err)

	require.NotEmpty(t, result.Warnings)
	assert.True(t, strings.HasPrefix(result.Warnings[0], "uninstall.before_each hook failed for Bad Mod during purge (not purged): "),
		"got: %q", result.Warnings[0])
}

// TestService_DeployProfile_PurgeAfterEachWarning_UsesModID pins the
// deploy --purge after_each Warning's historical mod-ID attribution
// ("for <id>", not "for <name>") - previously untested wording, and the
// other side of a #61 divergence: `lmm purge` (doPurge, and now
// PurgeProfile) uses the mod NAME in the same warning.
func TestService_DeployProfile_PurgeAfterEachWarning_UsesModID(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "mod-id-1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("d")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod-id-1", "1.0")

	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{AfterEach: afterEachScript}}

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, nil)
	require.NoError(t, err)

	var found string
	for _, w := range result.Warnings {
		if strings.HasPrefix(w, "uninstall.after_each hook failed for ") {
			found = w
			break
		}
	}
	require.NotEmpty(t, found, "expected a purge-pass after_each warning; got %v", result.Warnings)
	assert.True(t, strings.HasPrefix(found, "uninstall.after_each hook failed for mod-id-1: "),
		"deploy --purge attributes by mod ID, got: %q", found)
}

// TestService_DeployProfile_OverridesWarningEmittedBeforeDeferredHookWarnings
// guards finding 2: the pre-extraction CLI printed the profile-overrides
// warning (computed and printed immediately once the deploy loop and
// install.after_all hook had already run) BEFORE its batched hook-warning
// print (install.after_each entries in mod order, then install.after_all) -
// even though, in both the pre-extraction CLI and this flow, after_each/
// after_all are computed earlier in the function than the overrides check.
// DeployProfile reproduces this by deferring the after_each/after_all
// DeployWarning events (queued, not emitted immediately) until after the
// overrides DeployWarning has been emitted - execution order (and the
// Warnings slice's append order) is unchanged; only the moment each event
// is *emitted* (and hence printed) is deferred.
func TestService_DeployProfile_OverridesWarningEmittedBeforeDeferredHookWarnings(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	installDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, InstallPath: installDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	profile, err := svc.NewProfileManager().Get("g1", "default")
	require.NoError(t, err)
	// An absolute override path is rejected by ApplyProfileOverrides
	// deterministically, with no filesystem trickery required.
	profile.Overrides = map[string][]byte{"/etc/passwd": []byte("x")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{AfterEach: afterEachScript, AfterAll: afterAllScript}}
	runner := core.NewHookRunner(5 * time.Second)

	var events []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Hooks: hooks, HookRunner: runner,
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	require.Len(t, result.Warnings, 3, "after_each + after_all + overrides")

	overridesIdx, afterEachIdx, afterAllIdx := -1, -1, -1
	for i, e := range events {
		if e.Phase != core.DeployWarning {
			continue
		}
		switch {
		case strings.Contains(e.Detail, "applying profile overrides"):
			overridesIdx = i
		case strings.Contains(e.Detail, "after_each"):
			afterEachIdx = i
		case strings.Contains(e.Detail, "after_all"):
			afterAllIdx = i
		}
	}
	require.NotEqual(t, -1, overridesIdx, "expected an overrides DeployWarning event")
	require.NotEqual(t, -1, afterEachIdx, "expected an after_each DeployWarning event")
	require.NotEqual(t, -1, afterAllIdx, "expected an after_all DeployWarning event")
	assert.Less(t, overridesIdx, afterEachIdx, "overrides warning must be emitted before the after_each hook warning")
	assert.Less(t, overridesIdx, afterAllIdx, "overrides warning must be emitted before the after_all hook warning")
}

// TestService_DeployProfile_ContextCancelledBetweenMods_ReturnsPartialResultWithCtxErr
// guards Task 6 item d: DeployProfile's per-mod loop must check ctx between
// iterations, aborting BETWEEN mods (never mid-file-operation) with the
// established partial-result convention (see
// TestService_DeployProfile_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult) -
// the seam 5b's cancel-then-drain quit path (TUI) relies on. The progress
// callback cancels ctx the instant the first mod's DeployDeployed event
// fires; the second and third mods must never be touched at all.
func TestService_DeployProfile_ContextCancelledBetweenMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	seedNamedInstalledMod(t, svc, game, "src", "c", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "c", "1.0")

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.DeployDeployed && p.ModName == "Mod A" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result, "diagnostics/progress accumulated before cancellation must not be discarded")
	assert.Equal(t, 1, result.Deployed, "only the mod already fully deployed before cancellation was observed counts")

	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "Mod A must have fully deployed before cancellation")
	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "Mod B must never have been touched - cancellation lands BETWEEN mods")
	_, err = os.Lstat(filepath.Join(gameDir, "c.esp"))
	assert.True(t, os.IsNotExist(err), "Mod C must never have been touched")
}

// --- PlanProfileSwitch / ApplyProfileSwitch (Task 4) ---
//
// These extract doProfileSwitch (cmd/lmm/profile.go) into a pure diff
// computation (PlanProfileSwitch) plus an execution step (ApplyProfileSwitch)
// that reuses the DeployProgress carrier/phase-constant pattern established
// by Task 3, extended with Switch*-prefixed phases rather than a parallel
// SwitchProgress type - see the task report for the full phase mapping.

// seedInstalledModUnderProfile is seedNamedInstalledMod with a
// caller-supplied ProfileName, needed because the pre-extraction
// doProfileSwitch's enable-loop calls SetModEnabled(..., targetName, ...) -
// the NEW profile's name, not the mod's own current ProfileName (see the
// task report) - so a toEnable mod's DB row must already live under the
// target profile name for that call to find and update it.
func seedInstalledModUnderProfile(t *testing.T, svc *core.Service, game *domain.Game, profileName, sourceID, modID, name, version string, enabled bool, files map[string][]byte) {
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
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// installEnabledBlockingTrigger mirrors installBlockingTrigger but targets
// installed_mods.enabled specifically, isolating SetModEnabled failures from
// SetModLinkMethod/SetModDeployed (which installBlockingTrigger blocks) or
// any other column.
func installEnabledBlockingTrigger(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_enabled_updates
		BEFORE UPDATE OF enabled ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}

// --- PlanProfileSwitch ---

func TestService_PlanProfileSwitch_AlreadyActive(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.AlreadyActive)
	assert.Equal(t, "g1", plan.GameID)
	assert.Equal(t, "default", plan.From)
	assert.Equal(t, "default", plan.To)
	assert.False(t, plan.NoChanges)
	assert.Empty(t, plan.ToDisable)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToInstall)
}

// TestService_PlanProfileSwitch_NoChangesWhenModSetsMatch guards the no-op
// fast path: when the target profile's mod set already matches what's
// enabled under the current default profile, PlanProfileSwitch reports
// NoChanges (only SetDefault is needed) - mirroring doProfileSwitch's
// "No mod changes, just switch the default" branch.
func TestService_PlanProfileSwitch_NoChangesWhenModSetsMatch(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "other")
	require.NoError(t, err)

	seedInstalledMod(t, svc, game, "src", "shared", "1.0", true, map[string][]byte{"shared.esp": []byte("s")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "other", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "other")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.False(t, plan.AlreadyActive)
	assert.True(t, plan.NoChanges)
	assert.Empty(t, plan.ToDisable)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToInstall)
}

// TestService_PlanProfileSwitch_ComputesDisableEnableInstallBuckets guards
// the diff algorithm's three buckets in one mixed scenario: a mod only in
// the current profile's enabled set goes to ToDisable, a mod installed (and
// cached) but disabled that the target references goes to ToEnable, and a
// mod the target references with no DB row at all goes to ToInstall.
func TestService_PlanProfileSwitch_ComputesDisableEnableInstallBuckets(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	// modC: enabled under "default", absent from "target" -> ToDisable.
	seedNamedInstalledMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "modC", Version: "1.0"}))

	// modB: installed (under "default") but disabled, cached, referenced by
	// "target" -> ToEnable.
	seedNamedInstalledMod(t, svc, game, "src", "modB", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modB", Version: "1.0"}))

	// modD: referenced by "target" only, no DB row at all -> ToInstall.
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modD", Version: "2.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.False(t, plan.NoChanges)

	require.Len(t, plan.ToDisable, 1)
	assert.Equal(t, "modC", plan.ToDisable[0].ID)

	require.Len(t, plan.ToEnable, 1)
	assert.Equal(t, "modB", plan.ToEnable[0].ID)

	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "modD", plan.ToInstall[0].ModID)
	assert.Equal(t, "2.0", plan.ToInstall[0].Version)
}

// TestService_PlanProfileSwitch_DeterministicOrder guards Task 1's
// PlanProfileSwitch fix: ToDisable/ToEnable/ToInstall used to be built by
// ranging Go maps (currentEnabled/targetKeys), so their element order was
// arbitrary from run to run. This seeds three mods per bucket with
// profile.Mods orders that deliberately don't match alphabetical or
// insertion order, plans twice, and asserts: (1) both runs produce identical
// slices, and (2) ToDisable follows the FROM profile's ("default") Mods
// order while ToEnable/ToInstall follow the TO profile's ("target") Mods
// order - interleaved in a single Mods list, to prove each bucket
// independently preserves its relative order within that shared list.
func TestService_PlanProfileSwitch_DeterministicOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	// ToDisable candidates: enabled under "default", absent from "target".
	// default.Mods is deliberately not alphabetical (disC, disA, disB).
	for _, id := range []string{"disC", "disA", "disB"} {
		seedNamedInstalledMod(t, svc, game, "src", id, "Dis "+id, "1.0", true, nil)
		require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}

	// ToEnable candidates: installed (under "default") but disabled, cached,
	// referenced by "target".
	for _, id := range []string{"enA", "enB", "enC"} {
		seedNamedInstalledMod(t, svc, game, "src", id, "En "+id, "1.0", false, map[string][]byte{id + ".dat": []byte(id)})
	}

	// ToInstall candidates: referenced by "target" only, no DB row at all.
	// target.Mods interleaves enable/install refs in an order that matches
	// neither alphabetical nor insertion order for either sub-sequence.
	for _, id := range []string{"enC", "insB", "enA", "insC", "enB", "insA"} {
		require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}

	plan1, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	plan2, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)

	assert.Equal(t, plan1.ToDisable, plan2.ToDisable, "two runs must produce identical ToDisable order")
	assert.Equal(t, plan1.ToEnable, plan2.ToEnable, "two runs must produce identical ToEnable order")
	assert.Equal(t, plan1.ToInstall, plan2.ToInstall, "two runs must produce identical ToInstall order")

	var disableIDs []string
	for _, im := range plan1.ToDisable {
		disableIDs = append(disableIDs, im.ID)
	}
	assert.Equal(t, []string{"disC", "disA", "disB"}, disableIDs, "ToDisable must follow the FROM profile's (default) Mods order")

	var enableIDs []string
	for _, im := range plan1.ToEnable {
		enableIDs = append(enableIDs, im.ID)
	}
	assert.Equal(t, []string{"enC", "enA", "enB"}, enableIDs, "ToEnable must follow the TO profile's (target) Mods order")

	var installIDs []string
	for _, ref := range plan1.ToInstall {
		installIDs = append(installIDs, ref.ModID)
	}
	assert.Equal(t, []string{"insB", "insC", "insA"}, installIDs, "ToInstall must follow the TO profile's (target) Mods order")
}

// TestService_PlanProfileSwitch_CacheMissForcesReinstallWithPreservedFileIDs
// guards doProfileSwitch's re-download branch: when a mod the target
// profile references IS installed in the DB but its cache entry is gone,
// PlanProfileSwitch classifies it as ToInstall (not ToEnable) and carries
// over the installed mod's own FileIDs (not the profile YAML's, which may be
// empty or stale) so the redownload uses the same files.
func TestService_PlanProfileSwitch_CacheMissForcesReinstallWithPreservedFileIDs(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	// DB row exists (with FileIDs), but nothing was ever stored in the cache.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "modE", SourceID: "src", Name: "Mod E", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"f1", "f2"},
	}))
	// Profile YAML's own FileIDs are deliberately absent, to prove
	// PlanProfileSwitch uses the INSTALLED mod's FileIDs, not the profile's.
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modE", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)

	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "modE", plan.ToInstall[0].ModID)
	assert.Equal(t, []string{"f1", "f2"}, plan.ToInstall[0].FileIDs)
	assert.Empty(t, plan.ToEnable, "a cache-miss mod must be reinstalled, not merely re-enabled")
}

func TestService_PlanProfileSwitch_UnknownTargetProfileReturnsError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile not found: missing")
	assert.Nil(t, plan)
}

// TestService_PlanProfileSwitch_PerformsZeroMutations guards the
// "pure computation" contract the TUI depends on: calling
// PlanProfileSwitch speculatively (e.g. to render a confirmation modal) and
// discarding the result must leave the DB, cache, and profile YAMLs
// byte-for-byte untouched.
func TestService_PlanProfileSwitch_PerformsZeroMutations(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "modC", Version: "1.0"}))
	seedNamedInstalledMod(t, svc, game, "src", "modB", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modB", Version: "1.0"}))
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "modD", Version: "2.0"}))

	defaultPath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	targetPath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "target.yaml")
	beforeDefault, err := os.ReadFile(defaultPath)
	require.NoError(t, err)
	beforeTarget, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	beforeMods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.False(t, plan.NoChanges, "sanity: this scenario must exercise all three diff buckets")

	afterDefault, err := os.ReadFile(defaultPath)
	require.NoError(t, err)
	afterTarget, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, beforeDefault, afterDefault, "profile YAML must be byte-for-byte unchanged after planning")
	assert.Equal(t, beforeTarget, afterTarget, "profile YAML must be byte-for-byte unchanged after planning")

	afterMods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)
	assert.Equal(t, beforeMods, afterMods, "DB rows must be untouched after planning")

	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "planning must not deploy any files")
}

// --- ApplyProfileSwitch ---

// TestService_ApplyProfileSwitch_ExecutesDisableThenEnableThenInstall_SetDefaultLastAndUnchangedOnFailure
// guards doProfileSwitch's overall execution order (disable loop, then
// enable loop, then install loop, with SetDefault always last) and the
// error-path convention: SetDefault's own failure must not discard the
// Disabled/Enabled/Installed accounting from everything that already ran
// before it, and must leave the previous default profile in place. The
// target profile is deliberately never created, so both the install loop's
// UpsertMod call and the final SetDefault call fail deterministically
// (ErrProfileNotFound) - UpsertMod's failure is expected and non-fatal (see
// TestService_ApplyProfileSwitch_FatalSetDefaultErrorAfterAccumulatedDiagnostics_ReturnsPartialResult
// for a dedicated, isolated test of that same convention).
func TestService_ApplyProfileSwitch_ExecutesDisableThenEnableThenInstall_SetDefaultLastAndUnchangedOnFailure(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	seedNamedInstalledMod(t, svc, game, "src", "disable-me", "Disable Me", "1.0", true, map[string][]byte{"disable.esp": []byte("d")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "disable-me", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	seedInstalledModUnderProfile(t, svc, game, "target", "src", "enable-me", "Enable Me", "1.0", false, map[string][]byte{"enable.esp": []byte("e")})

	disableMod, err := svc.GetInstalledMod("src", "disable-me", "g1", "default")
	require.NoError(t, err)
	enableMod, err := svc.GetInstalledMod("src", "enable-me", "g1", "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"install.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "install-me", SourceID: "src", Name: "Install Me", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
		ToEnable:  []domain.InstalledMod{*enableMod},
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "install-me", Version: "1.0"}},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.Error(t, err, "SetDefault must fail deterministically: target profile was never created")
	assert.Contains(t, err.Error(), "setting default profile")
	require.NotNil(t, result, "counts/diagnostics accumulated before the fatal SetDefault error must not be discarded")
	assert.Equal(t, 1, result.Disabled)
	assert.Equal(t, 1, result.Enabled)
	assert.Equal(t, 1, result.Installed)
	require.Len(t, result.Notes, 1, "the install loop's UpsertMod failure (target profile doesn't exist) must be recorded")
	assert.Contains(t, result.Notes[0], "could not update profile")

	var disabledIdx, enabledIdx, installingIdx = -1, -1, -1
	for i, e := range events {
		switch e.Phase {
		case core.SwitchDisabled:
			disabledIdx = i
		case core.SwitchEnabled:
			if enabledIdx == -1 {
				enabledIdx = i
			}
		case core.SwitchInstalling:
			installingIdx = i
		}
	}
	require.NotEqual(t, -1, disabledIdx, "expected a SwitchDisabled event")
	require.NotEqual(t, -1, enabledIdx, "expected a SwitchEnabled event")
	require.NotEqual(t, -1, installingIdx, "expected a SwitchInstalling event")
	assert.Less(t, disabledIdx, enabledIdx, "disable phase must complete before the enable phase starts")
	assert.Less(t, enabledIdx, installingIdx, "enable phase must complete before the install phase starts")

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", def.Name, "a failed SetDefault must leave the previous default profile in place")
}

// TestService_ApplyProfileSwitch_UsesTargetProfileLinkMethod guards #81's
// no-override-hook path: a switch INTO a profile with an explicit link_method
// must deploy the target's mods with the TARGET profile's method, not the
// game's. Game explicitly symlink, target profile explicitly copy - the
// enabled mod's file must land as a real copy.
func TestService_ApplyProfileSwitch_UsesTargetProfileLinkMethod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)
	setProfileLinkMethod(t, svc, "g1", "target", domain.LinkCopy)

	seedInstalledModUnderProfile(t, svc, game, "target", "src", "enable-me", "Enable Me", "1.0", false, map[string][]byte{"enable.esp": []byte("e")})
	enableMod, err := svc.GetInstalledMod("src", "enable-me", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*enableMod},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Enabled)
	assert.Empty(t, result.Notes)

	info, err := os.Lstat(filepath.Join(gameDir, "enable.esp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "the target profile's explicit copy must beat the game's explicit symlink")
}

// TestService_ApplyProfileSwitch_DisableLoopUsesSourceProfileLinkMethod pins
// the awkward half of #81's two-profile span: the disable loop undeploys the
// FROM profile's mods, so it must use the FROM profile's method. The from
// profile is explicitly copy (its file on disk is a real file); undeploying
// with the game/target symlink method would refuse ("not a symlink") and
// leave the file behind with a Note.
func TestService_ApplyProfileSwitch_DisableLoopUsesSourceProfileLinkMethod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)
	setProfileLinkMethod(t, svc, "g1", "default", domain.LinkCopy)

	seedNamedInstalledMod(t, svc, game, "src", "disable-me", "Disable Me", "1.0", true, map[string][]byte{"disable.esp": []byte("d")})
	copyInstaller := svc.NewInstallerWithLinker(game, svc.GetLinker(domain.LinkCopy))
	require.NoError(t, copyInstaller.Install(context.Background(), game, &domain.Mod{ID: "disable-me", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	deployedPath := filepath.Join(gameDir, "disable.esp")
	info, err := os.Lstat(deployedPath)
	require.NoError(t, err, "precondition: the from-profile's mod must be deployed")
	require.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "precondition: the from-profile deployment must be a real copy")

	disableMod, err := svc.GetInstalledMod("src", "disable-me", "g1", "default")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Disabled)
	assert.Empty(t, result.Notes, "undeploying the from-profile's copy with the from-profile's method must not error")

	_, err = os.Lstat(deployedPath)
	assert.True(t, os.IsNotExist(err), "the disable loop must remove the from-profile's copied file")
}

// TestService_ApplyProfileSwitch_DisableLoop_UndeployAndSetEnabledFailuresAreNonFatalNotes_SuccessEventStillFires
// guards doProfileSwitch's disable-loop semantics: BOTH a failed Uninstall
// and a failed SetModEnabled are recorded as Notes (never Warnings, never
// fatal) and the mod is still counted as Disabled with its success event
// still firing - the disable loop always "wins" regardless of either
// sub-step's outcome, unlike the enable loop (see the InstallFailureSkipsMod
// test below).
func TestService_ApplyProfileSwitch_DisableLoop_UndeployAndSetEnabledFailuresAreNonFatalNotes_SuccessEventStillFires(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// Corrupt the deployed symlink so Uninstall fails deterministically.
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	// Block updates to installed_mods.enabled so SetModEnabled fails too.
	installEnabledBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	disableMod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Disabled, "the mod must still be counted as disabled despite both failures")
	require.Len(t, result.Notes, 2)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to undeploy Test Mod: "), "note[0]: %q", result.Notes[0])
	assert.True(t, strings.HasPrefix(result.Notes[1], "Warning: failed to update Test Mod: "), "note[1]: %q", result.Notes[1])

	var noteEvents []core.DeployProgress
	disabledIdx := -1
	for i, e := range events {
		if e.Phase == core.SwitchDisableNote {
			noteEvents = append(noteEvents, e)
		}
		if e.Phase == core.SwitchDisabled {
			disabledIdx = i
		}
	}
	require.Len(t, noteEvents, 2)
	assert.Equal(t, result.Notes[0], noteEvents[0].Detail)
	assert.Equal(t, result.Notes[1], noteEvents[1].Detail)
	require.NotEqual(t, -1, disabledIdx, "expected a SwitchDisabled event despite both failures")
	assert.Greater(t, disabledIdx, 0, "the success event must come after the note events")
}

// TestService_ApplyProfileSwitch_EnableLoop_InstallFailureSkipsModEntirely
// guards the enable loop's differing semantics from the disable loop: a
// failed Install is fatal FOR THAT MOD ONLY - it is recorded as a Note, but
// SetModEnabled is never called and no SwitchEnabled event fires, mirroring
// doProfileSwitch's `continue` immediately after the Install failure branch.
func TestService_ApplyProfileSwitch_EnableLoop_InstallFailureSkipsModEntirely(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	// Block deployment deterministically: "blocked" already exists as a
	// regular file, so the linker's os.MkdirAll(filepath.Dir(dst)) fails -
	// mirrors TestService_EnableMod_DeployFailurePropagatesAndLeavesDBUntouched.
	seedInstalledModUnderProfile(t, svc, game, "target", "src", "1", "Test Mod", "1.0", false, map[string][]byte{"blocked/plugin.esp": []byte("data")})
	require.NoError(t, os.WriteFile(filepath.Join(gameDir, "blocked"), []byte("occupied"), 0644))

	enableMod, err := svc.GetInstalledMod("src", "1", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*enableMod},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "an Install failure must not fail the whole switch")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Enabled)
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to deploy Test Mod: "), "note: %q", result.Notes[0])

	for _, e := range events {
		assert.NotEqual(t, core.SwitchEnabled, e.Phase, "no SwitchEnabled event must fire for a mod whose Install failed")
	}

	mod, err := svc.GetInstalledMod("src", "1", "g1", "target")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "SetModEnabled must never be called after a failed Install")
}

// TestService_ApplyProfileSwitch_EnableLoop_SetModEnabledFailureIsNonFatalNote
// guards the enable loop's OTHER sub-step: when Install succeeds but
// SetModEnabled fails, the mod is still counted as Enabled and its success
// event still fires - contrasting with the Install-failure case above.
func TestService_ApplyProfileSwitch_EnableLoop_SetModEnabledFailureIsNonFatalNote(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "target", "src", "1", "Test Mod", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	installEnabledBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	enableMod, err := svc.GetInstalledMod("src", "1", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*enableMod},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Enabled, "Install still succeeded, so the mod must still be counted as enabled")
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: failed to update Test Mod: "))

	var sawEnabled bool
	for _, e := range events {
		if e.Phase == core.SwitchEnabled {
			sawEnabled = true
		}
	}
	assert.True(t, sawEnabled, "a SwitchEnabled event must still fire despite the SetModEnabled failure")

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "the mod must still have been deployed")
}

// TestService_ApplyProfileSwitch_EnableLoop_ModInstalledUnderOtherProfile_CreatesTargetRow
// guards the #60 fix: when the target profile's YAML references a mod whose
// installed_mods row lives only under a DIFFERENT profile (reachable via
// profile export/import or hand-edited profile lists), enabling it must
// create a row under the target profile - otherwise the deployment is
// orphaned: files on disk and tracked in deployed_files, but invisible to
// GetInstalledMods(game, target), so a later switch AWAY from the target
// never undeploys the mod.
func TestService_ApplyProfileSwitch_EnableLoop_ModInstalledUnderOtherProfile_CreatesTargetRow(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	// Installed+disabled under "default"; referenced by "target"'s YAML
	// without ever having been installed there.
	seedInstalledModUnderProfile(t, svc, game, "default", "src", "1", "Test Mod", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "target", "src", "1", "1.0")

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "target")
	require.NoError(t, err)
	require.Len(t, plan.ToEnable, 1)
	require.Equal(t, "default", plan.ToEnable[0].ProfileName,
		"precondition: the planned mod's row must live under the other profile")

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Enabled)
	assert.Empty(t, result.Notes, "creating the missing target row must not be downgraded to a warning")

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "the mod must be deployed to the game dir")

	row, err := svc.GetInstalledMod("src", "1", "g1", "target")
	require.NoError(t, err, "an installed_mods row must exist under the target profile")
	assert.True(t, row.Enabled)
	assert.True(t, row.Deployed)

	// The orphan regression: switching back away from "target" must see the
	// mod as enabled there and schedule it for disable/undeploy.
	backPlan, err := svc.PlanProfileSwitch(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, backPlan.ToDisable, 1, "switching away must undeploy the mod (it was orphaned before #60)")
	assert.Equal(t, "1", backPlan.ToDisable[0].ID)
}

// TestService_ApplyProfileSwitch_InstallLoop_FetchFailureSkipsModAndContinuesToNextMod
// guards the install loop's per-ref isolation: a mod whose source isn't
// registered (GetMod fails) is skipped via a SwitchInstallError event and
// does not stop the remaining ToInstall entries from installing.
func TestService_ApplyProfileSwitch_InstallLoop_FetchFailureSkipsModAndContinuesToNextMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"good.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "good", SourceID: "src", Name: "Good Mod", Version: "1.0", GameID: "g1"})
	// "bad" is never registered with the mock source, so GetMod fails.

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{
			{SourceID: "src", ModID: "bad", Version: "1.0"},
			{SourceID: "src", ModID: "good", Version: "1.0"},
		},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "a per-mod fetch failure must not fail the whole switch")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "the good mod must still install")

	var errEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.SwitchInstallError {
			errEvt = &events[i]
			break
		}
	}
	require.NotNil(t, errEvt)
	assert.Equal(t, "bad", errEvt.ModID)
	assert.Contains(t, errEvt.Detail, "failed to fetch mod")

	_, err = os.Lstat(filepath.Join(gameDir, "good.esp"))
	assert.NoError(t, err, "the second mod must still be installed")
}

// TestService_ApplyProfileSwitch_InstallLoop_DownloadFailureEmitsBlankErrorBlankSequence
// guards the dual-event sequence (SwitchDownloadFailed then, always,
// SwitchDownloadDone) that reproduces doProfileSwitch's exact
// blank-line/error/blank-line console sequence on a failed download - see
// SwitchDownloadDone's doc comment.
func TestService_ApplyProfileSwitch_InstallLoop_DownloadFailureEmitsBlankErrorBlankSequence(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src") // no AddDownload: the server 404s every file ID
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed)

	var failIdx, doneIdx = -1, -1
	for i, e := range events {
		switch e.Phase {
		case core.SwitchDownloadFailed:
			failIdx = i
		case core.SwitchDownloadDone:
			doneIdx = i
		}
	}
	require.NotEqual(t, -1, failIdx, "expected a SwitchDownloadFailed event")
	require.NotEqual(t, -1, doneIdx, "expected a SwitchDownloadDone event")
	assert.Less(t, failIdx, doneIdx, "the loop-done event must fire after the failure event, mirroring the unconditional trailing blank line")
	assert.Contains(t, events[failIdx].Detail, "download failed")
}

// TestService_ApplyProfileSwitch_InstallLoop_StoredFileIDsGone_FailsMod
// guards #95: when a ToInstall ref's FileIDs don't match any file the source
// currently offers, ApplyProfileSwitch must fail that mod (SwitchInstallError,
// via selectDeployFiles's allowFallback=false) rather than silently
// substituting the primary file (the old SwitchFallbackUsed phase, removed
// entirely in the renderer-cleanup task B2) - and the install loop must
// continue past it to the next mod.
func TestService_ApplyProfileSwitch_InstallLoop_StoredFileIDsGone_FailsMod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod2.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent) // mockSource.GetModFiles always returns file ID "1"
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{
			// "stale-id" does not match the mock source's file ID ("1") - mod1
			// must fail, not silently fall back to the primary file.
			{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"stale-id"}},
			// mod2 has no stored FileIDs, so it installs normally - proving
			// mod1's failure doesn't stop the loop.
			{SourceID: "src", ModID: "mod2", Version: "1.0"},
		},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "only mod2 should install; mod1 fails")

	var failEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.SwitchInstallError && events[i].ModName == "Mod One" {
			failEvt = &events[i]
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "no longer available upstream")
	assert.Contains(t, failEvt.Detail, "stale-id")

	_, err = svc.GetInstalledMod("src", "mod1", "g1", "target")
	assert.Error(t, err, "mod1 must not be installed via fallback substitution")
	_, err = svc.GetInstalledMod("src", "mod2", "g1", "target")
	assert.NoError(t, err, "mod2 must still install, proving the loop continues")
}

// TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion is the #94
// regression test for ApplyProfileSwitch's install loop: the source's served
// file ("1") carries its own Version ("1.0"), distinct from the mod-level
// Version ("1.5") GetMod returns. The saved InstalledMod row and the cache
// dir must be stamped with the file's version, not the mod's, mirroring
// flows_install_test.go's TestApplyInstall_ExplicitOldFile_
// RecordsFileVersionAndCacheKey for this flow.
//
// ref.Version is deliberately "" (a legacy/unpinned ref), not "1.5": #96
// made a non-empty ref.Version authoritative for FileIDs found upstream
// (selectVersionedDeployFiles' fast path re-resolves by version when the
// found files' effective version disagrees with the record - the hand-edited
// -YAML-drift case decision 2 targets). "1.5" here was only ever the
// mod-level label coincidentally reused for the ref before #96 existed; the
// mod-vs-file version discrepancy this test actually guards is entirely
// between mock.AddMod's Version and the served file's Version, independent
// of ref.Version - so "" (decision 1/4's exempted legacy-ref case) preserves
// the exact contract under test without asserting a real version pin.
func TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	mock := &versionedFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "", FileIDs: []string{"1"}}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "DB row must record the selected file's version, not the mod's latest")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "mod1", "1.0"), "cache must be keyed by the installed file's version")
}

// twoVersionSource serves two versions of the same mod's files: 1.5
// (current, ID "10", primary) and 1.0 (archived, ID "9") - the #96 fixture
// used to prove ApplyProfileSwitch's install loop resolves a pinned/recorded
// version to its matching file instead of always taking the latest/primary.
type twoVersionSource struct{ *mockSourceWithDownloads }

func (s *twoVersionSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "10", Name: "Main", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Name: "Old", FileName: mod.ID + "-old.zip", Version: "1.0", Category: "ARCHIVED"},
	}, nil
}

// newTwoVersionSource wires up a twoVersionSource with both file IDs'
// downloads registered (distinct payloads so a test can tell which file was
// actually fetched) and mod1 registered under g1.
func newTwoVersionSource(t *testing.T) *twoVersionSource {
	t.Helper()
	mock := &twoVersionSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)

	tmpDir := t.TempDir()
	newZip := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "new-payload"})
	newContent, err := os.ReadFile(newZip)
	require.NoError(t, err)
	mock.AddDownload("10", newContent)

	oldZip := createTestZip(t, tmpDir, map[string]string{"mod1-old.esp": "old-payload"})
	oldContent, err := os.ReadFile(oldZip)
	require.NoError(t, err)
	mock.AddDownload("9", oldContent)

	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"})
	return mock
}

// TestApplyProfileSwitch_HonorsProfileVersion_Downgrade is the #96 regression
// guard for selectVersionedDeployFiles' downgrade path: a profile pins mod1
// at 1.0 (hand-edited - no stored FileIDs to lean on), and the source serves
// both 1.5 (current/primary) and 1.0 (archived). The install loop must
// resolve 1.0 -> file "9" and record installed.Version == "1.0" - not fall
// back to the source's latest/primary file.
func TestApplyProfileSwitch_HonorsProfileVersion_Downgrade(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "must record the pinned version, not the source's latest")
	assert.Equal(t, []string{"9"}, installed.FileIDs, "must have selected the archived 1.0 file, not the primary 1.5 file")
}

// TestApplyProfileSwitch_StoredIDsGone_HealsToRecordedVersion is the #96
// healing guard: the profile's stored FileIDs ("999") no longer match
// anything upstream, but its recorded Version ("1.0") still resolves to file
// "9". Pre-#96 this hard-failed with #95's errStoredFilesUnavailable; #96
// heals it by re-resolving to the SAME version instead of erroring or
// silently taking latest.
func TestApplyProfileSwitch_StoredIDsGone_HealsToRecordedVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"999"}}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "must heal to the recorded version, not hard-fail")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)
}

// TestApplyProfileSwitch_StoredIDsGone_VersionAlsoGone_HardFails is #96's
// unresolvable case: neither the stored FileIDs ("999") nor the recorded
// Version ("0.5") match anything upstream. mod1 must fail with the extended
// #95 wording naming both the stale IDs and the missing version; mod2 (a
// normal, resolvable install) must still install, proving the install loop
// continues past the per-mod failure.
func TestApplyProfileSwitch_StoredIDsGone_VersionAlsoGone_HardFails(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "1.5", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{
			{SourceID: "src", ModID: "mod1", Version: "0.5", FileIDs: []string{"999"}},
			{SourceID: "src", ModID: "mod2", Version: "1.5"},
		},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "a per-mod resolution failure must not fail the whole switch")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "mod2 must still install")

	var failEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.SwitchInstallError && events[i].ModName == "Mod One" {
			failEvt = &events[i]
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "no longer available upstream")
	assert.Contains(t, failEvt.Detail, "999")
	assert.Contains(t, failEvt.Detail, `version "0.5" not available`)

	_, err = svc.GetInstalledMod("src", "mod1", "g1", "stable")
	assert.Error(t, err, "mod1 must not be installed via fallback substitution")
	_, err = svc.GetInstalledMod("src", "mod2", "g1", "stable")
	assert.NoError(t, err, "mod2 must still install, proving the loop continues")
}

// TestApplyProfileSwitch_VersionlessSource_KeepsLegacyBehavior pins the #130
// vacuous-version rule as it interacts with #96: when the source's files
// carry no Version info at all, selectVersionedDeployFiles must fall straight
// through to the pre-#96 selectDeployFiles - including its un-extended
// errStoredFilesUnavailable wording (no "version" mention) - even though the
// profile ref itself carries a (meaningless, in this context) Version.
func TestApplyProfileSwitch_VersionlessSource_KeepsLegacyBehavior(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	// mockSourceWithDownloads' embedded mockSource.GetModFiles always returns
	// a single versionless file with ID "1" - anyFileHasVersion(files) is
	// false, so version resolution must not engage at all.
	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	// Deliberately no AddDownload("1", ...) - selectDeployFiles must fail
	// before any download is attempted, matching
	// TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError's
	// idiom.

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"999"}}},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed, "a versionless source's stale FileIDs must still hard-fail exactly as before #96")

	var failEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.SwitchInstallError && events[i].ModName == "Mod One" {
			failEvt = &events[i]
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "no longer available upstream")
	assert.Contains(t, failEvt.Detail, "999")
	assert.NotContains(t, failEvt.Detail, "not available", "a versionless file list must produce the un-extended #95 wording, not the #96 extension")

	_, err = svc.GetInstalledMod("src", "mod1", "g1", "stable")
	assert.Error(t, err)
}

// TestApplyProfileSwitch_StoredIDsBothAgree_FastPathReturnsWholeSet is the
// #96 fix-round-1 regression guard for rule 2's fast path (previously
// untested end to end): when ALL of a mod's stored FileIDs are found
// upstream AND their combined EffectiveInstalledVersion agrees with the
// recorded version, selectVersionedDeployFiles must return the WHOLE stored
// set as-is - not re-resolve to matches(version), which would be the
// narrower set. Here ref.Version is "1.5" (the primary file10's own
// version), and the stored pair is [file10 (1.5, primary), file9 (1.0)]:
// EffectiveInstalledVersion picks file10's version ("1.5", since it's
// primary) as representative, agreeing with the record, so the fast path
// fires and keeps file9 riding along even though file9's own version isn't
// 1.5. Rule 3 (stored ∩ matches) would instead compute matches("1.5") =
// [file10] only and keep just that one file - the DIFFERENT, narrower
// outcome that distinguishes "fast path took over" from "matches-based
// resolution ran instead".
func TestApplyProfileSwitch_StoredIDsBothAgree_FastPathReturnsWholeSet(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.5", FileIDs: []string{"10", "9"}}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"10", "9"}, installed.FileIDs,
		"the fast path must keep the whole stored set, not re-resolve to the narrower matches(\"1.5\") set")
}

// TestApplyProfileSwitch_VersionMatchesNothing_NoStoredIDs_ErrVersionNotFoundWrap
// is the #96 fix-round-1 regression guard for rule 5 (previously untested):
// no stored FileIDs at all, and the recorded version matches nothing
// upstream. Must fail naming the version and pointing at editing the
// profile or reinstalling - not the #95 "gone upstream"/stored-IDs wording,
// which doesn't apply when there were never any stored IDs to begin with.
func TestApplyProfileSwitch_VersionMatchesNothing_NoStoredIDs_ErrVersionNotFoundWrap(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "9.9"}},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed)

	var failEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.SwitchInstallError && events[i].ModName == "Mod One" {
			failEvt = &events[i]
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.Contains(t, failEvt.Detail, "is not available upstream")
	assert.Contains(t, failEvt.Detail, "edit the profile's version or reinstall")
}

// TestApplyProfileSwitch_StoredIDPresentButVersionGone_NewErrorWording is
// the #96 fix-round-1 regression guard for the FINDING 2 split: when at
// least one stored FileID IS still present upstream but the recorded
// version doesn't match anything, that's a wrong version RECORD on a file
// that's still there - not a gone file - so it must get the distinct
// ErrVersionNotFound wrap (pointing at 'lmm verify --fix'/'lmm update'),
// never the #95 "no longer available upstream" wording (which implies the
// file itself is gone, misleading here). file10 (stored) resolves upstream
// fine; only the recorded version ("9.9") is bogus.
func TestApplyProfileSwitch_StoredIDPresentButVersionGone_NewErrorWording(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "9.9", FileIDs: []string{"10"}}},
	}

	var events []core.DeployProgress
	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Installed)

	var failEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.SwitchInstallError && events[i].ModName == "Mod One" {
			failEvt = &events[i]
		}
	}
	require.NotNil(t, failEvt, "expected a SwitchInstallError event for mod1")
	assert.NotContains(t, failEvt.Detail, "no longer available upstream", "a present-upstream stored ID must not get the gone-file wording")
	assert.Contains(t, failEvt.Detail, `installed file(s) (ID(s): 10) do not match recorded version "9.9"`)
	assert.Contains(t, failEvt.Detail, "run 'lmm verify --fix' to correct the version record, or 'lmm update' to adopt the current version")
}

// TestPlanProfileSwitch_VersionDrift_SchedulesReinstall is the #96
// convergence guard: mod1 is installed+enabled at 1.5 under "testing" (the
// current default profile), but the target profile "stable" pins it at 1.0.
// PlanProfileSwitch must classify this as ToInstall (carrying the ref as-is,
// version "1.0") rather than leaving it alone as already-satisfied - the
// version mismatch itself, not cache/enabled state, drives the decision.
func TestPlanProfileSwitch_VersionDrift_SchedulesReinstall(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "testing")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "testing"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "testing", "src", "mod1", "Mod One", "1.5", true, map[string][]byte{"mod1.esp": []byte("v1.5")})
	require.NoError(t, pm.AddMod(game.ID, "testing", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))
	require.NoError(t, pm.UpsertMod(game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.False(t, plan.NoChanges)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToDisable)
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "mod1", plan.ToInstall[0].ModID)
	assert.Equal(t, "1.0", plan.ToInstall[0].Version, "must schedule reinstall at the profile's (lower) version")
}

// TestPlanProfileSwitch_MatchingVersion_RemainsNoop is the regression guard
// for the new drift case's guard conditions: when the target ref's Version
// matches the installed mod's (or is empty), the mod must be classified
// exactly as it was before #96 - no ToInstall entry.
func TestPlanProfileSwitch_MatchingVersion_RemainsNoop(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "testing")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "testing"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "testing", "src", "mod1", "Mod One", "1.5", true, map[string][]byte{"mod1.esp": []byte("v1.5")})
	require.NoError(t, pm.AddMod(game.ID, "testing", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))
	require.NoError(t, pm.UpsertMod(game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.True(t, plan.NoChanges, "matching version must remain a no-op, exactly as before #96")
	assert.Empty(t, plan.ToInstall)
	assert.Empty(t, plan.ToEnable)
	assert.Empty(t, plan.ToDisable)
}

// TestApplyProfileSwitch_Downgrade_EndToEnd is the #96 convergence
// end-to-end guard: mod1 installed+DEPLOYED at 1.5 (file "10", live on disk
// as "mod1.esp"); switching to "stable" (which pins 1.0) must plan AND apply
// a full reinstall at 1.0 - resolving to the archived file "9" via T5's
// selectVersionedDeployFiles, caching it, recording the downgrade in the DB,
// and REPLACING the live 1.5 deployment (not merely installing 1.0
// alongside it) - review round 1 finding 1: a bare Installer.Install call
// only adds the new version's files; it never removes files the old,
// still-live deployment holds that the new version doesn't serve, leaving
// mod1.esp (1.5) orphaned on disk forever (invisible to later uninstalls,
// which only ever undeploy the CURRENTLY RECORDED version's files).
func TestApplyProfileSwitch_Downgrade_EndToEnd(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Test Mod", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"10"},
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.5", GameID: game.ID}, "default"))
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	require.NoError(t, err, "precondition: 1.5 must be actually deployed")

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))
	require.NoError(t, pm.UpsertMod(game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "stable")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.Len(t, plan.ToInstall, 1, "the version-drifted mod must be scheduled for reinstall")

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs, "must have selected the archived 1.0 file, not the primary 1.5 file")
	assert.True(t, installed.Deployed, "the row must record that files are actually live on disk (review finding 3)")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "the downgraded version must be cached")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the obsolete 1.5 file must be removed by Replace, not left behind by a bare Install (review finding 1)")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the new 1.0 file must be deployed")
}

// TestApplyProfileSwitch_PartialCacheEntry_StillDownloads is the #96 review
// round 1 finding 2 guard: a version directory can exist on disk without
// being fully populated (e.g. a previous download run that broke off before
// any file was committed, or partway through a multi-file mod). The
// cache-first guard must not mistake directory presence alone for "already
// cached" - bare cache.Exists would report true here and silently skip the
// download forever, leaving the mod stuck without its files.
func TestApplyProfileSwitch_PartialCacheEntry_StillDownloads(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// Simulate a version directory left behind by a previously broken-off
	// download run: the directory exists (bare cache.Exists would report
	// "cached") but holds none of the expected files.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.MkdirAll(gameCache.ModPath(game.ID, "src", "mod1", "1.0"), 0755))
	require.True(t, gameCache.Exists(game.ID, "src", "mod1", "1.0"), "precondition: bare Exists must see the empty dir as already cached")

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed, "a partial cache entry must not fool the cache-first guard into skipping the download")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, []string{"9"}, installed.FileIDs, "the download must actually have run and selected the 1.0 file")

	// The DB's FileIDs alone don't prove the download actually ran (they're
	// stamped from the SELECTED files regardless of whether the download
	// loop executed) - what distinguishes a real download from a
	// fooled-by-directory-presence skip is whether the byte content is
	// actually on disk, both in the cache and deployed into the game dir.
	cached, err := gameCache.ListFiles(game.ID, "src", "mod1", "1.0")
	require.NoError(t, err)
	assert.Contains(t, cached, "mod1-old.esp", "the cache entry must actually contain the downloaded file, not just an empty directory")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the file must actually be deployed, not merely a DB row claiming so")

	// The completed download must leave the entry MARKED, so the next
	// convergence pass over the same profile is a genuine cache hit.
	assert.True(t, gameCache.HasFileIDs(game.ID, "src", "mod1", "1.0", []string{"9"}),
		"a successful download must commit the per-file completion marker")
	assert.Equal(t, 1, mock.DownloadCount(), "exactly one download, not a retry loop")
}

// TestApplyProfileSwitch_FullyMarkedCache_SkipsDownload is the #96 review
// round 2 guard: the cache-first guard must actually FIRE for a complete
// cache entry. Round 1 keyed the check off DownloadableFile.FileName, but the
// default DeployExtract flow stores an archive's extracted MEMBERS
// ("mod1-old.esp" here) rather than the archive's own name
// ("mod1-old.zip") - so the guard read false for essentially every
// archive-based mod and redownloaded despite a complete cache, hollowing out
// the "deploy from cache when possible" design decision (which matters most
// for downgrades, whose archived files can vanish upstream).
func TestApplyProfileSwitch_FullyMarkedCache_SkipsDownload(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "stable")
	require.NoError(t, err)

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// A COMPLETE cache entry as a real extract-mode download leaves it: the
	// archive member on disk under a name that matches nothing about the
	// DownloadableFile ("mod1-old.zip"), plus file "9"'s completion marker.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "mod1", "1.0", "mod1-old.esp", []byte("old-payload")))
	require.NoError(t, cache.MarkFileComplete(gameCache.ModPath(game.ID, "src", "mod1", "1.0"), "9"))

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "stable",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	assert.Equal(t, 0, mock.DownloadCount(),
		"a fully-marked cache entry must be deployed from cache, not redownloaded")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the cached file must still be deployed")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "stable")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, []string{"9"}, installed.FileIDs)
}

// TestService_ApplyProfileSwitch_InstallLoop_SavesWithNormalizedGameID
// regression-guards the P3 orphaning bug class (see profile_gameid_test.go's
// CLI-level counterpart): Service.GetMod may stamp a source-mapped GameID
// onto the fetched *domain.Mod, but the InstalledMod row ApplyProfileSwitch
// saves must always be normalized to the lmm game.ID, so every other DB read
// (which queries by the lmm game ID) can find it again.
func TestService_ApplyProfileSwitch_InstallLoop_SavesWithNormalizedGameID(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	// The mock mod's own GameID deliberately differs from game.ID ("g1"),
	// mirroring what Service.GetMod stamps on when a source-mapped ID is in
	// play - the saved row must NOT inherit this value.
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "source-mapped-id"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "target")
	require.NoError(t, err, "the installed row must be visible under the lmm game ID")
	assert.Equal(t, "g1", installed.GameID, "persisted GameID must be normalized to the lmm game, not the source-mapped value")
}

// TestService_ApplyProfileSwitch_FatalSetDefaultErrorAfterAccumulatedDiagnostics_ReturnsPartialResult
// guards the Task 2/3 error-path convention applied to ApplyProfileSwitch: a
// fatal error (here, SetDefault) must not discard the SwitchResult
// accumulated up to that point. Isolated to the simplest possible scenario
// (no disable/enable buckets) so it stands independently of
// TestService_ApplyProfileSwitch_ExecutesDisableThenEnableThenInstall_SetDefaultLastAndUnchangedOnFailure,
// which exercises the same convention but with all three buckets active.
func TestService_ApplyProfileSwitch_FatalSetDefaultErrorAfterAccumulatedDiagnostics_ReturnsPartialResult(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	// "target" is never created, so both UpsertMod and the final SetDefault
	// fail deterministically.

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0"}},
	}

	result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting default profile")
	require.NotNil(t, result, "the result accumulated before the fatal error must not be discarded")
	assert.Equal(t, 1, result.Installed, "the install itself succeeded before the later fatal SetDefault error")
	require.Len(t, result.Notes, 1)
	assert.Contains(t, result.Notes[0], "could not update profile")
}

// TestService_ApplyProfileSwitch_NilProgressCallbackIsSafe guards that
// progress may be nil per the required API (mirroring
// TestService_DeployProfile_NilProgressCallbackIsSafe).
func TestService_ApplyProfileSwitch_NilProgressCallbackIsSafe(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	disableMod, err := svc.GetInstalledMod("src", "1", "g1", "default")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*disableMod},
	}

	assert.NotPanics(t, func() {
		result, err := svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.Disabled)
	})
}

// --- Task 6 item d: cancel-then-drain (ctx checked between loop iterations) ---

// TestService_ApplyProfileSwitch_ContextCancelledBetweenDisableLoopMods_ReturnsPartialResultWithCtxErr
// guards the disable loop: a cancelled ctx aborts BETWEEN mods, never
// mid-file-operation, with the partial-result convention. The progress
// callback cancels the instant the first mod's SwitchDisabled event fires;
// the second mod must never be touched (still deployed).
func TestService_ApplyProfileSwitch_ContextCancelledBetweenDisableLoopMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "a", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "b", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	modA, err := svc.GetInstalledMod("src", "a", "g1", "default")
	require.NoError(t, err)
	modB, err := svc.GetInstalledMod("src", "b", "g1", "default")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToDisable: []domain.InstalledMod{*modA, *modB},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyProfileSwitch(ctx, game, plan, func(p core.DeployProgress) {
		if p.Phase == core.SwitchDisabled && p.ModName == "Mod A" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Disabled)

	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.NoError(t, err, "Mod B must never have been touched - still deployed")
}

// TestService_ApplyProfileSwitch_ContextCancelledBetweenEnableLoopMods_ReturnsPartialResultWithCtxErr
// mirrors the disable-loop test above for the enable loop: the second mod
// must never be deployed/enabled.
func TestService_ApplyProfileSwitch_ContextCancelledBetweenEnableLoopMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedInstalledModUnderProfile(t, svc, game, "target", "src", "a", "Mod A", "1.0", false, map[string][]byte{"a.esp": []byte("a")})
	seedInstalledModUnderProfile(t, svc, game, "target", "src", "b", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})

	modA, err := svc.GetInstalledMod("src", "a", "g1", "target")
	require.NoError(t, err)
	modB, err := svc.GetInstalledMod("src", "b", "g1", "target")
	require.NoError(t, err)

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToEnable: []domain.InstalledMod{*modA, *modB},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyProfileSwitch(ctx, game, plan, func(p core.DeployProgress) {
		if p.Phase == core.SwitchEnabled && p.ModName == "Mod A" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Enabled)

	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "Mod B must never have been deployed/enabled")
}

// TestService_ApplyProfileSwitch_ContextCancelledBetweenInstallLoopMods_ReturnsPartialResultWithCtxErr
// mirrors the disable/enable-loop tests above for the install loop: the
// second ModReference must never be fetched/downloaded/installed.
func TestService_ApplyProfileSwitch_ContextCancelledBetweenInstallLoopMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	_, err = pm.Create(game.ID, "target")
	require.NoError(t, err)

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	tmpDir := t.TempDir()
	firstZip := createTestZip(t, tmpDir, map[string]string{"first.esp": "payload"})
	firstContent, err := os.ReadFile(firstZip)
	require.NoError(t, err)
	mock.AddDownload("1", firstContent)
	mock.AddMod("g1", &domain.Mod{ID: "first", SourceID: "src", Name: "First Mod", Version: "1.0", GameID: "g1"})
	secondZip := createTestZip(t, tmpDir, map[string]string{"second.esp": "payload"})
	secondContent, err := os.ReadFile(secondZip)
	require.NoError(t, err)
	mock.AddDownload("2", secondContent)
	mock.AddMod("g1", &domain.Mod{ID: "second", SourceID: "src", Name: "Second Mod", Version: "1.0", GameID: "g1"})

	plan := &core.SwitchPlan{
		GameID: "g1", From: "default", To: "target",
		ToInstall: []domain.ModReference{
			{SourceID: "src", ModID: "first", Version: "1.0"},
			{SourceID: "src", ModID: "second", Version: "1.0"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.ApplyProfileSwitch(ctx, game, plan, func(p core.DeployProgress) {
		if p.Phase == core.SwitchInstalled && p.ModID == "first" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Installed)

	_, err = os.Lstat(filepath.Join(gameDir, "second.esp"))
	assert.True(t, os.IsNotExist(err), "the second ModReference must never have been fetched/installed")
	_, err = svc.GetInstalledMod("src", "second", "g1", "target")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

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

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 2)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.Purged)
	assert.Empty(t, result.Skipped)

	for _, f := range []string{"a.esp", "b.esp"} {
		_, err := os.Lstat(filepath.Join(gameDir, f))
		assert.True(t, os.IsNotExist(err), "%s must be undeployed", f)
	}
	for _, id := range []string{"a", "b"} {
		mod, err := svc.GetInstalledMod("src", id, "g1", "default")
		require.NoError(t, err, "records must be preserved without Uninstall")
		assert.False(t, mod.Deployed, "mod %s must be marked not deployed", id)
	}

	require.NotEmpty(t, events)
	assert.Equal(t, core.DeployPurging, events[0].Phase)
	assert.Equal(t, 2, events[0].Total)
	var purged []core.DeployProgress
	for _, e := range events {
		if e.Phase == core.PurgeModPurged {
			purged = append(purged, e)
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
	assert.Equal(t, core.PurgeComplete, events[len(events)-1].Phase)
}

func TestService_PurgeProfile_EmptyModList_NoEventsNoHooks(t *testing.T) {
	svc := newFlowsTestService(t)
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	// A failing before_all hook is the witness that no hooks run on an
	// empty purge: if it ran, the (Force-less) call would return its error.
	failScript := createTestScript(t, scriptsDir, "before_all.sh", "#!/bin/bash\nexit 1")
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", nil, core.PurgeOptions{
		Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.Purged)
	assert.Empty(t, events)
}

func TestService_PurgeProfile_Uninstall_RemovesRecordsAndProfileEntries(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	installSeededMod(t, svc, game, "1")

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{Uninstall: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(err))
	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "Uninstall must delete the DB record")
	profile, err := svc.NewProfileManager().Get("g1", "default")
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: beforeEachScript}}

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	var found *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.PurgeModSkipped {
			found = &events[i]
			break
		}
	}
	require.NotNil(t, found, "expected a PurgeModSkipped event")
	assert.Equal(t, "Bad Mod", found.ModName)
	assert.Equal(t, "bad", found.ModID)
	assert.True(t, strings.HasPrefix(found.Detail, "uninstall.before_each hook failed: "),
		"Detail must carry the baked prefix the CLI prints after \"Skipped <name>: \"")
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Bad Mod: "+found.Detail, result.Skipped[0],
		"the Skipped entry must be the event's Detail behind the name prefix")

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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uninstall.before_all hook failed:")
	require.NotNil(t, result, "partial-result convention: the accumulated result comes back alongside the error")
	assert.Zero(t, result.Purged)
	assert.Empty(t, events)

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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}}

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second), Force: true,
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "uninstall.before_all hook failed (forced):")
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, core.DeployBeforeAllForced, events[0].Phase, "the forced warning must precede the purge header")
	assert.Equal(t, result.Warnings[0], events[0].Detail)
	assert.Equal(t, core.DeployPurging, events[1].Phase)
}

func TestService_PurgeProfile_AfterHookFailures_WarningsUseModNameAndDeferEmission(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installSeededMod(t, svc, game, "1")

	failScript := createTestScript(t, scriptsDir, "fail.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{AfterEach: failScript, AfterAll: failScript}}

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged)

	require.Len(t, result.Warnings, 2)
	assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed for Test Mod:",
		"lmm purge's after_each warning carries the mod NAME (unlike deploy --purge's mod ID)")
	assert.Contains(t, result.Warnings[1], "uninstall.after_all hook failed:")

	purgedIdx, firstWarnIdx := -1, -1
	for i, e := range events {
		if e.Phase == core.PurgeModPurged {
			purgedIdx = i
		}
		if e.Phase == core.PurgeWarning && firstWarnIdx == -1 {
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

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged, "an undeploy failure is best-effort: the mod still counts as purged")
	assert.Empty(t, result.Skipped)

	var found *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.PurgeNote {
			found = &events[i]
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

	mods, err := svc.GetInstalledMods("g1", "default")
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
	hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{AfterEach: afterEachScript}}

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{
		Uninstall: true, Hooks: hooks, HookRunner: core.NewHookRunner(5 * time.Second),
	}, func(p core.DeployProgress) { events = append(events, p) })
	require.NoError(t, err)

	assert.Zero(t, result.Purged)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "Test Mod: failed to remove record:")
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, result.Notes[0], "⚠ Test Mod - failed to remove record:")

	for _, e := range events {
		assert.NotEqual(t, core.PurgeModPurged, e.Phase, "a delete-record failure must not count as purged")
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
	_, err := pm.Create("g1", "default")
	require.NoError(t, err)

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	result, err := svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{Uninstall: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Purged, "a profile-YAML removal failure is note-only; the mod still purges")
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, result.Notes[0], "Note: Test Mod - ")

	_, err = svc.GetInstalledMod("src", "1", "g1", "default")
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

	mods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 2)

	ctx, cancel := context.WithCancel(context.Background())
	var events []core.DeployProgress
	result, err := svc.PurgeProfile(ctx, game, "default", mods, core.PurgeOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
		if p.Phase == core.PurgeModPurged {
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
	for _, e := range events {
		assert.NotEqual(t, core.PurgeComplete, e.Phase, "a cancelled purge must not report completion")
	}
}
