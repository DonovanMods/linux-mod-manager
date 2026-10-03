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

	t.Run("with the loader is 400", func(t *testing.T) {
		s, _ := newMissingModPathServer(t)
		rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
			`{"install_path":`+jsonString(t.TempDir())+`,"loader":{"kind":"bepinex"}}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	})
}
