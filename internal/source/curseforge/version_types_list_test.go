package curseforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #563: a game's version types come from GET /v1/games/{gameId}/version-types.
// The fixture is World of Warcraft's (game 1) list as the live API answered
// it on 2026-10-28, in the API's own order.
func versionTypesServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/games/1/version-types", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func wowVersionTypesFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/game_version_types_wow.json")
	require.NoError(t, err)
	return string(data)
}

func TestClient_GetGameVersionTypes(t *testing.T) {
	server := versionTypesServer(t, http.StatusOK, wowVersionTypesFixture(t))
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.SetBaseURL(server.URL)

	types, err := client.GetGameVersionTypes(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, types, 8)
	assert.Equal(t, GameVersionType{ID: 79434, GameID: 1, Name: "WoW Mists of Pandaria Classic", Slug: "wow-mists-of-pandaria-classic"}, types[0])
	assert.Equal(t, GameVersionType{ID: 517, GameID: 1, Name: "WoW Retail", Slug: "game"}, types[2])
}

func TestClient_GetGameVersionTypes_ServerError(t *testing.T) {
	server := versionTypesServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.SetBaseURL(server.URL)

	_, err := client.GetGameVersionTypes(context.Background(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error (status 500)")
}

// The source half: CurseForge is a source.GameVersionTypeLister, and maps
// the API's rows to the source-agnostic shape in the API's order.
func TestCurseForge_ListGameVersionTypes(t *testing.T) {
	server := versionTypesServer(t, http.StatusOK, wowVersionTypesFixture(t))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	var lister source.GameVersionTypeLister = cf
	types, err := lister.ListGameVersionTypes(context.Background(), "1")
	require.NoError(t, err)
	require.Len(t, types, 8)
	assert.Equal(t, source.GameVersionType{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"}, types[1])
	assert.Equal(t, source.GameVersionType{ID: 88568, Name: "WoW Forever", Slug: "wow-forever"}, types[7])
}

// No key is the auth error every other CurseForge call reports, so a
// frontend can offer `lmm auth login curseforge`.
func TestCurseForge_ListGameVersionTypes_NoKeyIsAuthRequired(t *testing.T) {
	server := versionTypesServer(t, http.StatusForbidden, ``)
	defer server.Close()

	cf := New(server.Client(), "")
	cf.client.SetBaseURL(server.URL)

	_, err := cf.ListGameVersionTypes(context.Background(), "1")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
}

// A 404 from the version-types endpoint means CurseForge has no game with
// that id - the game's mapping is wrong, not a mod missing (#563 review).
func TestCurseForge_ListGameVersionTypes_UnknownGameIsTheIdentifier(t *testing.T) {
	server := versionTypesServer(t, http.StatusNotFound, `{"error":"not found"}`)
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	_, err := cf.ListGameVersionTypes(context.Background(), "1")
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	assert.NotErrorIs(t, err, domain.ErrModNotFound)
	assert.Contains(t, err.Error(), "no game with id 1")
	assert.NotContains(t, err.Error(), "mod not found")
}

// A slug CurseForge does not know is the same configuration problem.
func TestCurseForge_ListGameVersionTypes_UnresolvableSlugIsTheIdentifier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/games", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":1,"name":"World of Warcraft","slug":"wow"}],"pagination":{"index":0,"pageSize":50,"resultCount":1,"totalCount":1}}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	_, err := cf.ListGameVersionTypes(context.Background(), "no-such-game")
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	assert.Contains(t, err.Error(), `"no-such-game"`)
}
