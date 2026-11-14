package core_test

// #635: the Vortex mod types a game's own extension registers are routed by
// that game's adapter table. starfield-data-folder is Starfield's "Data
// Folder" type: its files are relative to Data/, Starfield's mod_path, so a
// collection that states it imports for Starfield, deploys under Data, and
// is still refused for any other Bethesda game.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/modtypes"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const starfieldDataFolderDocument = `lmm_collection: 1
name: typed
game:
  sources: {nexusmods: starfield}
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1", mod_type: starfield-data-folder}
`

// bethesdaGameWithID is a game under the bethesda adapter that it
// identifies by id (mod_path = install_path/Data).
func bethesdaGameWithID(t *testing.T, svc *core.Service, id, nexusDomain string) *domain.Game {
	t.Helper()
	svc.RegisterAdapter(bethesda.New())
	install := t.TempDir()
	game := &domain.Game{ID: id, Name: id, InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: domain.LinkSymlink, Adapter: "bethesda", SourceIDs: map[string]string{"nexusmods": nexusDomain}}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return game
}

func TestCollectionImport_StarfieldDataFolderImportsForStarfieldOnly(t *testing.T) {
	svc := newFlowsTestService(t)
	bethesdaGameWithID(t, svc, "starfield", "starfield")
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(starfieldDataFolderDocument)})
	require.NoError(t, err)
	assert.Equal(t, []string{"mod_type:starfield-data-folder"}, plan.Collection.Requires)
	assert.Empty(t, plan.Collection.Unsupported, "Starfield's adapter row routes its Data Folder type")
	assert.Equal(t, "starfield-data-folder", plan.Profile.Mods[0].ModType)

	other := newFlowsTestService(t)
	bethesdaGameWithID(t, other, "skyrim-se", "starfield")
	plan, err = other.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(starfieldDataFolderDocument)})
	require.NoError(t, err)
	require.Len(t, plan.Collection.Unsupported, 1, "another Bethesda game does not know Starfield's type")
	assert.Equal(t, "mod_type:starfield-data-folder", plan.Collection.Unsupported[0].Feature)
}

func TestDeployModType_StarfieldDataFolderDeploysUnderData(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := bethesdaGameWithID(t, svc, "starfield", "starfield")
	seedNamedInstalledMod(t, svc, game, "src", "ki", "KiLoader", "1.0", true, map[string][]byte{
		"KiLoader/KiLoader.dll":              []byte("loader"),
		"SFSE/Plugins/KiLoaderSatellite.dll": []byte("plugin"),
	})
	row, err := svc.GetInstalledMod(context.Background(), "src", "ki", game.ID, "default")
	require.NoError(t, err)
	row.ModType = "starfield-data-folder"
	require.NoError(t, svc.SaveInstalledMod(context.Background(), row))
	seedProfileWithMod(t, svc, game.ID, "default", "src", "ki", "1.0")

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Skipped, "the type is known for Starfield")
	assert.Equal(t, "loader", readFileString(t, filepath.Join(game.ModPath, "KiLoader", "KiLoader.dll")))
	assert.Equal(t, "plugin", readFileString(t, filepath.Join(game.ModPath, "SFSE", "Plugins", "KiLoaderSatellite.dll")))
	assertAbsent(t, filepath.Join(game.InstallPath, "KiLoader"), "nothing of it lands at the game root")
}

// typedGameUnder is a game under one of the modtypes adapters, its mod_path
// modDir inside its install_path.
func typedGameUnder(t *testing.T, svc *core.Service, a *modtypes.Adapter, modDir, nexusDomain string) *domain.Game {
	t.Helper()
	svc.RegisterAdapter(a)
	install := t.TempDir()
	game := &domain.Game{ID: a.ID(), Name: a.ID(), InstallPath: install, ModPath: filepath.Join(install, modDir),
		LinkMethod: domain.LinkSymlink, Adapter: a.ID(), SourceIDs: map[string]string{"nexusmods": nexusDomain}}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return game
}

// seedTypedMod seeds an installed, profiled mod whose row states modType.
func seedTypedMod(t *testing.T, svc *core.Service, game *domain.Game, id, modType string, files map[string][]byte) {
	t.Helper()
	seedNamedInstalledMod(t, svc, game, "src", id, id, "1.0", true, files)
	row, err := svc.GetInstalledMod(context.Background(), "src", id, game.ID, "default")
	require.NoError(t, err)
	row.ModType = modType
	require.NoError(t, svc.SaveInstalledMod(context.Background(), row))
	seedProfileWithMod(t, svc, game.ID, "default", "src", id, "1.0")
}

func typedDoc(nexusDomain, modType string) []byte {
	return []byte(`lmm_collection: 1
name: typed
game:
  sources: {nexusmods: ` + nexusDomain + `}
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1", mod_type: ` + modType + `}
`)
}

// The Witcher 3 adapter knows witcher3tl and dinput (the game directory);
// witcher3dlc and witcher3menumodroot stay refused (#635: a DLC/ root and
// Vortex's XML merge, neither of which lmm has).
func TestCollectionImport_Witcher3Types(t *testing.T) {
	for typ, routed := range map[string]bool{"witcher3tl": true, "dinput": true, "witcher3dlc": false, "witcher3menumodroot": false} {
		svc := newFlowsTestService(t)
		typedGameUnder(t, svc, modtypes.Witcher3(), "mods", "witcher3")
		plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: typedDoc("witcher3", typ)})
		require.NoError(t, err)
		if routed {
			assert.Empty(t, plan.Collection.Unsupported, typ)
		} else {
			require.Len(t, plan.Collection.Unsupported, 1, typ)
			assert.Equal(t, "mod_type:"+typ, plan.Collection.Unsupported[0].Feature)
		}
	}
}

// A witcher3tl mod is laid out from the game directory: its mods/ folder is
// the game's mod_path, and its bin/ folder lands beside it at the root.
func TestDeployModType_Witcher3TopLevel(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := typedGameUnder(t, svc, modtypes.Witcher3(), "mods", "witcher3")
	seedTypedMod(t, svc, game, "menu", "witcher3tl", map[string][]byte{
		"mods/modMenu/content/blob0.bundle":                   []byte("bundle"),
		"bin/config/r4game/user_config_matrix/pc/modMenu.xml": []byte("menu"),
	})

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, "bundle", readFileString(t, filepath.Join(game.ModPath, "modMenu", "content", "blob0.bundle")),
		"mods/ is the mod_path, not a mods/ folder inside it")
	assert.Equal(t, "menu", readFileString(t, filepath.Join(game.InstallPath, "bin", "config", "r4game", "user_config_matrix", "pc", "modMenu.xml")))
	assertAbsent(t, filepath.Join(game.ModPath, "mods"), "nothing nests a second mods/")

	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "menu", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(game.InstallPath, "bin", "config", "r4game", "user_config_matrix", "pc", "modMenu.xml"), "uninstall finds it at the root")
}

// A bg3-loose mod's files are relative to Data/, Baldur's Gate 3's mod_path.
func TestDeployModType_BG3Loose(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := typedGameUnder(t, svc, modtypes.BaldursGate3(), "Data", "baldursgate3")
	seedTypedMod(t, svc, game, "loose", "bg3-loose", map[string][]byte{"Public/Mod/Stats/x.txt": []byte("stats")})

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, "stats", readFileString(t, filepath.Join(game.ModPath, "Public", "Mod", "Stats", "x.txt")))

	svc2 := newFlowsTestService(t)
	typedGameUnder(t, svc2, modtypes.BaldursGate3(), "Data", "baldursgate3")
	plan, err := svc2.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: typedDoc("baldursgate3", "bg3-loose")})
	require.NoError(t, err)
	assert.Empty(t, plan.Collection.Unsupported)
}

// #635 review: a Witcher 3 dinput mod's bin/x64/dinput8.dll lands over the
// game's own file at the game root; the stock file is kept first, and purge
// puts it back.
func TestDeployModType_Witcher3DinputOverAStockFileIsRestoredByPurge(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := typedGameUnder(t, svc, modtypes.Witcher3(), "mods", "witcher3")
	stock := filepath.Join(game.InstallPath, "bin", "x64", "dinput8.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0o755))
	require.NoError(t, os.WriteFile(stock, []byte("the game's own dinput8"), 0o644))
	seedTypedMod(t, svc, game, "online", "dinput", map[string][]byte{
		"bin/x64/dinput8.dll":                 []byte("the mod's dinput8"),
		"mods/modOnline/content/blob0.bundle": []byte("bundle"),
	})

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, "the mod's dinput8", readFileString(t, stock), "the mod's dinput8.dll is at the game root")
	assert.Equal(t, "bundle", readFileString(t, filepath.Join(game.ModPath, "modOnline", "content", "blob0.bundle")))

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyPurge(context.Background(), game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	info, err := os.Lstat(stock)
	require.NoError(t, err, "purge puts the game's own dinput8.dll back")
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, "the game's own dinput8", readFileString(t, stock))
	assertAbsent(t, filepath.Join(game.ModPath, "modOnline"), "the mod_path content goes too")
}
