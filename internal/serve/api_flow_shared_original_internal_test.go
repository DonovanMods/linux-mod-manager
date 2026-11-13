package serve

// #632 through the entry points the SPA drives: ENB and ReShade both ship
// d3d11.dll to the game root, over the game's own copy. The deploy job
// sets the game's file aside once and deploys the load-order winner with
// no claim that the user replaced anything, the conflicts read reports the
// path as an ordinary conflict, the Health read finds lmm's own file there,
// and the purge job puts the game's file back.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowSharedOriginal_TwoModsOverTheGamesOwnFile(t *testing.T) {
	s, svc, game := gameRootFlowFixture(t)
	ctx := context.Background()
	stock := filepath.Join(game.InstallPath, "d3d11.dll")

	reshade := domain.Mod{ID: "m2", SourceID: fixtureSourceID, Name: "ReShade", Version: "1.0", GameID: game.ID}
	require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: reshade.SourceID, ModID: reshade.ID, Version: reshade.Version},
		"d3d11.dll", []byte("reshade d3d11")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: reshade, ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: reshade.SourceID, ModID: reshade.ID, Version: reshade.Version}))

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	for _, w := range append(result.Warnings, result.Notes...) {
		assert.NotContains(t, w, "replaced lmm's link", "lmm never deployed that path")
		assert.NotContains(t, w, "was not replaced")
	}
	assert.Equal(t, "reshade d3d11", readFile(t, stock), "the load-order winner's file is at the game root")

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/conflicts", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var conflicts struct {
		Conflicts []core.ProfileConflict `json:"conflicts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conflicts), rec.Body.String())
	var found bool
	for _, c := range conflicts.Conflicts {
		if c.Path == "d3d11.dll" {
			found = true
			assert.Equal(t, domain.RootInstallPath, c.Root)
			assert.Equal(t, domain.ModKey(fixtureSourceID, "m2"), c.LoadOrderWinner.Key)
		}
	}
	assert.True(t, found, "the shared d3d11.dll is an ordinary conflict: %s", rec.Body.String())

	rec = doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "replaced lmm's link")
	assert.NotContains(t, rec.Body.String(), `"status": "deployed_modified"`)

	j = runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "the game's own", readFile(t, stock), "purge puts the game's own d3d11.dll back")
}
