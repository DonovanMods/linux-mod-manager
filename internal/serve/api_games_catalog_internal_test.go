package serve

// #606 owner request: PUT /api/v1/games/{id}'s `catalog` member sets and
// clears which known-games catalog entry a game is - asserted against
// games.yaml - and GET /api/v1/games/known lists the entries the game
// editor offers.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func knownGamesForTest() []core.KnownGame {
	return []core.KnownGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"},
		{Slug: "fallout3", Name: "Fallout 3", SteamAppID: "22380"},
	}
}

func TestAPIGameSources_CatalogSetAndClear(t *testing.T) {
	s := newGameSourcesServer(t)
	s.svc.SetKnownGames(knownGamesForTest)

	entry := putGame(t, s, `{"catalog":"skyrim-se"}`)
	assert.Equal(t, "skyrim-se", entry.Catalog)
	assert.Contains(t, gamesYAML(t, s), "catalog: skyrim-se")

	// A body without the member leaves it alone.
	entry = putGame(t, s, `{"name":"Renamed"}`)
	assert.Equal(t, "skyrim-se", entry.Catalog)

	entry = putGame(t, s, `{"catalog":""}`)
	assert.Empty(t, entry.Catalog)
	assert.NotContains(t, gamesYAML(t, s), "catalog:")
}

// An entry the catalog does not have is a 400 naming the field, and the
// reason names the entries it does have.
func TestAPIGameSources_UnknownCatalogEntryIsRefused(t *testing.T) {
	s := newGameSourcesServer(t)
	s.svc.SetKnownGames(knownGamesForTest)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"catalog":"skyrim-sx"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var envelope struct {
		Error   string `json:"error"`
		Details struct {
			Field string `json:"field"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "catalog", envelope.Details.Field)
	assert.Contains(t, envelope.Error, "fallout3, skyrim-se")
	assert.NotContains(t, gamesYAML(t, s), "catalog:")
}

// GET /api/v1/games/known: the catalog, by name, as core.KnownGame rows;
// an empty array (never null) when no catalog is registered.
func TestAPIKnownGames(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/games/known", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "[]\n", rec.Body.String())

	s.svc.SetKnownGames(knownGamesForTest)
	rec = doAPI(s, http.MethodGet, "/api/v1/games/known", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var rows []core.KnownGame
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows, json.RejectUnknownMembers(true)))
	require.Len(t, rows, 2)
	assert.Equal(t, "fallout3", rows[0].Slug, "sorted by name")
	assert.Equal(t, core.KnownGame{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"}, rows[1])
}
