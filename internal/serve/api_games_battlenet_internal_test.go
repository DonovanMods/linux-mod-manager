package serve

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

// fakeBattleNetWoW makes a Wine prefix under the sandboxed home with one
// World of Warcraft product installed in its flavor folder, and returns
// that folder.
func fakeBattleNetWoW(t *testing.T, product, flavor string) string {
	t.Helper()
	t.Setenv("WINEPREFIX", "")
	prefix := filepath.Join(os.Getenv("HOME"), "Games", "battlenet")
	root := filepath.Join(prefix, "drive_c", "Program Files (x86)", "World of Warcraft")
	install := filepath.Join(root, flavor)
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Interface", "AddOns"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".build.info"),
		[]byte("Branch!STRING:0|Active!DEC:1|Product!STRING:0\nus|1|"+product+"\n"), 0o644))
	return install
}

// #545: GET /api/v1/games/detect lists a Battle.net game - Setup's Add
// Game list - with its detector reference and its prefix.
func TestAPIGamesDetect_ListsABattleNetGame(t *testing.T) {
	s := newGamesServer(t)
	install := fakeBattleNetWoW(t, "wow_classic", "_classic_")

	listing := decodeDetectListing(t, s, "/api/v1/games/detect")
	require.Len(t, listing.Games, 1)
	g := listing.Games[0]
	assert.Equal(t, domain.DetectorBattleNet, g.Detector)
	assert.Equal(t, "wow_classic", g.DetectorID)
	assert.Empty(t, g.SteamAppID)
	assert.Equal(t, install, g.InstallPath)
	assert.Equal(t, filepath.Join(os.Getenv("HOME"), "Games", "battlenet"), g.WinePrefix)
	assert.Equal(t, 1, g.Index)
}

// POST /api/v1/games' from_detected adds a Battle.net game by its
// reference: the catalog's sources and CurseForge flavor, and the launch
// through its prefix, all from the scan.
func TestAPIGameAdd_FromDetectedAddsABattleNetGame(t *testing.T) {
	s := newGamesServer(t)
	install := fakeBattleNetWoW(t, "wow_classic", "_classic_")

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{"from_detected":"battlenet:wow_classic"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "wow-classic", entry.ID)
	assert.Equal(t, install, entry.InstallPath)
	assert.Equal(t, map[string]string{"curseforge": "1"}, entry.SourceIDs)

	game, err := s.svc.GetGame("wow-classic")
	require.NoError(t, err)
	assert.Equal(t, []int{79434}, game.CurseForgeVersionTypes())
	require.NotNil(t, game.Launch)
	assert.Equal(t, &domain.BattleNetLaunch{Prefix: "~/Games/battlenet"}, game.Launch.BattleNet)
}

// A reference nothing detected is the caller's mistake, named by the field
// it came in.
func TestAPIGameAdd_FromDetectedUnknownReference(t *testing.T) {
	s := newGamesServer(t)
	fakeBattleNetWoW(t, "wow_classic", "_classic_")

	for body, field := range map[string]string{
		`{"from_detected":"battlenet:wow"}`:                                 "from_detected",
		`{"from_detected":"nonsense"}`:                                      "from_detected",
		`{"from_detected":"battlenet:wow_classic","from_steam_app_id":"1"}`: "from_detected",
	} {
		rec := doAPI(s, http.MethodPost, "/api/v1/games", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", body, rec.Body.String())
		var env struct {
			Details struct {
				Field string `json:"field"`
			} `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), body)
		assert.Equal(t, field, env.Details.Field, body)
	}
}
