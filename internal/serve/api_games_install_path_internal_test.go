package serve

// httptest coverage for PUT /api/v1/games/{id}'s `install_path` member
// (#528): the web half of `lmm game edit --install-path`. core owns the
// move policy (internal/core/game_install_path_test.go); these pin the
// wire - the member, the status codes and the refusal's details - against
// the END STATE in games.yaml.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIGameSources_InstallPathCorrectsTheGame(t *testing.T) {
	s, game := newMissingModPathServer(t)
	fixed := t.TempDir()

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"install_path":`+jsonString(fixed)+`}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, fixed, entry.InstallPath)
	assert.Equal(t, filepath.Join(fixed, "Data"), entry.ModPath, "the mod_path inside it moved with it")
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs,
		"a body with no sources leaves the map alone")
	yaml := gamesYAML(t, s)
	assert.Contains(t, yaml, "install_path: "+fixed)
	assert.NotContains(t, yaml, game.InstallPath)
}

func TestAPIGameSources_InstallPathRefusals(t *testing.T) {
	t.Run("a missing directory is 400 on field install_path", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
			`{"install_path":`+jsonString(filepath.Join(game.InstallPath, "nope"))+`}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Details core.GameSpecError `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, "install_path", env.Details.Field)
	})

	t.Run("deployed files with the old folder still there are 409", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
		deployOneModFile(t, s.svc, game)
		other := t.TempDir()

		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"install_path":`+jsonString(other)+`}`)
		require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Error   string                         `json:"error"`
			Details core.GameInstallPathInUseError `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.True(t, env.Details.OldInstallPathExists)
		assert.Equal(t, 1, env.Details.DeployedFiles)
		assert.Contains(t, env.Error, "lmm purge --game skyrim-se --profile default")

		reloaded, err := s.svc.GetGame("skyrim-se")
		require.NoError(t, err)
		assert.Equal(t, game.InstallPath, reloaded.InstallPath)
	})

	// #527: a refused name beside it, and the install path is not written
	// either.
	t.Run("with a refused name writes neither", func(t *testing.T) {
		s, game := newMissingModPathServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
			`{"install_path":`+jsonString(t.TempDir())+`,"name":"  "}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Details core.GameSpecError `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, "name", env.Details.Field)
		reloaded, err := s.svc.GetGame("skyrim-se")
		require.NoError(t, err)
		assert.Equal(t, game.InstallPath, reloaded.InstallPath)
	})
}

// TestAPIGameDetectApply_RefusesAnInstallPathRepairAsAFinding (#528): the
// detect apply's repair of a configured game found at a new install path,
// while its old folder - with files deployed in it - still exists, is
// refused with the edit's own typed error, as a per-game finding in the
// partial result's "refused"; 409, and nothing written for that game.
func TestAPIGameDetectApply_RefusesAnInstallPathRepairAsAFinding(t *testing.T) {
	s := newGamesServer(t)
	install := fakeSteamApp(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	old := t.TempDir()
	game := &domain.Game{
		ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: old,
		ModPath: filepath.Join(old, "Data"), SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"},
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, s.svc.SaveGame(t.Context(), game))
	deployOneModFile(t, s.svc, game)

	rec := doAPI(s, http.MethodPost, "/api/v1/games/detect", `{"select":["skyrim-se"]}`)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var env struct {
		Error   string                `json:"error"`
		Details core.GameDetectResult `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "not repairing skyrim-se")
	require.Len(t, env.Details.Refused, 1)
	assert.Equal(t, "skyrim-se", env.Details.Refused[0].GameID)
	details, ok := env.Details.Refused[0].Details.(map[string]any)
	require.True(t, ok, "details: %#v", env.Details.Refused[0].Details)
	assert.Equal(t, install, details["new_install_path"])
	assert.Equal(t, true, details["old_install_path_exists"])

	reloaded, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, old, reloaded.InstallPath)
}
