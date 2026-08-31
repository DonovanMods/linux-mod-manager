package serve

// Task 9 flow 4: deploy from the profiles page
// (docs/plans/2026-08-30-serve-impl.md Task 9). The kind itself has existed
// since Task 7 - what lands here is its browser half, and the rule that
// makes it different from every other confirm page: deploy's options are
// PLAN-time, so the confirm page IS the plan page and changing an option
// re-plans rather than quietly applying something else.
//
// It is also the flow #257 exists for: a deploy is the long one, so its job
// has to stream what it is doing rather than leave a user staring at a
// spinner.

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_ProfileDeploy_ConfirmIsThePlanPage: the entry submission
// renders the plan - which mods, which files - and deploys nothing.
func TestServer_ProfileDeploy_ConfirmIsThePlanPage(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	require.NoFileExists(t, deployedFixturePath(game))

	rec := postForm(s, "/deploy", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "Mod One", "the plan's mods must be named")
	assert.Contains(t, body, deployFixtureFile, "and the files they would link")
	assert.Contains(t, body, `name="purge"`, "#226: deploy's options belong on its confirm page")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	require.NoFileExists(t, deployedFixturePath(game), "a plan must deploy nothing")
}

// TestServer_ProfileDeploy_ConfirmRunsTheJobAndDeploys is the apply half.
func TestServer_ProfileDeploy_ConfirmRunsTheJobAndDeploys(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	entry := postForm(s, "/deploy", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/deploy", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	require.FileExists(t, deployedFixturePath(game), "the deploy must reach the game directory")
}

// TestServer_ProfileDeploy_SyncFallback_MutatesIdentically is the no-JS
// path.
func TestServer_ProfileDeploy_SyncFallback_MutatesIdentically(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	entry := postForm(s, "/deploy", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/deploy?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Done.")
	require.FileExists(t, deployedFixturePath(game))
}

// TestServer_ProfileDeploy_ChangedOptionRePlansInsteadOfApplying is the
// rule deploy's own kind states: ApplyDeploy must receive the SAME options
// its plan was computed from, so a user who ticks --purge and presses the
// primary button gets a fresh plan to look at, never a mutation they never
// previewed.
func TestServer_ProfileDeploy_ChangedOptionRePlansInsteadOfApplying(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	entry := postForm(s, "/deploy", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code)
	firstPlanID := hiddenField(t, entry.Body.String(), "plan_id")

	rec := postForm(s, "/deploy", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": firstPlanID, "purge": "1",
	})

	require.Equal(t, http.StatusOK, rec.Code, "a changed plan-time option must re-plan, not apply")
	body := rec.Body.String()
	assert.Contains(t, body, "options changed")
	assert.NotEqual(t, firstPlanID, hiddenField(t, body, "plan_id"), "a re-plan issues a fresh handle")
	require.NoFileExists(t, deployedFixturePath(game), "nothing may have been deployed")
}

// TestServer_ProfileDeploy_WithoutCSRF_IsRefused pins the CSRF rule on the
// deploy route, and that a refused request deployed nothing.
func TestServer_ProfileDeploy_WithoutCSRF_IsRefused(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)

	rec := postFormWithoutCSRF(s, "/deploy", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NoFileExists(t, deployedFixturePath(game))
}

// TestServer_ProfileDeploy_JobStreamsLivePhases is #257: a deploy started
// from the page streams its progress phases over SSE, in order, and ends on
// the terminal done frame - so a no-JS user reading /jobs/{id} and Task
// 10's enhancement both see the same thing happening.
func TestServer_ProfileDeploy_JobStreamsLivePhases(t *testing.T) {
	s, _, game := newLiveMutationFixtureServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	// "purge": "1" so the purge pass actually runs (the fixture's one
	// installed mod makes it non-empty) - without it, opts.Purge is false
	// and deploy_purging/purge_complete never fire at all
	// (internal/core/deploy.go's `if opts.Purge {...}`), leaving nothing
	// for the ordered-subsequence assertion below to pin (task-9 review
	// Minor 6 asked for the DeployPhase vocabulary to be seen in order,
	// not just the single deploy_deployed phase the untouched happy path
	// produces).
	entry := postForm(s, "/deploy", formValues{"game": game.ID, "profile": "default", "purge": "1"})
	require.Equal(t, http.StatusOK, entry.Code)
	rec := postForm(s, "/deploy", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "purge": "1",
		"plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/jobs/")

	stream := getStream(t, srv, "/api/v1/jobs/"+id+"/events")
	defer func() { _ = stream.Body.Close() }()
	body, err := io.ReadAll(stream.Body)
	require.NoError(t, err)
	frames := parseSSE(t, string(body))
	require.NotEmpty(t, frames)

	var phases []string
	for _, frame := range frames[:len(frames)-1] {
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
	var final jobStatus
	require.NoError(t, json.Unmarshal([]byte(terminal.Data), &final))
	assert.Equal(t, jobSucceeded, final.State)
	assert.Equal(t, "deploy", final.Kind)

	require.FileExists(t, deployedFixturePath(game))

	// The same phases are on the no-JS page, which is what a user without
	// JavaScript reads instead of the stream.
	page := getPage(s, "/jobs/"+id)
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "deploy_deployed")
}

// assertOrderedSubsequence fails t unless want appears within got as an
// ordered subsequence (other phases may appear between, before, or after
// want's entries; want's own entries must not appear out of order or be
// missing). A plain assert.Contains per element - what this replaced
// (task-9 review Minor 6) - would pass even if the phases arrived in the
// wrong order, since each membership check is independent of the others.
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
