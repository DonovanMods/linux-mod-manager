package serve

// The two plan-free toggles over /api/v1 (kind_toggle.go): POST
// /api/v1/mods/{source}/{id}/{enable,disable}. They are the ONE sanctioned
// mutation path with no plan step - single-row toggles with nothing to
// preview - so they start a job directly. Every test here asserts the END
// STATE (the database row, the game directory), never merely the status
// code, exactly as the deleted mutations_toggle_internal_test.go did.

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowToggle_DisableRunsAsJobAndFlipsTheRow is the headline test: the
// disable endpoint starts a job and the mod really ends up disabled.
func TestFlowToggle_DisableRunsAsJobAndFlipsTheRow(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)

	j := startToggle(t, s, game, "disable", fixtureSourceID, "m1")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "the disable job must leave the row disabled")
}

// TestFlowToggle_EnableRunsAsJobAndDeploysFiles proves enable is a real
// mutation on both halves: the row flips AND the cached file lands in the
// game directory.
func TestFlowToggle_EnableRunsAsJobAndDeploysFiles(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedFixtureModEnabled(t, svc, game, false)

	j := startToggle(t, s, game, "enable", fixtureSourceID, "m1")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
	assert.FileExists(t, deployedFixturePath(game))
}

// TestFlowToggle_WithoutCSRF_IsRefused pins the CSRF rule on both toggle
// routes: no token, no mutation.
func TestFlowToggle_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)

	for _, action := range []string{"enable", "disable"} {
		rec := doAPIWithoutCSRF(s, http.MethodPost,
			scoped("/api/v1/mods/"+fixtureSourceID+"/m1/"+action, game), "")
		require.Equal(t, http.StatusForbidden, rec.Code, action)
	}

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "a refused request must not have mutated anything")
}

// TestFlowToggle_UnknownMod_FailsTheJob proves a bad target is reported
// through the ordinary job-failure surface rather than a panic or a bare
// 500: the job runs, fails, and its status carries the envelope.
func TestFlowToggle_UnknownMod_FailsTheJob(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	j := startToggle(t, s, game, "disable", fixtureSourceID, "nope")
	require.Equal(t, jobFailed, j.status().State)
	require.NotNil(t, j.status().Error)
	assert.Contains(t, j.status().Error.Error, "nope")
}

// TestFlowToggle_UnknownAction_Is404Envelope pins the closed table: an
// action no toggle kind registers is not a handler's problem to refuse, it
// simply is not a route - so it lands on the /api/v1/ fallback's JSON 404
// rather than a text/plain one.
func TestFlowToggle_UnknownAction_Is404Envelope(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/incinerate", game), "")

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.NotEmpty(t, envelope.Error)
}

// TestFlowToggle_JobStreamsToItsTerminalFrame proves the documented claim
// in kind_toggle.go: a plan-free toggle is a JOB like any other, so its SSE
// stream behaves like any other - it replays what there is and ends on the
// terminal done frame carrying the final status. What it does NOT carry is
// progress events, because DisableMod takes no EventSink; the stream is
// still the signal a client waits on rather than polling.
func TestFlowToggle_JobStreamsToItsTerminalFrame(t *testing.T) {
	s, svc, game := newLiveFlowFixtureServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	j := startToggle(t, s, game, "disable", fixtureSourceID, "m1")
	id := string(j.status().ID)

	stream := getStream(t, srv, "/api/v1/jobs/"+id+"/events")
	defer func() { _ = stream.Body.Close() }()
	require.Equal(t, sseContentType, stream.Header.Get("Content-Type"))

	body, err := io.ReadAll(stream.Body)
	require.NoError(t, err)
	frames := parseSSE(t, string(body))
	require.NotEmpty(t, frames)

	terminal := frames[len(frames)-1]
	require.Equal(t, sseDoneEvent, terminal.Event)
	var final jobStatus
	require.NoError(t, json.Unmarshal([]byte(terminal.Data), &final))
	assert.Equal(t, jobSucceeded, final.State)
	assert.Equal(t, "disable", final.Kind)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)
}
