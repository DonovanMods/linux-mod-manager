package serve

// Internal (package serve) tests for the /jobs/{id} page
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "live job page
// (progress; result when done)"). Internal because every case needs a job
// in a known state, which means driving the registry directly.

import (
	"context"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finishedJob starts a job that returns result/err immediately and waits
// for it, so a page test can render a known terminal state.
func finishedJob(t *testing.T, s *Server, kind string, result any, err error) jobID {
	t.Helper()
	id, startErr := s.jobs.Start(kind, func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		return result, err
	})
	require.NoError(t, startErr)
	j, ok := s.jobs.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")
	return id
}

// TestJobPage_RunningShowsStateAndARefreshAffordance is the no-JS
// requirement: with JavaScript off the page still says what the job is
// doing and offers a way to see the next state.
func TestJobPage_RunningShowsStateAndARefreshAffordance(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	id, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		<-release
		return &core.DeployResult{}, nil
	})
	require.NoError(t, err)

	rec := doAPI(s, http.MethodGet, "/jobs/"+string(id), "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	assert.Contains(t, body, "Deploy")
	assert.Contains(t, body, "running")
	assert.Contains(t, body, `href="/jobs/`+string(id)+`"`, "a manual refresh link is the accessible affordance")
	assert.Contains(t, body, `<noscript><meta http-equiv="refresh"`,
		"with JS off the page also refreshes itself while the job is running")
}

// TestJobPage_SucceededRendersTheResultsKeyFacts covers the result page:
// the numbers a user reads off a finished deploy, not a JSON dump.
func TestJobPage_SucceededRendersTheResultsKeyFacts(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := finishedJob(t, s, "deploy", &core.DeployResult{
		Deployed: 3,
		Notes:    []string{"1 mod was already deployed"},
		Warnings: []string{"could not sync merged pak"},
	}, nil)

	rec := doAPI(s, http.MethodGet, "/jobs/"+string(id), "")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "succeeded")
	assert.Contains(t, body, "Deployed")
	assert.Contains(t, body, "3")
	assert.Contains(t, body, "could not sync merged pak")
	assert.Contains(t, body, "1 mod was already deployed")
	assert.NotContains(t, body, `<noscript><meta http-equiv="refresh"`,
		"a finished job must not keep refreshing itself")
}

// TestJobPage_FailedRendersTheEnvelope covers the other terminal state: the
// same {"error","details"} failure the API answers, rendered as a page.
func TestJobPage_FailedRendersTheEnvelope(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := finishedJob(t, s, "deploy", nil, &core.ConflictError{Conflicts: []core.Conflict{{
		RelativePath:    "Mods/a.pak",
		CurrentSourceID: "fake",
		CurrentModID:    "m9",
	}}})

	rec := doAPI(s, http.MethodGet, "/jobs/"+string(id), "")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "failed")
	assert.Contains(t, body, "Mods/a.pak", "the typed details must reach the page, not just the message")
	assert.Contains(t, body, "conflicts")
}

// TestJobPage_RendersTheEventLog proves the replayed events reach the page,
// which is what a no-JS user sees instead of the live stream.
func TestJobPage_RendersTheEventLog(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	id := finishedJob(t, s, "deploy", &core.DeployResult{Deployed: 1}, nil)

	rec := doAPI(s, http.MethodGet, "/jobs/"+string(id), "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), core.DeployDeployed.String())
}

// TestJobPage_UnknownJob_404Page answers a job that never existed or has
// aged out with a real HTML page, not bare error text (WEBUI.md).
func TestJobPage_UnknownJob_404Page(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/jobs/deadbeef", "")

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "no longer available")
}
