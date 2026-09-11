package steam_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #269 Unit 9: detection prefills `steamworkshop: <appid>` for a game whose
// appworkshop manifest declares installed items, and stamps the count that
// justifies it. Every test here sandboxes HOME and STEAM_ROOT and works
// against a temp library - nothing reads the machine's real Steam install.

const populatedWorkshopACF = `"AppWorkshop"
{
	"appid"		"%APPID%"
	"WorkshopItemsInstalled"
	{
		"3617086610"
		{
			"size"		"572330"
			"timeupdated"		"1764767935"
			"manifest"		"7987119735124793734"
		}
		"3512001122"
		{
			"size"		"104857600"
			"timeupdated"		"1758000000"
			"manifest"		"1122334455667788990"
		}
	}
}
`

const emptyStubWorkshopACF = `"AppWorkshop"
{
	"appid"		"%APPID%"
	"WorkshopItemsInstalled"
	{
	}
}
`

// steamFixture lays out a sandboxed Steam root holding one installed app,
// and points HOME and STEAM_ROOT at it so FindSteamRoots finds nothing else.
func steamFixture(t *testing.T, appID, name, installDir string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "SteamLibrary")
	t.Setenv("STEAM_ROOT", root)

	steamapps := filepath.Join(root, "steamapps")
	require.NoError(t, os.MkdirAll(filepath.Join(steamapps, "common", installDir), 0o755))
	manifest := `"AppState"
{
	"appid"		"` + appID + `"
	"name"		"` + name + `"
	"installdir"		"` + installDir + `"
}
`
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+appID+".acf"), []byte(manifest), 0o644))
	return root
}

func writeWorkshopACF(t *testing.T, root, appID, template string) {
	t.Helper()
	dir := filepath.Join(root, "steamapps", "workshop")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := strings.ReplaceAll(template, "%APPID%", appID)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "appworkshop_"+appID+".acf"), []byte(body), 0o644))
}

func detectOne(t *testing.T, opts steam.DetectOptions) steam.DetectedGame {
	t.Helper()
	games, warnings, err := steam.DetectGames(t.TempDir(), opts)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, games, 1)
	return games[0]
}

func TestDetectGames_PrefillsWorkshopForAnUnknownGameWithItems(t *testing.T) {
	root := steamFixture(t, "1133870", "Space Engineers 2", "SpaceEngineers2")
	writeWorkshopACF(t, root, "1133870", populatedWorkshopACF)

	got := detectOne(t, steam.DetectOptions{IncludeUnknown: true})
	assert.Equal(t, map[string]string{"steamworkshop": "1133870"}, got.Sources,
		"lmm cannot deploy to an uncurated game, but it can track what Steam downloaded")
	assert.Equal(t, 2, got.WorkshopItems)
	assert.False(t, got.Known)
	assert.Empty(t, got.ModPath, "detection still refuses to guess a mod path")
}

func TestDetectGames_EmptyStubACFDoesNotPrefill(t *testing.T) {
	root := steamFixture(t, "294100", "RimWorld", "RimWorld")
	writeWorkshopACF(t, root, "294100", emptyStubWorkshopACF)

	got := detectOne(t, steam.DetectOptions{IncludeUnknown: true})
	assert.Empty(t, got.Sources, "nothing subscribed means nothing to track")
	assert.Zero(t, got.WorkshopItems)
}

func TestDetectGames_NoWorkshopManifestDoesNotPrefill(t *testing.T) {
	steamFixture(t, "9999990", "Uncurated Example Game", "UncuratedExampleGame")

	got := detectOne(t, steam.DetectOptions{IncludeUnknown: true})
	assert.Empty(t, got.Sources)
	assert.Zero(t, got.WorkshopItems)
}

func TestDetectGames_NoWorkshopOptionSuppressesThePrefill(t *testing.T) {
	root := steamFixture(t, "1133870", "Space Engineers 2", "SpaceEngineers2")
	writeWorkshopACF(t, root, "1133870", populatedWorkshopACF)

	got := detectOne(t, steam.DetectOptions{IncludeUnknown: true, NoWorkshop: true})
	assert.Empty(t, got.Sources)
	assert.Zero(t, got.WorkshopItems, "--no-workshop means lmm does not even report the count")
}

func TestDetectGames_PrefillAddsToACuratedSourcesMap(t *testing.T) {
	root := steamFixture(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	writeWorkshopACF(t, root, "489830", populatedWorkshopACF)

	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "steam-games.yaml"), []byte(`
"489830":
  slug: skyrim-se
  name: Skyrim Special Edition
  mod_path: Data
  sources:
    nexusmods: skyrimspecialedition
    curseforge: skyrim
`), 0o644))

	games, _, err := steam.DetectGames(configDir, steam.DetectOptions{})
	require.NoError(t, err)
	require.Len(t, games, 1)
	assert.Equal(t, map[string]string{
		"nexusmods":     "skyrimspecialedition",
		"curseforge":    "skyrim",
		"steamworkshop": "489830",
	}, games[0].Sources, "the workshop entry is ADDED to the curated map, never replaces it")
	assert.Equal(t, 2, games[0].WorkshopItems)
	assert.True(t, games[0].Known)
}

func TestDetectGames_PrefillKeepsTheNexusDerivationForANilSourcesEntry(t *testing.T) {
	root := steamFixture(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	writeWorkshopACF(t, root, "489830", populatedWorkshopACF)

	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "steam-games.yaml"), []byte(`
"489830":
  slug: skyrim-se
  name: Skyrim Special Edition
  mod_path: Data
  nexus_id: skyrimspecialedition
`), 0o644))

	games, _, err := steam.DetectGames(configDir, steam.DetectOptions{})
	require.NoError(t, err)
	require.Len(t, games, 1)
	assert.Equal(t, map[string]string{
		"nexusmods":     "skyrimspecialedition",
		"steamworkshop": "489830",
	}, games[0].Sources,
		"materialising the map must not silence core's {nexusmods: NexusID} derivation")
}
