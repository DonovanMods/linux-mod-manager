package serve

// The deploy flow over /api/v1, including #257's live-phase guarantee: a
// deploy job's event stream must show its progress phases IN ORDER, not
// merely contain them.
//
// Ported from the deleted mutations_deploy_internal_test.go's
// TestServer_ProfileDeploy_JobStreamsLivePhases (git show df38d23) onto the
// wire path the SPA actually drives (task-1 review Important 2): that
// test's subject was always the wire - only its entry point was the
// deleted confirm page. assertOrderedSubsequence is the same helper,
// carried over verbatim.

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowDeploy_JobStreamsLivePhasesInOrder plans and applies a deploy
// with purge=true - the fixture's one installed mod gives the purge pass
// real work, so deploy_purging and purge_complete actually fire ahead of
// deploy_deployed (without purge, only the latter would ever appear: task-9
// review Minor 6). It then reads the job's full event history back over
// GET /api/v1/jobs/{id}/events and asserts the three phases arrive as an
// ordered subsequence - a plain assert.Contains per phase would pass even
// if they arrived out of order, since each membership check is independent
// of the others.
func TestFlowDeploy_JobStreamsLivePhasesInOrder(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	j := runFlow(t, s, game, "deploy", `{"purge":true}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	require.FileExists(t, deployedFixturePath(game), "the deploy must reach the game directory")

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
				Phase string `json:"phase"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
		if payload.Data.Phase != "" {
			phases = append(phases, payload.Data.Phase)
		}
	}
	assert.Contains(t, phases, "deploy_deployed", "#257: the deploy's own phases must reach the stream")
	assertOrderedSubsequence(t, phases, []string{"deploy_purging", "purge_complete", "deploy_deployed"},
		"#257: the deploy's phases must reach the stream IN ORDER, not just be present")

	terminal := frames[len(frames)-1]
	require.Equal(t, sseDoneEvent, terminal.Event)
}

// assertOrderedSubsequence fails t unless want appears within got as an
// ordered subsequence (other entries may appear between, before, or after
// want's own entries; want's entries themselves must not appear out of
// order or be missing).
func assertOrderedSubsequence(t *testing.T, got, want []string, msgAndArgs ...any) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		assert.Fail(t, fmt.Sprintf("phases %v did not contain %v in order", got, want), msgAndArgs...)
	}
}
