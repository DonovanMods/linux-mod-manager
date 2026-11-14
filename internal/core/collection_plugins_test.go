package core_test

// #616 x #621: a profile's plugins: block travels in the lmm collection
// format. An export carries it and declares `requires: plugin_order`, so an
// lmm without plugin lists refuses the document rather than importing it
// with the load order dropped; an import keeps it as written, and refuses a
// stranger's plugin names it could not keep as written.

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

var collectionPlugins = domain.ProfilePlugins{
	Order: []domain.PluginEntry{
		{Name: "Unofficial Skyrim Special Edition Patch.esp", Enabled: true},
		{Name: "SkyUI_SE.esp", Enabled: true},
		{Name: "Optional Thing.esp", Enabled: false},
	},
	Rules: domain.PluginRules{
		Groups:  []domain.PluginGroup{{Name: "Late Loaders", After: []string{"default"}}},
		Plugins: []domain.PluginRule{{Name: "Patch.esp", Group: "Late Loaders", After: []string{"SkyUI_SE.esp"}}},
	},
}

// TestExportCollection_PluginsRequirePluginOrder is the version-1
// export/import round trip for the plugins: block, against its golden.
func TestExportCollection_PluginsRequirePluginOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "cp", Name: "Cyberpunk 2077", ModPath: t.TempDir(), SourceIDs: map[string]string{"steamworkshop": "1091500"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	profile := &domain.Profile{
		Name: "plugins", GameID: game.ID,
		Mods:       []domain.ModReference{{SourceID: "nexusmods", ModID: "1", Version: "1.0"}},
		Collection: &domain.CollectionInfo{ID: "2f8c7a3e-0000-4000-8000-000000000003", Version: 1},
		Plugins:    collectionPlugins,
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	export, err := svc.ExportCollection(context.Background(), game.ID, "plugins", core.CollectionExportOptions{})
	require.NoError(t, err)
	checkCollectionGolden(t, "export_v1_plugins", export.Document)
	require.Contains(t, string(export.Document), "requires:\n    - plugin_order\n")

	recv, _ := recipient(t)
	plan, err := recv.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: export.Document, GameID: "cyber"})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Unsupported, "plugin_order is in the supported set")
	require.Contains(t, core.SupportedCollectionFeatures(), core.CollectionFeaturePluginOrder)
	require.Equal(t, collectionPlugins, plan.Profile.Plugins, "the plan previews the list it will save")
	_, err = recv.ApplyCollectionImport(context.Background(), &domain.Game{ID: "cyber"}, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(recv.ConfigDir(), "cyber", "plugins")
	require.NoError(t, err)
	require.Equal(t, collectionPlugins, saved.Plugins, "the list is saved as written")

	// The round trip: the imported profile exports the same plugins: block.
	again, err := recv.ExportCollection(context.Background(), "cyber", "plugins", core.CollectionExportOptions{})
	require.NoError(t, err)
	reparsed, err := config.ParseCollection(again.Document)
	require.NoError(t, err)
	require.Equal(t, collectionPlugins, reparsed.Plugins)
	require.Equal(t, []string{core.CollectionFeaturePluginOrder}, reparsed.Requires)
}

func collectionPluginsDoc(pluginsBlock string) string {
	return "lmm_collection: 1\nname: p\ngame:\n  steam_app_id: \"1091500\"\nrequires: [plugin_order]\n" +
		"mods:\n  - {source_id: s, mod_id: m}\nplugins: " + pluginsBlock + "\n"
}

// TestCollectionImport_UndeclaredPluginOrderIsDerived: a hand-edited
// document that carries a plugin list but drops the feature is gated on
// it all the same.
func TestCollectionImport_UndeclaredPluginOrderIsDerived(t *testing.T) {
	svc, _ := recipient(t)
	doc := strings.Replace(collectionPluginsDoc("{order: [{name: A.esp}]}"), "requires: [plugin_order]\n", "", 1)
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
	require.NoError(t, err)
	require.Contains(t, plan.Collection.Requires, core.CollectionFeaturePluginOrder)
}

// TestCollectionImport_RefusesPluginNamesItCannotKeepAsWritten: plugin
// names are matched against deployed files, so none is rewritten.
func TestCollectionImport_RefusesPluginNamesItCannotKeepAsWritten(t *testing.T) {
	svc, _ := recipient(t)
	for name, block := range map[string]string{
		"bidi in an order name":    "{order: [{name: \"A\u202e.esp\"}]}",
		"newline in an order name": "{order: [{name: \"A\\n*B.esp\"}]}",
		"path in an order name":    "{order: [{name: ../A.esp}]}",
		"empty order name":         "{order: [{name: \"\"}]}",
		"long rule name":           "{rules: {plugins: [{name: " + strings.Repeat("x", 2000) + "}]}}",
		"escape in an after name":  "{rules: {plugins: [{name: A.esp, after: [\"B\\e.esp\"]}]}}",
		"zero-width group":         "{rules: {groups: [{name: \"G\u200b\"}]}}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(collectionPluginsDoc(block)), GameID: "cyber"})
			require.ErrorIs(t, err, core.ErrInvalidCollection)
			require.Contains(t, err.Error(), "its plugins:")
		})
	}
	_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{
		Data: []byte(collectionPluginsDoc("{order: [{name: \"Café – Ultra.esp\", enabled: false}], rules: {groups: [{name: Late, after: [default]}]}}")), GameID: "cyber"})
	require.NoError(t, err, "unusual but plain names import")
}
