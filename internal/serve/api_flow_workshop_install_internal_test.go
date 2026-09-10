package serve

// The Steam Workshop install flow over /api/v1 (#269 Tier 3), driven
// through the same entry point the SPA drives - POST /api/v1/plans/install
// then POST /api/v1/jobs - with the REAL steamworkshop source pointed at a
// recorded metadata fixture and the FAKE steamcmd on PATH. No test here
// reaches Valve, runs the real tool, or reads the owner's Steam library.
//
// The assertions are on END STATE, as every other flow test's are: the
// database row (External: false - an item lmm downloaded is lmm-managed,
// unlike the Tier-1 item it merely tracks), the profile, the deployed tree,
// and - for the refusals - that the failed job's stored envelope carries
// the typed Details() the SPA renders its steamcmd explainer from.

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workshopFetchAppID is the app id the fixture game maps steamworkshop to.
const workshopFetchAppID = "1133870"

// workshopDetailsFixtureServer serves GetPublishedFileDetails for the fake
// steamcmd's item ids with an empty file_url, so every one of them takes
// the source.Fetcher path rather than a direct download.
func workshopDetailsFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		id := "3000000001"
		for _, candidate := range []string{"3000000001", "3000000002", "3000000003"} {
			if strings.Contains(string(body), candidate) {
				id = candidate
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"response":{"result":1,"resultcount":1,"publishedfiledetails":[
			{"publishedfileid":%q,"result":1,"creator":"76561198000000000",
			 "title":"Workshop Item %s","description":"A Workshop item.",
			 "file_size":"240","file_url":"","hcontent_file":"7987119735124793734",
			 "time_created":1700000000,"time_updated":1764767935,
			 "lifetime_subscriptions":1,"tags":[]}]}}`, id, id)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newWorkshopInstallServer is the install fixture with the Steam Workshop
// source in place of the ordinary downloading one.
func newWorkshopInstallServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	sandboxEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("STEAM_ROOT", t.TempDir())
	testutil.FakeSteamcmdOnPath(t)

	svc := newJobsService(t)
	svc.RegisterSource(steamworkshop.New(steamworkshop.Options{
		BaseURL:    workshopDetailsFixtureServer(t).URL,
		CacheDir:   t.TempDir(),
		SteamRoots: []string{t.TempDir()},
	}))

	ctx := t.Context()
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"steamworkshop": workshopFetchAppID},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err := svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game
}

// workshopPlanBody is the plan request naming one Workshop item.
func workshopPlanBody(fileID string) string {
	return `{"source_id":"steamworkshop","mod_id":"` + fileID + `"}`
}

// TestFlowWorkshopInstall_JobDownloadsAndDeploysAnOrdinaryMod is Tier 3's
// point over the web frontend: no new job kind, no new plan type, and the
// resulting row is an ordinary lmm-managed mod.
func TestFlowWorkshopInstall_JobDownloadsAndDeploysAnOrdinaryMod(t *testing.T) {
	s, svc, game := newWorkshopInstallServer(t)

	j := runFlow(t, s, game, "install", workshopPlanBody("3000000001"), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), "steamworkshop", "3000000001", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, installed.External, "an item lmm downloaded is lmm-managed, not external")
	assert.True(t, installed.Deployed)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 1)

	require.FileExists(t, deployedPath(game, "mod.txt"))
}

// TestFlowWorkshopInstall_AnonymousRefusalReachesTheClientTyped is what the
// SPA's steamcmd explainer is built from: the failed job keeps the typed
// error AND its Details(), so the mod page can explain the refusal and name
// the Tier-1 route instead of printing a subprocess's stderr.
func TestFlowWorkshopInstall_AnonymousRefusalReachesTheClientTyped(t *testing.T) {
	s, svc, game := newWorkshopInstallServer(t)

	j := runFlow(t, s, game, "install", workshopPlanBody("3000000002"), "")
	require.Equal(t, jobFailed, j.status().State)

	var fetchErr *core.WorkshopFetchError
	require.ErrorAs(t, j.failure(), &fetchErr, "the job must store the typed fetch error")
	require.NotNil(t, j.status().Error, "the failed job must carry the wire envelope")
	require.NotNil(t, j.status().Error.Details, "the stored envelope must keep Details()")

	status := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.status().ID), "")
	require.Equal(t, http.StatusOK, status.Code)
	body := status.Body.String()
	assert.Contains(t, body, `"tool": "steamcmd"`, "the tool must be on the wire, not only in the sentence")
	assert.Contains(t, body, "lmm import --workshop", "the Tier-1 fallback is the whole point of this refusal")
	assert.Contains(t, body, workshopFetchAppID)

	_, err := svc.GetInstalledMod(t.Context(), "steamworkshop", "3000000002", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "a refused download must install nothing")
}

// TestFlowWorkshopInstall_MissingSteamcmdIsAnActionableRefusal pins the
// runtime probe over the web frontend: the install action stays offered,
// and a machine without the tool gets the install hint as typed details.
func TestFlowWorkshopInstall_MissingSteamcmdIsAnActionableRefusal(t *testing.T) {
	s, svc, game := newWorkshopInstallServer(t)
	t.Setenv("PATH", t.TempDir())

	j := runFlow(t, s, game, "install", workshopPlanBody("3000000001"), "")
	require.Equal(t, jobFailed, j.status().State)
	require.ErrorIs(t, j.failure(), domain.ErrExternalToolMissing)

	status := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.status().ID), "")
	assert.Contains(t, status.Body.String(), "SteamCMD")

	_, err := svc.GetInstalledMod(t.Context(), "steamworkshop", "3000000001", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestFlowWorkshopInstall_AccessDeniedReportsAnUnavailableItem is the third
// outcome, and shares its sentinel with the Web API's own result 9.
func TestFlowWorkshopInstall_AccessDeniedReportsAnUnavailableItem(t *testing.T) {
	s, _, game := newWorkshopInstallServer(t)

	j := runFlow(t, s, game, "install", workshopPlanBody("3000000003"), "")
	require.Equal(t, jobFailed, j.status().State)
	require.ErrorIs(t, j.failure(), domain.ErrWorkshopItemUnavailable)
}

// TestFlowWorkshopInstall_FetchPhasesReachTheJobsEventStream is the
// server-side half of the progress claim (the browser-side half is
// e2e_test.go's TestE2E_FetchPhasesReachTheScreenHumanized).
//
// It is a REGRESSION test for a real gap: every flow adapts the
// downloader's raw stream with a progressFn that keeps DownloadEvents and
// drops everything else, which is right for the HTTP path and silently ate
// every fetch phase - so a multi-gigabyte steamcmd download reported
// nothing at all between "Working…" and done.
func TestFlowWorkshopInstall_FetchPhasesReachTheJobsEventStream(t *testing.T) {
	s, _, game := newWorkshopInstallServer(t)

	j := runFlow(t, s, game, "install", workshopPlanBody("3000000001"), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	replay, _, cancel := j.subscribe(1)
	t.Cleanup(cancel)

	var phases []string
	var details []string
	for _, e := range replay {
		flow, ok := e.(core.FlowEvent)
		if !ok {
			continue
		}
		phase := flow.FlowPhase().String()
		if !strings.HasPrefix(phase, "workshop_fetch_") {
			continue
		}
		phases = append(phases, phase)
		if step, ok := e.(core.StepEvent); ok {
			details = append(details, step.Detail)
		}
	}

	require.NotEmpty(t, phases, "no fetch phase reached the job's stream")
	assert.Equal(t, "workshop_fetch_started", phases[0])
	assert.Equal(t, "workshop_fetch_done", phases[len(phases)-1])
	assert.Contains(t, phases, "workshop_fetch_progress")
	assert.Contains(t, strings.Join(details, "\n"), "78.90",
		"the tool's own progress lines must survive the flow's adapter")
}
