package core_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #543: a mod whose source will not serve its files through the
// API (source.ErrManualDownload, or the source's own classification) is
// recorded as manual-only, shown as such, and never attempted by a batch.

// countingFromFileSource is fromFileSource counting every download it
// refuses, so a test can prove a batch never asked.
type countingFromFileSource struct {
	*fromFileSource
	urlCalls atomic.Int32
}

func (s *countingFromFileSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.urlCalls.Add(1)
	return s.fromFileSource.GetDownloadURL(ctx, mod, fileID)
}

// refusingSource serves its files until refuse is set, then refuses every
// download the way CurseForge does for an author's opt-out.
type refusingSource struct {
	*multiFileDownloadSource
	refuse atomic.Bool
}

func (s *refusingSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	if s.refuse.Load() {
		return "", &source.ManualDownloadError{Reason: manualReason}
	}
	return s.multiFileDownloadSource.GetDownloadURL(ctx, mod, fileID)
}

// failSingleUpdate runs the explicit update of the fixture's Auctionator,
// which its source refuses, and requires the refusal.
func (f *fromFileFixture) failSingleUpdate(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *f.row(t), NewVersion: "2.0",
		FileIDReplacements: map[string]string{"100": "200"}})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{}, nil)
	require.ErrorIs(t, err, source.ErrManualDownload)
}

// The install the source refused leaves no row, so the fact is carried: the
// install from the hand-downloaded archive that follows records it, though
// this source says nothing about it in its metadata.
func TestInstallFromFile_AfterTheSourceRefusedTheInstall_RecordsManualOnly(t *testing.T) {
	f := newInstallFromFileFixture(t)
	dl := f.failedInstall(t)
	require.True(t, dl.ManualDownload)
	_, err := f.svc.GetInstalledMod(context.Background(), "msrc", "43", "g1", "default")
	require.Error(t, err, "a refused install records no row")

	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{ExpectedFileID: dl.FileID})
	_, err = f.apply(t, plan, core.ImportArchiveOptions{})
	require.NoError(t, err)

	assert.True(t, f.row(t).ManualOnly, "the refusal the install met is the mod's, and outlives the failed install")
}

// An archive import named to a source-backed mod asks the source: one that
// classifies the mod as manual-only (CurseForge's allowModDistribution) is
// recorded without any download ever being tried.
func TestImportArchive_SourceClassifiesTheModManualOnly_RecordsIt(t *testing.T) {
	f := newInstallFromFileFixture(t)
	f.src.mods["43"].ManualOnly = true

	archive := f.archive(t, "MapUtils-1.2.zip")
	plan, err := f.svc.PlanImportArchive(context.Background(), f.game, "default", archive,
		core.ImportArchiveOptions{SourceID: "msrc", ModID: "43"})
	require.NoError(t, err)
	_, err = f.svc.ApplyImportArchive(context.Background(), f.game, "default", plan,
		core.ImportArchiveOptions{SourceID: "msrc", ModID: "43"}, nil)
	require.NoError(t, err)

	assert.True(t, f.row(t).ManualOnly)
}

// The import of a mod its source serves records nothing.
func TestImportArchive_SourceServesTheMod_NotManualOnly(t *testing.T) {
	f := newInstallFromFileFixture(t)

	archive := f.archive(t, "MapUtils-1.2.zip")
	plan, err := f.svc.PlanImportArchive(context.Background(), f.game, "default", archive,
		core.ImportArchiveOptions{SourceID: "msrc", ModID: "43"})
	require.NoError(t, err)
	_, err = f.svc.ApplyImportArchive(context.Background(), f.game, "default", plan,
		core.ImportArchiveOptions{SourceID: "msrc", ModID: "43"}, nil)
	require.NoError(t, err)

	assert.False(t, f.row(t).ManualOnly)
}

// An update from file asks the source the same way.
func TestUpdateFromArchive_SourceClassifiesTheModManualOnly_RecordsIt(t *testing.T) {
	f := newFromFileFixture(t)
	f.src.mods["42"].ManualOnly = true
	require.False(t, f.row(t).ManualOnly)

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)

	assert.True(t, f.row(t).ManualOnly)
}

// An update of an installed mod whose download the source refuses records
// the fact on that row - and on the listing every surface reads.
func TestApplyUpdate_SourceRefusesTheDownload_RecordsManualOnly(t *testing.T) {
	f := newFromFileFixture(t)
	require.False(t, f.row(t).ManualOnly)

	f.failSingleUpdate(t)

	assert.True(t, f.row(t).ManualOnly)
	mods, err := f.svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.True(t, mods[0].ManualOnly)
}

// The acceptance case: the update check still REPORTS a manual-only mod's
// update - the user needs to know a newer file exists - but a batch skips
// it with its own reason and makes no download request at all.
func TestApplyUpdateBatch_ManualOnlyRowIsSkippedNotAttempted(t *testing.T) {
	f := newFromFileFixture(t)
	counting := &countingFromFileSource{fromFileSource: f.src}
	f.svc.RegisterSource(counting)
	ctx := context.Background()
	row := f.row(t)
	row.SourceURL = "https://example.test/mods/auctionator" // what an install records from the source
	require.NoError(t, f.svc.SaveInstalledMod(ctx, row))
	f.failSingleUpdate(t)
	counting.urlCalls.Store(0)

	installed, err := f.svc.GetInstalledMods(ctx, "g1", "default")
	require.NoError(t, err)
	report, err := f.svc.CheckGameUpdateReport(ctx, f.game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, report.Updates, 1, "the check still reports the update")
	assert.True(t, report.Updates[0].InstalledMod.ManualOnly, "and says it is manual-only")
	assert.Equal(t, "2.0", report.Updates[0].NewVersion)

	plan, err := f.svc.PlanUpdateBatchFrom(ctx, f.game, "default", report.Updates, nil)
	require.NoError(t, err)
	result, err := f.svc.ApplyUpdateBatch(ctx, f.game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err, "a manual-only row is a policy outcome, never an error for the batch")

	assert.Empty(t, result.Applied)
	assert.Empty(t, result.Failed, "not attempted, so not a failure")
	require.Len(t, result.Skipped, 1)
	skip := result.Skipped[0]
	assert.Equal(t, core.UpdateSkipped, skip.Status)
	assert.Equal(t, core.ReasonManualDownload, skip.Reason)
	assert.True(t, skip.ManualOnly)
	assert.False(t, skip.Mod.Locked)
	assert.Equal(t, "https://example.test/mods/auctionator", skip.ModURL)
	assert.Equal(t, "1.0", skip.FromVersion)
	assert.Equal(t, "2.0", skip.ToVersion)
	assert.Zero(t, counting.urlCalls.Load(), "the batch must not ask the source for a download")
	assert.Equal(t, "1.0", f.row(t).Version)
}

// A batch handed an update that came from an OLDER check - one made before
// the refusal was recorded - still skips the row: the fact is read from the
// row at apply time, not trusted from the caller's copy.
func TestApplyUpdateBatch_ManualOnlyRecordedAfterTheCheck_StillSkipped(t *testing.T) {
	f := newFromFileFixture(t)
	counting := &countingFromFileSource{fromFileSource: f.src}
	f.svc.RegisterSource(counting)
	stale := domain.Update{InstalledMod: *f.row(t), NewVersion: "2.0", FileIDReplacements: map[string]string{"100": "200"}}
	require.False(t, stale.InstalledMod.ManualOnly)
	f.failSingleUpdate(t)
	counting.urlCalls.Store(0)

	plan, err := f.svc.PlanUpdateBatchFrom(context.Background(), f.game, "default", []domain.Update{stale}, nil)
	require.NoError(t, err)
	result, err := f.svc.ApplyUpdateBatch(context.Background(), f.game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, core.ReasonManualDownload, result.Skipped[0].Reason)
	assert.Zero(t, counting.urlCalls.Load())
}

// A source that serves the file again clears the fact: the user's explicit
// single update is still attempted (only the batch declines), and its
// successful download proves the source serves the mod.
func TestApplyUpdate_ExplicitUpdateStillAttempted_SuccessClearsManualOnly(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	row := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	mock := &refusingSource{multiFileDownloadSource: &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "new.esp", IsPrimary: true}},
	}}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	ctx := context.Background()
	update := func() error {
		plan, err := svc.PlanUpdateFrom(ctx, game, "default", domain.Update{InstalledMod: *row, NewVersion: "2.0"})
		require.NoError(t, err)
		_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
		return err
	}

	mock.refuse.Store(true)
	require.ErrorIs(t, update(), source.ErrManualDownload)
	got, err := svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	require.True(t, got.ManualOnly)

	mock.refuse.Store(false)
	require.NoError(t, update(), "an explicit update of a manual-only mod is still attempted")
	got, err = svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", got.Version)
	assert.False(t, got.ManualOnly, "a download the source served proves it serves the mod")
}

// An adoption matched to a source that classifies the mod manual-only
// records it - and keeps adopt's own ManualDownload, which verify reads.
func TestApplyAdopt_SourceClassifiesTheModManualOnly_RecordsIt(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1", ManualOnly: true}}
	src.files = []domain.DownloadableFile{{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "42", "g1", "default")
	require.NoError(t, err)
	assert.True(t, installed.ManualOnly)
	assert.True(t, installed.ManualDownload, "adopt's own flag is unchanged")
}

// An adoption whose source says nothing is not manual-only: adopt's
// ManualDownload ("adopted in place") is a different fact, and the
// indicator must not mislabel an adopted mod its source serves.
func TestApplyAdopt_SourceServesTheMod_NotManualOnly(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1"}}
	src.files = []domain.DownloadableFile{{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "42", "g1", "default")
	require.NoError(t, err)
	assert.False(t, installed.ManualOnly)
	assert.True(t, installed.ManualDownload)
}

// The detail view says so too - before any row exists, since the install
// the source refused is exactly when the user goes looking.
func TestModDetail_SaysManualOnlyOnceTheSourceRefused(t *testing.T) {
	f := newInstallFromFileFixture(t)
	detail, err := f.svc.ModDetail(context.Background(), f.game, "default", "msrc", "43")
	require.NoError(t, err)
	require.False(t, detail.Mod.ManualOnly)

	_ = f.failedInstall(t)

	detail, err = f.svc.ModDetail(context.Background(), f.game, "default", "msrc", "43")
	require.NoError(t, err)
	assert.Nil(t, detail.Installed)
	assert.True(t, detail.Mod.ManualOnly)
}
