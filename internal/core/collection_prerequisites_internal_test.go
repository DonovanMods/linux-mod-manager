package core

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stardewInput is a conversion for a Stardew Valley game.
func stardewInput(m *source.CollectionManifest) convertInput {
	in := convertInputFor(m)
	in.gameID = "sdv"
	in.game = domain.CollectionGame{Name: "Stardew Valley", Sources: map[string]string{"nexusmods": "stardewvalley"}}
	return in
}

// #635, owner ruling: an item the source classifies as a loader installed
// with its own installer (SMAPI: Vortex's "SMAPI" mod type on Stardew
// Valley) is a prerequisite step. It is listed for the user to install,
// never installed, and it raises no mod_type feature, so the rest of the
// collection imports.
func TestConvert_PrerequisiteInstallerIsAnExternalStep(t *testing.T) {
	smapi := nexusMod("SMAPI - Stardew Modding API", "2400", "100", "aa")
	smapi.ModType = "SMAPI"
	smapi.Instructions = "Install this first."
	other := nexusMod("Content Patcher", "1915", "200", "bb")
	c := mustConvert(t, stardewInput(&source.CollectionManifest{Name: "C", GameSourceID: "stardewvalley",
		Mods: []source.ManifestMod{smapi, other}}))

	assert.Empty(t, c.Unsupported, "SMAPI no longer blocks the import")
	assert.NotContains(t, c.Features, "mod_type:SMAPI")
	it := itemByName(t, c, "SMAPI - Stardew Modding API")
	assert.Equal(t, CollectionItemExternal, it.State)
	assert.Empty(t, it.ModType, "the type is the prerequisite's, never a profile entry's")
	assert.Equal(t, "https://www.nexusmods.com/stardewvalley/mods/2400", it.URL, "its page, when the published list does not name the file's")
	assert.Contains(t, it.Note, "own installer")
	assert.Contains(t, it.Instructions, "install on Linux.sh")
	assert.True(t, strings.HasSuffix(it.Instructions, "Install this first."), "the curator's words are kept, after lmm's")

	require.Len(t, c.Document.External, 1)
	assert.Equal(t, it.URL, c.Document.External[0].URL)
	assert.Equal(t, it.Instructions, c.Document.External[0].Instructions)
	require.Len(t, c.Document.Items, 1, "only Content Patcher is a profile entry")
	assert.Equal(t, "1915", c.Document.Items[0].Ref.ModID)
	assert.NotContains(t, c.Document.Requires, "mod_type:SMAPI")
	assert.Equal(t, 1, c.Counts[CollectionItemExternal])
}

// The published list's file page is the better link, when it has one.
func TestConvert_PrerequisiteInstallerKeepsTheListedFilePage(t *testing.T) {
	smapi := nexusMod("SMAPI", "2400", "100", "aa")
	smapi.ModType = "SMAPI"
	in := stardewInput(&source.CollectionManifest{Name: "C", GameSourceID: "stardewvalley", Mods: []source.ManifestMod{smapi}})
	page := "https://www.nexusmods.com/stardewvalley/mods/2400?tab=files&file_id=100"
	in.list.Items = []source.CollectionItem{{Kind: source.CollectionItemModFile, ModID: "2400", FileID: "100", Name: "SMAPI", URL: page}}
	c := mustConvert(t, in)
	assert.Equal(t, page, itemByName(t, c, "SMAPI").URL)
}

// The table is keyed by the source's classification on the source's game:
// the same type on another game, or another type on Stardew Valley, is not
// a prerequisite.
func TestConvert_PrerequisiteInstallerIsPerGameAndType(t *testing.T) {
	smapi := nexusMod("SMAPI", "2400", "100", "aa")
	smapi.ModType = "SMAPI"
	c := mustConvert(t, convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{smapi}}))
	assert.Equal(t, []string{"mod_type:SMAPI"}, featureNames(c.Unsupported), "on Skyrim, SMAPI is just an unknown type")

	other := nexusMod("Root thing", "2400", "100", "aa")
	other.ModType = "sdvrootfolder"
	c = mustConvert(t, stardewInput(&source.CollectionManifest{Name: "C", GameSourceID: "stardewvalley", Mods: []source.ManifestMod{other}}))
	assert.Equal(t, []string{"mod_type:sdvrootfolder"}, featureNames(c.Unsupported))
}

// Every row of the table names what the plan shows: a name, a page and
// instructions.
func TestPrerequisiteInstallersAreComplete(t *testing.T) {
	require.NotEmpty(t, prerequisiteInstallers)
	for _, p := range prerequisiteInstallers {
		assert.NotEmpty(t, p.sourceID)
		assert.NotEmpty(t, p.gameSourceID)
		assert.NotEmpty(t, p.modType)
		assert.NotEmpty(t, p.name)
		assert.True(t, strings.HasPrefix(p.page, "https://"), p.page)
		assert.NotEmpty(t, p.instructions)
	}
}
