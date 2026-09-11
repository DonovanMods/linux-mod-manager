package steam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// TestKnownGamesFileDeclaresEachAppIDOnce reads the RAW embedded catalog,
// which is the only way this particular mistake can be caught before it
// ships (#409 review F4).
//
// yaml.v3 refuses a document with a duplicated mapping key by failing the
// WHOLE parse, so one app id written twice does not shadow one game - it
// takes LoadKnownGames down, and with it `lmm game detect` and `lmm init`
// for every game in the file. That failure mode is exactly what a curation
// wave invites: two branches each append an entry for a game the other one
// also researched, in different regions of the file, and git merges both
// without a conflict. It happened here - #406 landed Valheim while #409
// was appending its own "892970".
//
// internal/app's TestKnownGamesListIsWellFormed cannot see it: it reads the
// PARSED map, where a duplicate has already become a load error with no
// app id in the message. So the check reads the file, and names the two
// lines.
func TestKnownGamesFileDeclaresEachAppIDOnce(t *testing.T) {
	data, err := defaultSteamGamesFS.ReadFile(defaultSteamGamesPath)
	require.NoError(t, err)

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.Len(t, doc.Content, 1, "the catalog is one YAML document")
	root := doc.Content[0]
	require.Equal(t, yaml.MappingNode, root.Kind, "the catalog is one mapping, app id -> entry")

	firstLine := make(map[string]int, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if line, seen := firstLine[key.Value]; seen {
			t.Errorf("app id %q is defined twice, at line %d and line %d: yaml.v3 refuses a "+
				"duplicate key by failing the whole file, so this would stop `lmm game detect` "+
				"for EVERY game - merge the two entries instead of appending a second one",
				key.Value, line, key.Line)
			continue
		}
		firstLine[key.Value] = key.Line
	}
	require.NotEmpty(t, firstLine, "the catalog must not be empty")
}

// TestLoadKnownGames_ThunderstoreEntries pins #409's seeded communities:
// the design's "T2 seeds the Linux-relevant communities as DATA, not code",
// so `lmm game detect` prefills the community slug and the user never has
// to find it themselves.
//
// Two properties matter more than the list. First, the mapped value is the
// Thunderstore COMMUNITY slug, which is not derivable from the Steam app id
// and is not the lmm game id either (Risk of Rain 2 is `risk-of-rain-2`
// locally and `riskofrain2` there) - which is exactly why it is curated
// rather than guessed. Second, every one of these is a BepInEx game that
// deploys into the GAME ROOT, so mod_path is empty: steam.DetectGames reads
// that as "the install path itself", which is the shape #358's normaliser
// produces paths for.
//
// Valheim and For The King carry a NexusMods page as well, and a sources:
// map REPLACES the {nexusmods: <nexus_id>} derivation - so both spell
// nexusmods: out inside the map, and this pins that they kept it.
func TestLoadKnownGames_ThunderstoreEntries(t *testing.T) {
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)

	for appID, want := range map[string]struct{ slug, community, nexus string }{
		"1966720": {"lethal-company", "lethal-company", ""},
		"892970":  {"valheim", "valheim", "valheim"},
		"632360":  {"risk-of-rain-2", "riskofrain2", ""},
		"3241660": {"repo", "repo", ""},
		"2881650": {"content-warning", "content-warning", ""},
		"527230":  {"for-the-king", "for-the-king", "fortheking"},
	} {
		info, ok := games[appID]
		require.Truef(t, ok, "app %s must be in the shipped catalog", appID)
		assert.Equal(t, want.slug, info.Slug)
		assert.Equal(t, want.community, info.Sources["thunderstore"],
			"app %s maps to the Thunderstore community, not to lmm's own game id", appID)
		assert.Equal(t, want.nexus, info.Sources["nexusmods"],
			"app %s: a sources map replaces the nexus_id derivation, so a game with "+
				"both has to spell nexusmods out inside it", appID)
		assert.Emptyf(t, info.ModPath, "app %s is a game-root (BepInEx) game", appID)
	}
}
