package serve

// #544: PUT /api/v1/games/{id}'s `curseforge` member sets, replaces and
// clears the CurseForge game version types a game accepts - asserted against
// games.yaml, the end state, not only the echoed row.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putGame(t *testing.T, s *Server, body string) core.GameListEntry {
	t.Helper()
	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	return entry
}

func TestAPIGameSources_CurseForgeVersionTypesSetAndClear(t *testing.T) {
	s := newGameSourcesServer(t)

	entry := putGame(t, s, `{"curseforge":{"game_version_types":[67408]}}`)
	require.NotNil(t, entry.CurseForge)
	assert.Equal(t, []int{67408}, entry.CurseForge.GameVersionTypes)
	assert.Contains(t, gamesYAML(t, s), "game_version_types:")
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs, "the source map is left alone")

	// A body without the member leaves it alone.
	entry = putGame(t, s, `{"name":"Renamed"}`)
	require.NotNil(t, entry.CurseForge)
	assert.Equal(t, []int{67408}, entry.CurseForge.GameVersionTypes)

	entry = putGame(t, s, `{"curseforge":{"game_version_types":[]}}`)
	assert.Nil(t, entry.CurseForge)
	assert.NotContains(t, gamesYAML(t, s), "curseforge:\n")
}

// A non-positive id is a 400 naming the field, so the editor marks it.
func TestAPIGameSources_CurseForgeVersionTypeMustBePositive(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"curseforge":{"game_version_types":[0]}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var envelope struct {
		Details struct {
			Field string `json:"field"`
			Value string `json:"value"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "curseforge.game_version_types", envelope.Details.Field)
	assert.Equal(t, "0", envelope.Details.Value)
	assert.NotContains(t, gamesYAML(t, s), "game_version_types")
}
