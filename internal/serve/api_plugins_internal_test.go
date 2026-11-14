package serve

// #617 through the routes the reorder modal's Plugins tab drives: GET
// /api/v1/profiles/{name}/plugins is `lmm plugins list --json`'s document,
// and POST .../plugins runs core's plugin-edit Plan/Apply pair - a dry run
// answers the plan and writes nothing, a real one writes the profile and
// the game's Plugins.txt. The assertions are on the end state: the list
// file and the profile on disk.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginsTabFixture is pluginsFlowFixture with a second plugin, Other.esp,
// in mod m1, deployed.
func pluginsTabFixture(t *testing.T) (*Server, *core.Service, *domain.Game, string) {
	t.Helper()
	s, svc, game, list := pluginsFlowFixture(t)
	key := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(key, "Other.esp", flowPlugin(0)))
	deployFixtureProfile(t, s, game)
	require.Equal(t, "# the user's\r\n*Mod.esp\r\n*Other.esp\r\n", readFile(t, list))
	return s, svc, game, list
}

func pluginsTarget(game *domain.Game) string {
	return scoped("/api/v1/profiles/default/plugins", game)
}

func TestAPIPlugins_ReadsTheEffectiveOrder(t *testing.T) {
	s, _, game, list := pluginsTabFixture(t)

	rec := doAPI(s, http.MethodGet, pluginsTarget(game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.PluginOrderReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	assert.True(t, report.Active)
	assert.Equal(t, list, report.ListFile)
	require.Len(t, report.Plugins, 2)
	assert.Equal(t, "Mod.esp", report.Plugins[0].Name)
	assert.Equal(t, []string{"Absent.esm"}, report.Plugins[0].MissingMasters)
	assert.Equal(t, core.PluginPlacementNew, report.Plugins[0].Placement)
	require.NotEmpty(t, report.Findings)
	assert.Equal(t, core.PluginFindingMissingMaster, report.Findings[0].Code)
}

func TestAPIPlugins_EditsWriteTheProfileAndTheList(t *testing.T) {
	s, svc, game, list := pluginsTabFixture(t)

	// A dry run answers the plan and writes nothing.
	rec := doAPI(s, http.MethodPost, pluginsTarget(game), `{"edit":{"op":"disable","plugins":["Mod.esp"]},"dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var plan core.PluginEditPlan
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plan))
	assert.Equal(t, []string{"Mod.esp: disabled"}, plan.Changes)
	assert.Equal(t, "# the user's\r\n*Mod.esp\r\n*Other.esp\r\n", readFile(t, list))

	rec = doAPI(s, http.MethodPost, pluginsTarget(game), `{"edit":{"op":"disable","plugins":["Mod.esp"]}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res core.PluginEditResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.True(t, res.Changed)
	require.NotNil(t, res.List)
	assert.Equal(t, core.PluginListWritten, res.List.Status)
	assert.Equal(t, "# the user's\r\nMod.esp\r\n*Other.esp\r\n", readFile(t, list))

	rec = doAPI(s, http.MethodPost, pluginsTarget(game), `{"edit":{"op":"move","plugin":"Other.esp","to":"first"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "# the user's\r\n*Other.esp\r\nMod.esp\r\n", readFile(t, list))

	rec = doAPI(s, http.MethodPost, pluginsTarget(game), `{"edit":{"op":"rule_add","rule":{"name":"Other.esp","after":["Mod.esp"]}}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "# the user's\r\nMod.esp\r\n*Other.esp\r\n", readFile(t, list), "the rule holds Other.esp after Mod.esp")

	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []domain.PluginEntry{{Name: "Other.esp", Enabled: true}, {Name: "Mod.esp", Enabled: false}}, profile.Plugins.Order)
	assert.Equal(t, []domain.PluginRule{{Name: "Other.esp", After: []string{"Mod.esp"}}}, profile.Plugins.Rules.Plugins)

	// A rule that would close a cycle is a conflict, with the cycle in the
	// envelope's details, and nothing changes.
	rec = doAPI(s, http.MethodPost, pluginsTarget(game), `{"edit":{"op":"rule_add","rule":{"name":"Mod.esp","after":["Other.esp"]}}}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var env apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "the plugin order has a cycle")
	assert.Contains(t, rec.Body.String(), `"cycle"`)
	assert.Equal(t, "# the user's\r\nMod.esp\r\n*Other.esp\r\n", readFile(t, list))
}

func TestAPIPlugins_Refusals(t *testing.T) {
	s, _, game, _ := pluginsTabFixture(t)
	for _, tc := range []struct {
		name, method, target, body string
		status                     int
	}{
		{"unknown plugin", http.MethodPost, pluginsTarget(game), `{"edit":{"op":"enable","plugins":["Nope.esp"]}}`, http.StatusBadRequest},
		{"unknown op", http.MethodPost, pluginsTarget(game), `{"edit":{"op":"shuffle"}}`, http.StatusBadRequest},
		{"no op", http.MethodPost, pluginsTarget(game), `{"edit":{}}`, http.StatusBadRequest},
		{"unknown member", http.MethodPost, pluginsTarget(game), `{"edit":{"op":"sort"},"nope":1}`, http.StatusBadRequest},
		{"game's own plugin", http.MethodPost, pluginsTarget(game), `{"edit":{"op":"move","plugin":"Mod.esp","before":"Skyrim.esm"}}`, http.StatusBadRequest},
		{"unknown profile", http.MethodGet, scoped("/api/v1/profiles/ghost/plugins", game), "", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doAPI(s, tc.method, tc.target, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error)
		})
	}
}

func TestAPIPlugins_AGameWithNoPluginListIsNotFound(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	rec := doAPI(s, http.MethodGet, pluginsTarget(game), "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "this game has no plugin list")
}
