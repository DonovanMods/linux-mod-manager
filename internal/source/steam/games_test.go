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

// TestLoadKnownGames_OverrideFile_Loader pins #416's schema half: a curated
// entry may declare the game's mod loader, so `lmm game detect` and `lmm game
// add --from-detected` write the `loader:` block games.yaml already
// round-trips (#359) instead of leaving the very first plugin install to be
// refused.
//
// version is optional, and an UNKNOWN kind is accepted - GameLoader.Kind is
// deliberately an open string (#359), so a catalog naming a loader lmm has
// never heard of simply fires none of lmm's own rules rather than failing the
// whole load.
func TestLoadKnownGames_OverrideFile_Loader(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "steam-games.yaml"), []byte(`
"777777":
  slug: bepinex-game
  name: BepInEx Game
  nexus_id: bepinexgame
  mod_path: ""
  loader:
    kind: bepinex
    version: 5.4.23.5
"777778":
  slug: kindless-game
  name: Kindless Game
  nexus_id: kindlessgame
  mod_path: ""
"777779":
  slug: future-loader-game
  name: Future Loader Game
  nexus_id: futureloadergame
  mod_path: ""
  loader:
    kind: melonloader
`), 0644))

	games, err := LoadKnownGames(dir)
	require.NoError(t, err)

	info, ok := games["777777"]
	require.True(t, ok)
	require.NotNil(t, info.Loader)
	assert.Equal(t, "bepinex", info.Loader.Kind)
	assert.Equal(t, "5.4.23.5", info.Loader.Version)

	kindless, ok := games["777778"]
	require.True(t, ok)
	assert.Nil(t, kindless.Loader, "an entry with no loader: block declares none")

	future, ok := games["777779"]
	require.True(t, ok)
	require.NotNil(t, future.Loader)
	assert.Equal(t, "melonloader", future.Loader.Kind)
	assert.Equal(t, "", future.Loader.Version)
}

// TestLoadKnownGames_Loader_EmptyKindIsRefused: a `loader:` block that names
// no kind declares nothing, and silently loading it would produce a curated
// entry whose declaration fires no rule while looking like it should. It is a
// catalog-authoring mistake, so it fails the load naming the entry.
func TestLoadKnownGames_Loader_EmptyKindIsRefused(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "steam-games.yaml"), []byte(`
"777780":
  slug: broken-game
  name: Broken Game
  nexus_id: brokengame
  mod_path: ""
  loader:
    version: 5.4.23.5
`), 0644))

	_, err := LoadKnownGames(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loader.kind")
	assert.Contains(t, err.Error(), "broken-game")
}
