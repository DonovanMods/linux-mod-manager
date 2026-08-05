package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ConvergeDeployedFiles (#168/#212) ---
//
// These tests use newFlowsTestService/seedInstalledMod (flows_test.go) to
// build a real Service (temp config/data/cache dirs, :memory: DB) and the
// real Installer to produce genuine deployed_files rows and game-dir
// symlinks/files, then mutate the cache or game dir to create the "stale"
// or "dangling" shapes convergence must detect.

func TestConverge_RowDrivenStaleRemoved(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"a.esp":    []byte("a"),
		"gone.esp": []byte("g"),
	})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// gone.esp is no longer provided by m1: remove it from the cache so
	// deployableFiles (full ListFiles union - no manifests recorded here) no
	// longer includes it, while its deployed_files row still exists.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "gone.esp", result.Removed[0].Path)
	assert.Equal(t, "src", result.Removed[0].SourceID)
	assert.Equal(t, "m1", result.Removed[0].ModID)
	assert.Contains(t, result.Removed[0].Reason, "no longer provided by")

	_, err = os.Lstat(filepath.Join(gameDir, "gone.esp"))
	assert.True(t, os.IsNotExist(err), "gone.esp's game-dir symlink should be removed")

	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "a.esp must be untouched")

	rows, err := svc.GetDeployedFilesForMod("g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.esp"}, rows, "gone.esp's row must be deleted")
}

func TestConverge_SharedPathProtectedByUnion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	// m1 deploys shared.esp (creating the row); m2 also provides shared.esp
	// in its own cache entry, but is never deployed.
	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"shared.esp": []byte("m1-copy"),
	})
	seedInstalledMod(t, svc, game, "src", "m2", "1.0", true, map[string][]byte{
		"shared.esp": []byte("m2-copy"),
	})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// m1 no longer provides shared.esp itself (its cache copy is gone), but
	// m2's cache copy still exists, so the union still contains shared.esp.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "shared.esp")))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", false)
	require.NoError(t, err)
	assert.Empty(t, result.Removed, "shared.esp must be protected by m2's still-current claim")

	_, err = os.Lstat(filepath.Join(gameDir, "shared.esp"))
	assert.NoError(t, err, "shared.esp's game-dir symlink must survive")

	rows, err := svc.GetDeployedFilesForMod("g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"shared.esp"}, rows, "row must survive too")
}

func TestConverge_DanglingCacheLinkSwept(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	// A symlink into the game's cache root pointing at a file that was never
	// (or is no longer) actually there, with no deployed_files row at all.
	cacheRoot := svc.GetGameCachePath(game)
	target := filepath.Join(cacheRoot, "g1", "src-stray", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(target, filepath.Join(gameDir, "stray.pak")))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "stray.pak", result.Removed[0].Path)
	assert.Equal(t, "dangling link into lmm cache", result.Removed[0].Reason)
	assert.Empty(t, result.Removed[0].SourceID)
	assert.Empty(t, result.Removed[0].ModID)

	_, err = os.Lstat(filepath.Join(gameDir, "stray.pak"))
	assert.True(t, os.IsNotExist(err), "the dangling link must be removed")
}

func TestConverge_ForeignSymlinkUntouched(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	// A symlink pointing entirely outside any lmm cache root - dangling, but
	// foreign, so convergence must never touch it.
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "does-not-exist.pak")
	require.NoError(t, os.Symlink(target, filepath.Join(gameDir, "user.pak")))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", false)
	require.NoError(t, err)
	assert.Empty(t, result.Removed)

	_, err = os.Lstat(filepath.Join(gameDir, "user.pak"))
	assert.NoError(t, err, "foreign symlink must survive")
}

func TestConverge_HealthySymlinkUntouched(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	// A symlink into the cache root whose target genuinely exists (the
	// merged-pak shape) but has no deployed_files row - must survive.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store("g1", "src", "merged", "1.0", "merged.pak", []byte("content")))
	target := gameCache.GetFilePath("g1", "src", "merged", "1.0", "merged.pak")
	require.NoError(t, os.Symlink(target, filepath.Join(gameDir, "merged.pak")))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", false)
	require.NoError(t, err)
	assert.Empty(t, result.Removed)

	_, err = os.Lstat(filepath.Join(gameDir, "merged.pak"))
	assert.NoError(t, err, "healthy link with no row must survive")
}

func TestConverge_DryRunTouchesNothing(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	// Scenario 1 shape: stale row.
	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"a.esp":    []byte("a"),
		"gone.esp": []byte("g"),
	})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	// Scenario 3 shape: dangling cache-rooted link, no row.
	cacheRoot := svc.GetGameCachePath(game)
	strayTarget := filepath.Join(cacheRoot, "g1", "src-stray", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(strayTarget, filepath.Join(gameDir, "stray.pak")))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", true)
	require.NoError(t, err)
	require.Len(t, result.Removed, 2)

	var paths []string
	for _, cf := range result.Removed {
		paths = append(paths, cf.Path)
	}
	assert.ElementsMatch(t, []string{"gone.esp", "stray.pak"}, paths)

	// Nothing on disk changed.
	for _, name := range []string{"a.esp", "gone.esp", "stray.pak"} {
		_, err := os.Lstat(filepath.Join(gameDir, name))
		assert.NoError(t, err, "%s must survive a dry run", name)
	}

	// Nothing in the DB changed.
	rows, err := svc.GetDeployedFilesForMod("g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a.esp", "gone.esp"}, rows, "dry run must not delete rows")
}

func TestConverge_RegularFileNeedsRow(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	// Copy-mode deploy: undeployed content is a REGULAR file, not a symlink.
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"a.esp":    []byte("a"),
		"gone.esp": []byte("g"),
	})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	// An untracked regular file dropped directly in the game dir, no row -
	// the sweep must never touch it (it only ever considers symlinks).
	require.NoError(t, os.WriteFile(filepath.Join(gameDir, "untracked.txt"), []byte("leave me alone"), 0644))

	result, err := svc.ConvergeDeployedFiles(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "gone.esp", result.Removed[0].Path)

	_, err = os.Lstat(filepath.Join(gameDir, "gone.esp"))
	assert.True(t, os.IsNotExist(err), "gone.esp's regular file must be removed via the row pass")

	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "a.esp must be untouched")

	_, err = os.Lstat(filepath.Join(gameDir, "untracked.txt"))
	assert.NoError(t, err, "an untracked regular file with no row must never be touched")

	rows, err := svc.GetDeployedFilesForMod("g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.esp"}, rows)
}
