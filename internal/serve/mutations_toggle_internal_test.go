package serve

// Task 8 flow 1: enable/disable (docs/plans/2026-08-30-serve-impl.md Task
// 8). These two are the ONE sanctioned non-Plan mutation path - single-row
// toggles with nothing to preview - so they start a job directly instead of
// rendering a confirm page. Every test here asserts the END STATE (the
// database row, the game directory), never merely the status code.

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_ModDisable_RunsAsJobAndFlipsTheRow is the headline RED test:
// POSTing the /mods row's disable form starts a job, redirects to its page,
// and the mod really ends up disabled in the database.
func TestServer_ModDisable_RunsAsJobAndFlipsTheRow(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)

	rec := postForm(s, "/mods/fake/m1/disable", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusSeeOther, rec.Code)
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	mod, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "the disable job must leave the row disabled")
}

// TestServer_ModEnable_RunsAsJobAndDeploysFiles proves enable is a real
// mutation on both halves: the row flips AND the cached file lands in the
// game directory.
func TestServer_ModEnable_RunsAsJobAndDeploysFiles(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	seedFixtureModEnabled(t, svc, game, false)

	rec := postForm(s, "/mods/fake/m1/enable", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusSeeOther, rec.Code)
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	mod, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
	assert.FileExists(t, deployedFixturePath(game))
}

// TestServer_ModDisable_SyncFallback_MutatesIdentically is the no-JS
// fallback (?sync=1): the Apply runs inline and the result page is rendered
// directly, reaching the SAME end state the job path reaches.
func TestServer_ModDisable_SyncFallback_MutatesIdentically(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)

	rec := postForm(s, "/mods/fake/m1/disable?sync=1", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Disable")

	mod, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "the sync fallback must mutate exactly like the job path")
}

// TestServer_ModToggle_WithoutCSRF_IsRefused pins the Global Constraints
// CSRF rule on both toggle routes: no token, no mutation.
func TestServer_ModToggle_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)

	for _, action := range []string{"enable", "disable"} {
		rec := postFormWithoutCSRF(s, "/mods/fake/m1/"+action, formValues{"game": game.ID, "profile": "default"})
		require.Equal(t, http.StatusForbidden, rec.Code, action)
	}

	mod, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "a refused request must not have mutated anything")
}

// TestServer_ModToggle_UnknownMod_FailsTheJob proves a bad target is
// reported through the ordinary job-failure surface rather than a panic or
// a bare 500: the job runs, fails, and its page carries the envelope.
func TestServer_ModToggle_UnknownMod_FailsTheJob(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)

	rec := postForm(s, "/mods/fake/nope/disable", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusSeeOther, rec.Code)

	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobFailed, j.status().State)
	require.NotNil(t, j.status().Error)
	assert.Contains(t, j.status().Error.Error, "nope")
}

// TestServer_ModDisable_JobStreamsToItsTerminalFrame proves the documented
// claim in kind_toggle.go: a plan-free toggle is a JOB like any other, so
// its SSE stream behaves like any other - it replays what there is and ends
// on the terminal done frame carrying the final status. What it does NOT
// carry is progress events, because DisableMod takes no EventSink; the
// stream is still the signal a client waits on rather than polling.
func TestServer_ModDisable_JobStreamsToItsTerminalFrame(t *testing.T) {
	s, svc, game := newLiveMutationFixtureServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	rec := postForm(s, "/mods/fake/m1/disable", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/jobs/")

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

	mod, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)
}
