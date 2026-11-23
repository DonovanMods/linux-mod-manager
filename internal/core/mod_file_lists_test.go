package core_test

// #676: a mod's file list is reused across flows - deploy, switch, apply,
// install, verify, a repair - while its source's change signal vouches the
// mod has not changed, rather than fetched again by each. Correctness
// first: a wanted file ID missing from the stored list fetches it again
// once, a mod the source no longer has drops its stored list, and an
// explicit refresh never reads it.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// listingSource serves downloads and a scripted file list, counts every
// file-list fetch, and has a change signal: every mod is unchanged unless
// changed says otherwise.
type listingSource struct {
	*mockSourceWithDownloads
	mu       sync.Mutex
	files    []domain.DownloadableFile
	fetches  int
	vouches  int
	changed  bool
	notFound bool
}

func newListingSource(t *testing.T) *listingSource {
	t.Helper()
	s := &listingSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("lsrc"),
		files:                   []domain.DownloadableFile{{ID: "1", FileName: "mod.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"}},
	}
	t.Cleanup(s.Close)
	return s
}

func (s *listingSource) GetModFiles(_ context.Context, _ *domain.Mod) ([]domain.DownloadableFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	if s.notFound {
		return nil, domain.ErrModNotFound
	}
	return append([]domain.DownloadableFile(nil), s.files...), nil
}

func (s *listingSource) UnchangedSince(_ context.Context, _ string, since map[string]time.Time) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vouches++
	out := map[string]time.Time{}
	if s.changed {
		return out, nil
	}
	for id := range since {
		out[id] = time.Now()
	}
	return out, nil
}

func (s *listingSource) fetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

var _ source.ChangeTracker = (*listingSource)(nil)

// plainFilesSource counts file-list fetches and has no change signal.
type plainFilesSource struct {
	*mockSource
	calls int
}

func (s *plainFilesSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	s.calls++
	return []domain.DownloadableFile{{ID: "1"}}, nil
}

// newListingService registers a listingSource for game g1 with mod 1.
func newListingService(t *testing.T) (*core.Service, *domain.Game, *listingSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	src := newListingSource(t)
	src.AddMod("g1", &domain.Mod{ID: "1", SourceID: "lsrc", Name: "Listed Mod", Version: "1.0", GameID: "g1"})
	svc.RegisterSource(src)
	return svc, game, src
}

// ageStoredLists makes every stored list older than the store's own
// freshness window, so its use depends on the source vouching for it.
func ageStoredLists(t *testing.T, svc *core.Service) {
	t.Helper()
	old := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	require.NoError(t, svc.ExecForTest(context.Background(), `UPDATE mod_file_lists SET checked_at = ?, fetched_at = ?`, old, old))
	core.ForgetFileListVouchesForTest(svc)
}

func listMod() *domain.Mod {
	return &domain.Mod{ID: "1", SourceID: "lsrc", Name: "Listed Mod", Version: "1.0", GameID: "g1"}
}

func TestGetModFiles_AnUnchangedModsListIsReused(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()

	first, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	second, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)

	assert.Equal(t, 1, src.fetchCount(), "the source vouched for it: no second fetch")
	assert.Equal(t, first, second)
}

func TestGetModFiles_AChangedModIsFetchedAgain(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)

	src.mu.Lock()
	src.changed = true
	src.files = append(src.files, domain.DownloadableFile{ID: "2", FileName: "mod-2.zip", Version: "2.0", IsPrimary: true, Category: "MAIN"})
	src.mu.Unlock()
	files, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	assert.Equal(t, 2, src.fetchCount())
	assert.Len(t, files, 2, "the new list, stored in place of the old")
}

func TestGetModFiles_ASourceWithoutAChangeSignalIsAlwaysAsked(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	plain := &plainFilesSource{mockSource: newMockSource("plain")}
	svc.RegisterSource(plain)
	for range 2 {
		_, err := svc.GetModFiles(context.Background(), game, "plain", &domain.Mod{ID: "1", SourceID: "plain", GameID: "g1"})
		require.NoError(t, err)
	}
	assert.Equal(t, 2, plain.calls, "nothing vouches for its list, so nothing is reused")
}

func TestGetModFiles_NotFoundDropsTheStoredList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)

	src.mu.Lock()
	src.changed, src.notFound = true, true
	src.mu.Unlock()
	ageStoredLists(t, svc)
	_, err = svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.ErrorIs(t, err, domain.ErrModNotFound)

	// Back, and vouched for again: there is no stored list left to answer.
	src.mu.Lock()
	src.changed, src.notFound = false, false
	src.mu.Unlock()
	_, err = svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	assert.Equal(t, 3, src.fetchCount(), "the gone mod's list was not kept")
}

// TestDeploy_AWantedFileMissingFromTheStoredListFetchesOnce: the stored
// list predates the installed file (a file uploaded and installed within
// the listing's lag): the deploy does not trust it, fetches once, and
// deploys the file.
func TestDeploy_AWantedFileMissingFromTheStoredListFetchesOnce(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)

	src.mu.Lock()
	src.files = []domain.DownloadableFile{{ID: "2", FileName: "mod-2.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"}}
	src.mu.Unlock()
	src.AddDownload("2", zipBytes(t, map[string]string{"plugin.esp": "two"}))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *listMod(), ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"2"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "lsrc", "1", "1.0")

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deployed, "%+v", result.Skipped)
	assert.Equal(t, 2, src.fetchCount(), "file 2 was not in the stored list: fetched once more")
}

// TestDeploy_ASecondRedownloadOfAnUnchangedModFetchesNoList is the
// acceptance case: two deploys that each have to re-download an unchanged
// mod ask for its file list once.
func TestDeploy_ASecondRedownloadOfAnUnchangedModFetchesNoList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	src.AddDownload("1", zipBytes(t, map[string]string{"plugin.esp": "payload"}))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *listMod(), ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"1"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "lsrc", "1", "1.0")

	for i := range 2 {
		if i > 0 {
			ageStoredLists(t, svc)
			require.NoError(t, svc.GetGameCache(game).Delete(cache.Key{GameID: "g1", SourceID: "lsrc", ModID: "1", Version: "1.0"}))
		}
		result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
		require.Equal(t, 1, result.Deployed, "%+v", result.Skipped)
	}
	assert.Equal(t, 1, src.fetchCount(), "the second deploy re-used the vouched list")
	assert.Equal(t, int64(2), src.downloads.Load(), "the file itself was downloaded both times")
}

// TestDeploy_AFileTheHostNoLongerHasDropsTheStoredList: a download that
// fails as gone (HTTP 404) leaves no stored list behind, so the next
// attempt asks the source.
func TestDeploy_AFileTheHostNoLongerHasDropsTheStoredList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *listMod(), ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"1"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "lsrc", "1", "1.0")

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil) // no payload for file 1: 404
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0].Reason, "404")

	ageStoredLists(t, svc)
	_, err = svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	assert.Equal(t, 2, src.fetchCount(), "the list that offered the gone file was dropped")
}

// TestVerify_RefreshBypassesTheStoredList: an explicit re-verify asks the
// source for the list again.
func TestVerify_RefreshBypassesTheStoredList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *listMod(), ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"1"},
	}))
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)

	_, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull, Force: true, Refresh: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, src.fetchCount(), "refresh asks the source")

	ageStoredLists(t, svc)
	_, err = svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	assert.Equal(t, 2, src.fetchCount(), "and stores what it got")
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	data, err := os.ReadFile(createTestZip(t, filepath.Join(t.TempDir()), files))
	require.NoError(t, err)
	return data
}

// TestProfileApply_ASecondInstallOfAnUnchangedModFetchesNoList: applying a
// profile that installs an unchanged mod twice over - the second time
// after its install was removed - asks for its file list once.
func TestProfileApply_ASecondInstallOfAnUnchangedModFetchesNoList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	src.AddDownload("1", zipBytes(t, map[string]string{"plugin.esp": "payload"}))
	seedProfileWithMod(t, svc, "g1", "default", "lsrc", "1", "1.0")

	for i := range 2 {
		if i > 0 {
			ageStoredLists(t, svc)
			require.NoError(t, svc.DeleteInstalledMod(ctx, "lsrc", "1", "g1", "default"))
			require.NoError(t, svc.GetGameCache(game).Delete(cache.Key{GameID: "g1", SourceID: "lsrc", ModID: "1", Version: "1.0"}))
		}
		plan, err := svc.PlanProfileApply(ctx, game, "default")
		require.NoError(t, err)
		result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		installed, err := svc.GetInstalledMods(ctx, "g1", "default")
		require.NoError(t, err)
		require.Len(t, installed, 1, "apply %d installed the mod: %+v", i+1, result)
	}
	assert.Equal(t, 1, src.fetchCount(), "the second apply re-used the vouched list")
}

// TestGetModFiles_ConcurrentReadsShareOneAnswer: `lmm serve` reads lists
// from many requests at once. Run under -race; the reads agree, and the
// change signal is asked once for them all.
func TestGetModFiles_ConcurrentReadsShareOneAnswer(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			files, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
			if err == nil && len(files) != 1 {
				err = assert.AnError
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, src.fetchCount(), "every read answered from the store")
	src.mu.Lock()
	defer src.mu.Unlock()
	assert.Equal(t, 1, src.vouches, "one change-signal answer for all of them")
}
