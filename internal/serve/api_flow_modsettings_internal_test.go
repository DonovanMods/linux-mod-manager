package serve

// #646 through the entry points the SPA drives, on a Baldur's Gate 3 game
// whose mod ships a (synthetic) .pak with a module: the deploy job lists
// the module in modsettings.lsx and carries the list's outcome; the
// load-order routes the reorder modal's tab reads and edits answer the
// .pak order; Health reports a list the game reset, and the verify_fix job
// lists the pak again; the purge job puts back the user's list. The
// assertions are on the end state: the list file and the profile on disk.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian/lariantest"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modOneUUID = "0e1e2e3e-4e5e-6e7e-8e9e-aeaebecedeee"

// modSettingsFlowFixture is prefixFlowFixture with m1's pak made a real
// (synthetic) one declaring a module, and the game's mod list as its first
// run writes it. It returns the list's path and its first content.
func modSettingsFlowFixture(t *testing.T) (*Server, *core.Service, *domain.Game, string, []byte) {
	t.Helper()
	s, svc, game, prefix := prefixFlowFixture(t, true)
	key := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(key, deployFixtureFile, lariantest.ModPak("ModOne", "Mod One", modOneUUID)))
	dir := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios", "Baldur's Gate 3", "PlayerProfiles", "Public")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	list := filepath.Join(dir, "modsettings.lsx")
	original := lariantest.ModSettings(lariantest.GustavX)
	require.NoError(t, os.WriteFile(list, original, 0o644))
	return s, svc, game, list, original
}

func TestFlowModSettings_DeployListsThePakAndPurgeRestores(t *testing.T) {
	s, _, game, list, original := modSettingsFlowFixture(t)

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	res, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, "baldursgate3", res.LoadOrders[0].ID)
	assert.Equal(t, core.PluginListWritten, res.LoadOrders[0].Status)
	assert.Contains(t, readFile(t, list), modOneUUID, "the job enabled the pak")

	j = runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, string(original), readFile(t, list), "the purge job puts the user's list back")
}

func TestFlowModSettings_LoadOrderRoutes(t *testing.T) {
	s, svc, game, list, _ := modSettingsFlowFixture(t)
	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	rec := doAPI(s, http.MethodGet, loadOrdersTarget(game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.FileLoadOrdersReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	require.Len(t, report.Orders, 1)
	o := report.Orders[0]
	assert.Equal(t, "baldursgate3", o.ID)
	assert.True(t, o.Enables)
	assert.Equal(t, list, o.ListFile)
	require.Len(t, o.Files, 2)
	assert.Equal(t, "GustavX", o.Files[0].Name)
	assert.False(t, o.Files[0].Managed)
	assert.Equal(t, "one.pak", o.Files[1].Name)
	assert.True(t, o.Files[1].Managed)

	// A move records the order on the profile (one pak: to the end, where it is).
	rec = doAPI(s, http.MethodPost, loadOrdersTarget(game), `{"edit":{"op":"move","file":"one.pak","to":"last"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"one.pak"}, profile.LoadOrders["baldursgate3"])

	// The game's own entry is not the profile's to move: 400.
	rec = doAPI(s, http.MethodPost, loadOrdersTarget(game), `{"edit":{"op":"move","file":"GustavX","to":"last"},"dry_run":true}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// A reset's plan says the paks stay enabled.
	rec = doAPI(s, http.MethodPost, loadOrdersTarget(game), `{"edit":{"op":"reset"},"dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "lmm keeps listing its files in modsettings.lsx")
	assert.Contains(t, readFile(t, list), modOneUUID)
}

func TestFlowModSettings_HealthReportsAResetListAndVerifyFixListsThePak(t *testing.T) {
	s, _, game, list, original := modSettingsFlowFixture(t)
	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	require.NoError(t, os.WriteFile(list, original, 0o644)) // the game resets its list
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status": "load_order_drift"`)
	assert.Contains(t, rec.Body.String(), "one.pak is not in modsettings.lsx")

	j = runFlow(t, s, game, "verify_fix", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Contains(t, readFile(t, list), modOneUUID, "verify_fix listed the pak again")
}
