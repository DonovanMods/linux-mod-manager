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

// skyrimGamesYAML is a games.yaml with one game, plus block (which may be
// empty) appended to its entry.
func skyrimGamesYAML(dir, block string) string {
	install := filepath.Join(dir, "skyrim")
	return "games:\n  skyrim-se:\n    name: Skyrim SE\n" +
		"    install_path: \"" + install + "\"\n" +
		"    mod_path: \"" + install + "/Data\"\n" +
		"    sources:\n      nexusmods: skyrimspecialedition\n" + block
}

// TestLoadGamesReadsThePrefix: #605's `prefix:` and `prefix_user:`, with
// "~/" expanded in the prefix like every other path in the file.
func TestLoadGamesReadsThePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	yaml := skyrimGamesYAML(dir, "    prefix: ~/Games/Heroic/Prefixes/skyrim\n    prefix_user: dyoung\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	game := games["skyrim-se"]
	assert.Equal(t, filepath.Join(home, "Games/Heroic/Prefixes/skyrim"), game.Prefix)
	assert.Equal(t, "dyoung", game.PrefixUser)
}

// Neither key is no setting at all: today's behaviour.
func TestLoadGamesLeavesThePrefixEmptyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(skyrimGamesYAML(dir, "")), 0o644))
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Empty(t, games["skyrim-se"].Prefix)
	assert.Empty(t, games["skyrim-se"].PrefixUser)
}

// Both keys round-trip through SaveGame, and a game without them gains no
// `prefix:` key - including the game the text-preserving editor leaves
// alone when another game is saved.
func TestSaveGameRoundTripsThePrefix(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "skyrim")
	original := skyrimGamesYAML(dir, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(original), 0o644))

	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "fallout4", Name: "Fallout 4", InstallPath: install, ModPath: install,
		SourceIDs:  map[string]string{"nexusmods": "fallout4"},
		Prefix:     "/pfx/fallout4",
		PrefixUser: "steamuser",
	}))

	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), original), "the other game's text is untouched:\n%s", data)
	assert.Equal(t, 1, strings.Count(string(data), "prefix:"), "only the game that sets one gains the key:\n%s", data)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, "/pfx/fallout4", games["fallout4"].Prefix)
	assert.Equal(t, "steamuser", games["fallout4"].PrefixUser)
	assert.Empty(t, games["skyrim-se"].Prefix)

	// Clearing them removes the keys.
	game := games["fallout4"]
	game.Prefix, game.PrefixUser = "", ""
	require.NoError(t, config.SaveGame(dir, game))
	data, err = os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "prefix")
}
