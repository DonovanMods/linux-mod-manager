package core_test

// #607: a version-1 document whose items state a mod type ("dinput")
// requires mod_type:<type>, and the requirement is met per game: a game
// whose adapter routes the type imports it, any other refuses it, so an
// older or differently-configured lmm never deploys a script extender
// under Data/.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const typedDocument = `lmm_collection: 1
name: typed
game:
  sources: {nexusmods: skyrimspecialedition}
requires: ["mod_type:dinput"]
mods:
  - source_id: nexusmods
    mod_id: "30379"
    version: 2.2.6
    mod_type: dinput
`

func typedGame(t *testing.T, svc *core.Service, adapterID string) *domain.Game {
	t.Helper()
	svc.RegisterAdapter(bethesda.New())
	install := t.TempDir()
	game := &domain.Game{ID: "sse", Name: "Skyrim SE", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		Adapter: adapterID, SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return game
}

func TestCollectionImport_ModTypeIsSupportedPerGame(t *testing.T) {
	svc := newFlowsTestService(t)
	game := typedGame(t, svc, "bethesda")
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(typedDocument)})
	require.NoError(t, err)
	assert.Equal(t, []string{"mod_type:dinput"}, plan.Collection.Requires)
	assert.Empty(t, plan.Collection.Unsupported, "the bethesda adapter routes dinput to the game root")
	assert.Equal(t, "dinput", plan.Profile.Mods[0].ModType)

	_, err = svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "typed")
	require.NoError(t, err)
	assert.Equal(t, "dinput", saved.Mods[0].ModType, "the profile keeps the stated type")
}

func TestCollectionImport_ModTypeRefusedWhereNoAdapterRoutesIt(t *testing.T) {
	svc := newFlowsTestService(t)
	game := typedGame(t, svc, "")
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(typedDocument)})
	require.NoError(t, err)
	require.Len(t, plan.Collection.Unsupported, 1)
	assert.Equal(t, "mod_type:dinput", plan.Collection.Unsupported[0].Feature)
	_, err = svc.ApplyCollectionImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	assert.True(t, core.IsCollectionUnsupported(err))

	// An undeclared type is derived from the content, as every feature is.
	undeclared := []byte(`lmm_collection: 1
name: typed
game:
  sources: {nexusmods: skyrimspecialedition}
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1", mod_type: enb}
`)
	plan, err = svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: undeclared})
	require.NoError(t, err)
	assert.Equal(t, []string{"mod_type:enb"}, plan.Collection.Requires)
	require.Len(t, plan.Collection.Unsupported, 1)
}

func TestCollectionExport_DeclaresStatedModTypes(t *testing.T) {
	svc := newFlowsTestService(t)
	game := typedGame(t, svc, "bethesda")
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "p")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "p", domain.ModReference{SourceID: "nexusmods", ModID: "30379", Version: "2.2.6", ModType: "dinput"}))
	export, err := svc.ExportCollection(context.Background(), game.ID, "p", core.CollectionExportOptions{})
	require.NoError(t, err)
	doc, err := config.ParseCollection(export.Document)
	require.NoError(t, err)
	assert.Contains(t, doc.Requires, "mod_type:dinput")
	assert.Equal(t, "dinput", doc.Items[0].Ref.ModType)
}
