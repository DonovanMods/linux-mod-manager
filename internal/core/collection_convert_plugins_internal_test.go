package core

// #611 C6: a Nexus recipe's plugin list and plugin rules become the
// profile's plugins: block (#616). The curator's ordered list is the
// order; the LOOT-userlist rules land as written - groups, the groups'
// own after-edges and each plugin's group, after and req - and P3's sort
// turns group membership into edges between the member plugins.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginRecipe is a one-mod recipe carrying plugins and rules.
func pluginRecipe(plugins []source.ManifestPlugin, rules source.ManifestPluginRules) *source.CollectionManifest {
	return &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")},
		Plugins: plugins, PluginRules: rules}
}

// tier1 is convertInputFor for a game whose plugin list lmm manages.
func tier1(m *source.CollectionManifest) convertInput {
	in := convertInputFor(m)
	in.pluginOrder = true
	return in
}

func TestConvert_PluginOrderBecomesTheProfilesPluginList(t *testing.T) {
	m := pluginRecipe(
		[]source.ManifestPlugin{{Name: "A.esp", Enabled: true}, {Name: "b.ESP", Enabled: false}, {Name: "C.esm", Enabled: true}, {Name: "a.ESP", Enabled: false}, {Name: " "}},
		source.ManifestPluginRules{
			Plugins: []source.ManifestPluginRule{
				{Name: "B.esp", Group: "Late Loaders", After: []string{"a.esp"}, Req: []string{"C.esm"}},
				{Name: "C.esm", Group: "default"},
			},
			Groups: []source.ManifestPluginGroup{{Name: "Late Loaders", After: []string{"default"}}, {Name: "default"}},
		})
	c := mustConvert(t, tier1(m))
	assert.Empty(t, c.Unsupported)
	assert.Contains(t, c.Features, CollectionFeaturePluginOrder)
	assert.Contains(t, c.Document.Requires, CollectionFeaturePluginOrder)
	assert.Equal(t, domain.ProfilePlugins{
		Order: []domain.PluginEntry{{Name: "A.esp", Enabled: true}, {Name: "b.ESP", Enabled: false}, {Name: "C.esm", Enabled: true}},
		Rules: domain.PluginRules{
			Groups: []domain.PluginGroup{{Name: "Late Loaders", After: []string{"default"}}, {Name: "default"}},
			Plugins: []domain.PluginRule{
				{Name: "B.esp", Group: "Late Loaders", After: []string{"a.esp"}, Req: []string{"C.esm"}},
				{Name: "C.esm", Group: "default"},
			},
		},
	}, c.Document.Plugins, "names match case-insensitively, so a.ESP is A.esp again and the first entry wins; a blank name is no plugin")
	assert.True(t, containsSubstring(c.Notes, "listed more than once"), "%v", c.Notes)
}

// Rules alone (no list) are a plugin order too: they order the plugins
// the profile deploys.
func TestConvert_PluginRulesAloneArePluginOrder(t *testing.T) {
	c := mustConvert(t, tier1(pluginRecipe(nil, source.ManifestPluginRules{Groups: []source.ManifestPluginGroup{{Name: "Fixes"}}})))
	assert.Empty(t, c.Unsupported)
	assert.Equal(t, []domain.PluginGroup{{Name: "Fixes"}}, c.Document.Plugins.Rules.Groups)
	assert.Empty(t, c.Document.Plugins.Order)

	c = mustConvert(t, tier1(pluginRecipe(nil, source.ManifestPluginRules{})))
	assert.NotContains(t, c.Features, CollectionFeaturePluginOrder, "no list and no rules is no plugin order")
	assert.True(t, c.Document.Plugins.IsZero())
}

// plugin_order is supported only where lmm manages the game's plugin list
// (a Tier 1 PluginOrderer game): anywhere else the list would be saved
// and never written, the "looks right, plays wrong" outcome the gate
// exists to refuse.
func TestConvert_PluginOrderNeedsAGameWithAPluginList(t *testing.T) {
	require.Contains(t, SupportedCollectionFeatures(), CollectionFeaturePluginOrder, "the document format supports it")
	m := pluginRecipe([]source.ManifestPlugin{{Name: "A.esp", Enabled: true}}, source.ManifestPluginRules{})
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{CollectionFeaturePluginOrder}, featureNames(c.Unsupported))
	assert.Empty(t, c.Unsupported[0].Items, "the collection's, not an item's")

	assert.Empty(t, mustConvert(t, tier1(m)).Unsupported)
}

// collectionConfig.excludePluginRules: the curator asks that the rules
// not be applied. The list is still the collection's load order.
func TestConvert_ExcludePluginRulesLeavesTheRulesOut(t *testing.T) {
	m := pluginRecipe([]source.ManifestPlugin{{Name: "A.esp", Enabled: true}},
		source.ManifestPluginRules{
			Plugins: []source.ManifestPluginRule{{Name: "A.esp", After: []string{"B.esp"}, Inc: []string{"C.esp"}}},
			Groups:  []source.ManifestPluginGroup{{Name: "Late"}},
		})
	m.ExcludePluginRules = true
	c := mustConvert(t, tier1(m))
	assert.Empty(t, c.Unsupported, "an excluded incompatible rule is not applied, so it needs nothing")
	assert.Equal(t, []domain.PluginEntry{{Name: "A.esp", Enabled: true}}, c.Document.Plugins.Order)
	assert.True(t, c.Document.Plugins.Rules.IsZero())
	assert.True(t, containsSubstring(c.Notes, "excludePluginRules"), "%v", c.Notes)

	// Nothing to leave out, nothing to say.
	m = pluginRecipe([]source.ManifestPlugin{{Name: "A.esp", Enabled: true}}, source.ManifestPluginRules{})
	m.ExcludePluginRules = true
	assert.False(t, containsSubstring(mustConvert(t, tier1(m)).Notes, "excludePluginRules"))
}

// An incompatible rule (LOOT's inc, Vortex's incompatible) has no place in
// the profile's rule model, so it is refused rather than dropped
// (coordinator ruling, #611): plugin_rules_incompatible.
func TestConvert_IncompatiblePluginRulesAreRefused(t *testing.T) {
	m := pluginRecipe([]source.ManifestPlugin{{Name: "A.esp", Enabled: true}},
		source.ManifestPluginRules{Plugins: []source.ManifestPluginRule{{Name: "A.esp", Req: []string{"B.esp"}, Inc: []string{"C.esp"}}}})
	c := mustConvert(t, tier1(m))
	assert.Equal(t, []string{CollectionFeaturePluginRulesIncompatible}, featureNames(c.Unsupported))
	assert.Contains(t, c.Features, CollectionFeaturePluginOrder)
	assert.Equal(t, []domain.PluginRule{{Name: "A.esp", Req: []string{"B.esp"}}}, c.Document.Plugins.Rules.Plugins,
		"the rest of the rule is still the document's")
	assert.NotContains(t, SupportedCollectionFeatures(), CollectionFeaturePluginRulesIncompatible)
}

// The converter's plugin names are matched against deployed files, so
// one L1 would refuse is refused here too, never cleaned into another
// plugin's name.
func TestConvert_RefusesPluginNamesL1Refuses(t *testing.T) {
	m := pluginRecipe([]source.ManifestPlugin{{Name: "../A.esp", Enabled: true}}, source.ManifestPluginRules{})
	_, err := convertCollection(tier1(m))
	assert.ErrorIs(t, err, ErrInvalidCollection)
}
