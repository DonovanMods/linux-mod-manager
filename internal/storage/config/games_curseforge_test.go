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

// wowGamesYAML is a games.yaml with one CurseForge game, plus block (which
// may be empty) appended to its entry.
func wowGamesYAML(dir, block string) string {
	install := filepath.Join(dir, "wow")
	return "games:\n  wow-classic:\n    name: WoW Classic\n" +
		"    install_path: \"" + install + "\"\n" +
		"    mod_path: \"" + install + "\"\n" +
		"    sources:\n      curseforge: \"1\"\n" + block
}

// TestLoadGamesReadsTheCurseForgeBlock: #544's `curseforge:` block names the
// CurseForge gameVersionTypeIds the game accepts.
func TestLoadGamesReadsTheCurseForgeBlock(t *testing.T) {
	dir := t.TempDir()
	yaml := wowGamesYAML(dir, "    curseforge:\n      game_version_types: [67408, 77522]\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	game := games["wow-classic"]
	require.NotNil(t, game.CurseForge)
	assert.Equal(t, []int{67408, 77522}, game.CurseForge.GameVersionTypes)
}

// No block, or an empty list, is no setting at all: today's behaviour.
func TestLoadGamesLeavesTheCurseForgeBlockNilWhenAbsent(t *testing.T) {
	for _, block := range []string{"", "    curseforge: {}\n", "    curseforge:\n      game_version_types: []\n"} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(wowGamesYAML(dir, block)), 0o644))

		games, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Nil(t, games["wow-classic"].CurseForge, "block %q", block)
	}
}

// A type id that is not positive fails the load, naming the game and the
// field - like every other games.yaml value.
func TestLoadGamesRefusesANonPositiveCurseForgeType(t *testing.T) {
	dir := t.TempDir()
	yaml := wowGamesYAML(dir, "    curseforge:\n      game_version_types: [67408, 0]\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	_, err := config.LoadGames(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidCurseForgeVersionType)
	assert.Contains(t, err.Error(), `game "wow-classic"`)
	assert.Contains(t, err.Error(), "curseforge.game_version_types")
}

// The block round-trips through SaveGame, and a game without one gains no
// `curseforge:` key.
func TestSaveGameRoundTripsTheCurseForgeBlock(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "wow")
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "wow-classic", Name: "WoW Classic", InstallPath: install, ModPath: install,
		SourceIDs:  map[string]string{"curseforge": "1"},
		CurseForge: &domain.GameCurseForge{GameVersionTypes: []int{67408}},
	}))
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "plain", Name: "Plain", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "plain"},
	}))

	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "curseforge:\n"), "only the configured game writes the block:\n%s", data)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.NotNil(t, games["wow-classic"].CurseForge)
	assert.Equal(t, []int{67408}, games["wow-classic"].CurseForge.GameVersionTypes)
	assert.Nil(t, games["plain"].CurseForge)
}
