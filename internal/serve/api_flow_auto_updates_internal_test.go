package serve

// #587 through the endpoint the Updates card reads: a GET /api/v1/updates
// that finds auto-policy updates applies them, server-side, as an ordinary
// "updates" job - once, never under a running job, and never while the
// game is running.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAutoUpdatesFixtureServer is the updates fixture with u1 on the auto
// policy, u2 on auto but locked, and u3 left on notify - the control.
func newAutoUpdatesFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	return newAutoUpdatesFixtureServerWith(t, newUpdatesSource(t))
}

// newAutoUpdatesFixtureServerWith is newAutoUpdatesFixtureServer over src.
func newAutoUpdatesFixtureServerWith(t *testing.T, src *updatesSource) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newUpdatesFixtureServerWith(t, src)
	ctx := t.Context()
	for _, id := range []string{"u1", "u2"} {
		_, err := svc.SetModUpdatePolicy(ctx, fixtureSourceID, id, game.ID, "default", domain.UpdateAuto)
		require.NoError(t, err)
	}
	require.NoError(t, svc.NewProfileManager().SetModLock(ctx, game.ID, "default", fixtureSourceID, "u2", ""))
	return s, svc, game
}

// installedVersion is modID's installed version in the fixture profile.
func installedVersion(t *testing.T, svc *core.Service, game *domain.Game, modID string) string {
	t.Helper()
	im, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, modID, game.ID, "default")
	require.NoError(t, err)
	return im.Version
}

// updatesJobs is every "updates" job the registry holds.
func updatesJobs(s *Server) []jobSummary {
	var out []jobSummary
	for _, j := range s.jobs.list() {
		if j.Kind == "updates" {
			out = append(out, j)
		}
	}
	return out
}

// TestAPIFlow_AutoUpdates_ACheckAppliesOnlyTheAutoRows is the fix itself:
// the check names the job it started, the job applies the auto row and
// nothing else - not the locked auto row, not the notify row - and the
// next check no longer lists what it applied.
func TestAPIFlow_AutoUpdates_ACheckAppliesOnlyTheAutoRows(t *testing.T) {
	s, svc, game := newAutoUpdatesFixtureServer(t)

	report := getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoUpdate, "a check that finds an auto-policy update must act on it")
	assert.Equal(t, core.ModAutoUpdateApplying, report.AutoUpdate.State)
	assert.Equal(t, []string{fixtureSourceID + ":u1"}, report.AutoUpdate.Mods)
	require.NotEmpty(t, report.AutoUpdate.JobID)

	j := awaitJob(t, s, jobID(report.AutoUpdate.JobID))
	status := j.status()
	require.Equal(t, jobSucceeded, status.State, "job failed: %+v", status.Error)
	assert.Equal(t, "updates", status.Kind, "an automatic pass is an ordinary updates job, with its tally")
	assert.Equal(t, jobOriginAuto, status.Origin)
	require.NotNil(t, status.Tally)
	assert.Equal(t, 1, status.Tally.Applied)

	assert.Equal(t, updateToVersion, installedVersion(t, svc, game, "u1"))
	assert.Equal(t, updatableContent("u1", updateToVersion), deployedContent(t, game, updatableModFile("u1")))
	assert.Equal(t, updateFromVersion, installedVersion(t, svc, game, "u2"), "a lock outranks the auto policy")
	assert.Equal(t, updateFromVersion, installedVersion(t, svc, game, "u3"), "a notify row is the user's to apply")

	after := getUpdatesReport(t, s, game)
	assert.ElementsMatch(t, []string{"u2", "u3"}, updateIDs(after.Updates))
	assert.Nil(t, after.AutoUpdate, "nothing left to apply automatically")
	assert.Len(t, updatesJobs(s), 1)
}

// TestAPIFlow_AutoUpdates_StepAsideWhileAJobRuns: a check made while a
// mutation runs never queues one behind it; the next check applies.
func TestAPIFlow_AutoUpdates_StepAsideWhileAJobRuns(t *testing.T) {
	s, svc, game := newAutoUpdatesFixtureServer(t)

	release := make(chan struct{})
	busy, err := s.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-release
		return nil, nil
	})
	require.NoError(t, err)

	report := getUpdatesReport(t, s, game)
	assert.Nil(t, report.AutoUpdate, "the pass steps aside, and says nothing it is not doing")
	assert.Empty(t, updatesJobs(s), "nothing is queued behind the running job")

	close(release)
	awaitJob(t, s, busy)

	report = getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoUpdate)
	awaitJob(t, s, jobID(report.AutoUpdate.JobID))
	assert.Equal(t, updateToVersion, installedVersion(t, svc, game, "u1"))
}

// TestAPIFlow_AutoUpdates_WaitForTheGameToClose is #587 decision 2: while
// the game runs the check reports what it is waiting for and applies
// nothing; the first check after it closes applies.
func TestAPIFlow_AutoUpdates_WaitForTheGameToClose(t *testing.T) {
	s, svc, game := newAutoUpdatesFixtureServer(t)
	running := true
	var mu sync.Mutex
	const unit = "lmm-launch-g1-0a1b2c3d.service"
	svc.SetGameProbe(&core.GameProbe{
		Run: func(context.Context, string, ...string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			if !running {
				return nil, nil
			}
			return []byte(unit + " loaded active running lmm: Fixture Game\n"), nil
		},
		UnitPIDs: func(context.Context, string) ([]int, error) { return []int{500}, nil },
		Processes: func() ([]core.ProcessInfo, error) {
			mu.Lock()
			defer mu.Unlock()
			if !running {
				return nil, nil
			}
			exe := game.InstallPath + "/game.bin"
			return []core.ProcessInfo{{PID: 500, Exe: exe, Cmdline: []string{exe}}}, nil
		},
	})

	report := getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoUpdate)
	assert.Equal(t, core.ModAutoUpdateWaitingForGame, report.AutoUpdate.State)
	assert.Equal(t, "Fixture Game", report.AutoUpdate.GameName)
	assert.Equal(t, core.GameRunningLaunchUnit, report.AutoUpdate.DetectedBy)
	assert.Equal(t, []string{fixtureSourceID + ":u1"}, report.AutoUpdate.Mods)
	assert.Empty(t, updatesJobs(s))
	assert.Equal(t, updateFromVersion, installedVersion(t, svc, game, "u1"))

	mu.Lock()
	running = false
	mu.Unlock()
	report = getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoUpdate)
	require.Equal(t, core.ModAutoUpdateApplying, report.AutoUpdate.State)
	awaitJob(t, s, jobID(report.AutoUpdate.JobID))
	assert.Equal(t, updateToVersion, installedVersion(t, svc, game, "u1"))
}

// TestAPIFlow_AutoUpdates_ConcurrentChecksApplyOnce is the two-tabs case
// (and the page-load check racing another): however many checks arrive
// together, one job applies the update, once.
func TestAPIFlow_AutoUpdates_ConcurrentChecksApplyOnce(t *testing.T) {
	s, svc, game := newAutoUpdatesFixtureServer(t)

	const checks = 6
	var wg sync.WaitGroup
	reports := make([]core.UpdateCheckReport, checks)
	for i := range checks {
		wg.Go(func() { reports[i] = getUpdatesReport(t, s, game) })
	}
	wg.Wait()

	jobs := updatesJobs(s)
	require.Len(t, jobs, 1, "exactly one automatic job, however many checks")
	awaitJob(t, s, jobs[0].ID)
	for _, r := range reports {
		if r.AutoUpdate != nil {
			assert.Equal(t, string(jobs[0].ID), r.AutoUpdate.JobID, "every check reporting it names the one job")
		}
	}
	assert.Equal(t, updateToVersion, installedVersion(t, svc, game, "u1"))
	status, _ := s.jobs.job(jobs[0].ID)
	assert.Equal(t, 1, status.status().Tally.Applied)
}

// TestAutoUpdater_AnAttemptedUpdateIsNotRetriedUntilAskedTo: an update the
// automatic pass already tried at a version - failed, skipped, or applied
// and still offered - is left for the user rather than tried by every
// check: the job's own completion re-reads the updates, so a retry on every
// check would be a loop. A new version, or an explicit refresh, tries again.
func TestAutoUpdater_AnAttemptedUpdateIsNotRetriedUntilAskedTo(t *testing.T) {
	var a autoUpdater
	key := autoUpdateKey("g1", "default")
	update := func(id, version string) domain.Update {
		return domain.Update{InstalledMod: domain.InstalledMod{Mod: domain.Mod{ID: id, SourceID: "src"}, UpdatePolicy: domain.UpdateAuto}, NewVersion: version}
	}
	planned := []domain.Update{update("a", "2"), update("b", "2"), update("c", "2"), update("d", "2")}

	a.running = map[string]autoUpdateJob{key: {id: "j1"}}
	a.recordOutcome(key, planned, &core.UpdateBatchResult{
		Failed:  []core.UpdateBatchFailure{{Mod: "src:a", ToVersion: "2", Error: "boom"}},
		Skipped: []core.UpdateApplyResult{{Mod: domain.ModReference{SourceID: "src", ModID: "b"}}},
		Applied: []core.UpdateApplyResult{{Mod: domain.ModReference{SourceID: "src", ModID: "c"}}},
	}, nil)
	got := a.retryable(key, planned, false)
	assert.Equal(t, []string{"d"}, updateIDs(got), "failed, skipped and applied are held back; the one never reached is not")
	assert.Equal(t, []core.ModAutoUpdateFailure{{
		UpdateBatchFailure: core.UpdateBatchFailure{Mod: "src:a", ToVersion: "2", Error: "boom"},
		JobID:              "j1",
	}}, a.failures(key, planned), "only the failed one is reported, with the job it failed in")
	assert.Empty(t, a.failures(key, []domain.Update{update("a", "3")}), "a failure at an older version is no longer the news")
	notify := update("a", "2")
	notify.InstalledMod.UpdatePolicy = domain.UpdateNotify
	assert.Empty(t, a.failures(key, []domain.Update{notify}), "nor one the user took off the auto policy")

	got = a.retryable(key, []domain.Update{update("a", "3")}, false)
	assert.Equal(t, []string{"a"}, updateIDs(got), "a newer version is a new attempt")

	a.running = map[string]autoUpdateJob{key: {id: "j2"}}
	a.recordOutcome(key, planned, nil, errors.New("whole job failed"))
	assert.Empty(t, a.retryable(key, planned, false), "a job that failed outright holds back all it planned")
	failures := a.failures(key, planned)
	require.Len(t, failures, 4, "and reports every one of them failed")
	assert.Equal(t, core.ModAutoUpdateFailure{
		UpdateBatchFailure: core.UpdateBatchFailure{Mod: "src:b", ToVersion: "2", Error: "whole job failed"},
		JobID:              "j2",
	}, failures[1])
	assert.Len(t, a.retryable(key, planned, true), 4, "an explicit refresh asks again")
	assert.Len(t, a.retryable(key, planned, false), 4, "and the refresh cleared the record")
	assert.Empty(t, a.failures(key, planned), "failures included")
}

// TestAPIFlow_AutoUpdates_NeverForAnotherProfile is decision 3 over HTTP:
// checking a profile that is not the deployed one applies nothing.
func TestAPIFlow_AutoUpdates_NeverForAnotherProfile(t *testing.T) {
	s, svc, game := newAutoUpdatesFixtureServer(t)
	_, err := svc.NewProfileManager().Create(t.Context(), game.ID, "other")
	require.NoError(t, err)

	rec := doAPI(s, http.MethodGet, "/api/v1/updates?"+gameParam+"="+game.ID+"&"+profileParam+"=other", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "auto_update")
	assert.Empty(t, updatesJobs(s))
}

// TestAPIFlow_AutoUpdates_AFailureIsReportedNotHidden: an auto update that
// fails inside a batch which itself ends "succeeded" is held back, so the
// next check would list it with no word of why. The report says it failed,
// why, and in which job - until an explicit check tries it again.
func TestAPIFlow_AutoUpdates_AFailureIsReportedNotHidden(t *testing.T) {
	src := newUpdatesSource(t)
	src.failing.Store("u1", true)
	s, svc, game := newAutoUpdatesFixtureServerWith(t, src)

	report := getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoUpdate)
	require.Equal(t, core.ModAutoUpdateApplying, report.AutoUpdate.State)
	first := jobID(report.AutoUpdate.JobID)
	status := awaitJob(t, s, first).status()
	require.Equal(t, jobSucceeded, status.State, "a batch with a failed item still succeeds as a job")
	require.NotNil(t, status.Tally)
	require.Equal(t, 1, status.Tally.Failed)

	report = getUpdatesReport(t, s, game)
	assert.Contains(t, updateIDs(report.Updates), "u1", "the failed update is still offered")
	require.NotNil(t, report.AutoUpdate, "and the report says why it is still there")
	assert.Equal(t, core.ModAutoUpdateFailed, report.AutoUpdate.State)
	assert.Equal(t, []string{fixtureSourceID + ":u1"}, report.AutoUpdate.Mods)
	require.Len(t, report.AutoUpdate.Failed, 1)
	failure := report.AutoUpdate.Failed[0]
	assert.Equal(t, fixtureSourceID+":u1", failure.Mod)
	assert.Equal(t, "U1", failure.Name)
	assert.Equal(t, updateToVersion, failure.ToVersion)
	assert.NotEmpty(t, failure.Error)
	assert.Equal(t, string(first), failure.JobID)
	assert.Len(t, updatesJobs(s), 1, "a failure is reported, not retried")

	src.failing.Delete("u1")
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game)+"&refresh=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var retried core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &retried))
	require.NotNil(t, retried.AutoUpdate)
	require.Equal(t, core.ModAutoUpdateApplying, retried.AutoUpdate.State, "an explicit check tries it again")
	assert.Empty(t, retried.AutoUpdate.Failed)
	awaitJob(t, s, jobID(retried.AutoUpdate.JobID))
	assert.Equal(t, updateToVersion, installedVersion(t, svc, game, "u1"))
	assert.Nil(t, getUpdatesReport(t, s, game).AutoUpdate)
}

// TestAPIFlow_AutoUpdates_OnlyTheSPAStartsThePass: GET needs no CSRF token,
// so a cross-site page could make the browser fetch /api/v1/updates (an
// <img> tag will do) - and, were the pass run for any GET, start it. The
// pass runs only for a request carrying the token the SPA sends, which a
// cross-site page cannot set; without it the GET answers, read-only.
func TestAPIFlow_AutoUpdates_OnlyTheSPAStartsThePass(t *testing.T) {
	s, svc, game := newAutoUpdatesFixtureServer(t)

	for name, token := range map[string]string{"no token": "", "a wrong token": "not-the-token"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+internalTestAddr+scoped("/api/v1/updates", game), nil)
		if token != "" {
			req.Header.Set(csrfHeaderName, token)
		}
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s: the read itself still works: %s", name, rec.Body.String())
		var report core.UpdateCheckReport
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
		assert.Contains(t, updateIDs(report.Updates), "u1", name)
		assert.Nil(t, report.AutoUpdate, "%s: no automatic pass", name)
	}
	assert.Empty(t, updatesJobs(s))
	assert.Equal(t, updateFromVersion, installedVersion(t, svc, game, "u1"))

	report := getUpdatesReport(t, s, game) // the SPA's request: token attached
	require.NotNil(t, report.AutoUpdate)
	awaitJob(t, s, jobID(report.AutoUpdate.JobID))
	assert.Equal(t, updateToVersion, installedVersion(t, svc, game, "u1"))
}
