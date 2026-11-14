package core

import (
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// prerequisiteInstaller is a mod a source's collections list that lmm does
// not install: a loader that its own installer sets up, which a collection
// item names by the source's own classification (a Vortex mod type on one
// game). The converter lists such an item as an external step - its page
// and plain instructions - so the rest of the collection imports (#635,
// owner ruling 2026-11-14). A future loader joins by gaining a row.
type prerequisiteInstaller struct {
	// sourceID, gameSourceID and modType are the key: the source, the
	// game's id on it (a NexusMods domain) and the type it states.
	sourceID     string
	gameSourceID string
	modType      string
	// name is the loader's display name.
	name string
	// page is the loader's page, the link shown when the source's
	// published list does not name the item's own file page.
	page string
	// instructions say how to install it, in plain words.
	instructions string
}

// prerequisiteInstallers is the table. Sources (read for facts only):
//
//	SMAPI: Vortex's game-stardewvalley (github.com/Nexus-Mods/vortex-games
//	at 18bbee9) registers the "SMAPI" mod type for SMAPI itself
//	(index.ts:445-450, :754), and its installer unpacks the platform's
//	install.dat from inside the download and generates
//	StardewModdingAPI.deps.json (index.ts:462-538), which no deploy of the
//	downloaded archive can do; its page is mod 2400 (constants.ts:6-8).
//	SMAPI's own README (github.com/Pathoschild/SMAPI at e5b8bf3,
//	src/SMAPI.Installer/assets/README.txt) and installer scripts
//	("install on Linux.sh", "install on macOS.command", "install on
//	Windows.bat" beside it) are the install steps.
var prerequisiteInstallers = []prerequisiteInstaller{
	{
		sourceID: "nexusmods", gameSourceID: "stardewvalley", modType: "SMAPI",
		name: "SMAPI",
		page: "https://www.nexusmods.com/stardewvalley/mods/2400",
		instructions: "Download SMAPI from this page and unzip it. Run the installer for your system " +
			"(\"install on Linux.sh\", \"install on macOS.command\" or \"install on Windows.bat\") and follow its prompts; " +
			"https://stardewvalleywiki.com/Modding:Player_Guide has help. Install it before you play: " +
			"the collection's other mods load through SMAPI.",
	},
}

// prerequisiteFor returns the table's row for a stated mod type on a game
// of a source, if it has one.
func prerequisiteFor(sourceID, gameSourceID, modType string) (prerequisiteInstaller, bool) {
	if modType == "" {
		return prerequisiteInstaller{}, false
	}
	for _, p := range prerequisiteInstallers {
		if p.sourceID == sourceID && p.gameSourceID == gameSourceID && p.modType == modType {
			return p, true
		}
	}
	return prerequisiteInstaller{}, false
}

// asPrerequisite turns a recipe item into the prerequisite step p: an
// external item with p's page (unless the published list gave the item's own
// file page), p's instructions before any of the curator's, and no stated
// type - the type is the prerequisite's, never a profile entry's.
func (it *convItem) asPrerequisite(p prerequisiteInstaller) {
	it.kind = source.CollectionItemExternal
	it.ModType = ""
	if it.URL == "" {
		it.URL = p.page
	}
	it.Note = p.name + " is installed with its own installer, not by lmm: install it by hand, and the rest of the collection imports."
	if curator := strings.TrimSpace(it.Instructions); curator != "" {
		it.Instructions = p.instructions + " " + curator
	} else {
		it.Instructions = p.instructions
	}
}
