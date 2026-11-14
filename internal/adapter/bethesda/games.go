package bethesda

import (
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Sources for the tables below (read for facts only; see the package doc):
//
//	[LLO]  github.com/Ortham/libloadorder src/game_settings.rs - load order
//	       method per game, appdata_folder_name (incl. the Galaxy64.dll /
//	       EOSSDK-Win64-Shipping.dll install markers), the hardcoded
//	       plugin lists, ccc_file_paths (Starfield's My Games copy first:
//	       "If the My Games CCC file is present, it overrides the other,
//	       even if empty"), additional_plugins_directories, and Skyrim's
//	       implicitly active Update.esm; src/load_order/writable.rs -
//	       MAX_ACTIVE_LIGHT_PLUGINS 4096, MAX_ACTIVE_MEDIUM_PLUGINS 256;
//	       src/load_order/mutable.rs - 255 full, one fewer per light/medium
//	       class in use; src/load_order/asterisk_based.rs - the
//	       "BlueprintShips-" prefix; src/enums.rs - light plugins for SSE,
//	       Skyrim VR, FO4, FO4 VR and Starfield, medium for Starfield only,
//	       and VR games read as SSE/FO4 plugins.
//	[LLOD] libloadorder doc/src/active_plugins.md (Windows-1252, 255 / 4096)
//	       and doc/src/textfile_standard.md (loadorder.txt, UTF-8 no BOM).
//	[ESP]  github.com/Ortham/esplugin src/plugin.rs - master flag 0x1; light
//	       0x200 (SSE, FO4) and 0x100 (Starfield); medium 0x400, update 0x200
//	       and blueprint 0x800 (Starfield); .esm/.esl imply master and .esl
//	       implies light for SSE, FO4 and Starfield only.
//	[STEAM] store.steampowered.com/api/appdetails for each app id.
//	[VXSF] github.com/Nexus-Mods/game-starfield at 3d3332d (Vortex's
//	       Starfield extension, read for behaviour only): src/index.ts
//	       registers the "starfield-data-folder" mod type for Starfield
//	       alone, with src/modTypes/dataPath.ts's getDataPath as its target.
//
// Omitted on purpose: the Microsoft Store variants ("... MS" folders,
// appxmanifest.xml, the MS Fallout 4 DLC directories), which do not run on
// Linux; Enderal, which is a different game sharing Skyrim's engine; and
// the INI sTestFile overrides, which a later story can read.

// gameKind is one Tier 1 game: how the adapter recognises it, and its
// plugin rules.
type gameKind struct {
	// slug is the game id `lmm game detect` gives it (steam-games.yaml).
	slug string
	// steamAppIDs are its Steam app ids [STEAM].
	steamAppIDs []string
	// spec returns its rules, freshly built on every call so a caller can
	// never edit the table through the slices it was handed.
	spec func() adapter.PluginSpec
	// modTypes are the Vortex mod types this game's own extension
	// registers, and the root each one's files are relative to (#635).
	// The types every Tier 1 game shares are in RootForModType.
	modTypes map[string]adapter.DeployRoot
}

// games is the Tier 1 table.
var games = []gameKind{
	{slug: "skyrim-se", steamAppIDs: []string{"489830"}, spec: skyrimSE},
	{slug: "skyrim-vr", steamAppIDs: []string{"611670"}, spec: skyrimVR},
	{slug: "fallout4", steamAppIDs: []string{"377160"}, spec: fallout4},
	{slug: "fallout4-vr", steamAppIDs: []string{"611660"}, spec: fallout4VR},
	{slug: "starfield", steamAppIDs: []string{"1716740"}, spec: starfield, modTypes: map[string]adapter.DeployRoot{
		// [VXSF] src/modTypes/dataPath.ts getDataPath: the game
		// directory's Data/, Starfield's mod_path.
		"starfield-data-folder": adapter.RootModPath,
	}},
	{slug: "skyrim", steamAppIDs: []string{"72850"}, spec: skyrimLE},
}

// recognisedSlugs lists the slugs the adapter recognises, for messages.
func recognisedSlugs() []string {
	out := make([]string, len(games))
	for i, k := range games {
		out[i] = k.slug
	}
	return out
}

// kindFor identifies which Tier 1 game g is, from - in order - its
// `catalog:` (the known-games catalog entry games.yaml says it is, by the
// slug detection gives it: the way a GOG or Epic copy under an id of its
// own is recognised, #606), its launch.steam_app_id or a numeric
// steamworkshop source mapping (the two places games.yaml states a Steam
// app id, read the way core's launcher reads them), and its game id. A
// stated app id that is not a Tier 1 game is an answer too: the game is
// something else, whatever its id says. A catalog entry the adapter does
// not know is not: it is skipped (see below).
//
// It never reads the NexusMods domain: Skyrim VR and Fallout 4 VR mods are
// hosted on the Skyrim SE and Fallout 4 domains, so the domain cannot tell
// them apart, and the VR games keep their plugin lists elsewhere.
func kindFor(g *domain.Game) (gameKind, bool) {
	if g == nil {
		return gameKind{}, false
	}
	// A catalog entry that is not one of these games - a typo, or a game
	// the catalog has that is not Tier 1 - is ignored, as core ignores an
	// unknown slug (verify's catalog_unknown note): the adapter cannot see
	// the catalog to tell the two apart, and falling through to the app id
	// and the game id is what the key's absence would do.
	if g.Catalog != "" {
		for _, k := range games {
			if k.slug == g.Catalog {
				return k, true
			}
		}
	}
	if id := statedAppID(g); id != "" {
		for _, k := range games {
			if slices.Contains(k.steamAppIDs, id) {
				return k, true
			}
		}
		return gameKind{}, false
	}
	for _, k := range games {
		if k.slug == g.ID {
			return k, true
		}
	}
	return gameKind{}, false
}

// statedAppID is the Steam app id games.yaml states for g, or "".
func statedAppID(g *domain.Game) string {
	if g.Launch != nil && g.Launch.SteamAppID != "" {
		return g.Launch.SteamAppID
	}
	if id := g.SourceIDs["steamworkshop"]; id != "" && strings.Trim(id, "0123456789") == "" {
		return id
	}
	return ""
}

// PluginRules returns g's plugin rules, or the zero spec when the adapter
// cannot tell which of its games g is.
func (*Adapter) PluginRules(g *domain.Game) adapter.PluginSpec {
	k, ok := kindFor(g)
	if !ok {
		return adapter.PluginSpec{}
	}
	return k.spec()
}

// Shared facts.
const (
	listFile   = "Plugins.txt"   // [LLO] plugins_file_path: the games read "Plugins.txt"
	masterFlag = uint32(0x1)     // [ESP] is_master_flag_set
	maxFull    = 255             // [LLOD], [LLO] mutable.rs
	maxLight   = 4096            // [LLO] writable.rs
	cccSkyrim  = "Skyrim.ccc"    // [LLO] ccc_file_paths
	cccFO4     = "Fallout4.ccc"  // [LLO] ccc_file_paths
	cccSF      = "Starfield.ccc" // [LLO] ccc_file_paths
)

// skyrimSEImplicit is [LLO]'s SKYRIM_SE_HARDCODED_PLUGINS.
func skyrimSEImplicit() []string {
	return []string{"Skyrim.esm", "Update.esm", "Dawnguard.esm", "HearthFires.esm", "Dragonborn.esm"}
}

// lightGame is the shape SSE, Skyrim VR, FO4 and FO4 VR share: an asterisk
// list, .esl light plugins on flag 0x200 [ESP; LLO enums.rs maps the VR
// games onto SSE and FO4].
func lightGame(folders []adapter.AppDataFolder, implicit []string, ccc []adapter.RootedPath) adapter.PluginSpec {
	return adapter.PluginSpec{
		ListKind:         adapter.ListAsterisk,
		AppDataFolders:   folders,
		ListFile:         listFile,
		Extensions:       []string{".esm", ".esp", ".esl"},
		HeaderKind:       adapter.HeaderTES4,
		MasterFlag:       masterFlag,
		LightFlag:        0x200,
		ExtImpliesMaster: []string{".esm", ".esl"},
		ExtImpliesLight:  []string{".esl"},
		Implicit:         implicit,
		CCCFiles:         ccc,
		MaxFull:          maxFull,
		MaxLight:         maxLight,
	}
}

// skyrimSE is Skyrim Special Edition / Anniversary Edition.
func skyrimSE() adapter.PluginSpec {
	return lightGame(
		[]adapter.AppDataFolder{
			{Name: "Skyrim Special Edition GOG", When: "Galaxy64.dll"},
			{Name: "Skyrim Special Edition EPIC", When: "EOSSDK-Win64-Shipping.dll"},
			{Name: "Skyrim Special Edition"},
		},
		skyrimSEImplicit(),
		[]adapter.RootedPath{{Root: adapter.SpecRootGame, Path: cccSkyrim}},
	)
}

// skyrimVR is Skyrim VR: SSE's plugins plus SkyrimVR.esm, and no CCC file.
func skyrimVR() adapter.PluginSpec {
	return lightGame(
		[]adapter.AppDataFolder{{Name: "Skyrim VR"}},
		append(skyrimSEImplicit(), "SkyrimVR.esm"),
		nil,
	)
}

// fallout4 is Fallout 4.
func fallout4() adapter.PluginSpec {
	return lightGame(
		[]adapter.AppDataFolder{
			{Name: "Fallout4 EPIC", When: "EOSSDK-Win64-Shipping.dll"},
			{Name: "Fallout4"},
		},
		[]string{
			"Fallout4.esm", "DLCRobot.esm", "DLCworkshop01.esm", "DLCCoast.esm", "DLCworkshop02.esm",
			"DLCworkshop03.esm", "DLCNukaWorld.esm", "DLCUltraHighResolution.esm",
		},
		[]adapter.RootedPath{{Root: adapter.SpecRootGame, Path: cccFO4}},
	)
}

// fallout4VR is Fallout 4 VR: no CCC file [LLO: "Fallout 4 VR ... has no
// CCC plugins file"].
func fallout4VR() adapter.PluginSpec {
	return lightGame(
		[]adapter.AppDataFolder{{Name: "Fallout4VR"}},
		[]string{"Fallout4.esm", "Fallout4_VR.esm"},
		nil,
	)
}

// starfield is Starfield: light (0x100), medium (0x400) and update (0x200)
// flags, blueprint plugins (0x800) that the game delists itself, a CCC
// file whose My Games copy overrides the game directory's, and a second
// plugin directory under My Games.
func starfield() adapter.PluginSpec {
	return adapter.PluginSpec{
		ListKind:         adapter.ListAsterisk,
		AppDataFolders:   []adapter.AppDataFolder{{Name: "Starfield"}},
		ListFile:         listFile,
		Extensions:       []string{".esm", ".esp", ".esl"},
		HeaderKind:       adapter.HeaderTES4,
		MasterFlag:       masterFlag,
		LightFlag:        0x100,
		MediumFlag:       0x400,
		UpdateFlag:       0x200,
		BlueprintFlag:    0x800,
		ExtImpliesMaster: []string{".esm", ".esl"},
		ExtImpliesLight:  []string{".esl"},
		UnlistedPrefixes: []string{"BlueprintShips-"},
		Implicit: []string{
			"Starfield.esm", "Constellation.esm", "OldMars.esm", "ShatteredSpace.esm",
			"SFBGS003.esm", "SFBGS004.esm", "SFBGS006.esm", "SFBGS007.esm",
			"SFBGS008.esm", "SFBGS00D.esm", "SFBGS047.esm", "SFBGS050.esm",
		},
		CCCFiles: []adapter.RootedPath{
			{Root: adapter.SpecRootMyGames, Path: cccSF},
			{Root: adapter.SpecRootGame, Path: cccSF},
		},
		ExtraPluginDirs: []adapter.RootedPath{{Root: adapter.SpecRootMyGames, Path: "Data"}},
		MaxFull:         maxFull,
		MaxLight:        maxLight,
		MaxMedium:       256,
	}
}

// skyrimLE is Skyrim (2011): the textfile standard, no light plugins, the
// master flag alone decides a master [ESP], and Update.esm always active
// without a fixed position [LLO implicitly_active_plugins].
func skyrimLE() adapter.PluginSpec {
	return adapter.PluginSpec{
		ListKind:         adapter.ListTextfile,
		AppDataFolders:   []adapter.AppDataFolder{{Name: "Skyrim"}},
		ListFile:         listFile,
		OrderFile:        "loadorder.txt",
		Extensions:       []string{".esm", ".esp"},
		HeaderKind:       adapter.HeaderTES4,
		MasterFlag:       masterFlag,
		Implicit:         []string{"Skyrim.esm"},
		ImplicitlyActive: []string{"Update.esm"},
		MaxFull:          maxFull,
	}
}

// UsesPrefix reports whether lmm writes into g's Wine/Proton prefix (#605):
// true for every game the adapter recognises, whose Plugins.txt lives under
// the prefix's AppData/Local. A game it cannot identify has no plugin rules,
// so lmm writes nothing there; its verify note says why instead.
func (*Adapter) UsesPrefix(g *domain.Game) bool {
	_, ok := kindFor(g)
	return ok
}
