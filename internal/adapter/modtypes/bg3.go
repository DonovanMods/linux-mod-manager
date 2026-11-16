package modtypes

import (
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Baldur's Gate 3 ([VXG] game-baldursgate3; facts only, see the package
// comment), #636:
//
//	.pak       the default type's mods are .pak files, deployed to
//	           %LOCALAPPDATA%/Larian Studios/Baldur's Gate 3/Mods
//	           (queryModPath modsPath, index.tsx:236; util.ts:27-33, whose
//	           documentsPath is Vortex's localAppData path). Inside a
//	           Proton prefix that is the user directory's AppData/Local.
//	           A pak is rooted at itself (STOP_PATTERNS '[^/]*\.pak$',
//	           index.tsx:55, 248), so a wrapper folder is dropped: the Mods
//	           folder stays flat.
//	replacer   a pak named like one of the game's own (ORIGINAL_FILES,
//	           common.ts:87) is Vortex's bg3-replacer, deployed to the game's
//	           Data/ (index.tsx:294-297 getGameDataPath; modTypes.ts
//	           isReplacer): the catalog's mod_path.
//	bg3-loose  Data/ (index.tsx:289-292 getGameDataPath; util.ts:17-25).
//	info.json  never deployed (IGNORE_PATTERNS '**/info.json', common.ts:79;
//	           ignoreDeploy, index.tsx:250).
//
// Left out: bg3-bg3se deploys to the game's bin/ folder (index.tsx:284-287),
// which is neither the mod_path nor the game directory a whole-mod route
// would use, and bg3-lslib-divine-tool is Vortex's own tool (no deploy
// path, index.tsx:279-282). Both stay refused.

// bg3ModsDir is the folder, relative to the prefix's Windows user
// directory, that Baldur's Gate 3 loads .pak mods from.
const bg3ModsDir = "AppData/Local/Larian Studios/Baldur's Gate 3/Mods"

// bg3ProfileDir is the folder, relative to the prefix's Windows user
// directory, that holds the game's mod list, modsettings.lsx: the Public
// player profile beside the Mods folder ([VXG] util.ts:35-37 profilesPath,
// :66-70 getActivePlayerProfile - "Public" for every game version since
// 4.1.206; NexusMods.App's BG3 developer notes, "Load Order").
const bg3ProfileDir = "AppData/Local/Larian Studios/Baldur's Gate 3/PlayerProfiles/Public"

// PakOrderID is Baldur's Gate 3's load order id: the game's id in Vortex,
// which keys the game's one load order by the game ([VXG] index.tsx:299-301
// registerLoadOrder({gameId: GAME_ID}), common.ts:73 GAME_ID).
const PakOrderID = "baldursgate3"

// bg3GamePaks are the game's own .pak files, lower-cased ([VXG] common.ts:87
// ORIGINAL_FILES): a mod's pak named like one replaces it in Data/.
var bg3GamePaks = map[string]bool{
	"assets.pak": true, "effects.pak": true, "engine.pak": true, "engineshaders.pak": true,
	"game.pak": true, "gameplatform.pak": true, "gustav.pak": true, "gustav_textures.pak": true,
	"icons.pak": true, "lowtex.pak": true, "materials.pak": true, "minimaps.pak": true,
	"models.pak": true, "shared.pak": true, "sharedsoundbanks.pak": true, "sharedsounds.pak": true,
	"textures.pak": true, "virtualtextures.pak": true,
}

// BG3 is Baldur's Gate 3's adapter: its mod-type table, plus the per-file
// rule that sends a .pak mod into the game's Mods folder inside its prefix
// (#636). It is not adapter.LayoutNeutral: an untyped mod's .pak files do
// not deploy under the mod_path.
type BG3 struct {
	types map[string]adapter.DeployRoot
}

// Compile-time proof of the capabilities BG3 has.
var (
	_ adapter.GameAdapter     = (*BG3)(nil)
	_ adapter.RootRouter      = (*BG3)(nil)
	_ adapter.PrefixPlacer    = (*BG3)(nil)
	_ adapter.PrefixConsumer  = (*BG3)(nil)
	_ adapter.FileRouter      = (*BG3)(nil)
	_ adapter.LaunchAdvisor   = (*BG3)(nil)
	_ adapter.FileLoadOrderer = (*BG3)(nil)
)

// BaldursGate3 returns Baldur's Gate 3's adapter.
func BaldursGate3() *BG3 {
	return &BG3{types: map[string]adapter.DeployRoot{
		"bg3-loose":    adapter.RootModPath,
		"bg3-replacer": adapter.RootModPath,
	}}
}

// ID returns the adapter's games.yaml value.
func (*BG3) ID() string { return BaldursGate3ID }

// Label returns the display name a frontend shows.
func (*BG3) Label() string { return "Baldur's Gate 3 (Vortex mod types)" }

// NormalizeArchive is the identity: archives are cached as packaged, and a
// .pak's wrapper folder is dropped where it deploys (PrefixPath), not in
// the cache.
func (*BG3) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// isModPak reports whether member is a .pak mod for the Mods folder: a
// .pak, matched case-insensitively, not named like one of the game's own.
func isModPak(member string) bool {
	base := strings.ToLower(path.Base(member))
	return path.Ext(base) == ".pak" && !bg3GamePaks[base]
}

// RouteRoots sends every .pak mod into the prefix (PrefixPath says where)
// and every other file - loose files, a replacer pak - under the mod_path,
// the game's Data/.
func (*BG3) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for i, f := range files {
		if isModPak(f) {
			out[i] = adapter.RootPrefix
		}
	}
	return out
}

// RootForModType answers from the table; a type it does not list is
// unknown.
func (a *BG3) RootForModType(_ *domain.Game, modType string) (adapter.DeployRoot, bool) {
	r, ok := a.types[modType]
	return r, ok
}

// PrefixPath places a .pak directly inside the Mods folder, whatever folder
// wrapped it in the archive: the game loads a pak only from there, and a
// folder inside Mods makes it reset its mod list.
func (*BG3) PrefixPath(_ *domain.Game, member string) string {
	return bg3ModsDir + "/" + path.Base(member)
}

// UsesPrefix is true: .pak mods deploy into the prefix.
func (*BG3) UsesPrefix(*domain.Game) bool { return true }

// RouteFile skips Vortex's per-mod info.json, which the game never reads.
func (*BG3) RouteFile(_ *domain.Game, rel string) adapter.FileRoute {
	if strings.EqualFold(path.Base(rel), "info.json") {
		return adapter.RouteSkip
	}
	return adapter.RouteLink
}

// FileLoadOrders returns the .pak order (#646): the paks in the Mods
// folder, listed - by the module each one's meta.lsx declares - in
// modsettings.lsx, which both enables them and orders them; the last one
// loaded wins a conflict (NexusMods.App's BG3 developer notes, "Load
// Order"). Core writes it (adapter.FileListModSettings). A Vortex
// collection carries it as its top-level loadOrder, whose entries' ids are
// the pak file names ([VXG] loadOrder.ts:125-133 deserialize).
func (*BG3) FileLoadOrders(*domain.Game) []adapter.FileLoadOrderSpec {
	return []adapter.FileLoadOrderSpec{PakOrder()}
}

// PakOrder is Baldur's Gate 3's .pak load order spec.
func PakOrder() adapter.FileLoadOrderSpec {
	return adapter.FileLoadOrderSpec{
		ID:                  PakOrderID,
		Label:               "Paks",
		Dir:                 adapter.RootedPath{Root: adapter.SpecRootUserDir, Path: bg3ModsDir},
		ListDir:             adapter.RootedPath{Root: adapter.SpecRootUserDir, Path: bg3ProfileDir},
		Extensions:          []string{".pak"},
		ListFile:            "modsettings.lsx",
		Format:              adapter.FileListModSettings,
		CollectionLoadOrder: true,
	}
}

// LaunchAdvice, when a .pak is deployed, says what can undo lmm's
// modsettings.lsx (#646): the game resets the list - and runs with no mods
// - when it finds a mod it cannot load ([VXG] util.ts:448-459, Vortex's
// "modsettings.lsx file was reset" dialog). It sets no launch arguments.
func (*BG3) LaunchAdvice(_ *domain.Game, mods []adapter.DeployedMod, _ string) adapter.Advice {
	for _, m := range mods {
		for _, f := range m.PrefixFiles {
			if isModPak(f) {
				return adapter.Advice{Lines: []string{
					"Baldur's Gate 3 loads a .pak mod from its Mods folder only while its mod list, modsettings.lsx (PlayerProfiles/Public, beside the Mods folder in the game's prefix), enables it; lmm lists the paks it deploys there at every deploy.",
					"If the game finds a mod it cannot load, it resets that list and runs without mods: `lmm verify` reports it, and `lmm verify --fix` (or `lmm deploy`) lists them again. A pak that only replaces game files, with no meta.lsx, needs no entry.",
				}}
			}
		}
	}
	return adapter.Advice{}
}
