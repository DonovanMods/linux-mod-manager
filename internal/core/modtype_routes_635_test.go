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
