package serve

// #620 through the routes the reorder modal's load-order tab drives: GET
// /api/v1/profiles/{name}/load-orders is `lmm load-order list --json`'s
// document, and POST .../load-orders runs core's load-order Plan/Apply pair
// - a dry run answers the plan and writes nothing, a real one writes the
// profile and the game's modlist.txt. A deploy job and a purge job carry
// the list's outcome. The assertions are on the end state: the list file
// and the profile on disk.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archivesFlowFixture is the flow fixture's game as Cyberpunk 2077 - the
// cyberpunk2077 adapter, its mod_path the install root - with mod m1
// shipping a.archive and b.archive. It returns the list file's path.
func archivesFlowFixture(t *testing.T) (*Server, *core.Service, *domain.Game, string) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	app.RegisterAdapters(svc)
	game.ModPath = game.InstallPath
	game.Adapter = "cyberpunk2077"
	require.NoError(t, svc.SaveGame(context.Background(), game))
	key := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}
	for _, f := range []string{"a.archive", "b.archive"} {
		require.NoError(t, svc.GetGameCache(game).Store(key, "archive/pc/mod/"+f, []byte(f)))
	}
	return s, svc, game, filepath.Join(game.InstallPath, "archive", "pc", "mod", "modlist.txt")
}

func loadOrdersTarget(game *domain.Game) string {
	return scoped("/api/v1/profiles/default/load-orders", game)
}

func TestAPILoadOrders_ReadsTheOrder(t *testing.T) {
	s, _, game, _ := archivesFlowFixture(t)
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodGet, loadOrdersTarget(game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.FileLoadOrdersReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	require.Len(t, report.Orders, 1)
	o := report.Orders[0]
	assert.Equal(t, "cyberpunk2077-archive", o.ID)
	assert.True(t, o.Active)
	assert.False(t, o.Recorded)
	require.Len(t, o.Files, 2)
	assert.Equal(t, "a.archive", o.Files[0].Name)
	assert.True(t, o.Files[0].Managed)
}

// A game without a file load order answers 204: no tab, and no error in
// the browser's console.
func TestAPILoadOrders_NoneIsNoContent(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	rec := doAPI(s, http.MethodGet, loadOrdersTarget(game), "")
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func TestAPILoadOrders_EditsWriteTheProfileAndTheList(t *testing.T) {
	s, svc, game, list := archivesFlowFixture(t)
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodPost, loadOrdersTarget(game), `{"edit":{"op":"move","file":"b.archive","to":"first"},"dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var plan core.FileLoadOrderEditPlan
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plan))
	assert.Contains(t, plan.Changes, "move b.archive to the top")
	assert.NoFileExists(t, list, "a dry run writes nothing")

	rec = doAPI(s, http.MethodPost, loadOrdersTarget(game), `{"edit":{"op":"move","file":"b.archive","to":"first"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res core.FileLoadOrderEditResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.True(t, res.Changed)
	require.NotNil(t, res.List)
	assert.Equal(t, core.PluginListWritten, res.List.Status)
	assert.Equal(t, "b.archive\r\na.archive", readFile(t, list))
	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.FileLoadOrders{"cyberpunk2077-archive": {"b.archive", "a.archive"}}, profile.LoadOrders)

	rec = doAPI(s, http.MethodPost, loadOrdersTarget(game), `{"edit":{"op":"reset"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NoFileExists(t, list, "a reset hands back the list lmm created")
}

func TestAPILoadOrders_Refusals(t *testing.T) {
	s, _, game, _ := archivesFlowFixture(t)
	deployFixtureProfile(t, s, game)
	for body, want := range map[string]int{
		`{"edit":{}}`: http.StatusBadRequest,
		`{"edit":{"op":"move","file":"nope.archive","to":"first"}}`: http.StatusBadRequest,
		`{"edit":{"op":"move","file":"a.archive"}}`:                 http.StatusBadRequest,
		`{"edit":{"id":"cyberpunk2077-redmod","op":"reset"}}`:       http.StatusNotFound,
	} {
		rec := doAPI(s, http.MethodPost, loadOrdersTarget(game), body)
		assert.Equal(t, want, rec.Code, "%s: %s", body, rec.Body.String())
	}
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/profiles/nope/load-orders", game), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// A deploy job and a purge job carry what they did to the list, on the
// wire the SPA reads.
func TestFlowLoadOrders_DeployAndPurge(t *testing.T) {
	s, svc, game, list := archivesFlowFixture(t)
	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	profile.LoadOrders = domain.FileLoadOrders{"cyberpunk2077-archive": {"b.archive"}}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
	require.NoError(t, os.MkdirAll(filepath.Dir(list), 0o755))
	require.NoError(t, os.WriteFile(list, []byte("mine.archive\n"), 0o644))

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	require.Len(t, result.LoadOrders, 1)
	assert.Equal(t, core.PluginListWritten, result.LoadOrders[0].Status)
	// a.archive is not in the order, so it takes its alphabetical place,
	// before b.archive; mine.archive is not on disk, so it is dropped.
	assert.Equal(t, "a.archive\nb.archive\n", readFile(t, list))
	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"load_orders": [`)

	j = runFlow(t, s, game, "purge", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	purged, ok := j.status().Result.(*core.PurgeResult)
	require.True(t, ok, "%T", j.status().Result)
	require.Len(t, purged.LoadOrders, 1)
	assert.Equal(t, core.PluginListRestored, purged.LoadOrders[0].Status)
	assert.Equal(t, "mine.archive\n", readFile(t, list))
}
