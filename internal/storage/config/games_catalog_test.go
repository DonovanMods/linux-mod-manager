package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #606 owner request: `catalog:` names the known-games catalog entry a game
// is, by slug. It is read as written: an unknown slug is verify's to report,
// never a load failure, so a hand-edited typo cannot stop lmm starting.
func TestLoadGamesReadsTheCatalogEntry(t *testing.T) {
	dir := t.TempDir()
	yaml := skyrimGamesYAML(dir, "    catalog: no-such-game\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, "no-such-game", games["skyrim-se"].Catalog)
}

// The key round-trips through SaveGame, a game without one gains no key -
// including the game the text-preserving editor leaves alone - and clearing
// it removes the key.
func TestSaveGameRoundTripsTheCatalogEntry(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "skyrim")
	original := skyrimGamesYAML(dir, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(original), 0o644))

	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "gog-skyrim", Name: "Skyrim (GOG)", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"},
		Catalog:   "skyrim-se",
	}))
	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), original), "the other game's text is untouched:\n%s", data)
	assert.Equal(t, 1, strings.Count(string(data), "catalog:"), "only the game that sets one gains the key:\n%s", data)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", games["gog-skyrim"].Catalog)
	assert.Empty(t, games["skyrim-se"].Catalog)

	game := games["gog-skyrim"]
	game.Catalog = ""
	require.NoError(t, config.SaveGame(dir, game))
	data, err = os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "catalog:")
}
