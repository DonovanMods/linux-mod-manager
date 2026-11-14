package core_test

// #645: a row lmm cannot download again - a local import, an archive
// variant, a row that records no source file - whose cache entry lost a
// member used to lose its deployed copy too: convergence (and so verify
// --fix) removed it as "no longer provided". Under copy or hardlink deploy
// that file may be the last copy anywhere. Convergence now keeps it, and
// verify reports it as cache_incomplete, not fixable, because the deployed
// copy is the only one left. A symlink deploy is unaffected: removing a
// dangling link loses nothing.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// dropCacheMember deletes member and every completion marker from k's
// entry, as a person tidying the cache by hand would, leaving the identity
// marker.
func dropCacheMember(t *testing.T, c *cache.Cache, k cache.Key, member string) {
	t.Helper()
	dir := c.ModPath(k)
	require.FileExists(t, filepath.Join(dir, cache.KeyMarkerName), "precondition: the entry carries the identity marker")
	require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(member))))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), cache.ReservedPrefix+"file-") {
			require.NoError(t, os.Remove(filepath.Join(dir, e.Name())))
		}
	}
}

// lastCopyFindings is the cache_incomplete findings for path.
func lastCopyFindings(result *core.VerifyResult, path string) []core.VerifyFinding {
	var out []core.VerifyFinding
	for _, f := range findingsWithStatus(result, core.VerifyStatusCacheIncomplete) {
		if f.FileID == path {
			out = append(out, f)
		}
	}
	return out
}

// assertLastCopyKept runs verify plain and with --fix: each reports path as
// one cache_incomplete finding that is not fixable, removes nothing, and
// leaves the deployed file and its record where they are.
func assertLastCopyKept(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, path, content string) {
	t.Helper()
	ctx := context.Background()
	for _, fix := range []bool{false, true} {
		result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: fix}, nil)
		require.NoError(t, err)
		rows := lastCopyFindings(result, path)
		require.Len(t, rows, 1, "fix=%v: one finding per path: %+v", fix, result.Findings)
		row := rows[0]
		assert.Equal(t, modID, row.ModID)
		assert.NotEmpty(t, row.ModName)
		assert.Contains(t, row.Note, path)
		assert.False(t, row.Fixable, "fix=%v", fix)
		assert.Equal(t, core.LastCopyReason, row.FixableReason)
		assert.Empty(t, findingsWithStatus(result, "stale_deployment"), "fix=%v: %+v", fix, result.Findings)
		assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"), "fix=%v: %+v", fix, result.Findings)
		assert.Empty(t, findingsWithStatus(result, "missing"), "fix=%v: not counted twice", fix)
		assert.GreaterOrEqual(t, result.Issues, 1)

		target := filepath.Join(game.ModPath, filepath.FromSlash(path))
		info, err := os.Lstat(target)
		require.NoError(t, err, "fix=%v: the deployed copy is kept", fix)
		assert.True(t, info.Mode().IsRegular())
		b, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, content, string(b))
		recorded, err := svc.GetDeployedFilesForMod(ctx, game.ID, "default", sourceID, modID)
		require.NoError(t, err)
		assert.Contains(t, recorded, filepath.FromSlash(path), "fix=%v: and so is its record", fix)
	}
}

// importLocal imports a two-file archive with no source, on a game
// deployed by method, and returns the installed row.
func importLocal(t *testing.T, method domain.LinkMethod) (*core.Service, *domain.Game, domain.InstalledMod) {
	t.Helper()
	svc, game := newDefaultProfileService(t)
	game.LinkMethod, game.LinkMethodExplicit = method, true
	ctx := context.Background()
	archive := filepath.Join(t.TempDir(), "LocalMod-1.0.zip")
	createImportTestZip(t, archive, map[string]string{"local-a.esp": "a", "local-b.esp": "b"})
	_, err := svc.ImportArchive(ctx, game, "default", archive, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	require.Equal(t, domain.SourceLocal, mods[0].SourceID)
	require.Empty(t, mods[0].FileIDs, "precondition: a local import records no source file")
	return svc, game, mods[0]
}

// A local import deployed by copy loses a member from its entry: the
// deployed copy is the only one left, so it is kept and reported.
func TestVerify_LastCopy_LocalImportUnderCopy(t *testing.T) {
	svc, game, row := importLocal(t, domain.LinkCopy)
	c := svc.GetGameCache(game)
	dropCacheMember(t, c, cacheKeyOf(&row), "local-a.esp")

	assertLastCopyKept(t, svc, game, row.SourceID, row.ID, "local-a.esp", "a")
	assert.True(t, deployedFile(t, game, "local-b.esp"), "the untouched file stays")
}

// An archive variant (#644: an archive its source does not list) deployed
// by hardlink loses its member: there is nothing to download it from.
func TestVerify_LastCopy_ArchiveVariantUnderHardlink(t *testing.T) {
	f := newFromFileFixture(t)
	f.game.LinkMethod, f.game.LinkMethodExplicit = domain.LinkHardlink, true
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	plan := f.plan(t, customArchive(t, "custom build"), core.UpdateFromArchiveOptions{})
	require.Empty(t, plan.FileIDs, "precondition: the archive matched no source file")
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{AcceptMismatch: true}, nil)
	require.NoError(t, err)
	row := f.row(t)
	require.NotEmpty(t, row.Variant, "precondition: an archive variant")
	require.Empty(t, row.FileIDs)
	c := f.svc.GetGameCache(f.game)
	const member = "Auctionator/Auctionator-new.lua"
	dropCacheMember(t, c, cacheKeyOf(row), member)

	assertLastCopyKept(t, f.svc, f.game, "msrc", "42", member, "custom build")
}

// The symlink control: a link into a lost member is dangling, and removing
// it loses nothing - convergence still removes it as a stale deployment.
func TestVerify_LastCopy_SymlinkDeployIsUnaffected(t *testing.T) {
	svc, game, row := importLocal(t, domain.LinkSymlink)
	ctx := context.Background()
	c := svc.GetGameCache(game)
	dropCacheMember(t, c, cacheKeyOf(&row), "local-a.esp")

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, lastCopyFindings(result, "local-a.esp"), "%+v", result.Findings)
	stale := findingsWithStatus(result, "stale_deployment")
	require.Len(t, stale, 1, "%+v", result.Findings)
	assert.Equal(t, "local-a.esp", stale[0].FileID)

	result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, lastCopyFindings(result, "local-a.esp"))
	fixed := findingsWithStatus(result, "fixed_stale_deployment")
	require.Len(t, fixed, 1, "%+v", result.Findings)
	assert.Equal(t, "local-a.esp", fixed[0].FileID)
	_, err = os.Lstat(filepath.Join(game.ModPath, "local-a.esp"))
	assert.True(t, os.IsNotExist(err), "the dangling link is removed")
	assert.True(t, deployedFile(t, game, "local-b.esp"))
}

// A mod lmm can download again keeps #642's behaviour under copy: the lost
// file is a fixable cache_incomplete row, --fix re-downloads it, and no
// row says the deployed copy is the only one left.
func TestVerify_LastCopy_RedownloadableModIsRepairedAsBefore(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkCopy, "20", "21")
	ctx := context.Background()
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	loseMember(t, c, k, "mod1-a.esp", "20")

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	lost := modFinding(t, result, "20")
	assert.Equal(t, core.VerifyStatusCacheIncomplete, lost.Status)
	assert.True(t, lost.Fixable)
	assert.Empty(t, lastCopyFindings(result, "mod1-a.esp"))
	assert.Equal(t, 1, result.Issues)

	result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", modFinding(t, result, "20").Status)
	assert.Empty(t, lastCopyFindings(result, "mod1-a.esp"))
	assert.Equal(t, 0, result.Issues)
	assert.True(t, c.HasFileIDs(k, []string{"20", "21"}))
}

// A re-downloadable mod's stale copy - a path its complete entry no longer
// provides - is still removed: the rule is only for a row lmm cannot
// download again.
func TestVerify_LastCopy_RedownloadableModStaleCopyIsStillRemoved(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkCopy, "20", "21")
	ctx := context.Background()
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	// The entry stops providing mod1-b.esp with its marker recording that:
	// file 21's marker now records no member.
	require.NoError(t, os.Remove(filepath.Join(c.ModPath(k), "mod1-b.esp")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(c.ModPath(k), "21", nil))

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, lastCopyFindings(result, "mod1-b.esp"), "%+v", result.Findings)
	fixed := findingsWithStatus(result, "fixed_stale_deployment")
	require.Len(t, fixed, 1, "%+v", result.Findings)
	assert.Equal(t, "mod1-b.esp", fixed[0].FileID)
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-b.esp"))
	assert.True(t, os.IsNotExist(err))
}

// The converge result carries the kept path, so a caller other than verify
// sees it too.
func TestConverge_LastCopy_IsKeptNotRemoved(t *testing.T) {
	svc, game, row := importLocal(t, domain.LinkCopy)
	c := svc.GetGameCache(game)
	dropCacheMember(t, c, cacheKeyOf(&row), "local-a.esp")

	for _, dryRun := range []bool{true, false} {
		result, err := svc.ConvergeDeployedFilesForTest(context.Background(), game, "default", dryRun)
		require.NoError(t, err)
		assert.Empty(t, result.Removed, "dryRun=%v", dryRun)
		kept := core.ConvergeLastCopiesForTest(result)
		require.Len(t, kept, 1, "dryRun=%v", dryRun)
		assert.Equal(t, "local-a.esp", kept[0].Path)
		assert.Equal(t, row.ID, kept[0].ModID)
	}
	assert.True(t, deployedFile(t, game, "local-a.esp"))
}

// #645's second item: a legacy entry - a BARE completion marker, written
// before markers recorded members, plus the identity marker - is re-laid
// out under BepInEx/ by verify --fix (#424), which drops the package's
// manifest.json and moves the plugin. The re-layout used to keep the marker
// bare, so a profile whose records still name the old layout read the
// entry as having lost them - cache_incomplete forever, and convergence
// kept those records forever too. The rebuilt entry's sole marker now
// records the members the re-layout placed.
func TestVerify_LoaderTier_RelayoutRecordsABareMarkersMembers(t *testing.T) {
	svc, game := newBepInExGameRootService(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()
	archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Jotunn/Jotunn.dll": "assembly",
		"Jotunn/Jotunn.xml": "<doc/>",
		"manifest.json":     "{}",
	})
	var mod *domain.Mod
	for _, profile := range []string{"default", "second"} {
		if profile != "default" {
			_, err := pm.Create(ctx, game.ID, profile)
			require.NoError(t, err)
		}
		if _, err := pm.Get(ctx, game.ID, profile); err == nil {
			require.NoError(t, pm.SetDefault(ctx, game.ID, profile))
		}
		res, err := svc.ImportArchive(ctx, game, profile, archivePath,
			core.ImportArchiveOptions{SourceID: domain.SourceLocal, ModID: "1138", Force: true}, nil)
		require.NoError(t, err)
		mod = res.Mod
		row, err := svc.GetInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, profile)
		require.NoError(t, err)
		row.FileIDs = []string{"1"} // a legacy row records its file
		if profile == "second" {
			// second's rows outlive its deployment (the #183 drift), so
			// the re-layout leaves them naming the old layout.
			row.Deployed = false
		}
		require.NoError(t, svc.SaveInstalledMod(ctx, row))
	}
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	k := cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, Variant: mod.Variant}
	c := svc.GetGameCache(game)
	entry := c.ModPath(k)
	require.FileExists(t, filepath.Join(entry, cache.KeyMarkerName))
	require.NoError(t, cache.MarkFileComplete(entry, "1"))
	stale, err := svc.GetDeployedFilesForMod(ctx, game.ID, "second", mod.SourceID, mod.ID)
	require.NoError(t, err)
	require.Contains(t, stale, "manifest.json", "precondition: second records the old layout")

	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Now())
	game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapProton}
	require.NoError(t, svc.SaveGame(ctx, game))
	fixed, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, findingWithStatus(fixed.Result, "fixed_loader_deployed_outside_loader"), "%v", findingStatuses(fixed.Result))

	manifests, err := c.FileManifests(k)
	require.NoError(t, err)
	require.Contains(t, manifests, "1")
	assert.True(t, manifests["1"].Recorded, "the sole marker records the members the re-layout placed")
	members := make([]string, 0, len(manifests["1"].Members))
	for _, m := range manifests["1"].Members {
		members = append(members, filepath.ToSlash(m))
	}
	assert.ElementsMatch(t, []string{"BepInEx/plugins/Jotunn/Jotunn.dll", "BepInEx/plugins/Jotunn/Jotunn.xml"}, members)

	for _, profile := range []string{"default", "second"} {
		again, err := svc.VerifyReport(ctx, game, profile, core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		assert.Empty(t, findingsWithStatus(again.Result, core.VerifyStatusCacheIncomplete),
			"profile %s: the re-layout lost nothing: %+v", profile, again.Result.Findings)
	}
}
