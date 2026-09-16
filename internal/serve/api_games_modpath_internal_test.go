package serve

// httptest coverage for PUT /api/v1/games/{id}'s `mod_path` member (#427,
// #456): the web half of `lmm game edit --mod-path`, the repair for a
// mod_path that no longer exists. Asserted against the END STATE in
// games.yaml, like the adapter half.

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMissingModPathServer is newGameSourcesServer's game with a mod_path
// nobody created - where a new game starts, since the first deploy creates
// it.
func newMissingModPathServer(t *testing.T) (*Server, *domain.Game) {
	t.Helper()
	s := newGameSourcesServer(t)
	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	moved := *game
	moved.ModPath = filepath.Join(game.InstallPath, "Data")
	require.NoError(t, s.svc.SaveGame(t.Context(), &moved))
	return s, &moved
}

// TestAPIGames_RowsFlagAMissingModPath: the Games rows (GET /api/v1/games)
// and the game page (GET /api/v1/games/{id}) carry the repair - for a
// mod_path lmm deployed into that has since gone, and not for one nobody
// has deployed to yet (#427 review F3).
func TestAPIGames_RowsFlagAMissingModPath(t *testing.T) {
	s, game := newMissingModPathServer(t)

	listed := getGameRows(t, s)
	assert.Empty(t, listed[0].ModPathError, "a game nobody has deployed to is not broken")

	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	deployOneModFile(t, s.svc, game)
	require.NoError(t, os.RemoveAll(game.ModPath))

	listed = getGameRows(t, s)
	assert.Contains(t, listed[0].ModPathError, game.ModPath+" does not exist, but lmm recorded 1 deployed file(s) under it")
	assert.Contains(t, listed[0].ModPathError, "lmm game edit skyrim-se --mod-path")

	rec := doAPI(s, http.MethodGet, "/api/v1/games/skyrim-se", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var detail core.GameDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	assert.Equal(t, listed[0].ModPathError, detail.ModPathError)
}

func getGameRows(t *testing.T, s *Server) []core.GameListEntry {
	t.Helper()
	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var listed []core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed, 1)
	return listed
}

func TestAPIGameSources_ModPathRepairsTheGame(t *testing.T) {
	s, game := newMissingModPathServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"mod_path":`+jsonString(game.InstallPath)+`}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, game.InstallPath, entry.ModPath)
	assert.Empty(t, entry.ModPathError)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs,
		"a body with no sources leaves the map alone")
	assert.Contains(t, gamesYAML(t, s), "mod_path: "+game.InstallPath)
}

// TestAPIGameSources_ModPathThenAdapter pins the order: the mod_path is
// written first, because bepinex is refused off the game root, so one body
// can move a game to its root and onto bepinex.
func TestAPIGameSources_ModPathThenAdapter(t *testing.T) {
	s, game := newMissingModPathServer(t)
	app.RegisterAdapters(s.svc) // what the real binary resolves against

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"mod_path":`+jsonString(game.InstallPath)+`,"adapter":"bepinex"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, game.InstallPath, entry.ModPath)
	assert.Equal(t, "bepinex", entry.Adapter)
}

func TestAPIGameSources_ModPathRefusals(t *testing.T) {
	t.Run("a file is 400 on field mod_path", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		file := filepath.Join(game.InstallPath, "a-file")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"mod_path":`+jsonString(file)+`}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Details core.GameSpecError `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, "mod_path", env.Details.Field)
	})

	t.Run("deployed files are 409 naming the purge", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
		deployOneModFile(t, s.svc, game)

		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"mod_path":`+jsonString(game.InstallPath)+`}`)
		require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Error   string                     `json:"error"`
			Details core.GameModPathInUseError `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, 1, env.Details.DeployedFiles)
		assert.Contains(t, env.Error, "lmm purge --game skyrim-se")

		reloaded, err := s.svc.GetGame("skyrim-se")
		require.NoError(t, err)
		assert.Equal(t, game.ModPath, reloaded.ModPath)
	})

	t.Run("with the loader is 400", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
			`{"mod_path":`+jsonString(game.InstallPath)+`,"loader":{"kind":"bepinex"}}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("an unknown game is 404", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/games/nope", `{"mod_path":`+jsonString(game.InstallPath)+`}`)
		require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	})
}

// deployOneModFile deploys one cached file for game through the real
// installer, so deployed_files holds a row under the game's mod_path.
func deployOneModFile(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	ctx := context.Background()
	mod := &domain.Mod{ID: "m1", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: game.ID}
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, mod.SourceID, mod.ID, mod.Version, "plugin.esp", []byte("x")))
	pm := svc.NewProfileManager()
	_, err := pm.CreateOrResetDefaultAfterGameSave(ctx, game.ID)
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *mod, ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}))
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(game.ModPath, "plugin.esp"))
	require.NoError(t, err, "fixture: the file is deployed")
}
