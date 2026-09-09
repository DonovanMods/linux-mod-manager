package serve

// Shared fixtures for the package-internal tests that drive a whole
// mutation flow - Plan, then Apply as a job - and then assert on the state
// it left behind (the DB row, the profile, the deployed tree).
//
// These are the surviving half of what was mutations_testhelpers_internal_
// test.go: the browser-form helpers went with the server-rendered page
// layer (docs/plans/2026-08-31-serve-spa-design.md), while the fixture
// seeding and the end-state readers below are entry-point-agnostic and are
// what the /api/v1 flow tests assert with.

import (
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// newFlowFixtureServer is newDeployFixtureServer plus the Service, which
// every flow test needs for its end-state assertions (the DB row, the
// profile, the deployed tree).
func newFlowFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	svc, game := newDeployFixtureService(t)
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game
}

// newLiveFlowFixtureServer is newFlowFixtureServer bound to a WILDCARD
// address, for the tests that drive the server over real TCP through
// httptest.NewServer (which picks its own ephemeral port): a concrete bind
// would pin a port the test cannot know in advance and 403 every request.
// In-process helpers still work against it, since a wildcard bind admits
// any IP-literal Host.
func newLiveFlowFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	svc, game := newDeployFixtureService(t)
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: ":0"}), svc, game
}

// seedFixtureModEnabled re-saves the fixture's seeded mod with the given
// enabled state - the full-row upsert SaveInstalledMod performs is the only
// way to reach the flag from outside package core.
func seedFixtureModEnabled(t *testing.T, svc *core.Service, game *domain.Game, enabled bool) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// scoped appends the fixture game/profile selection every scoped endpoint
// resolves from, so a flow test names its context exactly the way the SPA
// does (the pair lives in the SPA's path, /g/{game}/{profile}, and reaches
// /api/v1 as these two query params).
func scoped(target string, game *domain.Game) string {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + gameParam + "=" + url.QueryEscape(game.ID) + "&" + profileParam + "=default"
}

// planFlow drives POST /api/v1/plans/{kind} for the fixture game/profile
// and returns the issued single-use handle together with the raw plan
// response, so a caller can assert on the plan DOCUMENT as well as redeem
// it. body is the kind's own plan-time options JSON ("" for a kind that
// takes none).
func planFlow(t *testing.T, s *Server, game *domain.Game, kind, body string) (planID, []byte) {
	t.Helper()
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/"+kind, game), body)
	require.Equal(t, http.StatusOK, rec.Code, "plan %s: %s", kind, rec.Body.String())

	var resp struct {
		PlanID planID `json:"plan_id"`
		Kind   string `json:"kind"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.PlanID)
	require.Equal(t, kind, resp.Kind)
	return resp.PlanID, rec.Body.Bytes()
}

// startFlowJob redeems a plan handle through POST /api/v1/jobs with the
// kind's apply-time options ("" for none) and blocks until the Apply has
// returned, so the caller's end-state assertions never race the goroutine.
func startFlowJob(t *testing.T, s *Server, id planID, options string) *job {
	t.Helper()
	body := `{"plan_id":"` + string(id) + `"}`
	if options != "" {
		body = `{"plan_id":"` + string(id) + `","options":` + options + `}`
	}
	rec := doAPI(s, http.MethodPost, "/api/v1/jobs", body)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var started struct {
		JobID jobID `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	return awaitJob(t, s, started.JobID)
}

// runFlow is planFlow followed by startFlowJob - the whole Plan -> Apply
// round trip the SPA performs for one mutation.
func runFlow(t *testing.T, s *Server, game *domain.Game, kind, planBody, applyOptions string) *job {
	t.Helper()
	id, _ := planFlow(t, s, game, kind, planBody)
	return startFlowJob(t, s, id, applyOptions)
}

// awaitJob blocks until the named job's Apply has returned.
func awaitJob(t *testing.T, s *Server, id jobID) *job {
	t.Helper()
	j, ok := s.jobs.job(id)
	require.True(t, ok, "the registry does not hold job %q", id)
	select {
	case <-j.done():
	case <-time.After(30 * time.Second):
		t.Fatal("job did not finish")
	}
	return j
}

// startToggle drives one of the two plan-free toggle endpoints and blocks
// until its job has finished.
func startToggle(t *testing.T, s *Server, game *domain.Game, action, sourceID, modID string) *job {
	t.Helper()
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+sourceID+"/"+modID+"/"+action, game), "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var started struct {
		JobID jobID `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	return awaitJob(t, s, started.JobID)
}

// deployFixtureProfile runs a REAL deploy of the fixture profile, so the
// deployed_files rows a conflict check reads and the game-dir file an
// uninstall removes both genuinely exist. It goes through the plan/apply
// pair (never DeployProfile, the Ruling-10 convenience wrapper) for the same
// reason serve itself does.
func deployFixtureProfile(t *testing.T, s *Server, game *domain.Game) {
	t.Helper()
	plan, err := s.svc.PlanDeploy(t.Context(), game, "default", core.DeployOptions{})
	require.NoError(t, err)
	_, err = s.svc.ApplyDeploy(t.Context(), game, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)
}

// deployedPath is where a relative game-directory path lands under the
// fixture game's ModPath - the tree half of every flow's end-state
// assertion (task-8 review: install's tests asserted the DB row and the
// profile but never the file, so a regression that stopped install
// deploying would have gone unnoticed).
func deployedPath(game *domain.Game, rel string) string {
	return filepath.Join(game.ModPath, filepath.FromSlash(rel))
}

// deployedContent reads what a deployed path actually resolves to. The
// deployment is a symlink into the cache, so this follows it - which is
// exactly the point: it proves WHICH cached file the game directory is
// pointing at, not merely that something is there.
func deployedContent(t *testing.T, game *domain.Game, rel string) string {
	t.Helper()
	data, err := os.ReadFile(deployedPath(game, rel))
	require.NoError(t, err)
	return string(data)
}

// doAPIWithoutCSRF is doAPI with the token left off, for the refusal tests
// every mutation entry point carries.
func doAPIWithoutCSRF(s *Server, method, target, body string) *httptest.ResponseRecorder {
	req := apiRequest(s, method, target, body)
	req.Header.Del(csrfHeaderName)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}
