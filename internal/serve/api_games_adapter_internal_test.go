package serve

// httptest coverage for the game-ADAPTER halves of the two game mutation
// surfaces (#353/#411, I6): `adapter` on POST /api/v1/games and the
// pointer-valued `adapter` on PUT /api/v1/games/{id}.
//
// Both shipped with no test of any kind: their request types already had
// goldens and both new members are omitempty, so the JSON wire-contract
// ratchet stayed green without pinning either. The pointer contract
// (absent never clears, explicit "" does), the "adapter first, then
// sources" ordering and the `sources: null` early return are all decisions
// a caller depends on, so they are asserted here against the END STATE in
// games.yaml rather than against the echoed document.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gamesYAML is the file the write must actually reach: GetGame answers from
// the in-memory map, which would be equally happy with a write that never
// landed on disk.
func gamesYAML(t *testing.T, s *Server) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(s.svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)
	return string(body)
}

// TestAPIGameAdd_AdapterIsWrittenAndReadBack pins POST /api/v1/games'
// `adapter` member end to end: games.yaml carries the key, the answering
// row carries it, and GET /api/v1/games reads it back.
func TestAPIGameAdd_AdapterIsWrittenAndReadBack(t *testing.T) {
	s := newGamesServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{
		"source_id": "nexusmods",
		"identifier": "skyrimspecialedition",
		"name": "Skyrim Special Edition",
		"adapter": "generic-files",
		"install_path": `+jsonString(t.TempDir())+`
	}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "generic-files", entry.Adapter)

	assert.Contains(t, gamesYAML(t, s), "adapter: generic-files")

	rec = doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var listed []core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, "generic-files", listed[0].Adapter)
}

// TestAPIGameAdd_AbsentAdapterWritesNoKey: the identity is what a game with
// no `adapter:` key already means, so an add that does not name one must
// not start writing the key into everyone's games.yaml.
func TestAPIGameAdd_AbsentAdapterWritesNoKey(t *testing.T) {
	s := newGamesServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{
		"source_id": "nexusmods", "identifier": "skyrimspecialedition",
		"name": "Skyrim Special Edition", "install_path": `+jsonString(t.TempDir())+`
	}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Empty(t, entry.Adapter)
	assert.NotContains(t, gamesYAML(t, s), "adapter:")
}

// TestAPIGameAdd_UnregisteredAdapterIs400NamingTheRegisteredSet pins the
// refusal the SPA marks its input from: field "adapter", and the message
// names what this build does ship.
func TestAPIGameAdd_UnregisteredAdapterIs400NamingTheRegisteredSet(t *testing.T) {
	s := newGamesServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{
		"source_id": "nexusmods", "identifier": "skyrimspecialedition",
		"name": "Skyrim Special Edition", "adapter": "melonloader",
		"install_path": `+jsonString(t.TempDir())+`
	}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Error   string             `json:"error"`
		Details core.GameSpecError `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "adapter", env.Details.Field)
	assert.Equal(t, "melonloader", env.Details.Value)
	assert.Contains(t, env.Details.Reason, "generic-files", "the refusal must name the registered set")

	games, err := s.svc.ListGameEntries(t.Context())
	require.NoError(t, err)
	assert.Empty(t, games, "no games.yaml row for a game whose adapter was refused")
}

// newGameAdapterServer is newGameSourcesServer with the adapter already
// set, so a PUT can be seen to leave it, change it, or clear it.
func newGameAdapterServer(t *testing.T) *Server {
	t.Helper()
	s := newGameSourcesServer(t)
	_, err := s.svc.SetGameAdapter(t.Context(), "skyrim-se", "generic-files")
	require.NoError(t, err)
	return s
}

// TestAPIGameSources_AbsentAdapterLeavesIt is the pointer contract's whole
// point: every caller before #353 sends only a source map, and none of
// them may clear an adapter they never knew about.
func TestAPIGameSources_AbsentAdapterLeavesIt(t *testing.T) {
	s := newGameAdapterServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"sources":{"curseforge":"1704"}}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "generic-files", entry.Adapter)
	assert.Equal(t, map[string]string{"curseforge": "1704"}, entry.SourceIDs)
	assert.Contains(t, gamesYAML(t, s), "adapter: generic-files")
}

// TestAPIGameSources_ExplicitEmptyAdapterClearsIt is the other half: an
// explicit "" removes the key, which is `lmm game edit --adapter ""`.
func TestAPIGameSources_ExplicitEmptyAdapterClearsIt(t *testing.T) {
	s := newGameAdapterServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"adapter":""}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Empty(t, entry.Adapter)
	assert.NotContains(t, gamesYAML(t, s), "adapter:")

	// `sources: null` is the early return: the map must be untouched, not
	// treated as a replacement that removes every source.
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs)
	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, game.SourceIDs)
}

// TestAPIGameSources_AdapterOnlyLeavesTheSourceMap pins the same early
// return for a body that names a real adapter.
func TestAPIGameSources_AdapterOnlyLeavesTheSourceMap(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"adapter":"generic-files"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "generic-files", entry.Adapter)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs)
	assert.Contains(t, gamesYAML(t, s), "adapter: generic-files")
}

// TestAPIGameSources_AdapterAndSourcesApplyBoth pins the documented
// ordering: the adapter is its own gated write, taken FIRST, and a body
// carrying both leaves both applied.
func TestAPIGameSources_AdapterAndSourcesApplyBoth(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"adapter":"generic-files","sources":{"curseforge":"1704"}}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "generic-files", entry.Adapter)
	assert.Equal(t, map[string]string{"curseforge": "1704"}, entry.SourceIDs)

	yaml := gamesYAML(t, s)
	assert.Contains(t, yaml, "adapter: generic-files")
	assert.Contains(t, yaml, "curseforge")
}

// TestAPIGameSources_UnregisteredAdapterIs400NamingTheRegisteredSet: the
// refusal names the field the SPA marks and the set this build ships, and
// the source map is left alone because the adapter is written first.
func TestAPIGameSources_UnregisteredAdapterIs400NamingTheRegisteredSet(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se",
		`{"adapter":"melonloader","sources":{"curseforge":"1704"}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Error   string             `json:"error"`
		Details core.GameSpecError `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "adapter", env.Details.Field)
	assert.Equal(t, "melonloader", env.Details.Value)
	assert.Contains(t, env.Details.Reason, "generic-files")

	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Empty(t, game.Adapter)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"},
		game.SourceIDs, "the source map must not move when the adapter is refused")
}

// TestAPIGameSources_CompileGameRefusesANonCompilingAdapter pins the
// composition rule over the wire: `deploy_mode: compile` plus an EXPLICIT
// adapter that cannot compile is refused by name, naming both keys.
func TestAPIGameSources_CompileGameRefusesANonCompilingAdapter(t *testing.T) {
	s := newGameSourcesServer(t)
	game, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	game.DeployMode = domain.DeployCompile
	require.NoError(t, s.svc.SaveGame(t.Context(), game))

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"adapter":"generic-files"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, strings.ToLower(env.Error), "compile")
	assert.Contains(t, env.Error, "generic-files")

	after, err := s.svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Empty(t, after.Adapter, "a refused adapter must not be written")
}
