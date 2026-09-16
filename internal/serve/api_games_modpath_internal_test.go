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

// TestAPIGameSources_ModPathThenAdapter: the body is checked as a whole,
// so it can move a game to its root and onto bepinex - which neither edit
// could do alone.
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
		assert.Equal(t, []core.ProfileDeployedFiles{{Profile: "default", DeployedFiles: 1}}, env.Details.Profiles)
		assert.Contains(t, env.Error, "lmm purge --game skyrim-se --profile default")

		reloaded, err := s.svc.GetGame("skyrim-se")
		require.NoError(t, err)
		assert.Equal(t, game.ModPath, reloaded.ModPath)
	})

	// #445 review F2: with no single active profile, the purges the
	// refusal would name are refused too - so the move is refused with that.
	t.Run("an unknown active profile is 409", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
		deployOneModFile(t, s.svc, game)
		dir := filepath.Join(s.svc.ConfigDir(), "games", "skyrim-se", "profiles")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "second.yaml"), []byte("name: second\n  game_id: skyrim-se\n"), 0o644))

		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"mod_path":`+jsonString(game.InstallPath)+`}`)
		require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		assert.Contains(t, rec.Body.String(), "lmm profile list")
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

// TestAPIGameSources_OffBepInExInOneBody (#427 review F5): the reverse of
// ModPathThenAdapter - a bepinex game moved off its root and onto
// generic-files - in one body, which the fixed mod-path-first order refused.
func TestAPIGameSources_OffBepInExInOneBody(t *testing.T) {
	s, game := newMissingModPathServer(t)
	app.RegisterAdapters(s.svc)
	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"mod_path":`+jsonString(game.InstallPath)+`,"adapter":"bepinex"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	rec = doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"mod_path":"Data"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "--adapter generic-files --mod-path")

	rec = doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"adapter":"generic-files","mod_path":"Data"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, game.ModPath, entry.ModPath)
	assert.Equal(t, "generic-files", entry.Adapter)
}

// TestAPIGameSources_WritesAllOrNothing (#427 review F6): a body whose
// source map is refused leaves the mod_path where it was.
func TestAPIGameSources_WritesAllOrNothing(t *testing.T) {
	s, game := newMissingModPathServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"mod_path":`+jsonString(game.InstallPath)+`,"sources":{"no-such-source":"x"}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	reloaded, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, game.ModPath, reloaded.ModPath)
	assert.NotContains(t, gamesYAML(t, s), "mod_path: "+game.InstallPath+"\n")
}

// TestAPIGameDetectApply_RefusesToMoveAModPathWithFilesDeployed (#427
// review F1): selecting an already-configured row is detection's repair,
// and it rewrites mod_path - so it is refused, 409 as on PUT, while files
// are deployed under the old one, and it writes nothing.
func TestAPIGameDetectApply_RefusesToMoveAModPathWithFilesDeployed(t *testing.T) {
	s := newGamesServer(t)
	install := fakeSteamApp(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	game := &domain.Game{
		ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: install,
		ModPath: filepath.Join(install, "mods"), SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"},
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, s.svc.SaveGame(t.Context(), game))
	deployOneModFile(t, s.svc, game)

	rec := doAPI(s, http.MethodPost, "/api/v1/games/detect", `{"select":["1"]}`)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var env struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "lmm purge --game skyrim-se --profile default")

	reloaded, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, game.ModPath, reloaded.ModPath)
	profile, err := s.svc.NewProfileManager().Get(t.Context(), "skyrim-se", "default")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 1, "the default profile was not reset either")
}

// TestAPIGameDetectApply_AnUnknownActiveProfileIs409 (#445 review F2): the
// same repair, with a profile file lmm cannot read, is refused as on PUT -
// 409, naming `lmm profile list` - rather than naming purges that would be
// refused too.
func TestAPIGameDetectApply_AnUnknownActiveProfileIs409(t *testing.T) {
	s := newGamesServer(t)
	install := fakeSteamApp(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	game := &domain.Game{
		ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: install,
		ModPath: filepath.Join(install, "mods"), SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"},
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, s.svc.SaveGame(t.Context(), game))
	deployOneModFile(t, s.svc, game)
	dir := filepath.Join(s.svc.ConfigDir(), "games", "skyrim-se", "profiles")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "second.yaml"), []byte("name: second\n  game_id: skyrim-se\n"), 0o644))

	rec := doAPI(s, http.MethodPost, "/api/v1/games/detect", `{"select":["1"]}`)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "lmm profile list")
	reloaded, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, game.ModPath, reloaded.ModPath)
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
