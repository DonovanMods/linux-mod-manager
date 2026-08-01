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
	// Optional fields absent from this override: must be the zero value, not
	// inherited or defaulted from anywhere.
	assert.Equal(t, "", info.DeployMode)
	assert.Nil(t, info.Sources)
	// Embedded default still present
	_, ok = games["489830"]
	require.True(t, ok)
}

// TestLoadKnownGames_IcarusEntry pins the #177 known-games entry: Icarus has
// no NexusMods presence (nexus_id absent, unlike every other embedded game),
// and needs the two new optional fields (deploy_mode, sources) that #175's
// compile pipeline and games.yaml schema already support.
func TestLoadKnownGames_IcarusEntry(t *testing.T) {
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	info, ok := games["1149460"]
	require.True(t, ok)
	assert.Equal(t, "icarus", info.Slug)
	assert.Equal(t, "Icarus", info.Name)
	assert.Equal(t, "", info.NexusID)
	assert.Equal(t, "Icarus/Content/Paks/mods", info.ModPath)
	assert.Equal(t, "compile", info.DeployMode)
	assert.Equal(t, map[string]string{"icarus": "icarus"}, info.Sources)
}

// TestLoadKnownGames_OverrideFile_DeployModeAndSources pins that the two new
// optional fields round-trip through a user's ~/.config/lmm/steam-games.yaml
// override exactly like every existing field already does — the schema
// extension isn't Icarus-only wiring, any override entry can use it.
func TestLoadKnownGames_OverrideFile_DeployModeAndSources(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "steam-games.yaml")
	overrideYAML := `
"888888":
  slug: custom-compile-game
  name: Custom Compile Game
  mod_path: Mods
  deploy_mode: compile
  sources:
    customsrc: customsrc-id
`
	require.NoError(t, os.WriteFile(overridePath, []byte(overrideYAML), 0644))

	games, err := LoadKnownGames(dir)
	require.NoError(t, err)
	info, ok := games["888888"]
	require.True(t, ok)
	assert.Equal(t, "compile", info.DeployMode)
	assert.Equal(t, map[string]string{"customsrc": "customsrc-id"}, info.Sources)
	assert.Equal(t, "", info.NexusID) // optional, absent in this override too
}
