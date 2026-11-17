package serve

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowUpdates_JobStreamsEachStepAfterTheDownload is #664 over /api/v1:
// an updates job's stream carries every step of each item's update after
// its download - the replace over the old version, the recording, the mod
// updated - in order, before the batch's own "applied" bracket. The tray
// and the inline readout render exactly these frames.
func TestFlowUpdates_JobStreamsEachStepAfterTheDownload(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)

	j := runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id)+"/events", "")
	require.Equal(t, http.StatusOK, rec.Code)
	frames := parseSSE(t, rec.Body.String())
	require.NotEmpty(t, frames)

	var phases []string
	for _, frame := range frames[:len(frames)-1] {
		if frame.Event == "" {
			continue // a heartbeat
		}
		var payload struct {
			Data struct {
				Phase   string `json:"phase"`
				ModName string `json:"mod_name"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
		if payload.Data.Phase == "" {
			continue
		}
		phases = append(phases, payload.Data.Phase)
		switch payload.Data.Phase {
		case "update_installing", "update_recording", "update_mod_updated":
			assert.Equal(t, "U1", payload.Data.ModName, "%s names the mod it is about", payload.Data.Phase)
		}
	}
	assertOrderedSubsequence(t, phases, []string{
		"update_batch_item", "update_download_done", "update_installing",
		"update_recording", "update_mod_updated", "update_batch_item_applied",
	}, "#664: every step after the download reaches the stream, in order (got %v)", phases)
}

// TestSummarizeJobEvent_CarriesTheFilePosition (#664 review): an update's
// download ticks and extracting steps say which of the mod's files they are
// about, and the tray's frame carries it, so the bar can place the second
// file's download after the first file's rather than back at the start.
func TestSummarizeJobEvent_CarriesTheFilePosition(t *testing.T) {
	at := core.FilePosition{FileIndex: 2, FileTotal: 3}
	for _, e := range []core.Event{
		core.DownloadEvent{Scope: core.Scope{Op: core.OpUpdate}, Phase: core.UpdateDownloading, Percent: 40, FilePosition: at},
		core.StepEvent{Scope: core.Scope{Op: core.OpUpdate}, Phase: core.ArchiveExtracting, FilePosition: at},
	} {
		frame := summarizeJobEvent("j1", "updates", e)
		assert.Equal(t, 2, frame.FileIndex, "%T", e)
		assert.Equal(t, 3, frame.FileTotal, "%T", e)
	}
	none := summarizeJobEvent("j1", "updates", core.StepEvent{Scope: core.Scope{Op: core.OpUpdate}, Phase: core.UpdateInstalling})
	assert.Zero(t, none.FileIndex)
	assert.Zero(t, none.FileTotal)
}
