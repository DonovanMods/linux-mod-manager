package steam

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// curatedCase pins one embedded known-games entry field for field. The
// curation epic (#406) is a data change, so the test that guards it is a
// data test: a row here is the claim "this app id maps to these facts",
// and the comment above the row in data/steam-games.yaml carries the
// public source the facts were read from.
type curatedCase struct {
	name       string
	appID      string
	slug       string
	gameName   string
	nexusID    string
	modPath    string
	deployMode string
	sources    map[string]string
}

// assertCurated loads the embedded list against a sandboxed config dir - so
// no override file on the machine running the test can reach it - and pins
// every field of each row.
func assertCurated(t *testing.T, cases []curatedCase) {
	t.Helper()
	sandboxEnv(t)
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := games[tc.appID]
			require.True(t, ok, "no known-games entry for Steam app id %s", tc.appID)
			assert.Equal(t, tc.slug, info.Slug)
			assert.Equal(t, tc.gameName, info.Name)
			assert.Equal(t, tc.nexusID, info.NexusID)
			assert.Equal(t, tc.modPath, info.ModPath)
			assert.Equal(t, tc.deployMode, info.DeployMode)
			assert.Equal(t, tc.sources, info.Sources)
		})
	}
}

// sandboxEnv points HOME and every XDG variable this project reads at a
// throwaway directory. Nothing in LoadKnownGames consults them today, but a
// curation test that silently started reading the developer's real config
// (or their real Steam library) would be worse than one that fails, and the
// sandbox costs one line.
func sandboxEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv("XDG_STATE_HOME", home+"/state")
	t.Setenv("STEAM_ROOT", home+"/steam")
}

// TestKnownGames_WorkshopNative pins #406 story S1: the games whose modding
// community publishes through the Steam Workshop rather than NexusMods.
//
// Only one of the three S1 candidates could be curated. Space Engineers
// (244850) loads local mods from %APPDATA%\SpaceEngineers\Mods and Space
// Engineers 2 (1133870) publishes no mod folder at all, so neither has an
// install-relative mod_path to write down - see the epic report. Detection
// still prefills `steamworkshop: <appid>` for both from their appworkshop
// manifests (workshop_prefill_test.go), which is the whole of what lmm can
// honestly claim about them.
func TestKnownGames_WorkshopNative(t *testing.T) {
	assertCurated(t, []curatedCase{
		{
			name:     "human host",
			appID:    "2393970",
			slug:     "human-host",
			gameName: "Human Host",
			modPath:  "BepInEx/plugins",
			sources:  map[string]string{"steamworkshop": "2393970"},
		},
	})
}

// TestKnownGames_NexusHeavy pins #406 story S2: the installed games whose
// modding community is centred on NexusMods. Each row's mod_path is the
// folder that community documents, relative to the Steam install directory
// - the four shapes are a BepInEx plugins folder (Unity/Mono games), a
// game-specific mods folder, an Unreal pak drop folder, and Cyberpunk's
// game root, where a mod archive's own archive/, bin/ and r6/ trees merge
// with the install's.
func TestKnownGames_NexusHeavy(t *testing.T) {
	assertCurated(t, []curatedCase{
		{
			name:     "cyberpunk 2077",
			appID:    "1091500",
			slug:     "cyberpunk2077",
			gameName: "Cyberpunk 2077",
			nexusID:  "cyberpunk2077",
			modPath:  "",
		},
		{
			name:     "valheim",
			appID:    "892970",
			slug:     "valheim",
			gameName: "Valheim",
			nexusID:  "valheim",
			modPath:  "BepInEx/plugins",
		},
		{
			name:     "7 days to die",
			appID:    "251570",
			slug:     "7-days-to-die",
			gameName: "7 Days to Die",
			nexusID:  "7daystodie",
			modPath:  "Mods",
		},
		{
			name:     "grim dawn",
			appID:    "219990",
			slug:     "grim-dawn",
			gameName: "Grim Dawn",
			nexusID:  "grimdawn",
			modPath:  "mods",
		},
		{
			name:     "no man's sky",
			appID:    "275850",
			slug:     "no-mans-sky",
			gameName: "No Man's Sky",
			nexusID:  "nomanssky",
			modPath:  "GAMEDATA/MODS",
		},
		{
			name:     "satisfactory",
			appID:    "526870",
			slug:     "satisfactory",
			gameName: "Satisfactory",
			nexusID:  "satisfactory",
			modPath:  "FactoryGame/Mods",
		},
		{
			name:     "subnautica 2",
			appID:    "1962700",
			slug:     "subnautica-2",
			gameName: "Subnautica 2",
			nexusID:  "subnautica2",
			modPath:  "Subnautica2/Content/Paks/LogicMods",
		},
		{
			name:     "the planet crafter",
			appID:    "1284190",
			slug:     "planet-crafter",
			gameName: "The Planet Crafter",
			nexusID:  "planetcrafter",
			modPath:  "BepInEx/plugins",
		},
		{
			name:     "tainted grail: the fall of avalon",
			appID:    "1466060",
			slug:     "tainted-grail-fall-of-avalon",
			gameName: "Tainted Grail: The Fall of Avalon",
			nexusID:  "taintedgrailthefallofavalon",
			modPath:  "BepInEx/plugins",
		},
	})
}

// TestKnownGames_ValheimCarriesNoLoaderBlock records a deliberate gap, so
// that closing it is a test change rather than an oversight. Valheim's
// whole mod ecosystem sits on BepInEx, and #359 is adding an optional
// `loader:` block to domain.Game for exactly that - but #359 had not merged
// into v2 when S2 landed, and there is no field here to fill. When it does,
// this entry is the first that should grow one, and this test is where the
// note lives.
func TestKnownGames_ValheimCarriesNoLoaderBlock(t *testing.T) {
	sandboxEnv(t)
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	info, ok := games["892970"]
	require.True(t, ok)
	assert.Equal(t, "BepInEx/plugins", info.ModPath,
		"the plugins folder is where a Valheim mod goes once BepInEx is installed; "+
			"installing BepInEx itself is #359's job, not the known-games list's")
}
