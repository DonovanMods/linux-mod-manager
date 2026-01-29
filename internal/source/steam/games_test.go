package steam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadKnownGames_EmbeddedDefault(t *testing.T) {
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, games)
	// Embedded default includes Skyrim SE
	info, ok := games["489830"]
	require.True(t, ok)
	assert.Equal(t, "skyrim-se", info.Slug)
	assert.Equal(t, "Skyrim Special Edition", info.Name)
	assert.Equal(t, "skyrimspecialedition", info.NexusID)
	assert.Equal(t, "Data", info.ModPath)
}

func TestLoadKnownGames_OverrideFile(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "steam-games.yaml")
	overrideYAML := `
"999999":
  slug: test-game
  name: Test Game
  nexus_id: testgame
  mod_path: Mods
`
	require.NoError(t, os.WriteFile(overridePath, []byte(overrideYAML), 0644))

	games, err := LoadKnownGames(dir)
	require.NoError(t, err)
	// Override adds new entry
	info, ok := games["999999"]
	require.True(t, ok)
	assert.Equal(t, "test-game", info.Slug)
	assert.Equal(t, "Test Game", info.Name)
	assert.Equal(t, "testgame", info.NexusID)
	assert.Equal(t, "Mods", info.ModPath)
	// Embedded default still present
	_, ok = games["489830"]
	require.True(t, ok)
}
