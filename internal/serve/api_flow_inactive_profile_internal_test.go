package serve

// #445 over /api/v1: the web UI offers Deploy and Purge for whichever
// profile the page is showing, including one that is not active. The game
// directory holds the active profile's mods, so a deploy plan is refused -
// 409, naming `lmm profile switch` - and a purge plan is a recorded-only
// cleanup: it removes only what that profile recorded as deployed and no
// other profile records, and it refuses --uninstall.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// altFile is the file only profile alt deploys.
const altFile = "Mods/alt.pak"

// mixedFlowServer is the flow fixture with a second profile, alt, whose
// mods - the fixture's m1, which default deploys too, and its own m2 - were
// deployed while it was briefly active; default is active again.
func mixedFlowServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	ctx := t.Context()
	pm := svc.NewProfileManager()
	deployFixtureProfile(t, s, game)
	_, err := pm.Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, fixtureSourceID, "m2", "1.0", altFile, []byte("alt bytes")))
	for _, id := range []string{"m1", "m2"} {
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:          domain.Mod{ID: id, SourceID: fixtureSourceID, Name: "Mod " + id, Version: "1.0", GameID: game.ID},
			ProfileName:  "alt",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		}))
		require.NoError(t, pm.AddMod(ctx, game.ID, "alt", domain.ModReference{SourceID: fixtureSourceID, ModID: id, Version: "1.0"}))
	}
	require.NoError(t, pm.SetDefault(ctx, game.ID, "alt"))
	_, err = svc.DeployProfile(ctx, game, "alt", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	require.FileExists(t, deployedPath(game, altFile))
	return s, svc, game
}

func altPlanTarget(kind string, game *domain.Game) string {
	return "/api/v1/plans/" + kind + "?" + gameParam + "=" + url.QueryEscape(game.ID) + "&" + profileParam + "=alt"
}

// requireConflict checks rec is #445's 409 for profile alt.
func requireConflict(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var envelope apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Contains(t, envelope.Error, core.ErrProfileNotActive.Error())
	assert.Contains(t, envelope.Error, "lmm profile switch alt")
}

func TestFlowInactiveProfile_DeployIsRefused(t *testing.T) {
	s, _, game := mixedFlowServer(t)
	rec := doAPI(s, http.MethodPost, altPlanTarget("deploy", game), "")
	requireConflict(t, rec)
	assert.FileExists(t, deployedFixturePath(game), "the active profile's deployment is untouched")
}

func TestFlowInactiveProfile_PurgeClearsOnlyWhatItRecorded(t *testing.T) {
	s, svc, game := mixedFlowServer(t)

	rec := doAPI(s, http.MethodPost, altPlanTarget("purge", game), `{"uninstall":true}`)
	requireConflict(t, rec)

	rec = doAPI(s, http.MethodPost, altPlanTarget("purge", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		PlanID planID         `json:"plan_id"`
		Plan   core.PurgePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Plan.RecordedOnly)
	assert.Equal(t, "default", resp.Plan.ActiveProfile)
	assert.Equal(t, []string{altFile}, resp.Plan.Remove)
	assert.Equal(t, []core.PurgeKeptPath{{Path: deployFixtureFile, Reason: core.PurgeKeptRecorded, Profiles: []string{"default"}}}, resp.Plan.Kept)
	assert.FileExists(t, deployedPath(game, altFile), "planning changes nothing")

	j := startFlowJob(t, s, resp.PlanID, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.NoFileExists(t, deployedPath(game, altFile), "alt's own file is gone")
	assert.FileExists(t, deployedFixturePath(game), "the file the active profile records too survives")
	rows, err := svc.GetInstalledMods(t.Context(), game.ID, "alt")
	require.NoError(t, err)
	assert.Len(t, rows, 2, "alt's records are kept")
}

// TestFlowSwitch_WithNoSingleActiveProfileOnlyMarksTheTarget is #445 review
// F2 over /api/v1: with no profile marked active, a deploy or a profile
// delete answers 409 naming `lmm profile list`, and a switch - the way out -
// only marks its target, with the notice on the plan, on the job's event
// stream and on its stored result.
func TestFlowSwitch_WithNoSingleActiveProfileOnlyMarksTheTarget(t *testing.T) {
	s, svc, game := mixedFlowServer(t)
	path := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "default.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "is_default: true\n", "")), 0o644))

	requireUnknown := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		var envelope apiErrorEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		assert.Contains(t, envelope.Error, core.ErrActiveProfileUnknown.Error())
		assert.Contains(t, envelope.Error, "lmm profile list")
	}
	requireUnknown(doAPI(s, http.MethodPost, "/api/v1/plans/deploy?"+gameParam+"="+url.QueryEscape(game.ID)+"&"+profileParam+"=default", ""))
	requireUnknown(doAPI(s, http.MethodDelete, "/api/v1/profiles/alt?"+gameParam+"="+url.QueryEscape(game.ID), ""))

	id, raw := planFlow(t, s, game, "switch", `{"profile":"alt"}`)
	var resp struct {
		Plan core.SwitchPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.True(t, resp.Plan.FlagOnly)
	require.Len(t, resp.Plan.Warnings, 1)
	assert.Contains(t, resp.Plan.Warnings[0], "only marks alt as the active profile")

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.SwitchResult)
	require.True(t, ok)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "alt is now the active profile")
	assert.Contains(t, result.Warnings[0], "`lmm verify`")
	var streamed bool
	replay, _, cancel := j.subscribe(1)
	cancel()
	for _, e := range replay {
		if w, ok := e.(core.WarningEvent); ok && w.Message == result.Warnings[0] {
			streamed = true
		}
	}
	assert.True(t, streamed, "the notice is on the job's event stream")

	active, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, "alt", active.Name)
	assert.FileExists(t, deployedPath(game, altFile), "nothing was removed")
	assert.FileExists(t, deployedFixturePath(game))
}
