package core_test

// #642: a cache entry that lost a member AND that member's completion marker
// outside lmm used to verify as "ok" - the entry directory was there - and
// --fix's convergence then removed the deployed copy as "no longer provided".
// verify now judges the entry against the installed row's own files: a
// file whose content is gone is a cache_incomplete finding, --fix
// re-downloads it, and no deployed copy is ever removed because the cache
// lost its member.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// loseMember deletes member and fileID's completion marker from k's entry,
// as a person tidying the cache by hand would.
func loseMember(t *testing.T, c *cache.Cache, k cache.Key, member, fileID string) {
	t.Helper()
	dir := c.ModPath(k)
	require.FileExists(t, filepath.Join(dir, filepath.FromSlash(member)))
	require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(member))))
	require.NoError(t, os.Remove(filepath.Join(dir, cache.ReservedPrefix+"file-"+fileID)))
}

// installSwapMod applies the default profile with mod1 pinned to ids, from
// sameVersionSwapSource, on a game deployed by method.
func installSwapMod(t *testing.T, method domain.LinkMethod, ids ...string) (*core.Service, *domain.Game, *sameVersionSwapSource) {
	t.Helper()
	svc, game := newDefaultProfileService(t)
	game.LinkMethod, game.LinkMethodExplicit = method, true
	src := newSameVersionSwapSource(t)
	svc.RegisterSource(src)
	require.NoError(t, svc.NewProfileManager().AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: ids}))
	applyProfile(t, svc, game, "default")
	return svc, game, src
}

func findingsWithStatus(result *core.VerifyResult, status string) []core.VerifyFinding {
	var out []core.VerifyFinding
	for _, f := range result.Findings {
		if f.Status == status {
			out = append(out, f)
		}
	}
	return out
}

func modFinding(t *testing.T, result *core.VerifyResult, fileID string) core.VerifyFinding {
	t.Helper()
	for _, f := range result.Findings {
		if f.FileID == fileID && f.ModID == "mod1" {
			return f
		}
	}
	require.Failf(t, "no finding", "no finding for file %s in %+v", fileID, result.Findings)
	return core.VerifyFinding{}
}

func installedRow(t *testing.T, svc *core.Service, game *domain.Game) *domain.InstalledMod {
	t.Helper()
	row, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	return row
}

// A plain mod of two files: one file's member and marker are deleted. The
// plain run reports that file as cache_incomplete - an issue a --fix run
// would repair - and nothing as a stale deployment; --fix re-downloads it,
// and the deployed file is never removed.
func TestVerify_CacheIncomplete_PlainModLostMemberAndMarker(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkSymlink, domain.LinkCopy} {
		t.Run(method.String(), func(t *testing.T) {
			svc, game, src := installSwapMod(t, method, "20", "21")
			ctx := context.Background()
			c := svc.GetGameCache(game)
			k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
			require.True(t, deployedFile(t, game, "mod1-a.esp"))
			loseMember(t, c, k, "mod1-a.esp", "20")

			result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
			require.NoError(t, err)
			lost := modFinding(t, result, "20")
			assert.Equal(t, core.VerifyStatusCacheIncomplete, lost.Status)
			assert.Contains(t, lost.Note, "mod1-a.esp")
			assert.True(t, lost.Fixable, "--fix re-downloads it")
			assert.Empty(t, lost.FixableReason)
			assert.Equal(t, "ok", modFinding(t, result, "21").Status, "the untouched file is fine")
			assert.Equal(t, 1, result.Issues)
			assert.Empty(t, findingsWithStatus(result, "stale_deployment"), "a lost cache member is not a stale deployment")

			downloads := src.DownloadCount()
			result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
			require.NoError(t, err)
			fixed := modFinding(t, result, "20")
			assert.Equal(t, "ok", fixed.Status)
			assert.Equal(t, "Re-downloaded OK", fixed.Repair)
			assert.Equal(t, 0, result.Issues)
			assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"), "the deployed copy is never removed")
			assert.Equal(t, downloads+1, src.DownloadCount(), "only the lost file is fetched")

			assert.True(t, c.HasFileIDs(k, []string{"20", "21"}), "the entry is whole again")
			assert.FileExists(t, filepath.Join(c.ModPath(k), "mod1-a.esp"))
			b, err := os.ReadFile(filepath.Join(game.ModPath, "mod1-a.esp"))
			require.NoError(t, err, "the deployed file is still there")
			assert.Equal(t, "a", string(b))
			assert.True(t, deployedFile(t, game, "mod1-b.esp"))

			again, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, 0, again.Issues, "%+v", again.Findings)
			assert.Equal(t, 0, again.Warnings, "%+v", again.Findings)
		})
	}
}

// A plain mod of ONE file loses its only member and its marker: the entry
// is left with no completion record at all, so the profile's own deployed
// records are what say a member is gone.
func TestVerify_CacheIncomplete_SingleFileModLostItsOnlyMember(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkCopy, "20")
	ctx := context.Background()
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	loseMember(t, c, k, "mod1-a.esp", "20")

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	lost := modFinding(t, result, "20")
	assert.Equal(t, core.VerifyStatusCacheIncomplete, lost.Status)
	assert.Contains(t, lost.Note, "mod1-a.esp")
	assert.Empty(t, findingsWithStatus(result, "stale_deployment"))

	result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", modFinding(t, result, "20").Status)
	assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"))
	assert.True(t, c.HasFileIDs(k, []string{"20"}))
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
}

// An entry that only lost a member - its marker, with the member list it
// records, is still there - is incomplete too.
func TestVerify_CacheIncomplete_RecordedMemberGone(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkSymlink, "20", "21")
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	require.NoError(t, os.Remove(filepath.Join(c.ModPath(k), "mod1-b.esp")))

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", modFinding(t, result, "21").Status)
	assert.Equal(t, "Re-downloaded OK", modFinding(t, result, "21").Repair)
	assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"))
	assert.FileExists(t, filepath.Join(c.ModPath(k), "mod1-b.esp"))
	assert.FileExists(t, filepath.Join(game.ModPath, "mod1-b.esp"), "the link resolves again")
}

// A legacy entry - written before completion markers, and holding every
// member the profile deployed - is not reported: nothing says it lost
// anything.
func TestVerify_CacheIncomplete_UnmarkedCompleteEntryIsOK(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkSymlink, "20", "21")
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	for _, id := range []string{"20", "21"} {
		require.NoError(t, os.Remove(filepath.Join(c.ModPath(k), cache.ReservedPrefix+"file-"+id)))
	}

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, findingsWithStatus(result, core.VerifyStatusCacheIncomplete), "%+v", result.Findings)
	assert.Equal(t, 0, result.Issues)
}

// A FOMOD mod's tree - its variant entry - loses an installed member and the
// archive's marker: --fix rebuilds the row's own tree from the recorded
// choices, and the deployed file stays.
func TestVerify_CacheIncomplete_FomodMod(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	require.NotEmpty(t, row.Variant)
	c := f.svc.GetGameCache(f.game)
	k := cacheKeyOf(row)
	loseMember(t, c, k, "textures/t.dds", "mod1")

	result, err := f.svc.VerifyForTest(ctx, f.game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	lost := modFinding(t, result, "mod1")
	assert.Equal(t, core.VerifyStatusCacheIncomplete, lost.Status)
	assert.Contains(t, lost.Note, "t.dds")
	assert.Empty(t, findingsWithStatus(result, "stale_deployment"))

	result, err = f.svc.VerifyForTest(ctx, f.game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", modFinding(t, result, "mod1").Status, "%+v", result.Findings)
	assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"))
	assert.FileExists(t, filepath.Join(c.ModPath(k), "textures", "t.dds"), "the row's own variant is rebuilt")
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "PLUGIN-v1", f.deployed(t, "plugin.esp"))
	assert.Equal(t, []string{filepath.Base(c.ModPath(k))}, modContainerEntries(t, f, row), "and no other entry")
}

// A #639 file-set entry - the tree of a same-version file swap - loses its
// member and marker: --fix refills that entry, never the plain one.
func TestVerify_CacheIncomplete_FileSetEntry(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkSymlink, "20")
	ctx := context.Background()
	setRefFiles(t, svc, game.ID, "default", "21")
	applyProfile(t, svc, game, "default")
	row := installedRow(t, svc, game)
	require.NotEmpty(t, row.Variant, "the swapped set has an entry of its own")
	c := svc.GetGameCache(game)
	k := cacheKeyOf(row)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	loseMember(t, c, k, "mod1-b.esp", "21")

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, core.VerifyStatusCacheIncomplete, modFinding(t, result, "21").Status)
	assert.Empty(t, findingsWithStatus(result, "stale_deployment"))

	result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", modFinding(t, result, "21").Status, "%+v", result.Findings)
	assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"))
	files, err := c.ListFiles(k)
	require.NoError(t, err)
	assert.Equal(t, []string{"mod1-b.esp"}, files, "the file-set entry is refilled with its own file")
	assert.True(t, c.HasFileIDs(k, []string{"21"}))
	plainFiles, err := c.ListFiles(plain)
	require.NoError(t, err)
	assert.Equal(t, []string{"mod1-a.esp"}, plainFiles, "the plain entry is untouched")
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.Equal(t, row.Variant, installedRow(t, svc, game).Variant)
}

// The mod's source cannot be reached: the plain run reports the lost file,
// --fix's re-download fails and says so, and the deployed copy is left
// exactly where it is - in both runs.
func TestVerify_CacheIncomplete_UnreachableSourceKeepsTheDeployedCopy(t *testing.T) {
	svc, game, src := installSwapMod(t, domain.LinkCopy, "20", "21")
	ctx := context.Background()
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	loseMember(t, c, k, "mod1-a.esp", "20")
	src.Close() // every download now fails

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, core.VerifyStatusCacheIncomplete, modFinding(t, result, "20").Status)
	assert.Empty(t, findingsWithStatus(result, "stale_deployment"))

	result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	lost := modFinding(t, result, "20")
	assert.Equal(t, core.VerifyStatusCacheIncomplete, lost.Status, "still incomplete")
	assert.NotEmpty(t, lost.Note, "the failure's reason")
	assert.False(t, lost.Fixable)
	assert.Equal(t, "this --fix run already attempted a repair for it", lost.FixableReason)
	assert.Equal(t, 1, result.Issues)
	assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"), "%+v", result.Findings)

	b, err := os.ReadFile(filepath.Join(game.ModPath, "mod1-a.esp"))
	require.NoError(t, err, "the deployed copy is kept")
	assert.Equal(t, "a", string(b))
	files, err := svc.GetDeployedFilesForMod(ctx, game.ID, "default", "src", "mod1")
	require.NoError(t, err)
	assert.Contains(t, files, "mod1-a.esp", "and so is its record")
}

// A locally imported mod has no source to fetch from: its lost file is
// reported, not fixable, and --fix keeps its deployed copy.
func TestVerify_CacheIncomplete_LocalModIsReportedNotFixable(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	game.LinkMethod, game.LinkMethodExplicit = domain.LinkCopy, true
	ctx := context.Background()
	archive := filepath.Join(t.TempDir(), "LocalMod-1.0.zip")
	createImportTestZip(t, archive, map[string]string{"local-a.esp": "a", "local-b.esp": "b"})
	_, err := svc.ImportArchive(ctx, game, "default", archive, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	row := mods[0]
	require.Equal(t, domain.SourceLocal, row.SourceID)
	c := svc.GetGameCache(game)
	k := cacheKeyOf(&row)
	// An archive import that resolves to no source records no files; a
	// local row that does - the state a v1.30.1 switch leaves - is judged
	// by them.
	require.Empty(t, row.FileIDs)
	require.NoError(t, cache.MarkFileCompleteWithMembers(c.ModPath(k), "LocalMod-1.0.zip", []string{"local-a.esp", "local-b.esp"}))
	row.FileIDs = []string{"LocalMod-1.0.zip"}
	require.NoError(t, svc.SaveInstalledMod(ctx, &row))
	require.NoError(t, svc.SaveFileChecksum(ctx, row.SourceID, row.ID, game.ID, "default", "LocalMod-1.0.zip", "abc123"))
	loseMember(t, c, k, "local-a.esp", "LocalMod-1.0.zip")

	for _, fix := range []bool{false, true} {
		result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: fix}, nil)
		require.NoError(t, err)
		lost := findingsWithStatus(result, core.VerifyStatusCacheIncomplete)
		require.NotEmpty(t, lost, "fix=%v: %+v", fix, result.Findings)
		assert.Contains(t, lost[0].Note, "local-a.esp")
		assert.False(t, lost[0].Fixable)
		assert.Contains(t, lost[0].FixableReason, "imported locally")
		assert.Empty(t, findingsWithStatus(result, "fixed_stale_deployment"))
		assert.Empty(t, findingsWithStatus(result, "stale_deployment"))
		b, err := os.ReadFile(filepath.Join(game.ModPath, "local-a.esp"))
		require.NoError(t, err, "fix=%v: the deployed copy is kept", fix)
		assert.Equal(t, "a", string(b))
	}
}
