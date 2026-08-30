package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- convergeDeployedFiles (#168/#212) ---
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
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// gone.esp is no longer provided by m1: remove it from the cache so
	// deployableFiles (full ListFiles union - no manifests recorded here) no
	// longer includes it, while its deployed_files row still exists.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
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

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
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
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// m1 no longer provides shared.esp itself (its cache copy is gone), but
	// m2's cache copy still exists, so the union still contains shared.esp.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "shared.esp")))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.NoError(t, err)
	assert.Empty(t, result.Removed, "shared.esp must be protected by m2's still-current claim")

	_, err = os.Lstat(filepath.Join(gameDir, "shared.esp"))
	assert.NoError(t, err, "shared.esp's game-dir symlink must survive")

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
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

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
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

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
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

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
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
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	// Scenario 3 shape: dangling cache-rooted link, no row.
	cacheRoot := svc.GetGameCachePath(game)
	strayTarget := filepath.Join(cacheRoot, "g1", "src-stray", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(strayTarget, filepath.Join(gameDir, "stray.pak")))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", true)
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
	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
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
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	// An untracked regular file dropped directly in the game dir, no row -
	// the sweep must never touch it (it only ever considers symlinks).
	require.NoError(t, os.WriteFile(filepath.Join(gameDir, "untracked.txt"), []byte("leave me alone"), 0644))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "gone.esp", result.Removed[0].Path)

	_, err = os.Lstat(filepath.Join(gameDir, "gone.esp"))
	assert.True(t, os.IsNotExist(err), "gone.esp's regular file must be removed via the row pass")

	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "a.esp must be untouched")

	_, err = os.Lstat(filepath.Join(gameDir, "untracked.txt"))
	assert.NoError(t, err, "an untracked regular file with no row must never be touched")

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.esp"}, rows)
}

// TestConverge_RowPass_UndeployFailureExcludedFromRemoved pins fix round 1
// (#168): a failed Undeploy must NOT be reported in Result.Removed, since
// callers (verify --fix's "removed N") treat that list as what actually
// happened. Mirrors TestService_DisableMod_UndeployFailureIsNonFatal's
// fixture shape (flows_test.go:304): corrupt a deployed symlink into a
// plain file so SymlinkLinker.Undeploy fails deterministically with "not a
// symlink" - exactly the linker-method-mismatch trigger the review flagged.
func TestConverge_RowPass_UndeployFailureExcludedFromRemoved(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"gone.esp": []byte("g"),
	})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// gone.esp becomes stale: no longer provided by m1.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Remove(gameCache.GetFilePath("g1", "src", "m1", "1.0", "gone.esp")))

	// Corrupt the deployed symlink into a plain file so the symlink linker's
	// Undeploy fails deterministically ("not a symlink").
	deployedPath := filepath.Join(gameDir, "gone.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.Error(t, err, "a failed Undeploy must surface as a joined error")
	assert.Contains(t, err.Error(), "gone.esp")
	assert.Empty(t, result.Removed, "a failed Undeploy must not be reported as removed")

	_, statErr := os.Stat(deployedPath)
	assert.NoError(t, statErr, "the file must survive a failed undeploy")

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"gone.esp"}, rows, "the row must survive a failed undeploy")
}

// TestConverge_SweepPass_RemoveFailureExcludedFromRemoved is the sweep
// pass's counterpart to the row-pass test above: os.Remove failing on a
// dangling cache-rooted symlink must not be reported in Result.Removed
// either. Removing a directory entry requires write permission on its
// parent, so stripping that (0o555: read+execute, no write) on the link's
// containing directory fails the removal deterministically while still
// letting WalkDir list and Lstat the entry. Root bypasses directory
// permission checks entirely (see TestGetProfileConflictsCacheReadErrorPropagates,
// conflicts_test.go:210), so this is meaningless there and skipped.
func TestConverge_SweepPass_RemoveFailureExcludedFromRemoved(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	subDir := filepath.Join(gameDir, "sub")
	require.NoError(t, os.Mkdir(subDir, 0755))

	cacheRoot := svc.GetGameCachePath(game)
	target := filepath.Join(cacheRoot, "g1", "src-stray", "1.0", "stray.pak")
	linkPath := filepath.Join(subDir, "stray.pak")
	require.NoError(t, os.Symlink(target, linkPath))

	require.NoError(t, os.Chmod(subDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(subDir, 0o755) })

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.Error(t, err, "a failed sweep os.Remove must surface as a joined error")
	assert.Contains(t, err.Error(), "stray.pak")
	assert.Empty(t, result.Removed, "a failed sweep removal must not be reported as removed")

	_, statErr := os.Lstat(linkPath)
	assert.NoError(t, statErr, "the dangling link must survive a failed removal")
}

// TestConverge_SweepPass_ChecksBothCacheRoots pins fix round 2 Finding 1: a
// game with a per-game CachePath override still keeps content-addressed
// content in the GLOBAL cache root too (nothing migrates existing global
// content when CachePath is set), so the sweep must recognize a dangling
// link into EITHER root, not just the one GetGameCachePath happens to
// resolve to for this game.
func TestConverge_SweepPass_ChecksBothCacheRoots(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	gameCachePath := t.TempDir() // per-game cache_path override
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, CachePath: gameCachePath, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	// A dangling symlink into the GLOBAL cache root - NOT the per-game
	// CachePath override. GetGameCachePath(game) resolves to gameCachePath
	// here, so a sweep that only checks that single root would (wrongly)
	// treat this as foreign and leave it dangling forever.
	globalCacheRoot := svc.GlobalCacheDir()
	target := filepath.Join(globalCacheRoot, "g1", "src-stray", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(target, filepath.Join(gameDir, "stray.pak")))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "stray.pak", result.Removed[0].Path)
	assert.Equal(t, "dangling link into lmm cache", result.Removed[0].Reason)

	_, err = os.Lstat(filepath.Join(gameDir, "stray.pak"))
	assert.True(t, os.IsNotExist(err), "the dangling link into the global cache root must be swept even though this game has a CachePath override")
}

// TestConverge_AbsentCacheEntry_UnknownProvenanceRowSpared pins fix round 2
// Finding 2: a mod whose cache entry is WHOLLY absent (not just missing one
// file - deployableFiles returns fs.ErrNotExist) must be treated as UNKNOWN
// provenance, never "provides nothing". Before this fix, the row pass
// silently deleted such a mod's rows and (for a copy-mode deployment) the
// game-dir file itself, exactly when verify couldn't repair the cache. A
// separate, genuinely row-less dangling symlink elsewhere under this same
// mod's (now-removed) cache dir proves the sweep's own independent
// detection still works unaffected by the row-pass skip.
func TestConverge_AbsentCacheEntry_UnknownProvenanceRowSpared(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	// Copy-mode deploy: undeployed content is a REGULAR file, the shape
	// Finding 2 is specifically about (an absent cache entry must not
	// delete a still-working copy-mode deployment).
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"a.esp": []byte("a"),
	})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// The mod's ENTIRE cache entry disappears (unlike TestConverge_RowDrivenStaleRemoved,
	// which removes just one file) - deployableFiles now returns
	// fs.ErrNotExist for m1, not "zero files".
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.RemoveAll(gameCache.ModPath("g1", "src", "m1", "1.0")))

	// A dangling symlink with no DB row, targeting the same now-removed
	// mod's cache dir - proves the sweep still catches genuinely row-less
	// dangling links "belonging to" an unknown-provenance mod.
	cacheRoot := svc.GetGameCachePath(game)
	strayTarget := filepath.Join(cacheRoot, "g1", "src", "m1", "1.0", "untracked.pak")
	require.NoError(t, os.Symlink(strayTarget, filepath.Join(gameDir, "untracked.pak")))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "untracked.pak", result.Removed[0].Path)
	assert.Equal(t, "dangling link into lmm cache", result.Removed[0].Reason)

	_, err = os.Stat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "a.esp must survive: an absent cache entry is unknown provenance, not grounds for deletion")

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.esp"}, rows, "the row must survive too")
}

// TestConverge_SweepPass_UnreadableDirSkippedNotAborted pins fix round 2
// Finding 4: a directory-read error (permission denied on ReadDir) during
// the sweep walk must not abort the whole sweep - it should be recorded as
// an error and the walk should continue past it (fs.SkipDir), so a dangling
// link in a SIBLING directory still gets swept. Mirrors
// TestConverge_SweepPass_RemoveFailureExcludedFromRemoved's root-guard/
// chmod-restore fixture shape.
func TestConverge_SweepPass_UnreadableDirSkippedNotAborted(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	unreadableDir := filepath.Join(gameDir, "locked")
	require.NoError(t, os.Mkdir(unreadableDir, 0755))
	require.NoError(t, os.Chmod(unreadableDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadableDir, 0o755) })

	// "sibling" sorts after "locked" lexically, so WalkDir hits the
	// unreadable dir first and must still reach this one.
	siblingDir := filepath.Join(gameDir, "sibling")
	require.NoError(t, os.Mkdir(siblingDir, 0755))
	cacheRoot := svc.GetGameCachePath(game)
	target := filepath.Join(cacheRoot, "g1", "src-stray", "1.0", "stray.pak")
	linkPath := filepath.Join(siblingDir, "stray.pak")
	require.NoError(t, os.Symlink(target, linkPath))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.Error(t, err, "the unreadable directory must surface as an error")
	assert.Contains(t, err.Error(), "locked")
	require.Len(t, result.Removed, 1, "the sibling directory's dangling link must still be swept")
	assert.Equal(t, filepath.Join("sibling", "stray.pak"), result.Removed[0].Path)

	_, statErr := os.Lstat(linkPath)
	assert.True(t, os.IsNotExist(statErr), "the sibling dangling link must actually be removed")
}

// TestConverge_AbsentCacheEntry_SymlinkRowSweptByPhysicalEvidence pins fix
// round 2's correction to Finding 2: an unknown-provenance mod's rows must
// be skipped by the row pass's BOOKKEEPING judgment, but NOT marked
// handled - a genuinely dangling cache-pointing symlink among those rows is
// still its own physical evidence and must be swept (with its now-orphaned
// row best-effort deleted), even though the row pass itself declined to
// judge it. This is the counterpart to
// TestConverge_AbsentCacheEntry_UnknownProvenanceRowSpared's copy-mode
// (regular file) shape, which is never a sweep candidate and must keep
// surviving unchanged.
func TestConverge_AbsentCacheEntry_SymlinkRowSweptByPhysicalEvidence(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	// Symlink-mode deploy: the row's own deployed file IS a symlink into
	// the mod's cache dir.
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"gone.esp": []byte("g"),
	})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// The mod's ENTIRE cache entry disappears - deployableFiles now returns
	// fs.ErrNotExist for m1 (unknown provenance), and the existing
	// game-dir symlink (still on disk - the row pass leaves it alone) is
	// now dangling: physical evidence the sweep pass judges independently
	// of the row pass's bookkeeping decision.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.RemoveAll(gameCache.ModPath("g1", "src", "m1", "1.0")))

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.NoError(t, err)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "gone.esp", result.Removed[0].Path)
	assert.Equal(t, "dangling link into lmm cache", result.Removed[0].Reason)
	// Sweep finds don't carry mod identity (ConvergedFile's own doc
	// comment) - SourceID/ModID are empty here even though the path
	// happens to trace back to a known mod's row.

	_, err = os.Lstat(filepath.Join(gameDir, "gone.esp"))
	assert.True(t, os.IsNotExist(err), "the dangling symlink must be swept, even though its row's mod has unknown provenance")

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.Empty(t, rows, "the sweep's best-effort DeleteDeployedFile should clean up the now-orphaned row")
}

// TestConverge_RowPass_RejectsUnsafeDeployedFileRecords pins the PR-review
// Finding 1 fix: a deployed_files row is bookkeeping, not truth. A
// corrupted or hand-edited relative_path - absolute, or escaping game.ModPath
// via ".." - must never steer the row pass's Undeploy (or row delete)
// outside game.ModPath. The two unsafe rows here are seeded directly against
// the DB file backing svc via a second connection (db.New), bypassing every
// normal write path (installer/deploy) entirely - exactly the "someone
// hand-edited the sqlite file" shape this guards against.
func TestConverge_RowPass_RejectsUnsafeDeployedFileRecords(t *testing.T) {
	// gameDir is a known child of baseDir so "../outside.pak" resolves to an
	// exact, controlled location outside game.ModPath.
	baseDir := t.TempDir()
	gameDir := filepath.Join(baseDir, "gamedir")
	require.NoError(t, os.Mkdir(gameDir, 0o755))

	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   dataDir,
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// Copy-mode: CopyLinker.Undeploy is an unconditional os.Remove(dst), so a
	// pre-fix Undeploy against an escaped path would actually delete the
	// outside file, not just refuse it as SymlinkLinker's "not a symlink"
	// guard would.
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{
		"a.esp": []byte("a"),
	})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// A real, precious file genuinely outside game.ModPath at exactly the
	// location "../outside.pak" resolves to.
	outsidePath := filepath.Join(baseDir, "outside.pak")
	require.NoError(t, os.WriteFile(outsidePath, []byte("precious"), 0o644))

	// An absolute-path row: also unsafe per filepath.IsLocal's contract, even
	// though filepath.Join(game.ModPath, absPath) happens not to escape
	// game.ModPath in practice (Join treats a leading "/" as an ordinary
	// path segment, not a root reset) - IsLocal is the correct, general
	// guard regardless of Join's particular behavior.
	otherDir := t.TempDir()
	absPath := filepath.Join(otherDir, "abs.pak")
	require.NoError(t, os.WriteFile(absPath, []byte("precious2"), 0o644))

	rawDB, err := db.New(filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	require.NoError(t, rawDB.SaveDeployedFile(context.Background(), "g1", "default", "../outside.pak", "src", "m1"))
	require.NoError(t, rawDB.SaveDeployedFile(context.Background(), "g1", "default", absPath, "src", "m1"))
	require.NoError(t, rawDB.Close())

	result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", false)
	require.Error(t, err, "an unsafe deployed-file record must surface as an error")
	assert.Contains(t, err.Error(), "unsafe")
	assert.Contains(t, err.Error(), "../outside.pak")
	assert.Contains(t, err.Error(), absPath)

	_, statErr := os.Stat(outsidePath)
	assert.NoError(t, statErr, "the escaped file outside game.ModPath must survive untouched")
	_, statErr = os.Stat(absPath)
	assert.NoError(t, statErr, "the absolute-path target must survive untouched")

	var paths []string
	for _, cf := range result.Removed {
		paths = append(paths, cf.Path)
	}
	assert.NotContains(t, paths, "../outside.pak")
	assert.NotContains(t, paths, absPath)

	rows, err := svc.GetDeployedFilesForMod(context.Background(), "g1", "default", "src", "m1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a.esp", "../outside.pak", absPath}, rows, "unsafe rows are left alone - deleting records based on corrupt data is its own hazard")
}
