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
			modPath:  "", // the game root: #358 normalises a plugin archive to a BepInEx/-rooted layout
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
			modPath:  "", // the game root: #358 normalises a plugin archive to a BepInEx/-rooted layout
			// A sources map replaces the nexus_id derivation, so the
			// Thunderstore community (#409) arrives alongside an explicit
			// nexusmods entry rather than in place of one.
			sources: map[string]string{"nexusmods": "valheim", "thunderstore": "valheim"},
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
			modPath:  "", // the game root: #358 normalises a plugin archive to a BepInEx/-rooted layout
		},
		{
			name:     "tainted grail: the fall of avalon",
			appID:    "1466060",
			slug:     "tainted-grail-fall-of-avalon",
			gameName: "Tainted Grail: The Fall of Avalon",
			nexusID:  "taintedgrailthefallofavalon",
			modPath:  "", // the game root: #358 normalises a plugin archive to a BepInEx/-rooted layout
		},
	})
}

// bepinexApps is the curated catalog's complete BepInEx set: the games
// whose entire mod ecosystem is BepInEx plugins, which is why each one's
// mod_path is the install root (#358's normaliser rewrites a plugin archive
// to a BepInEx/-rooted layout under it) and each one declares the loader
// (#416) so the FIRST plugin install is not refused by #359's precondition.
//
// The last four arrived with #409's Thunderstore communities and follow the
// same rule rather than a second one: a curated BepInEx game declares the
// loader. Declaring it is not a claim that BepInEx is INSTALLED in this copy
// - `lmm game show` reports that, and verify flags a declared-but-absent
// loader - so the user without it is told rather than silently deployed to.
var bepinexApps = map[string]string{
	"892970":  "valheim",
	"1284190": "planet-crafter",
	"1466060": "tainted-grail-fall-of-avalon",
	"527230":  "for-the-king",
	"2393970": "human-host",
	"1966720": "lethal-company",
	"632360":  "risk-of-rain-2",
	"3241660": "repo",
	"2881650": "content-warning",
}

// TestKnownGames_OnlyTheBepInExEntriesDeclareALoader pins #416's data half
// over the WHOLE shipped catalog, in both directions: exactly the entries in
// bepinexApps declare `loader: {kind: bepinex}`, and every other entry
// declares nothing at all.
//
// It replaces two deliberate gap markers, one from each branch that merged
// here - TestKnownGames_ValheimCarriesNoLoaderBlock (#406: "no field exists
// to fill yet, fail when one does") and
// TestKnownGames_NoShippedEntryDeclaresALoaderYet (#416's schema half: "the
// field exists, no entry uses it yet"). Both existed to force this decision
// and both are now answered, so they are replaced by the positive claim
// rather than deleted.
//
// The "every other entry declares nothing" half is the load-bearing one.
// Nothing on disk says which loader a game wants, so a declaration is a
// research claim about a particular game, and a wrong one makes lmm refuse
// a perfectly good non-BepInEx mod for it (#359's precondition) or report a
// missing loader at verify time. A new curated entry therefore has to come
// here and say so.
//
// No VERSION is declared. A catalog shipped inside the binary cannot know
// which BepInEx pack a user installed, and a version here would be compared
// against theirs at verify time - so pinning one would turn "you are on a
// newer pack than lmm was built against" into a reported problem. Version
// stays a per-installation fact, which is what `lmm game show` reads off the
// game directory.
func TestKnownGames_OnlyTheBepInExEntriesDeclareALoader(t *testing.T) {
	sandboxEnv(t)
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)

	declared := make(map[string]string)
	for appID, info := range games {
		if info.Loader == nil {
			continue
		}
		declared[appID] = info.Slug
		assert.Equal(t, "bepinex", info.Loader.Kind,
			"app %s (%s) declares loader kind %q; bepinex is the only kind the catalog curates today",
			appID, info.Slug, info.Loader.Kind)
		assert.Equal(t, "", info.Loader.Version,
			"app %s (%s) pins a loader version; the installed version is a fact about the "+
				"user's copy, not about the game, so verify would compare theirs against ours",
			appID, info.Slug)
		assert.Equal(t, "", info.ModPath,
			"app %s (%s) declares BepInEx, so its mod root must be the install root (#358): "+
				"the loader's own tree - plugins, patchers, config - hangs off BepInEx/ under it",
			appID, info.Slug)
	}
	assert.Equal(t, bepinexApps, declared,
		"the set of entries declaring a loader has changed; a declaration is a research claim "+
			"about one game, so add or remove the app id here with the reason in its "+
			"data/steam-games.yaml comment")
}

// TestKnownGames_LongTail pins #406 story S3: the rest of the installed
// games. Five of the seven are Unreal titles whose mods are pak bundles
// dropped in the engine's `~mods` overlay folder (the tilde is load-order
// significant to Unreal and is NOT a typo); Halo and LEGO Batman put that
// folder under their own project directory rather than the install root,
// which is why their paths carry a project segment.
//
// Two of the S3 candidates are not here. The Elder Scrolls Online keeps
// add-ons in the user's Documents folder, not under the install, so there
// is no install-relative mod_path to write down; Satisfactory Modeler is a
// factory-planning tool, not a moddable game. Both stay detect-only and
// both are in the epic report with the reason.
func TestKnownGames_LongTail(t *testing.T) {
	assertCurated(t, []curatedCase{
		{
			name:     "starrupture",
			appID:    "1631270",
			slug:     "starrupture",
			gameName: "StarRupture",
			nexusID:  "starrupture",
			modPath:  "StarRupture/Content/Paks/~mods",
		},
		{
			name:     "windrose",
			appID:    "3041230",
			slug:     "windrose",
			gameName: "Windrose",
			nexusID:  "windrose",
			modPath:  "R5/Content/Paks/~mods",
		},
		{
			name:     "cubic odyssey",
			appID:    "3400000",
			slug:     "cubic-odyssey",
			gameName: "Cubic Odyssey",
			nexusID:  "cubicodyssey",
			modPath:  "modded/data",
		},
		{
			name:     "the blood of dawnwalker",
			appID:    "3751260",
			slug:     "blood-of-dawnwalker",
			gameName: "The Blood of Dawnwalker",
			nexusID:  "thebloodofdawnwalker",
			modPath:  "Dawnwalker/Content/Paks/~mods",
		},
		{
			name:     "halo: campaign evolved",
			appID:    "2806050",
			slug:     "halo-campaign-evolved",
			gameName: "Halo: Campaign Evolved",
			nexusID:  "halocampaignevolved",
			modPath:  "Meteorite/Content/Paks/~mods",
		},
		{
			name:     "for the king",
			appID:    "527230",
			slug:     "for-the-king",
			gameName: "For The King",
			nexusID:  "fortheking",
			modPath:  "", // the game root: #358 normalises a plugin archive to a BepInEx/-rooted layout
			sources:  map[string]string{"nexusmods": "fortheking", "thunderstore": "for-the-king"},
		},
		{
			name:     "lego batman: legacy of the dark knight",
			appID:    "2215200",
			slug:     "lego-batman-lotdk",
			gameName: "LEGO Batman: Legacy of the Dark Knight",
			nexusID:  "legobatmanlegacyofthedarkknight",
			modPath:  "LEGOBatmanLotDK/Content/Paks/~mods",
		},
	})
}

// TestKnownGames_DetectOnlyStayUncurated is the other half of the epic's
// acceptance rule, and the reason it is a test rather than a line in a
// report: "we deliberately did not curate this" and "we forgot" look
// identical in a data file. A later contributor who adds one of these has
// to delete its row here, which is where they meet the reason.
func TestKnownGames_DetectOnlyStayUncurated(t *testing.T) {
	sandboxEnv(t)
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)

	for _, tc := range []struct{ appID, name, why string }{
		{"244850", "Space Engineers",
			"local mods load from %APPDATA%\\SpaceEngineers\\Mods, outside the install directory"},
		{"1133870", "Space Engineers 2",
			"the VRAGE3 Mod HUB publishes no mod folder"},
		{"306130", "The Elder Scrolls Online",
			"add-ons live in the user's Documents folder, outside the install directory"},
		{"3187030", "Satisfactory Modeler",
			"a factory-planning tool, not a moddable game"},
	} {
		_, ok := games[tc.appID]
		assert.Falsef(t, ok, "%s (%s) is curated, but it was left detect-only because %s",
			tc.name, tc.appID, tc.why)
	}
}

// TestKnownGames_EmptyModPathMeansTheInstallRoot pins what `mod_path: ""`
// resolves to, because five #406 entries now depend on it (the BepInEx
// games, #358) and Cyberpunk 2077 already did. The join lives in
// DetectGames (steam.go): a curated entry's mod_path is joined onto the
// install path, and an EMPTY one is the install path itself - which
// core.GameFromDetected then writes to games.yaml verbatim as the game's
// ModPath. Spelled as its own test so that changing the empty case into a
// <install>/mods default (the rule for an UNCURATED row, applied by
// core.GameSpecFromDetected) fails here rather than silently deploying a
// BepInEx archive one level too deep.
//
// It asserts THROUGH DetectGames against a fabricated Steam library, not
// against a copy of the join pasted into the test: the earlier spelling did
// the resolution itself and compared the result to itself, so mutating
// steam.go's `modPath := installPath` to a subdirectory left it green.
func TestKnownGames_EmptyModPathMeansTheInstallRoot(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	configDir := t.TempDir()

	games, err := LoadKnownGames(configDir)
	require.NoError(t, err)

	// One install directory per app, as Steam really lays them out.
	gameRootApps := []string{
		"892970", "1284190", "1466060", "527230", "2393970", "1091500",
		"1966720", "632360", "3241660", "2881650", // #409's Thunderstore communities
	}
	installs := make(map[string]string, len(gameRootApps))
	for _, appID := range gameRootApps {
		info, ok := games[appID]
		require.True(t, ok, "no known-games entry for app id %s", appID)
		require.Empty(t, info.ModPath, "app %s is a game-root entry", appID)

		installs[appID] = installApp(t, steamapps, "app-"+appID)
		writeAppManifestNamed(t, steamapps, appID, info.Name, "app-"+appID)
	}

	detected, _, err := DetectGames(configDir, DetectOptions{})
	require.NoError(t, err)

	byApp := make(map[string]DetectedGame, len(detected))
	for _, g := range detected {
		byApp[g.SteamAppID] = g
	}
	for _, appID := range gameRootApps {
		g, ok := byApp[appID]
		require.Truef(t, ok, "DetectGames did not report app %s", appID)
		assert.Equalf(t, installs[appID], g.ModPath,
			"app %s: an empty mod_path must resolve to the install root, not to a subdirectory", appID)
	}
}
