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

// launchGamesYAML is a games.yaml with one game, plus block (which may be
// empty) appended to its entry.
func launchGamesYAML(dir, block string) string {
	install := filepath.Join(dir, "game")
	return "games:\n  valheim:\n    name: Valheim\n" +
		"    install_path: \"" + install + "\"\n" +
		"    mod_path: \"" + install + "\"\n" +
		"    sources:\n      thunderstore: valheim\n" + block
}

// TestLoadGamesReadsTheLaunchBlock: #501's `launch:` block, verbatim - the
// "~" in command[0] is expanded when the launch is resolved, not here, so
// a save writes back what the user typed.
func TestLoadGamesReadsTheLaunchBlock(t *testing.T) {
	dir := t.TempDir()
	block := "    launch:\n      via: command\n      steam_app_id: \"892970\"\n      command: [\"~/bin/run.sh\", \"--profile\", \"{profile}\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(launchGamesYAML(dir, block)), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	launch := games["valheim"].Launch
	require.NotNil(t, launch)
	assert.Equal(t, "command", launch.Via)
	assert.Equal(t, "892970", launch.SteamAppID)
	assert.Equal(t, []string{"~/bin/run.sh", "--profile", "{profile}"}, launch.Command)
}

// An unquoted app id is a YAML integer; it still reads as the digits.
func TestLoadGamesReadsAnUnquotedSteamAppID(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(launchGamesYAML(dir, "    launch:\n      steam_app_id: 892970\n")), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.NotNil(t, games["valheim"].Launch)
	assert.Equal(t, "892970", games["valheim"].Launch.SteamAppID)
}

func TestLoadGamesLeavesTheLaunchBlockNilWhenAbsent(t *testing.T) {
	for _, block := range []string{"", "    launch: {}\n"} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(launchGamesYAML(dir, block)), 0o644))

		games, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Nil(t, games["valheim"].Launch, "block %q", block)
	}
}

// Every refusal fails the load loudly (#172's rule), naming the game and
// the key.
func TestLoadGamesRefusesABadLaunchBlock(t *testing.T) {
	cases := map[string]struct{ block, field string }{
		"unknown placeholder": {"    launch:\n      command: [run, \"{gmae_id}\"]\n", "launch.command[1]"},
		"empty program":       {"    launch:\n      command: [\"\", x]\n", "launch.command[0]"},
		"via command, none":   {"    launch:\n      via: command\n", "launch.command"},
		"non-numeric app id":  {"    launch:\n      steam_app_id: abc\n", "launch.steam_app_id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(launchGamesYAML(dir, tc.block)), 0o644))

			_, err := config.LoadGames(dir)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidLaunch)
			assert.Contains(t, err.Error(), `game "valheim"`)
			assert.Contains(t, err.Error(), tc.field)
		})
	}
}

// The block round-trips through SaveGame - with command[0]'s "~" as
// written - and a game without one gains no `launch:` key.
func TestSaveGameRoundTripsTheLaunchBlock(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "game")
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"thunderstore": "valheim"},
		Launch:    &domain.GameLaunch{Via: "command", SteamAppID: "892970", Command: []string{"~/run.sh", "{game_id}"}},
	}))
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "plain", Name: "Plain", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "plain"},
	}))

	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "launch:\n"), "only the configured game writes the block:\n%s", data)
	assert.Contains(t, string(data), "~/run.sh")

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, &domain.GameLaunch{Via: "command", SteamAppID: "892970", Command: []string{"~/run.sh", "{game_id}"}}, games["valheim"].Launch)
	assert.Nil(t, games["plain"].Launch)
}

// A save of ANOTHER game leaves a hand-written launch block - comments,
// flow style and all - byte-identical, and a save of the game itself keeps
// the block it loaded.
func TestSaveGameKeepsAHandWrittenLaunchBlock(t *testing.T) {
	dir := t.TempDir()
	block := "    launch: # run it through the wrapper\n      command: [~/bin/wrap.sh, \"{install_path}\"]\n"
	original := launchGamesYAML(dir, block)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(original), 0o644))

	install := filepath.Join(dir, "game")
	require.NoError(t, config.SaveGame(dir, &domain.Game{
		ID: "other", Name: "Other", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "other"},
	}))
	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), original), "the hand-written game is untouched:\n%s", data)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	game := games["valheim"]
	game.Name = "Valheim (renamed)"
	require.NoError(t, config.SaveGame(dir, game))
	games, err = config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"~/bin/wrap.sh", "{install_path}"}, games["valheim"].Launch.Command)
}
