package core_test

// #676: a mod's file list is reused across flows - deploy, switch, apply,
// install, verify, a repair - while its source's change signal vouches the
// mod has not changed, rather than fetched again by each. Correctness
// first: a wanted file ID missing from the stored list fetches it again
// once, a mod the source no longer has drops its stored list, and an
// explicit refresh never reads it.

import (
	"context"
	"fmt"
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
	// urlErr, when set, is every download-URL request's answer.
	urlErr error
	// hold, when set, keeps UnchangedSince from answering until closed.
	hold chan struct{}
}

func (s *listingSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.mu.Lock()
	err := s.urlErr
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.mockSourceWithDownloads.GetDownloadURL(ctx, mod, fileID)
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
	if s.hold != nil {
		<-s.hold
	}
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

// seedListedRow installs mod 1 in g1/default with fileIDs and lists it in
// the profile.
func seedListedRow(t *testing.T, svc *core.Service, fileIDs ...string) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: *listMod(), ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: fileIDs,
	}))
	seedProfileWithMod(t, svc, "g1", "default", "lsrc", "1", "1.0")
}

// TestDeploy_AGoneDownloadRefetchesTheListAndRetriesOnce (#676 review): a
// download that finds its file gone asks the source for the list once
// more, within the same deploy; the source still offers the file, so its
// download is tried again - and succeeds.
func TestDeploy_AGoneDownloadRefetchesTheListAndRetriesOnce(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	seedListedRow(t, svc, "1")
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	payload := zipBytes(t, map[string]string{"plugin.esp": "payload"})
	src.onDownload = func() { src.AddDownload("1", payload) } // the first answer is a 404

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deployed, "%+v", result.Skipped)
	assert.Equal(t, 2, src.fetchCount(), "the list was fetched once more, within the deploy")
	assert.Equal(t, int64(2), src.downloads.Load(), "and the download tried once more")
}

// TestDeploy_AGoneFileTheSourceNoLongerOffersFailsWithTheFreshList: the
// fresh list no longer offers the file, so the download is not retried; the
// deploy fails as it would have, and the stored list is the fresh one.
func TestDeploy_AGoneFileTheSourceNoLongerOffersFailsWithTheFreshList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	seedListedRow(t, svc, "1")
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	src.mu.Lock()
	src.files = []domain.DownloadableFile{{ID: "9", FileName: "other.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"}}
	src.mu.Unlock()
	src.urlErr = fmt.Errorf("getting download links: %w", source.ErrFileGone)

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, 2, src.fetchCount())
	ageStoredLists(t, svc)
	files, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	assert.Equal(t, "9", files[0].ID, "the stored list is the source's current one")
	assert.Equal(t, 2, src.fetchCount())
}

// TestDeploy_AThrottledDownloadKeepsTheStoredList (#676 review): a download
// refused for a throttle says nothing about the list - dropping it would
// only send the throttled host more requests.
func TestDeploy_AThrottledDownloadKeepsTheStoredList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	seedListedRow(t, svc, "1")
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	src.urlErr = &source.RetryLaterError{Source: "Mock", Until: time.Now().Add(time.Minute), Reason: "rate limited"}

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, 1, src.fetchCount(), "no list fetched for a throttle")
	ageStoredLists(t, svc)
	_, err = svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	assert.Equal(t, 1, src.fetchCount(), "and the stored list is still there")
}

// TestPlanInstall_AskTheSourceForTheCurrentList (#676 review): an install
// the user asks for picks the mod's current file, so its plan never takes
// a stored list - one inside the change signal's lag would offer the file
// before the latest.
func TestPlanInstall_AsksTheSourceForTheCurrentList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	_, err = svc.NewProfileManager().Create(ctx, "g1", "default")
	require.NoError(t, err)

	_, err = svc.PlanInstall(ctx, game, "default", "lsrc", "1", false)
	require.NoError(t, err)
	assert.Equal(t, 2, src.fetchCount(), "the plan fetched the list")
}

// TestProfileApply_AVersionAStoredListLacksIsLookedForFresh (#676 review): a
// profile entry with a version and no file IDs, at a version uploaded
// inside the change signal's lag: the stored list does not have it, so the
// apply looks once in the source's own list rather than refusing.
func TestProfileApply_AVersionAStoredListLacksIsLookedForFresh(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	_, err := svc.GetModFiles(ctx, game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	src.mu.Lock()
	src.files = append(src.files, domain.DownloadableFile{ID: "2", FileName: "mod-2.zip", Version: "2.0", IsPrimary: true, Category: "MAIN"})
	src.mu.Unlock()
	src.AddDownload("2", zipBytes(t, map[string]string{"plugin.esp": "two"}))
	seedProfileWithMod(t, svc, "g1", "default", "lsrc", "1", "2.0")

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	installed, err := svc.GetInstalledMods(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.Equal(t, []string{"2"}, installed[0].FileIDs, "version 2.0's file, from the fresh list")
}

// TestSwitch_ASecondSwitchOfAnUnchangedModFetchesNoList: switching to a
// profile that has to download an unchanged mod twice over - its cache
// entry lost in between - asks for the mod's file list once.
func TestSwitch_ASecondSwitchOfAnUnchangedModFetchesNoList(t *testing.T) {
	svc, game, src := newListingService(t)
	ctx := context.Background()
	src.AddDownload("1", zipBytes(t, map[string]string{"plugin.esp": "payload"}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, "g1", "default")
	require.NoError(t, err)
	_, err = pm.Create(ctx, "g1", "other")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(ctx, "g1", "other", domain.ModReference{SourceID: "lsrc", ModID: "1", Version: "1.0", FileIDs: []string{"1"}}))

	switchTo := func(target string) {
		t.Helper()
		plan, err := svc.PlanProfileSwitch(ctx, game, target)
		require.NoError(t, err)
		_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
		require.NoError(t, err)
	}
	switchTo("other")
	require.Equal(t, 1, src.fetchCount())
	switchTo("default")
	ageStoredLists(t, svc)
	require.NoError(t, svc.GetGameCache(game).Delete(cache.Key{GameID: "g1", SourceID: "lsrc", ModID: "1", Version: "1.0"}))
	switchTo("other")

	assert.Equal(t, int64(2), src.downloads.Load(), "the second switch downloaded the file again")
	assert.Equal(t, 1, src.fetchCount(), "but not its list")
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

// TestGetModFiles_ACancelledCallerDoesNotWaitOutAnotherCallersVouch (#676
// review): a read waiting for another caller's change-signal request gives
// up with its own context - a closed browser tab is not held behind it.
func TestGetModFiles_ACancelledCallerDoesNotWaitOutAnotherCallersVouch(t *testing.T) {
	svc, game, src := newListingService(t)
	_, err := svc.GetModFiles(context.Background(), game, "lsrc", listMod())
	require.NoError(t, err)
	ageStoredLists(t, svc)
	src.hold = make(chan struct{})
	defer close(src.hold)

	go func() { _, _ = svc.GetModFiles(context.Background(), game, "lsrc", listMod()) }() // holds the turn
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = svc.GetModFiles(ctx, game, "lsrc", listMod())
	assert.Less(t, time.Since(start), 2*time.Second, "the cancelled read did not wait for the held answer")
}
