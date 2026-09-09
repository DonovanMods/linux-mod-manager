package serve

// httptest coverage for PUT /api/v1/games/{id} - the source<->game
// mapping the epic live review's C-4 found unreachable from either
// frontend. Package-internal for the same reason as its siblings: the
// fixtures build the *Server directly, so doAPI can carry the process
// CSRF token.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGameSourcesServer is newGamesServer with one game already configured,
// mapping "nexusmods" - the state an edit starts from.
func newGameSourcesServer(t *testing.T) *Server {
	t.Helper()
	s := newGamesServer(t)
	require.NoError(t, s.svc.SaveGame(t.Context(), &domain.Game{
		ID:          "skyrim-se",
		Name:        "Skyrim Special Edition",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		SourceIDs:   map[string]string{"nexusmods": "skyrimspecialedition"},
	}))
	return s
}

// TestAPIGameSources_ReplacesTheMapAndAnswersWithTheRow is the happy path:
// the response is the frozen core.GameListEntry document (the same row GET
// /api/v1/games and `lmm game list --json` carry), and games.yaml moved.
func TestAPIGameSources_ReplacesTheMapAndAnswersWithTheRow(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"sources":{"nexusmods":"skyrimspecialedition","curseforge":"1704"}}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition", "curseforge": "1704"}, entry.SourceIDs)

	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, entry.SourceIDs, game.SourceIDs, "the write must have landed, not just been echoed")
}

// TestAPIGameSources_IsAReplacementNotAMerge: an omitted id is REMOVED,
// which is the only way a frontend can express "stop using this source".
func TestAPIGameSources_IsAReplacementNotAMerge(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"sources":{"curseforge":"1704"}}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"curseforge": "1704"}, game.SourceIDs)
}

// TestAPIGameSources_UnregisteredSourceIs400WithItsField pins the 400 the
// Setup form marks its own input from: details carry {field, value,
// reason} with field "sources", and nothing is written.
func TestAPIGameSources_UnregisteredSourceIs400WithItsField(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"sources":{"local-mods":"skyrim"}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Error   string             `json:"error"`
		Details core.GameSpecError `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "sources", env.Details.Field)
	assert.Equal(t, "local-mods", env.Details.Value)
	assert.NotEmpty(t, env.Details.Reason)

	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, game.SourceIDs)
}

// TestAPIGameSources_EmptyMapIs400: removing the last source would leave a
// game that can neither search nor install.
func TestAPIGameSources_EmptyMapIs400(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"sources":{}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIGameSources_UnknownGameIs404 - the treatment every other
// game-named route gives a name that resolves to nothing.
func TestAPIGameSources_UnknownGameIs404(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/nope", `{"sources":{"nexusmods":"x"}}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIGameSources_RejectsUnknownMembers pins the strict decode every
// mutation body in this package gets.
func TestAPIGameSources_RejectsUnknownMembers(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"sources":{"nexusmods":"x"},"source":{"typo":"y"}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIGameSources_RequiresCSRF - a state-changing route like every
// other.
func TestAPIGameSources_RequiresCSRF(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"sources":{"nexusmods":"x"}}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAPIGames_RowsCarryTheSourcesMap pins what the Setup page's Games
// table renders the "sources" column from: the listing already carries it
// (domain.Game.SourceIDs is part of the frozen row), so no additive field
// was needed for C-4's read half.
func TestAPIGames_RowsCarryTheSourcesMap(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"source_ids"`)

	var entries []core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entries, json.RejectUnknownMembers(true)))
	require.Len(t, entries, 1)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entries[0].SourceIDs)
}
