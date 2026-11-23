package core_test

// #673: a collection is checked as a collection. A collection profile's
// update check asks the collection's origin for its latest published
// revision - one request - while the mods the collection locks are still
// never checked one by one (#667), and a mod the user unlocked is checked
// like any other.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latestRevisionSource is the skip tests' source that also answers a
// collection's latest revision, counting the requests.
type latestRevisionSource struct {
	*skipTestSource
	latest int
	err    error
	calls  int
	asked  []string // "gameSourceID/slug" per request
}

func (s *latestRevisionSource) LatestCollectionRevision(_ context.Context, gameSourceID, slug string) (int, error) {
	s.calls++
	s.asked = append(s.asked, gameSourceID+"/"+slug)
	return s.latest, s.err
}

var _ source.CollectionRevisionChecker = (*latestRevisionSource)(nil)

// newLatestRevisionService is newCollectionLockedService (mod1 locked by the
// collection, mod2 locked by hand, mod3 neither) over a source that knows
// the collection's latest revision, with the collection's origin given.
func newLatestRevisionService(t *testing.T, origin *domain.CollectionOrigin, latest int) (*core.Service, *domain.Game, *latestRevisionSource) {
	t.Helper()
	svc, game, src := newCollectionLockedService(t, origin)
	game.SourceIDs = map[string]string{"src": "nightgame"}
	rs := &latestRevisionSource{skipTestSource: src, latest: latest}
	svc.RegisterSource(rs)
	return svc, game, rs
}

func nexusOrigin() *domain.CollectionOrigin {
	return &domain.CollectionOrigin{SourceID: "src", Slug: "abc123", Revision: 3}
}

func TestCheckGameUpdateReport_AsksTheCollectionsRevisionOnce(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 4)

	report := skipReport(t, svc, game)

	assert.Equal(t, 1, rs.calls, "one revision request per collection profile")
	assert.Equal(t, []string{"nightgame/abc123"}, rs.asked, "asked with the game's id on the source")
	assert.NotContains(t, rs.skipTestSource.asked, "mod1", "the collection's locked mods are not checked one by one")
	assert.Contains(t, rs.skipTestSource.asked, "mod3", "a mod the collection does not lock is checked")
	assert.Contains(t, rs.skipTestSource.asked, "mod2", "so is one locked by hand")
	assert.Equal(t, &core.CollectionRevisionStatus{
		Name: "Night City", Origin: core.CollectionOriginSource, SourceID: "src",
		Installed: 3, Latest: 4, Available: true, Checked: true, Sync: true,
	}, report.CollectionRevision)
	assert.Empty(t, report.ErrorMessage)
}

func TestCheckGameUpdateReport_CollectionUpToDate(t *testing.T) {
	svc, game, _ := newLatestRevisionService(t, nexusOrigin(), 3)

	report := skipReport(t, svc, game)
	require.NotNil(t, report.CollectionRevision)
	assert.True(t, report.CollectionRevision.Checked)
	assert.False(t, report.CollectionRevision.Available)
	assert.Equal(t, 3, report.CollectionRevision.Latest)
}

func TestCheckGameUpdateReport_AnUnlockedCollectionModIsCheckedAgain(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 4)
	_, err := svc.ClearModLock(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)

	report := skipReport(t, svc, game)
	assert.Contains(t, rs.skipTestSource.asked, "mod1", "unlocked, the mod is the user's and checked normally")
	assert.Zero(t, report.Skipped.Locked)
	assert.True(t, report.CollectionRevision.Available, "the collection itself is still checked")
}

func TestCheckGameUpdateReport_ARevisionCheckFailureDoesNotFailTheCheck(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 0)
	rs.err = errors.New("rate limited")

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err, "the mods' check completed; only the collection's answer is missing")
	assert.Empty(t, report.ErrorMessage)
	require.NotNil(t, report.CollectionRevision)
	assert.False(t, report.CollectionRevision.Checked)
	assert.Contains(t, report.CollectionRevision.Error, "rate limited")
}

func TestCheckGameUpdateReport_OriginsThatCannotBeChecked(t *testing.T) {
	t.Run("an .lmmc address", func(t *testing.T) {
		svc, game, rs := newLatestRevisionService(t, &domain.CollectionOrigin{ImportURL: "http://example.com/night.lmmc"}, 4) // plain http: lmm never fetches it
		got := skipReport(t, svc, game).CollectionRevision
		require.NotNil(t, got)
		assert.Equal(t, core.CollectionOriginURL, got.Origin)
		assert.False(t, got.Checked)
		assert.Equal(t, core.CollectionUncheckedURL, got.Reason)
		assert.True(t, got.Sync, "the sync plan still fetches it")
		assert.Equal(t, 3, got.Installed)
		assert.Zero(t, rs.calls)
	})
	t.Run("a file", func(t *testing.T) {
		svc, game, rs := newLatestRevisionService(t, nil, 4)
		got := skipReport(t, svc, game).CollectionRevision
		require.NotNil(t, got)
		assert.Equal(t, core.CollectionOriginFile, got.Origin)
		assert.False(t, got.Checked)
		assert.Equal(t, core.CollectionUncheckedFile, got.Reason)
		assert.False(t, got.Sync)
		assert.Zero(t, rs.calls)
	})
	t.Run("a source without revision checks", func(t *testing.T) {
		svc, game, _ := newCollectionLockedService(t, nexusOrigin()) // the plain skip source
		game.SourceIDs = map[string]string{"src": "nightgame"}
		got := skipReport(t, svc, game).CollectionRevision
		require.NotNil(t, got)
		assert.False(t, got.Checked)
		assert.Equal(t, core.CollectionUncheckedUnsupported, got.Reason)
	})
}

func TestCheckGameUpdateReport_NoCollectionNoRevision(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	assert.Nil(t, skipReport(t, svc, game).CollectionRevision)
}

func TestPlanUpdateBatch_AsksNoRevision(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 4)
	_, err := svc.PlanUpdateBatch(context.Background(), game, "default", nil)
	require.NoError(t, err)
	_, err = svc.CheckGameUpdates(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	assert.Zero(t, rs.calls, "only the report a frontend shows asks the collection")
}

// The installed revision is the collection's version; a duplicated profile
// keeps only the origin's revision, which then stands in for it.
func TestCheckGameUpdateReport_InstalledRevisionFallsBackToTheOrigins(t *testing.T) {
	svc, game, _ := newLatestRevisionService(t, nexusOrigin(), 5)
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection.Version = 0
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))

	got := skipReport(t, svc, game).CollectionRevision
	assert.Equal(t, 3, got.Installed)
	assert.True(t, got.Available)
}

func TestCheckGameUpdateReport_ARerunCanSkipTheRevision(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 4)
	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil,
		core.UpdateCheckOptions{NoCollectionRevision: true})
	require.NoError(t, err)
	assert.Nil(t, report.CollectionRevision)
	assert.Zero(t, rs.calls, "a re-run of a check that already asked does not ask again")
}

// #673 review: a collection the user authored - exporting a profile
// records its collection identity, with no origin - has nothing to check
// and is no import: it gets no line at all, never "import it again with
// --force", which would overwrite the author's own profile.
func TestCheckGameUpdateReport_AnAuthoredCollectionReportsNothing(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.ExportCollection(context.Background(), game.ID, "default", core.CollectionExportOptions{})
	require.NoError(t, err)
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NotNil(t, prof.Collection, "the export recorded the collection's identity")
	require.Nil(t, prof.Collection.Origin)

	assert.Nil(t, skipReport(t, svc, game).CollectionRevision)
}

// #673 review: a #671 hold is data, so both frontends can say when the
// source will be asked again.
func TestCheckGameUpdateReport_AHeldSourceSaysUntilWhen(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 0)
	until := time.Date(2026, 11, 22, 14, 3, 0, 0, time.UTC)
	rs.err = fmt.Errorf("checking NexusMods collection abc123: %w",
		&source.RetryLaterError{Source: "NexusMods", Until: until, Reason: "rate limited"})

	got := skipReport(t, svc, game).CollectionRevision
	require.NotNil(t, got)
	assert.False(t, got.Checked)
	assert.True(t, until.Equal(got.HeldUntil))
	assert.Equal(t, "NexusMods", got.HeldBy)
	assert.NotEmpty(t, got.Error)
}

// #673 review: the answer is kept for a few minutes, so every page open is
// not a request; Check for updates (Refresh) asks again.
func TestCheckGameUpdateReport_TheRevisionIsCachedUntilARefresh(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 4)
	skipReport(t, svc, game)
	second := skipReport(t, svc, game)
	assert.Equal(t, 1, rs.calls, "the second report reads the kept answer")
	assert.Equal(t, 4, second.CollectionRevision.Latest)

	rs.latest = 5
	refreshed, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil,
		core.UpdateCheckOptions{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, 2, rs.calls, "a refresh asks again")
	assert.Equal(t, 5, refreshed.CollectionRevision.Latest)
	assert.Equal(t, 5, skipReport(t, svc, game).CollectionRevision.Latest, "and keeps the new answer")
	assert.Equal(t, 2, rs.calls)
}

func TestCheckGameUpdateReport_AFailureIsNotCached(t *testing.T) {
	svc, game, rs := newLatestRevisionService(t, nexusOrigin(), 4)
	rs.err = errors.New("timeout")
	skipReport(t, svc, game)
	rs.err = nil
	assert.True(t, skipReport(t, svc, game).CollectionRevision.Checked)
	assert.Equal(t, 2, rs.calls)
}

// #673 review: with no installed revision recorded, the latest one is
// reported but not compared - never "installed 0".
func TestCheckGameUpdateReport_NoInstalledRevisionIsNotComparable(t *testing.T) {
	svc, game, _ := newLatestRevisionService(t, &domain.CollectionOrigin{SourceID: "src", Slug: "abc123"}, 4)
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection.Version = 0
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))

	got := skipReport(t, svc, game).CollectionRevision
	assert.True(t, got.Checked)
	assert.Equal(t, 4, got.Latest)
	assert.Zero(t, got.Installed)
	assert.True(t, got.InstalledUnknown)
	assert.False(t, got.Available, "nothing to compare against")
}
