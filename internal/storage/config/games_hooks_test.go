package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadGames_WithHooks(t *testing.T) {
	tempDir := t.TempDir()

	gamesYAML := `games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
    sources:
      nexusmods: "skyrimspecialedition"
    hooks:
      install:
        before_all: "~/.config/lmm/hooks/backup.sh"
        after_each: "/absolute/path/fnis.sh"
        after_all: "./relative/loot.sh"
      uninstall:
        after_all: "~/.config/lmm/hooks/cleanup.sh"
`
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "games.yaml"), []byte(gamesYAML), 0644))

	games, err := LoadGames(tempDir)
	require.NoError(t, err)

	game := games["skyrim-se"]
	require.NotNil(t, game)

	// Verify hooks are parsed and ~ paths expanded
	assert.NotEmpty(t, game.Hooks.Install.BeforeAll)
	assert.Contains(t, game.Hooks.Install.BeforeAll, "/.config/lmm/hooks/backup.sh")
	assert.Equal(t, "/absolute/path/fnis.sh", game.Hooks.Install.AfterEach)
	assert.Equal(t, "./relative/loot.sh", game.Hooks.Install.AfterAll)
	assert.NotEmpty(t, game.Hooks.Uninstall.AfterAll)
}

func TestLoadGames_NoHooks(t *testing.T) {
	tempDir := t.TempDir()

	gamesYAML := `games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
`
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "games.yaml"), []byte(gamesYAML), 0644))

	games, err := LoadGames(tempDir)
	require.NoError(t, err)

	game := games["skyrim-se"]
	require.NotNil(t, game)
	assert.True(t, game.Hooks.IsEmpty())
}
