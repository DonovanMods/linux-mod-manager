package serve

// #629 through the entry points the SPA drives: on a Bethesda game the
// deploy plan names a game-root file "<game root>/...", the deploy job puts
// it beside the game's executable (preserving the game's own copy first),
// the Health read, the conflicts read and the mod's files read name its
// root, and the purge job puts the game's own file back.

import (
	"context"
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

// gameRootFlowFixture is the deploy fixture made a Skyrim-shaped game: its
// mod path is <install>/Data, its adapter bethesda, and mod m1 also ships a
// d3d11.dll and an enbseries/ folder, which belong at the game root. The
// game ships a d3d11.dll of its own.
func gameRootFlowFixture(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	app.RegisterAdapters(svc)
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "bethesda"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	key := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(key, "d3d11.dll", []byte("enb d3d11")))
	require.NoError(t, svc.GetGameCache(game).Store(key, "enbseries/effect.fx", []byte("shader")))
	require.NoError(t, os.WriteFile(filepath.Join(game.InstallPath, "d3d11.dll"), []byte("the game's own"), 0o644))
	return s, svc, game
}

func TestFlowGameRoot_DeployHealthAndPurge(t *testing.T) {
	s, _, game := gameRootFlowFixture(t)
	stock := filepath.Join(game.InstallPath, "d3d11.dll")

	_, raw := planFlow(t, s, game, "deploy", `{}`)
	assert.Contains(t, string(raw), `"<game root>/d3d11.dll"`)
	assert.Contains(t, string(raw), `"<game root>/enbseries/effect.fx"`)
	assert.Contains(t, string(raw), `"`+deployFixtureFile+`"`)
	assert.NotContains(t, string(raw), `\u0000`)

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "enb d3d11", readFile(t, stock), "deployed beside the game's executable")
	_, err := os.Lstat(filepath.Join(game.ModPath, "d3d11.dll"))
	assert.True(t, os.IsNotExist(err), "not into Data/")

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/files", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"path": "enbseries/effect.fx"`)
	assert.Contains(t, rec.Body.String(), `"root": "install_path"`)

	// The user swaps the ENB shader for their own: Health names its root.
	shader := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	require.NoError(t, os.Remove(shader))
	require.NoError(t, os.WriteFile(shader, []byte("mine"), 0o644))
	rec = doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"file_id": "enbseries/effect.fx"`)
	assert.Contains(t, rec.Body.String(), `"root": "install_path"`)
	assert.Contains(t, rec.Body.String(), "<game root>/enbseries/effect.fx", "the note names the root in prose")
	assert.Contains(t, rec.Body.String(), `"status": "deployed_modified"`)
	assert.NotContains(t, rec.Body.String(), `\u0000`)

	j = runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.PurgeResult)
	require.True(t, ok, "%T", j.status().Result)
	var kept []domain.DeployPath
	for _, k := range result.Kept {
		kept = append(kept, k.DeployPath())
	}
	assert.Equal(t, []domain.DeployPath{domain.GameRootFile("enbseries/effect.fx")}, kept, "the user's file stays, under its root")
	info, err := os.Lstat(stock)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, "the game's own", readFile(t, stock), "purge puts the game's own d3d11.dll back")
	assert.Equal(t, "mine", readFile(t, shader))
}
