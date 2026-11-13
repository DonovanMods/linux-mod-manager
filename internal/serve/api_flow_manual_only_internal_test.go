package serve

// #543 over /api/v1: a mod whose source will not serve its files is marked
// manual_only on every listing the SPA reads, and the updates batch the
// SPA's "Update all"/"Update selected" start skips it - with the reason and
// the page - without asking the source for a download at all.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manualUpdatesSource is the updates fixture's source refusing every
// download of the mods in manual, counting each refusal.
type manualUpdatesSource struct {
	*updatesSource
	manual  map[string]bool
	refused atomic.Int32
}

func (s *manualUpdatesSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	if s.manual[mod.ID] {
		s.refused.Add(1)
		return "", &source.ManualDownloadError{Reason: "the author has turned off API downloads"}
	}
	return s.updatesSource.GetDownloadURL(ctx, mod, fileID)
}

func TestFlowUpdates_ManualOnlyMod_IsMarkedAndSkippedWithoutADownload(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	src := &manualUpdatesSource{updatesSource: newUpdatesSource(t), manual: map[string]bool{"u1": true}}
	svc.RegisterSource(src)

	// The first update of u1 meets the refusal - which is how lmm learns it.
	j := runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	first, ok := j.status().Result.(*core.UpdateBatchResult)
	require.True(t, ok)
	require.Len(t, first.Failed, 1)
	assert.True(t, first.Failed[0].ManualDownload)

	// Every listing the SPA reads now says so.
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updates core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updates))
	manual := map[string]bool{}
	for _, u := range updates.Updates {
		manual[u.InstalledMod.ID] = u.InstalledMod.ManualOnly
	}
	assert.Equal(t, map[string]bool{"u1": true, "u2": false, "u3": false}, manual,
		"the check still reports u1's update, marked")

	rec = doAPI(s, http.MethodGet, scoped("/api/v1/mods", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var mods core.ModList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mods))
	listed := map[string]bool{}
	for _, m := range mods.Mods {
		listed[m.ID] = m.ManualOnly
	}
	assert.Equal(t, map[string]bool{"u1": true, "u2": false, "u3": false}, listed)

	// The batch skips it, and asks the source for nothing.
	src.refused.Store(0)
	j = runFlow(t, s, game, "updates", updatesBatchBody, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.UpdateBatchResult)
	require.True(t, ok)
	assert.Empty(t, result.Failed, "not attempted, so not a failure")
	require.Len(t, result.Skipped, 1)
	skip := result.Skipped[0]
	assert.Equal(t, "u1", skip.Mod.ModID)
	assert.True(t, skip.ManualOnly)
	assert.Equal(t, core.ReasonManualDownload, skip.Reason)
	require.Len(t, result.Applied, 1)
	assert.Equal(t, "u2", result.Applied[0].Mod.ModID)
	assert.Zero(t, src.refused.Load(), "the batch must not ask the source for u1's download")

	u1, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateFromVersion, u1.Version)
	assert.Equal(t, updatableContent("u1", updateFromVersion), deployedContent(t, game, updatableModFile("u1")))
	assertUntouchedThirdMod(t, s, game)
}

// classifyingUpdatesSource is manualUpdatesSource reporting, during an
// update check, the classification set in distributable for each installed
// mod it fetches (source.ReportFetchedMod), as CurseForge does (#627).
type classifyingUpdatesSource struct {
	*manualUpdatesSource
	distributable map[string]bool
}

func (s *classifyingUpdatesSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, m := range installed {
		if d, ok := s.distributable[m.ID]; ok {
			source.ReportFetchedMod(ctx, domain.Mod{ID: m.ID, SourceID: m.SourceID, GameID: m.GameID, Distributable: &d})
		}
	}
	return s.manualUpdatesSource.CheckUpdates(ctx, installed)
}

func (s *classifyingUpdatesSource) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, _ source.UpdateProgressFunc) ([]domain.Update, error) {
	return s.CheckUpdates(ctx, installed)
}

// getUpdates is GET /api/v1/updates (target may add ?refresh=1), decoded.
func getUpdates(t *testing.T, s *Server, target string) core.UpdateCheckReport {
	t.Helper()
	rec := doAPI(s, http.MethodGet, target, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	return report
}

// #627 over /api/v1: once the source says it serves a manual-only mod
// again, the server's update check clears the mark and reports it in
// manual_only_cleared - and keeps reporting it, so the card the SPA's next
// re-read draws still says so, until the user's explicit check (?refresh=1)
// or until the mod is recorded manual-only again. The mod is then updated
// by the batch like any other.
func TestFlowUpdates_ManualOnlyMod_ClearedWhenTheSourceServesItAgain(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	manual := &manualUpdatesSource{updatesSource: newUpdatesSource(t), manual: map[string]bool{"u1": true}}
	src := &classifyingUpdatesSource{manualUpdatesSource: manual, distributable: map[string]bool{}}
	svc.RegisterSource(src)

	j := runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	updatesPath := scoped("/api/v1/updates", game)
	require.Empty(t, getUpdates(t, s, updatesPath).ManualOnlyCleared, "precondition: nothing cleared")

	// The author turns third-party downloads back on.
	src.distributable["u1"] = true
	manual.manual = map[string]bool{}

	report := getUpdates(t, s, updatesPath)
	require.Len(t, report.ManualOnlyCleared, 1)
	assert.Equal(t, "u1", report.ManualOnlyCleared[0].ModID)
	assert.Contains(t, report.ManualOnlyCleared[0].Message, "automatic downloads are available again")
	for _, u := range report.Updates {
		assert.False(t, u.InstalledMod.ManualOnly, "%s is an ordinary update", u.InstalledMod.ID)
	}
	u1, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, u1.ManualOnly, "the record is cleared")

	again := getUpdates(t, s, updatesPath)
	require.Len(t, again.ManualOnlyCleared, 1, "the server keeps saying so through the SPA's re-reads")

	assert.Empty(t, getUpdates(t, s, updatesPath+"&refresh=1").ManualOnlyCleared,
		"until the user's explicit check")

	j = runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.UpdateBatchResult)
	require.True(t, ok)
	assert.Empty(t, result.Skipped, "the batch takes it again")
	require.Len(t, result.Applied, 1)
	assert.Equal(t, "u1", result.Applied[0].Mod.ModID)
}

// A remembered notice is dropped once the mod is recorded manual-only again.
func TestFlowUpdates_ManualOnlyClearedNotice_DroppedWhenRecordedAgain(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	manual := &manualUpdatesSource{updatesSource: newUpdatesSource(t), manual: map[string]bool{"u1": true}}
	src := &classifyingUpdatesSource{manualUpdatesSource: manual, distributable: map[string]bool{}}
	svc.RegisterSource(src)
	j := runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	updatesPath := scoped("/api/v1/updates", game)

	src.distributable["u1"] = true
	require.Len(t, getUpdates(t, s, updatesPath).ManualOnlyCleared, 1)

	// The source says nothing any more, and refuses the next download.
	delete(src.distributable, "u1")
	j = runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	u1, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u1", game.ID, "default")
	require.NoError(t, err)
	require.True(t, u1.ManualOnly, "precondition: the refusal records it again")

	assert.Empty(t, getUpdates(t, s, updatesPath).ManualOnlyCleared, "the notice is stale now, so it is dropped")
}
