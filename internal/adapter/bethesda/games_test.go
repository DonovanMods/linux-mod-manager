package bethesda

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rulesFor(t *testing.T, g *domain.Game) adapter.PluginSpec {
	t.Helper()
	spec, ok := adapter.PluginRules(New(), g)
	require.True(t, ok, "no plugin rules for %+v", g)
	return spec
}

// Each Tier 1 game is recognised by the slug detection gives it and by its
// Steam app id, and the two agree.
func TestPluginRules_RecognisesTier1BySlugAndAppID(t *testing.T) {
	for slug, appID := range map[string]string{
		"skyrim-se":   "489830",
		"skyrim-vr":   "611670",
		"fallout4":    "377160",
		"fallout4-vr": "611660",
		"starfield":   "1716740",
		"skyrim":      "72850",
	} {
		t.Run(slug, func(t *testing.T) {
			bySlug := rulesFor(t, &domain.Game{ID: slug})
			byLaunch := rulesFor(t, &domain.Game{ID: "my-game", Launch: &domain.GameLaunch{SteamAppID: appID}})
			byWorkshop := rulesFor(t, &domain.Game{ID: "my-game", SourceIDs: map[string]string{"steamworkshop": appID}})
			assert.Equal(t, bySlug, byLaunch)
			assert.Equal(t, bySlug, byWorkshop)
		})
	}
}

// An explicit app id wins over the slug: a game the user named skyrim-se
// but pointed at Skyrim VR's app id is Skyrim VR.
func TestPluginRules_AppIDBeatsSlug(t *testing.T) {
	spec := rulesFor(t, &domain.Game{ID: "skyrim-se", Launch: &domain.GameLaunch{SteamAppID: "611670"}})
	assert.Equal(t, "Skyrim VR", spec.AppDataFolders[0].Name)
}

// #606 owner request: `catalog:` names the known-games catalog entry a game
// is, and it comes first - before the Steam app id and the game id - which
// is what makes a GOG or Epic copy under an id of its own recognisable.
func TestPluginRules_CatalogEntryComesFirst(t *testing.T) {
	gog := rulesFor(t, &domain.Game{ID: "my-gog-skyrim", Catalog: "skyrim-se"})
	assert.Equal(t, rulesFor(t, &domain.Game{ID: "skyrim-se"}), gog)

	vr := rulesFor(t, &domain.Game{ID: "skyrim-se", Catalog: "skyrim-vr", Launch: &domain.GameLaunch{SteamAppID: "489830"}})
	assert.Equal(t, "Skyrim VR", vr.AppDataFolders[0].Name, "the catalog entry beats both the app id and the game id")

	// A catalog entry that is some other game is an answer too: the game
	// is that one, whatever its id says.
	_, ok := adapter.PluginRules(New(), &domain.Game{ID: "skyrim-se", Catalog: "witcher3"})
	assert.False(t, ok)
	assert.True(t, New().UsesPrefix(&domain.Game{ID: "x", Catalog: "starfield"}))
}

// A game the adapter cannot identify gets no rules: it never guesses, and
// in particular it never reads the NexusMods domain, which Skyrim VR and
// Fallout 4 VR share with Skyrim SE and Fallout 4.
func TestPluginRules_UnknownGameHasNoRules(t *testing.T) {
	for name, g := range map[string]*domain.Game{
		"custom id":                     {ID: "my-skyrim"},
		"nexus domain alone":            {ID: "my-skyrim", SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}},
		"unknown app id":                {ID: "my-game", Launch: &domain.GameLaunch{SteamAppID: "22380"}},
		"non-numeric workshop mapping":  {ID: "my-game", SourceIDs: map[string]string{"steamworkshop": "skyrim-se"}},
		"unknown app id beats the slug": {ID: "skyrim-se", Launch: &domain.GameLaunch{SteamAppID: "22380"}},
	} {
		t.Run(name, func(t *testing.T) {
			spec, ok := adapter.PluginRules(New(), g)
			assert.False(t, ok)
			assert.Equal(t, adapter.PluginSpec{}, spec)
		})
	}
	_, ok := adapter.PluginRules(New(), nil)
	assert.False(t, ok, "a nil game has no rules")
}

// A caller that edits the spec it was handed must not edit the table.
func TestPluginRules_ReturnsAFreshCopy(t *testing.T) {
	a := rulesFor(t, &domain.Game{ID: "skyrim-se"})
	a.Implicit[0] = "Changed.esm"
	a.AppDataFolders[0].Name = "changed"
	b := rulesFor(t, &domain.Game{ID: "skyrim-se"})
	assert.Equal(t, "Skyrim.esm", b.Implicit[0])
	assert.NotEqual(t, "changed", b.AppDataFolders[0].Name)
}

// The tables themselves. Every value here is pinned against the source
// docs/adapters.md cites for it, so a change to one is a change to a
// cited fact and has to say why.
func TestPluginRules_SkyrimSE(t *testing.T) {
	s := rulesFor(t, &domain.Game{ID: "skyrim-se"})
	assert.Equal(t, adapter.ListAsterisk, s.ListKind)
	assert.Equal(t, []adapter.AppDataFolder{
		{Name: "Skyrim Special Edition GOG", When: "Galaxy64.dll"},
		{Name: "Skyrim Special Edition EPIC", When: "EOSSDK-Win64-Shipping.dll"},
		{Name: "Skyrim Special Edition"},
	}, s.AppDataFolders)
	assert.Equal(t, "Plugins.txt", s.ListFile)
	assert.Empty(t, s.OrderFile)
	assert.Equal(t, []string{".esm", ".esp", ".esl"}, s.Extensions)
	assert.Equal(t, adapter.HeaderTES4, s.HeaderKind)
	assert.Equal(t, uint32(0x1), s.MasterFlag)
	assert.Equal(t, uint32(0x200), s.LightFlag)
	assert.Zero(t, s.MediumFlag)
	assert.Zero(t, s.UpdateFlag)
	assert.Zero(t, s.BlueprintFlag)
	assert.Equal(t, []string{".esm", ".esl"}, s.ExtImpliesMaster)
	assert.Equal(t, []string{".esl"}, s.ExtImpliesLight)
	assert.Equal(t, []string{"Skyrim.esm", "Update.esm", "Dawnguard.esm", "HearthFires.esm", "Dragonborn.esm"}, s.Implicit)
	assert.Empty(t, s.ImplicitlyActive)
	assert.Equal(t, []adapter.RootedPath{{Root: adapter.SpecRootGame, Path: "Skyrim.ccc"}}, s.CCCFiles)
	assert.Empty(t, s.ExtraPluginDirs)
	assert.Equal(t, 255, s.MaxFull)
	assert.Equal(t, 4096, s.MaxLight)
	assert.Zero(t, s.MaxMedium)
}

func TestPluginRules_SkyrimVR(t *testing.T) {
	s := rulesFor(t, &domain.Game{ID: "skyrim-vr"})
	assert.Equal(t, adapter.ListAsterisk, s.ListKind)
	assert.Equal(t, []adapter.AppDataFolder{{Name: "Skyrim VR"}}, s.AppDataFolders)
	assert.Equal(t, []string{".esm", ".esp", ".esl"}, s.Extensions)
	assert.Equal(t, uint32(0x200), s.LightFlag)
	assert.Equal(t, []string{"Skyrim.esm", "Update.esm", "Dawnguard.esm", "HearthFires.esm", "Dragonborn.esm", "SkyrimVR.esm"}, s.Implicit)
	assert.Empty(t, s.CCCFiles, "Skyrim VR has no Creation Club file")
	assert.Equal(t, 255, s.MaxFull)
	assert.Equal(t, 4096, s.MaxLight)
}

func TestPluginRules_Fallout4(t *testing.T) {
	s := rulesFor(t, &domain.Game{ID: "fallout4"})
	assert.Equal(t, adapter.ListAsterisk, s.ListKind)
	assert.Equal(t, []adapter.AppDataFolder{
		{Name: "Fallout4 EPIC", When: "EOSSDK-Win64-Shipping.dll"},
		{Name: "Fallout4"},
	}, s.AppDataFolders)
	assert.Equal(t, uint32(0x200), s.LightFlag)
	assert.Equal(t, []string{".esm", ".esl"}, s.ExtImpliesMaster)
	assert.Equal(t, []string{
		"Fallout4.esm", "DLCRobot.esm", "DLCworkshop01.esm", "DLCCoast.esm", "DLCworkshop02.esm",
		"DLCworkshop03.esm", "DLCNukaWorld.esm", "DLCUltraHighResolution.esm",
	}, s.Implicit)
	assert.Equal(t, []adapter.RootedPath{{Root: adapter.SpecRootGame, Path: "Fallout4.ccc"}}, s.CCCFiles)
	assert.Equal(t, 255, s.MaxFull)
	assert.Equal(t, 4096, s.MaxLight)
}

func TestPluginRules_Fallout4VR(t *testing.T) {
	s := rulesFor(t, &domain.Game{ID: "fallout4-vr"})
	assert.Equal(t, adapter.ListAsterisk, s.ListKind)
	assert.Equal(t, []adapter.AppDataFolder{{Name: "Fallout4VR"}}, s.AppDataFolders)
	assert.Equal(t, uint32(0x200), s.LightFlag)
	assert.Equal(t, []string{"Fallout4.esm", "Fallout4_VR.esm"}, s.Implicit)
	assert.Empty(t, s.CCCFiles)
}

func TestPluginRules_Starfield(t *testing.T) {
	s := rulesFor(t, &domain.Game{ID: "starfield"})
	assert.Equal(t, adapter.ListAsterisk, s.ListKind)
	assert.Equal(t, []adapter.AppDataFolder{{Name: "Starfield"}}, s.AppDataFolders)
	assert.Equal(t, []string{".esm", ".esp", ".esl"}, s.Extensions)
	assert.Equal(t, uint32(0x1), s.MasterFlag)
	assert.Equal(t, uint32(0x100), s.LightFlag)
	assert.Equal(t, uint32(0x400), s.MediumFlag)
	assert.Equal(t, uint32(0x200), s.UpdateFlag)
	assert.Equal(t, uint32(0x800), s.BlueprintFlag)
	assert.Equal(t, []string{".esm", ".esl"}, s.ExtImpliesMaster)
	assert.Equal(t, []string{".esl"}, s.ExtImpliesLight)
	assert.Equal(t, []string{"BlueprintShips-"}, s.UnlistedPrefixes)
	assert.Equal(t, []string{
		"Starfield.esm", "Constellation.esm", "OldMars.esm", "ShatteredSpace.esm",
		"SFBGS003.esm", "SFBGS004.esm", "SFBGS006.esm", "SFBGS007.esm",
		"SFBGS008.esm", "SFBGS00D.esm", "SFBGS047.esm", "SFBGS050.esm",
	}, s.Implicit)
	// The My Games copy overrides the game directory's, even when empty,
	// so it comes first.
	assert.Equal(t, []adapter.RootedPath{
		{Root: adapter.SpecRootMyGames, Path: "Starfield.ccc"},
		{Root: adapter.SpecRootGame, Path: "Starfield.ccc"},
	}, s.CCCFiles)
	assert.Equal(t, []adapter.RootedPath{{Root: adapter.SpecRootMyGames, Path: "Data"}}, s.ExtraPluginDirs)
	assert.Equal(t, 255, s.MaxFull)
	assert.Equal(t, 4096, s.MaxLight)
	assert.Equal(t, 256, s.MaxMedium)
}

func TestPluginRules_SkyrimLE(t *testing.T) {
	s := rulesFor(t, &domain.Game{ID: "skyrim"})
	assert.Equal(t, adapter.ListTextfile, s.ListKind)
	assert.Equal(t, []adapter.AppDataFolder{{Name: "Skyrim"}}, s.AppDataFolders)
	assert.Equal(t, "Plugins.txt", s.ListFile)
	assert.Equal(t, "loadorder.txt", s.OrderFile)
	assert.Equal(t, []string{".esm", ".esp"}, s.Extensions)
	assert.Equal(t, uint32(0x1), s.MasterFlag)
	assert.Zero(t, s.LightFlag, "Skyrim LE has no light plugins")
	assert.Empty(t, s.ExtImpliesMaster, "Skyrim LE reads the master flag only")
	assert.Empty(t, s.ExtImpliesLight)
	assert.Equal(t, []string{"Skyrim.esm"}, s.Implicit)
	assert.Equal(t, []string{"Update.esm"}, s.ImplicitlyActive)
	assert.Empty(t, s.CCCFiles)
	assert.Equal(t, 255, s.MaxFull)
	assert.Zero(t, s.MaxLight)
}

// Invariants every table must hold, so a new row cannot get them wrong.
func TestPluginRules_TableInvariants(t *testing.T) {
	for _, k := range games {
		t.Run(k.slug, func(t *testing.T) {
			s := k.spec()
			assert.NotEqual(t, adapter.ListNone, s.ListKind)
			assert.Equal(t, adapter.HeaderTES4, s.HeaderKind)
			assert.Equal(t, "Plugins.txt", s.ListFile)
			assert.Equal(t, s.ListKind == adapter.ListTextfile, s.OrderFile != "", "only a textfile game has an order file")
			require.NotEmpty(t, s.AppDataFolders)
			assert.Empty(t, s.AppDataFolders[len(s.AppDataFolders)-1].When, "the last AppData candidate is the default")
			assert.NotEmpty(t, s.Implicit)
			assert.Equal(t, s.LightFlag != 0, s.MaxLight != 0, "a light flag goes with a light limit")
			assert.Equal(t, s.MediumFlag != 0, s.MaxMedium != 0, "a medium flag goes with a medium limit")
			for _, ext := range append(append([]string{}, s.ExtImpliesMaster...), s.ExtImpliesLight...) {
				assert.Contains(t, s.Extensions, ext)
			}
			for _, p := range append(append([]adapter.RootedPath{}, s.CCCFiles...), s.ExtraPluginDirs...) {
				assert.NotEmpty(t, p.Path)
				assert.NotContains(t, p.Path, `\`)
				assert.False(t, p.Path[0] == '/', "a rooted path is relative")
			}
			require.NotEmpty(t, k.steamAppIDs)
		})
	}
}
