package core_test

// #672: an update check or a verify asks a source for a mod's file list only
// when the mod has changed since lmm last checked it. A source that can say
// which mods changed (source.ChangeTracker) answers the rest from what lmm
// recorded; an explicit refresh always asks.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// trackingSource is a source with a change signal: it checks mods one by
// one (reporting each it finished), offers 2.0 for "upd", and says every
// mod is unchanged except those in changed.
type trackingSource struct {
	*mockSource
	mu        sync.Mutex
	checked   []string // mods whose update was checked in full
	listed    []string // mods whose file list verify fetched
	asked     int      // UnchangedSince calls
	changed   map[string]bool
	failTrack bool
}

func newTrackingSource() *trackingSource {
	return &trackingSource{mockSource: newMockSource("tsrc"), changed: map[string]bool{}}
}

func (s *trackingSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	for _, mod := range installed {
		s.mu.Lock()
		s.checked = append(s.checked, mod.ID)
		s.mu.Unlock()
		if mod.ID == "upd" && mod.Version == "1.0" {
			out = append(out, domain.Update{InstalledMod: mod, NewVersion: "2.0", Changelog: "fixed things"})
		}
		source.ReportModChecked(ctx, mod.ID)
	}
	return out, nil
}

func (s *trackingSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listed = append(s.listed, mod.ID)
	return []domain.DownloadableFile{{ID: "f1", Version: "1.0"}}, nil
}

func (s *trackingSource) UnchangedSince(_ context.Context, _ string, since map[string]time.Time) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked++
	out := map[string]time.Time{}
	if s.failTrack {
		return out, nil
	}
	for id := range since {
		if !s.changed[id] {
			out[id] = time.Now()
		}
	}
	return out, nil
}

func (s *trackingSource) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checked, s.listed = nil, nil
}

var _ source.ChangeTracker = (*trackingSource)(nil)

func newTrackingService(t *testing.T) (*core.Service, *domain.Game, *trackingSource) {
	t.Helper()
	svc, game := newVerifyThrottleGame(t)
	src := newTrackingSource()
	svc.RegisterSource(src)
	ctx := context.Background()
	for _, id := range []string{"same", "upd"} {
		seedVerifyMod(t, svc, game, "tsrc", id, id, "1.0", []string{"f1"}, true)
		require.NoError(t, svc.SaveFileChecksum(ctx, "tsrc", id, game.ID, "default", "f1", "cs-"+id))
	}
	return svc, game, src
}

func trackingCheck(t *testing.T, svc *core.Service, game *domain.Game, opts core.UpdateCheckOptions) *core.UpdateCheckReport {
	t.Helper()
	installed, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installed, nil, opts)
	require.NoError(t, err)
	return report
}

func updateIDs(report *core.UpdateCheckReport) []string {
	var ids []string
	for _, u := range report.Updates {
		ids = append(ids, u.InstalledMod.ID)
	}
	return ids
}

func TestUpdateCheck_AnUnchangedModIsAnsweredFromItsRecord(t *testing.T) {
	svc, game, src := newTrackingService(t)

	first := trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	assert.ElementsMatch(t, []string{"same", "upd"}, src.checked, "nothing recorded yet: both checked")
	assert.Equal(t, []string{"upd"}, updateIDs(first))
	assert.Zero(t, first.Unchanged)

	src.reset()
	second := trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	assert.Empty(t, src.checked, "neither changed: no file list fetched")
	assert.Equal(t, 2, second.Unchanged)
	require.Equal(t, []string{"upd"}, updateIDs(second), "the update found last time is still offered")
	assert.Equal(t, "2.0", second.Updates[0].NewVersion)
	assert.Equal(t, "fixed things", second.Updates[0].Changelog)
	assert.Equal(t, "1.0", second.Updates[0].InstalledMod.Version, "about the mod as installed")
}

func TestUpdateCheck_AModThatChangedIsCheckedAgain(t *testing.T) {
	svc, game, src := newTrackingService(t)
	trackingCheck(t, svc, game, core.UpdateCheckOptions{})

	src.reset()
	src.changed["same"] = true
	report := trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	assert.Equal(t, []string{"same"}, src.checked, "only the mod that changed")
	assert.Equal(t, 1, report.Unchanged)
	assert.Equal(t, []string{"upd"}, updateIDs(report))
}

func TestUpdateCheck_RefreshAsksTheSourceAboutEveryMod(t *testing.T) {
	svc, game, src := newTrackingService(t)
	trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	src.reset()
	asked := src.asked

	report := trackingCheck(t, svc, game, core.UpdateCheckOptions{Refresh: true})
	assert.ElementsMatch(t, []string{"same", "upd"}, src.checked)
	assert.Equal(t, asked, src.asked, "the record is not even consulted")
	assert.Zero(t, report.Unchanged)
}

func TestUpdateCheck_ARecordAboutAnotherInstalledVersionIsNotUsed(t *testing.T) {
	svc, game, src := newTrackingService(t)
	trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	src.reset()
	// The mod was updated since: the record is about 1.0.
	seedVerifyMod(t, svc, game, "tsrc", "upd", "upd", "2.0", []string{"f2"}, true)

	report := trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	assert.Equal(t, []string{"upd"}, src.checked)
	assert.Empty(t, updateIDs(report), "2.0 is up to date")
	assert.Equal(t, 1, report.Unchanged)
}

func TestUpdateCheck_ASourceThatCannotAnswerCostsOnlyTheFullCheck(t *testing.T) {
	svc, game, src := newTrackingService(t)
	trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	src.reset()
	src.failTrack = true

	report := trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	assert.ElementsMatch(t, []string{"same", "upd"}, src.checked)
	assert.Equal(t, []string{"upd"}, updateIDs(report))
}

func TestVerify_AnAlreadyConfirmedModIsNotAskedAgain(t *testing.T) {
	svc, game, src := newTrackingService(t)
	ctx := context.Background()
	full := core.VerifyOptions{Tier: core.VerifyFull, Force: true} // as `lmm verify` runs: past the #336 memo

	first, err := svc.VerifyForTest(ctx, game, "default", full, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"same", "upd"}, src.listed)
	assert.Zero(t, first.VersionUnchanged)

	src.reset()
	second, err := svc.VerifyForTest(ctx, game, "default", full, nil)
	require.NoError(t, err)
	assert.Empty(t, src.listed, "confirmed and unchanged: no file list fetched")
	assert.Equal(t, 2, second.VersionUnchanged)
	assert.Zero(t, second.Issues+second.Warnings)

	src.reset()
	src.changed["upd"] = true
	third, err := svc.VerifyForTest(ctx, game, "default", full, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"upd"}, src.listed, "a mod that changed is checked again")
	assert.Equal(t, 1, third.VersionUnchanged)

	src.reset()
	refreshed, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull, Refresh: true}, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"same", "upd"}, src.listed, "--refresh asks about every mod")
	assert.Zero(t, refreshed.VersionUnchanged)
}

func TestUpdateCheck_AnsweredModsStillTickTheProgress(t *testing.T) {
	svc, game, src := newTrackingService(t)
	trackingCheck(t, svc, game, core.UpdateCheckOptions{})
	src.reset()
	src.changed["upd"] = true

	installed, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	var ticks []core.UpdateCheckEvent
	_, err = svc.CheckGameUpdateReport(context.Background(), game, "default", installed, func(e core.Event) {
		if ev, ok := e.(core.UpdateCheckEvent); ok {
			ticks = append(ticks, ev)
		}
	}, core.UpdateCheckOptions{})
	require.NoError(t, err)
	// trackingSource reports no progress of its own: the answered mod is
	// ticked, at 1 of the batch's 2, so a progress line still moves.
	require.Len(t, ticks, 1)
	assert.Equal(t, "same", ticks[0].ModName)
	assert.Equal(t, 1, ticks[0].GlobalIndex)
	assert.Equal(t, 2, ticks[0].GlobalTotal)
	assert.Equal(t, 2, ticks[0].Total)
}
