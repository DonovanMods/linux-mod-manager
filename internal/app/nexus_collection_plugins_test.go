package app

// A NexusMods collection's plugin order, end to end (#611 C6): the real
// nexusmods recipe parser meets the real converter, over the survey's
// pluginRules sample and over hand-written recipes in both spellings of the
// rule keys, and the imported profile's plugins: block exports back out in
// the lmm collection format.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateAppCollectionGoldens = flag.Bool("update-app-collection-goldens", false, "rewrite internal/app/testdata/collection/*.golden")

// checkAppCollectionGolden compares got with testdata/collection/<name>.golden.
func checkAppCollectionGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "collection", name+".golden")
	if *updateAppCollectionGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update-app-collection-goldens", path)
	require.Equal(t, string(want), string(got))
}

// fallout4Game is a Fallout 4 the bethesda adapter identifies, so lmm
// manages its plugin list.
func fallout4Game(t *testing.T) *domain.Game {
	return &domain.Game{ID: "fo4", Name: "Fallout 4", InstallPath: t.TempDir(), ModPath: t.TempDir(), Adapter: "bethesda",
		Catalog: "fallout4", SourceIDs: map[string]string{"nexusmods": "fallout4"}}
}

// TestNexusCollection_PluginRuleSpellings: LOOT's req/inc and Vortex's
// requires/incompatible convert to the same document. req is an ordering
// edge the profile's rules keep; inc has no place in them, so the import
// is refused for it (plugin_rules_incompatible) rather than run without it.
func TestNexusCollection_PluginRuleSpellings(t *testing.T) {
	for _, spelling := range []string{"loot", "vortex"} {
		t.Run(spelling, func(t *testing.T) {
			c := convertNexusFixture(t, fallout4Game(t), "rules", "rules", 1, "",
				"testdata/collections/plugin-rules-"+spelling+".json", core.CollectionConvertOptions{})
			assert.Equal(t, []string{core.CollectionFeaturePluginRulesIncompatible}, unsupportedNames(c))
			assert.Contains(t, c.Features, core.CollectionFeaturePluginOrder)
			assert.Equal(t, domain.ProfilePlugins{
				Order: []domain.PluginEntry{{Name: "BasePatch.esm", Enabled: true}, {Name: "LateFix.esp", Enabled: true}, {Name: "LateFix - Optional.esp"}},
				Rules: domain.PluginRules{
					Groups: []domain.PluginGroup{{Name: "Late Loaders", After: []string{"default"}}, {Name: "default"}},
					Plugins: []domain.PluginRule{
						{Name: "LateFix.esp", Group: "Late Loaders", After: []string{"basepatch.esm"}, Req: []string{"BasePatch.esm"}},
						{Name: "LateFix - Optional.esp", Group: "Late Loaders"},
					},
				},
			}, c.Document.Plugins)
			data, err := config.MarshalCollection(c.Document)
			require.NoError(t, err)
			checkAppCollectionGolden(t, "convert_plugin_rules_"+spelling, data)
		})
	}
}

// TestNexusCollection_PluginOrderRoundTrip is the survey's real
// pluginRules shape (Fallout 4, kem16d): planned and imported as a profile
// whose plugins: block is the curator's order and rules, then exported in
// the lmm collection format - declaring plugin_order - and read back the
// same, against its golden.
func TestNexusCollection_PluginOrderRoundTrip(t *testing.T) {
	game := fallout4Game(t)
	sandboxHome(t)
	srv, client := nexusCollectionServer(t, "kem16d", 143, "", "fallout4-kem16d-143.json")
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(nexusmods.New(client, "testapikey"))
	RegisterAdapters(svc)
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, game))

	plan, err := svc.PlanSourceCollectionImport(ctx, game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "kem16d"})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Unsupported)
	want := plan.Collection.Source.Document.Plugins
	require.Len(t, want.Order, 113, "115 listed, two of them twice")
	require.Len(t, want.Rules.Plugins, 36-1, "one rule only named a group-less, edge-less plugin")
	require.Len(t, want.Rules.Groups, 7)
	require.NotNil(t, plan.Collection.Plugins)
	assert.Equal(t, core.CollectionPluginSummary{Plugins: 113, Active: 113, Rules: 35, Groups: []string{
		"Underrides", "PreVis & PreCombined Fixes", "Low Priority Overrides", "default", "Worldspace Settings", "Resources & Frameworks", "Fixes",
	}, Managed: true}, *plan.Collection.Plugins)

	_, err = svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, plan.Profile.Name)
	require.NoError(t, err)
	assert.Equal(t, want, saved.Plugins, "the profile records the collection's plugin list as converted")

	export, err := svc.ExportCollection(ctx, game.ID, plan.Profile.Name, core.CollectionExportOptions{})
	require.NoError(t, err)
	checkAppCollectionGolden(t, "export_v1_nexus_plugins", export.Document)
	reparsed, err := config.ParseCollection(export.Document)
	require.NoError(t, err)
	assert.Equal(t, want, reparsed.Plugins)
	assert.Contains(t, reparsed.Requires, core.CollectionFeaturePluginOrder)
}
