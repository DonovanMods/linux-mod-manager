package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A BepInEx game deploys into its own install directory, which lmm
// expresses as mod_path set to the SAME absolute path as install_path
// (#358, and the correction on #357: NOT mod_path: "", which the installer
// joins verbatim into a relative path and which the import scanner refuses).
//
// This is the round-trip that proves the shape is expressible with no new
// deploy-rule type at all: a hand-written games.yaml carrying it loads
// unchanged, a save writes it back unchanged, and the value a later run
// reads is the install path itself - not the working directory, and not
// <install_path>/mods.
func TestGameRootModPathRoundTripsThroughGamesYAML(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "steamapps", "common", "Lethal Company")
	require.NoError(t, os.MkdirAll(install, 0o755))

	yaml := "games:\n  lethal-company:\n    name: Lethal Company\n" +
		"    install_path: \"" + install + "\"\n" +
		"    mod_path: \"" + install + "\"\n" +
		"    sources:\n      nexusmods: lethalcompany\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	game := games["lethal-company"]
	require.NotNil(t, game)
	assert.Equal(t, install, game.InstallPath)
	assert.Equal(t, install, game.ModPath, "a game-root mod_path is the install path, verbatim")

	// A save re-marshals the whole file from the loaded map, so this is also
	// what every OTHER command's write leaves behind.
	require.NoError(t, config.SaveGame(dir, game))
	reloaded, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Equal(t, install, reloaded["lethal-company"].ModPath)
	assert.Equal(t, install, reloaded["lethal-company"].InstallPath)
}
