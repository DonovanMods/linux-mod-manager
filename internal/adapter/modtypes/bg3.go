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
	_ adapter.GameAdapter    = (*BG3)(nil)
	_ adapter.RootRouter     = (*BG3)(nil)
	_ adapter.PrefixPlacer   = (*BG3)(nil)
	_ adapter.PrefixConsumer = (*BG3)(nil)
	_ adapter.FileRouter     = (*BG3)(nil)
	_ adapter.LaunchAdvisor  = (*BG3)(nil)
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

// LaunchAdvice says what lmm does not do yet (#646): a .pak mod that
// carries a meta.lsx loads only once the game's modsettings.lsx lists it.
// It sets no launch arguments.
func (*BG3) LaunchAdvice(_ *domain.Game, mods []adapter.DeployedMod, _ string) adapter.Advice {
	for _, m := range mods {
		for _, f := range m.PrefixFiles {
			if isModPak(f) {
				return adapter.Advice{Lines: []string{
					"Baldur's Gate 3 loads a .pak mod from its Mods folder only once the game's mod list, modsettings.lsx (PlayerProfiles/Public, beside the Mods folder in the game's prefix), enables it; lmm doesn't write that file yet.",
					"Enable the deployed .pak mods with a tool that writes it, such as BG3 Mod Manager. A pak that only replaces game files and has no meta.lsx needs no entry.",
				}}
			}
		}
	}
	return adapter.Advice{}
}
