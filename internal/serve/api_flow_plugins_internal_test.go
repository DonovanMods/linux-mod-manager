package serve

// #616 through the entry points the SPA drives: on a Bethesda game the
// deploy job writes the game's Plugins.txt into its prefix and its result
// carries the plugin list's outcome and findings; the Health read reports
// a plugin lmm deployed that the list has since deactivated; the verify_fix
// job rewrites it; and the purge job puts the user's own list back. A game
// whose prefix lmm cannot find still deploys, and says why the list was not
// written.

import (
	"context"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowPlugin is a minimal TES4 plugin header naming masters.
func flowPlugin(flags uint32, masters ...string) []byte {
	var body []byte
	for _, m := range masters {
		data := append([]byte(m), 0)
		sub := make([]byte, 6, 6+len(data))
		copy(sub, "MAST")
		binary.LittleEndian.PutUint16(sub[4:], uint16(len(data)))
		body = append(body, append(sub, data...)...)
	}
	hdr := make([]byte, 24)
	copy(hdr, "TES4")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(body)))
	binary.LittleEndian.PutUint32(hdr[8:], flags)
	return append(hdr, body...)
}

// pluginsFlowFixture is the deploy fixture made Skyrim SE: mod path
// <install>/Data, the bethesda adapter, an explicit prefix, and mod m1
// shipping Mod.esp, whose master Absent.esm is installed nowhere. The user
// already had a Plugins.txt.
func pluginsFlowFixture(t *testing.T) (*Server, *core.Service, *domain.Game, string) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	app.RegisterAdapters(svc)
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "bethesda"
	game.Catalog = "skyrim-se"
	prefix := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), nil, 0o644))
	game.Prefix = prefix
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	key := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(key, "Mod.esp", flowPlugin(0, "Absent.esm")))
	listDir := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")
	require.NoError(t, os.MkdirAll(listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(listDir, "Plugins.txt"), []byte("# the user's\r\n"), 0o644))
	return s, svc, game, filepath.Join(listDir, "Plugins.txt")
}

func TestFlowPlugins_DeployHealthVerifyFixAndPurge(t *testing.T) {
	s, _, game, list := pluginsFlowFixture(t)

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	require.NotNil(t, result.Plugins)
	assert.Equal(t, core.PluginListWritten, result.Plugins.Status)
	assert.Equal(t, list, result.Plugins.ListFile)
	require.Len(t, result.Plugins.Findings, 1)
	assert.Equal(t, core.PluginFindingMissingMaster, result.Plugins.Findings[0].Code)
	assert.Equal(t, core.PluginSeverityError, result.Plugins.Findings[0].Severity)
	assert.Equal(t, "# the user's\r\n*Mod.esp\r\n", readFile(t, list))

	// The job document the SPA reads carries it on the wire.
	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"plugins": {`)
	assert.Contains(t, rec.Body.String(), `"code": "plugin_missing_master"`)

	// Someone deactivates lmm's plugin: Health says so, as a repairable
	// warning, beside the missing master.
	require.NoError(t, os.WriteFile(list, []byte("# the user's\r\nMod.esp\r\n"), 0o644))
	rec = doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status": "plugin_drift"`)
	assert.Contains(t, rec.Body.String(), "Mod.esp is inactive in Plugins.txt; the profile activates it")
	assert.Contains(t, rec.Body.String(), `"status": "plugin_missing_master"`)

	j = runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "# the user's\r\n*Mod.esp\r\n", readFile(t, list), "the repair rewrites the list")

	j = runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	purged, ok := j.status().Result.(*core.PurgeResult)
	require.True(t, ok, "%T", j.status().Result)
	require.NotNil(t, purged.Plugins)
	assert.Equal(t, core.PluginListRestored, purged.Plugins.Status)
	assert.Equal(t, "# the user's\r\n", readFile(t, list), "the purge puts the user's list back")
}

func TestFlowPlugins_UnresolvedPrefixStillDeploys(t *testing.T) {
	s, svc, game, _ := pluginsFlowFixture(t)
	game.Prefix = filepath.Join(t.TempDir(), "absent")
	require.NoError(t, svc.SaveGame(context.Background(), game))

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	assert.Positive(t, result.Deployed)
	require.NotNil(t, result.Plugins)
	assert.Equal(t, core.PluginListSkipped, result.Plugins.Status)
	require.Len(t, result.Plugins.Findings, 1)
	assert.Equal(t, core.FindingPrefixUnresolved, result.Plugins.Findings[0].Code)
	_, err := os.Lstat(filepath.Join(game.ModPath, "Mod.esp"))
	assert.NoError(t, err, "the mod is deployed")
}
