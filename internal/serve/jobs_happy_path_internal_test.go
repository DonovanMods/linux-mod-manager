package serve

// The end-to-end mutation path over real TCP: plan -> job -> SSE events ->
// result (docs/plans/2026-08-30-serve-impl.md Task 7). Every other test in
// this unit isolates one piece; this one is the whole contract a script (or
// Task 10's enhancement JS) actually follows, against a fake-source-seeded
// Service running a REAL ApplyDeploy - so the last assertion is not a
// status code but a file in the game directory.

import (
	"bufio"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveAPI issues one request against srv with the CSRF header an API caller
// uses for a state-changing call, and returns the status and body.
func liveAPI(t *testing.T, s *Server, srv *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, reader)
	require.NoError(t, err)
	if unsafeMethod(method) {
		req.Header.Set(csrfHeaderName, s.csrf.token)
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

func TestServeJobs_HappyPath_PlanThenJobThenEventsThenResult(t *testing.T) {
	s, game := newLiveFixtureServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	// 1. Plan. The response is the frozen core document plus the handle.
	code, raw := liveAPI(t, s, srv, http.MethodPost, "/api/v1/plans/deploy", `{}`)
	require.Equal(t, http.StatusOK, code, string(raw))
	var planned struct {
		PlanID planID          `json:"plan_id"`
		Kind   string          `json:"kind"`
		Plan   core.DeployPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &planned, json.RejectUnknownMembers(true)))
	require.NotEmpty(t, planned.PlanID)
	assert.Equal(t, "deploy", planned.Kind)
	require.Len(t, planned.Plan.Mods, 1)
	assert.Equal(t, []string{deployFixtureFile}, planned.Plan.Mods[0].Link)

	// 2. Job. The plan is redeemed and the Apply is already running.
	code, raw = liveAPI(t, s, srv, http.MethodPost, "/api/v1/jobs",
		`{"plan_id":"`+string(planned.PlanID)+`"}`)
	require.Equal(t, http.StatusAccepted, code, string(raw))
	var started jobStartResponse
	require.NoError(t, json.Unmarshal(raw, &started, json.RejectUnknownMembers(true)))
	require.NotEmpty(t, started.JobID)

	// 3. Events. Read the stream to its terminal frame; every payload must
	//    decode as a typed core event in the frozen {"type","data"} envelope.
	stream := getStream(t, srv, "/api/v1/jobs/"+string(started.JobID)+"/events")
	defer func() { _ = stream.Body.Close() }()
	require.Equal(t, sseContentType, stream.Header.Get("Content-Type"))

	body, err := io.ReadAll(bufio.NewReader(stream.Body))
	require.NoError(t, err)
	frames := parseSSE(t, string(body))
	require.NotEmpty(t, frames)

	var sawDeployEvent bool
	for _, frame := range frames[:len(frames)-1] {
		if frame.Event == "" {
			continue // a heartbeat
		}
		var envelope struct {
			Type string `json:"type"`
			Data struct {
				Op    string `json:"op"`
				Phase string `json:"phase"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &envelope),
			"every event frame's payload must be the core event envelope")
		assert.Equal(t, envelope.Type, frame.Event, "the event: name IS the core event's type name")
		if envelope.Data.Op == string(core.OpDeploy) {
			sawDeployEvent = true
		}
	}
	assert.True(t, sawDeployEvent, "a real deploy reports deploy-scoped events")

	terminal := frames[len(frames)-1]
	require.Equal(t, sseDoneEvent, terminal.Event)
	var final jobStatus
	require.NoError(t, json.Unmarshal([]byte(terminal.Data), &final))
	assert.Equal(t, jobSucceeded, final.State)

	// 4. Result. The job status document carries the core result, and the
	//    deploy really happened.
	code, raw = liveAPI(t, s, srv, http.MethodGet, "/api/v1/jobs/"+string(started.JobID), "")
	require.Equal(t, http.StatusOK, code, string(raw))
	var status struct {
		State  jobState           `json:"state"`
		Result *core.DeployResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &status))
	assert.Equal(t, jobSucceeded, status.State)
	require.NotNil(t, status.Result)
	assert.Equal(t, 1, status.Result.Deployed)

	_, statErr := os.Lstat(deployedFixturePath(game))
	require.NoError(t, statErr, "the deploy must have reached the game directory")
}
