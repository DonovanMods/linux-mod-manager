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
