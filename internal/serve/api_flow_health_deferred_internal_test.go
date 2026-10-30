package serve

// #588 through the endpoint the Health card reads: a GET /api/v1/health
// that lands while a job is changing the game answers at once, deferred,
// and never with findings read off the half-written tree.

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getHealthReport is GET /api/v1/health for game, with extra query.
func getHealthReport(t *testing.T, s *Server, game *domain.Game, extra string) core.VerifyReport {
	t.Helper()
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+extra, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.VerifyReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	require.NotNil(t, report.Result)
	return report
}

// TestAPIFlow_Health_DeferredWhileTheAutoUpdateJobRuns is the owner's
// case: the automatic update (#587) starts from the same page load that
// hydrates health. While its job is mid-update the health read - the
// plain hydrate and Re-verify's forced one alike - says it is deferred and
// shows the last check; once the job is done it is a real check again,
// with nothing the update left behind.
func TestAPIFlow_Health_DeferredWhileTheAutoUpdateJobRuns(t *testing.T) {
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	src := newUpdatesSource(t)
	src.hold, src.held = hold, make(chan struct{})
	s, _, game := newAutoUpdatesFixtureServerWith(t, src)

	before := getHealthReport(t, s, game, "")
	require.Empty(t, before.Result.Deferred)

	report := getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoUpdate)
	<-src.held

	for _, extra := range []string{"", "&force=1"} {
		during := getHealthReport(t, s, game, extra)
		assert.Equal(t, core.VerifyDeferredJobRunning, during.Result.Deferred, "query %q", extra)
		assert.Equal(t, before.Result.CheckedAt, during.Result.CheckedAt, "the last check, dated when it ran")
		assert.Equal(t, before.Result.Findings, during.Result.Findings)
	}

	// Repair's dry run is the same check: it refuses rather than preview a
	// repair from a check it could not make.
	plan := doAPI(s, http.MethodPost, scoped("/api/v1/plans/verify_fix", game), `{}`)
	assert.Equal(t, http.StatusConflict, plan.Code, plan.Body.String())

	release()
	j := awaitJob(t, s, jobID(report.AutoUpdate.JobID))
	require.Equal(t, jobSucceeded, j.status().State)
	plan = doAPI(s, http.MethodPost, scoped("/api/v1/plans/verify_fix", game), `{}`)
	assert.Equal(t, http.StatusOK, plan.Code, plan.Body.String())

	after := getHealthReport(t, s, game, "")
	assert.Empty(t, after.Result.Deferred, "the job is done, so the check runs")
	assert.True(t, after.Result.CheckedAt.After(before.Result.CheckedAt))
	// The fixture's seeded rows carry no checksums (warnings, before and
	// after - fewer after, the updated row now has one); the update itself
	// leaves nothing behind for verify to find.
	assert.Zero(t, after.Result.Issues, "%+v", after.Result.Findings)
	assert.Less(t, after.Result.Warnings, before.Result.Warnings, "%+v", after.Result.Findings)
	for _, f := range after.Result.Findings {
		assert.NotEqual(t, core.VerifyStatusDeployedBlocked, f.Status, "%+v", f)
	}
}
