package serve

// Internal (package serve) tests for GET /api/v1/jobs - the registry index
// the activity tray is built on (docs/plans/2026-08-31-serve-spa-design.md
// §Jobs: "Backed by the registry's retained jobs (adds GET /api/v1/jobs)").
//
// What actually matters here, and what a status-code test would miss: the
// order is newest-first (a tray reads top-down), a failed job carries its
// envelope inline so the tray can offer the next step without a second
// request, and a SUCCEEDED job does not carry its result document - the
// index is the quick path, the full document is one click away on
// GET /api/v1/jobs/{id}.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeJobsIndex decodes the index document strictly, so a member the
// handler invents without declaring fails the test instead of being
// silently dropped.
func decodeJobsIndex(t *testing.T, body []byte) jobsIndex {
	t.Helper()
	var index jobsIndex
	require.NoError(t, json.Unmarshal(body, &index, json.RejectUnknownMembers(true)),
		"the jobs index must decode into its declared type with no unknown members")
	return index
}

// runFinishedJob starts a job that returns immediately and waits for it, so
// a test can build a registry with a known history.
func runFinishedJob(t *testing.T, s *Server, kind string, result any, failure error) jobID {
	t.Helper()
	id, err := s.jobs.Start(kind, func(context.Context, core.EventSink) (any, error) {
		return result, failure
	})
	require.NoError(t, err)
	j, ok := s.jobs.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")
	return id
}

// TestAPIJobsIndex_NewestFirst pins the order the tray renders top-down.
func TestAPIJobsIndex_NewestFirst(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	first := runFinishedJob(t, s, "deploy", &core.DeployResult{Deployed: 1}, nil)
	second := runFinishedJob(t, s, "install", nil, errors.New("boom"))

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
	index := decodeJobsIndex(t, rec.Body.Bytes())
	require.Len(t, index.Jobs, 2)
	assert.Equal(t, second, index.Jobs[0].ID, "the newest job is first")
	assert.Equal(t, first, index.Jobs[1].ID)
}

// TestAPIJobsIndex_FailedJobCarriesItsEnvelope covers the tray's whole
// reason for existing: a failure names its next step inline (a conflict
// shows "Overwrite?" right in the tray), which needs the same
// {"error","details"} envelope the job status document carries.
func TestAPIJobsIndex_FailedJobCarriesItsEnvelope(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	runFinishedJob(t, s, "install", nil, &core.ConflictError{Conflicts: []core.Conflict{{
		RelativePath:    "Mods/a.pak",
		CurrentSourceID: "fake",
		CurrentModID:    "m9",
	}}})

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")

	require.Equal(t, http.StatusOK, rec.Code)
	index := decodeJobsIndex(t, rec.Body.Bytes())
	require.Len(t, index.Jobs, 1)
	row := index.Jobs[0]
	assert.Equal(t, jobFailed, row.State)
	require.NotNil(t, row.Error)
	assert.Contains(t, row.Error.Error, "file conflict detected")
	assert.NotNil(t, row.Error.Details, "the typed details are what the tray offers a next step from")
	assert.False(t, row.EndedAt.IsZero())
}

// TestAPIJobsIndex_SucceededJobOmitsItsResultDocument is the "quick path
// inline, full path one click away" rule in wire form: a result document
// can be arbitrarily large (a deploy's every skipped mod), and fifty of
// them in one index would make the tray's cheap poll the most expensive
// request in the application.
func TestAPIJobsIndex_SucceededJobOmitsItsResultDocument(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	id := runFinishedJob(t, s, "deploy", &core.DeployResult{
		Deployed: 2,
		Notes:    []string{"a note only the full document carries"},
	}, nil)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "a note only the full document carries",
		"the index summarises; the result document belongs to GET /api/v1/jobs/{id}")
	index := decodeJobsIndex(t, rec.Body.Bytes())
	require.Len(t, index.Jobs, 1)
	assert.Equal(t, jobSucceeded, index.Jobs[0].State)

	// ... and the full document really is one click away.
	full := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(id), "")
	require.Equal(t, http.StatusOK, full.Code)
	assert.Contains(t, full.Body.String(), "a note only the full document carries")
}

// TestAPIJobsIndex_RunningJobHasNoEndTime covers the row the tray renders
// with a progress bar rather than an outcome.
func TestAPIJobsIndex_RunningJobHasNoEndTime(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	release := make(chan struct{})
	emitted := make(chan struct{})
	_, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		close(emitted)
		<-release
		return &core.DeployResult{}, nil
	})
	require.NoError(t, err)
	waitFor(t, emitted, "the job's first event")

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")

	require.Equal(t, http.StatusOK, rec.Code)
	index := decodeJobsIndex(t, rec.Body.Bytes())
	require.Len(t, index.Jobs, 1)
	row := index.Jobs[0]
	assert.Equal(t, jobRunning, row.State)
	assert.True(t, row.EndedAt.IsZero(), "a running job has not ended")
	assert.Equal(t, 1, row.EventCount)
	close(release)
}

// TestAPIJobsIndex_EmptyRegistryIsAnEmptyList pins the empty answer as an
// empty ARRAY rather than null: "no jobs yet" and "the field is missing"
// must not be the same thing to the client that renders the tray.
func TestAPIJobsIndex_EmptyRegistryIsAnEmptyList(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"jobs": []`)
	index := decodeJobsIndex(t, rec.Body.Bytes())
	assert.Empty(t, index.Jobs)
}

// TestAPIJobsIndex_ReflectsRetention proves the index is the registry's
// view, not a second history: a job the registry has forgotten is not in
// it (jobs.go: "the registry keeps the last ~50 jobs, in memory only").
func TestAPIJobsIndex_ReflectsRetention(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	s.jobs.retain = 2

	runFinishedJob(t, s, "deploy", &core.DeployResult{}, nil)
	second := runFinishedJob(t, s, "deploy", &core.DeployResult{}, nil)
	third := runFinishedJob(t, s, "deploy", &core.DeployResult{}, nil)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")

	require.Equal(t, http.StatusOK, rec.Code)
	index := decodeJobsIndex(t, rec.Body.Bytes())
	require.Len(t, index.Jobs, 2, "the oldest job aged out of the registry")
	assert.Equal(t, third, index.Jobs[0].ID)
	assert.Equal(t, second, index.Jobs[1].ID)
}
