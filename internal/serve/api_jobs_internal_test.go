package serve

// Internal (package serve) tests for POST /api/v1/jobs
// (docs/plans/2026-08-30-serve-design.md §"/api/v1": "starts the Apply as
// a job, returns {job_id}"). The interesting behaviour is all refusal:
// a plan is single-use, a draining server admits nothing, and a queue that
// has run away is pushed back on rather than grown.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planDeployFixture runs the plan endpoint against s and returns the
// plan_id it issued - the handle every job test starts from.
func planDeployFixture(t *testing.T, s *Server) planID {
	t.Helper()
	rec := doAPI(s, http.MethodPost, "/api/v1/plans/deploy", `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		PlanID planID `json:"plan_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotEmpty(t, got.PlanID)
	return got.PlanID
}

// startJob POSTs plan id to /api/v1/jobs and returns the recorder.
func startJob(s *Server, id planID) *httptest.ResponseRecorder {
	return doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
}

// decodeEnvelope strict-decodes an /api/v1 failure body.
func decodeEnvelope(t *testing.T, body []byte) apiErrorEnvelope {
	t.Helper()
	var envelope apiErrorEnvelope
	require.NoError(t, json.Unmarshal(body, &envelope, json.RejectUnknownMembers(true)))
	return envelope
}

// TestAPIStartJob_RunsTheApplyAndReportsTheJobID is the headline: the job
// endpoint redeems a plan, starts the real ApplyDeploy, and the deploy
// actually reaches the game directory - the end-state assertion, not just
// a status code.
func TestAPIStartJob_RunsTheApplyAndReportsTheJobID(t *testing.T) {
	s, game := newDeployFixtureServer(t)
	id := planDeployFixture(t, s)

	rec := startJob(s, id)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))

	var got struct {
		JobID jobID `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got, json.RejectUnknownMembers(true)))
	require.NotEmpty(t, got.JobID)

	j, ok := s.jobs.job(got.JobID)
	require.True(t, ok)
	waitFor(t, j.done(), "the deploy job to finish")

	status := j.status()
	require.Equal(t, jobSucceeded, status.State, "job failed: %+v", status.Error)
	assert.Equal(t, "deploy", status.Kind)
	result, ok := status.Result.(*core.DeployResult)
	require.True(t, ok, "the stored result must be the core document, got %T", status.Result)
	assert.Equal(t, 1, result.Deployed)

	_, statErr := os.Lstat(deployedFixturePath(game))
	assert.NoError(t, statErr, "a succeeded deploy job must have left the file in the game directory")
}

// TestAPIStartJob_PlanIsSingleUse pins the store's single-use contract as
// the ruled 409: a double-submitted confirm form (or a script retrying)
// must not run the same Apply twice.
func TestAPIStartJob_PlanIsSingleUse(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := planDeployFixture(t, s)

	first := startJob(s, id)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())

	second := startJob(s, id)

	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	assert.Equal(t, apiContentType, second.Header().Get("Content-Type"))
	assert.Contains(t, decodeEnvelope(t, second.Body.Bytes()).Error, "no longer available")
}

// TestAPIStartJob_UnknownPlanID_409 answers an id that was never issued the
// same way as one already used: the store deliberately cannot tell them
// apart (plans.go's errPlanUnavailable), and every caller re-plans either
// way.
func TestAPIStartJob_UnknownPlanID_409(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := startJob(s, "deadbeef")

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, decodeEnvelope(t, rec.Body.Bytes()).Error, "no longer available")
}

// TestAPIStartJob_RejectsAnUnusableRequest covers the request body itself:
// malformed JSON, a missing plan_id, and options the kind does not accept
// are all bad input.
func TestAPIStartJob_RejectsAnUnusableRequest(t *testing.T) {
	tests := []struct {
		name string
		body func(planID) string
	}{
		{"malformed json", func(planID) string { return `{` }},
		{"missing plan_id", func(planID) string { return `{}` }},
		{"unknown member", func(id planID) string { return `{"plan_id":"` + string(id) + `","nope":1}` }},
		{"options the kind refuses", func(id planID) string {
			return `{"plan_id":"` + string(id) + `","options":{"purge":true}}`
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newDeployFixtureServer(t)
			id := planDeployFixture(t, s)

			rec := doAPI(s, http.MethodPost, "/api/v1/jobs", tc.body(id))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
			assert.Equal(t, 1, s.plans.len(), "a request refused before it ran must leave the plan takeable")
		})
	}
}

// TestAPIStartJob_QueueDepthBackpressure_409 pins the ruled backpressure:
// core's beginOp queues concurrent mutations rather than rejecting them, so
// without a cap a client could pile up unbounded "running" jobs that are
// really just waiting. Past maxQueuedJobs the server says so instead.
func TestAPIStartJob_QueueDepthBackpressure_409(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := planDeployFixture(t, s)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for range maxQueuedJobs + 1 {
		_, err := s.jobs.Start("filler", func(ctx context.Context, _ core.EventSink) (any, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, nil
		})
		require.NoError(t, err)
	}
	require.Greater(t, s.jobs.QueueDepth(), maxQueuedJobs)

	rec := startJob(s, id)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, decodeEnvelope(t, rec.Body.Bytes()).Error, "already running")
	assert.Equal(t, 1, s.plans.len(), "a refused job must not consume the plan")
}

// TestAPIStartJob_DrainingRegistry_503 pins the ruled shutdown answer: once
// the registry refuses starts (jobs.go's errRegistryClosing) the server is
// going away, so this is 503 - "not now, and not from this process" -
// rather than a client error.
func TestAPIStartJob_DrainingRegistry_503(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := planDeployFixture(t, s)

	drainCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s.jobs.shutdown(drainCtx)

	rec := startJob(s, id)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, decodeEnvelope(t, rec.Body.Bytes()).Error, "shutting down")
}

// TestAPIStartJob_WithoutCSRFToken_403 keeps the new state-changing route
// inside the same CSRF guard every other mutation entry point sits behind.
func TestAPIStartJob_WithoutCSRFToken_403(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := planDeployFixture(t, s)

	req := apiRequest(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
	req.Header.Del(csrfHeaderName)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 1, s.plans.len())
}

// TestAPIJobStatus_ReportsTheJobStatusDocument pins GET /api/v1/jobs/{id}:
// the document is the goldened jobStatus shape, carrying the core result
// document verbatim once the Apply has returned.
func TestAPIJobStatus_ReportsTheJobStatusDocument(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := planDeployFixture(t, s)

	start := startJob(s, id)
	require.Equal(t, http.StatusAccepted, start.Code, start.Body.String())
	var started jobStartResponse
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &started))
	j, ok := s.jobs.job(started.JobID)
	require.True(t, ok)
	waitFor(t, j.done(), "the deploy job to finish")

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(started.JobID), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))

	var got struct {
		ID            jobID              `json:"id"`
		Kind          string             `json:"kind"`
		State         jobState           `json:"state"`
		StartedAt     time.Time          `json:"started_at"`
		EndedAt       time.Time          `json:"ended_at"`
		Result        *core.DeployResult `json:"result"`
		EventCount    int                `json:"event_count"`
		DroppedEvents int                `json:"dropped_events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got, json.RejectUnknownMembers(true)))
	assert.Equal(t, started.JobID, got.ID)
	assert.Equal(t, "deploy", got.Kind)
	assert.Equal(t, jobSucceeded, got.State)
	assert.False(t, got.StartedAt.IsZero())
	assert.False(t, got.EndedAt.IsZero())
	require.NotNil(t, got.Result)
	assert.Equal(t, 1, got.Result.Deployed)
	assert.Positive(t, got.EventCount, "a real deploy emits events")
}

// TestAPIJobStatus_UnknownID_404 covers a job that never existed or has
// aged out of the registry's retention.
func TestAPIJobStatus_UnknownID_404(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/deadbeef", "")

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
	assert.Contains(t, decodeEnvelope(t, rec.Body.Bytes()).Error, "deadbeef")
}
