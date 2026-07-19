package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
