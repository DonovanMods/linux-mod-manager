// collection_convert_plugins.go maps a source recipe's plugin list and
// plugin rules onto the profile's plugins: block (#611 C6; design
// docs/plans/2026-11-10-collections-design.md §5, §6).
//
// The recipe's `plugins` is the curator's load order, so it becomes the
// block's order; its `pluginRules` is a LOOT userlist, the shape the
// block's rules already have, so it lands as written: groups with their
// after-edges, and each plugin's group, after and req. Group membership and
// the groups' order become edges between the member plugins in P3's sort
// (plugins_sort.go's addGroupEdges), not here, so a group keeps meaning
// what it meant to the curator as the profile's plugins change.
package core

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// convertPlugins is m's plugin list and rules as the profile's plugins:
// block, and the collection-level features they raise: plugin_order when
// there is a list or a rule, and plugin_rules_incompatible for an
// incompatibility rule the block cannot hold. Rules the curator excluded
// (collectionConfig.excludePluginRules) are left out, with a note, and
// raise nothing.
//
// Plugin names are compared case-insensitively, as the game compares them:
// a name listed twice keeps its first place. A blank name names no plugin
// and is skipped. Every other name is kept exactly as written - it is
// matched against deployed files - and L1's checks (checkCollectionPlugins)
// refuse one that could not be.
func convertPlugins(m *source.CollectionManifest, out *ConvertedCollection) (domain.ProfilePlugins, []string) {
	var p domain.ProfilePlugins
	seen := make(map[string]bool, len(m.Plugins))
	var repeated int
	for _, pl := range m.Plugins {
		if strings.TrimSpace(pl.Name) == "" {
			continue
		}
		if seen[pluginKey(pl.Name)] {
			repeated++
			continue
		}
		seen[pluginKey(pl.Name)] = true
		p.Order = append(p.Order, domain.PluginEntry{Name: pl.Name, Enabled: pl.Enabled})
	}
	if repeated > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d plugin(s) are listed more than once in the collection's plugin order (plugin names ignore case); each keeps its first place.", repeated))
	}

	var features []string
	rules := m.PluginRules
	if m.ExcludePluginRules {
		if !rules.IsZero() {
			out.Notes = append(out.Notes, fmt.Sprintf("The curator marked the collection's plugin rules not to be applied (excludePluginRules), so its %d plugin rule(s) and %d group(s) are left out; its plugin order is kept.",
				len(rules.Plugins), len(rules.Groups)))
		}
		rules = source.ManifestPluginRules{}
	}
	for _, g := range rules.Groups {
		if strings.TrimSpace(g.Name) == "" {
			continue
		}
		p.Rules.Groups = append(p.Rules.Groups, domain.PluginGroup{Name: g.Name, After: nonBlank(g.After)})
	}
	incompatible := false
	for _, r := range rules.Plugins {
		if strings.TrimSpace(r.Name) == "" {
			continue
		}
		if len(nonBlank(r.Inc)) > 0 {
			incompatible = true
		}
		rule := domain.PluginRule{Name: r.Name, Group: r.Group, After: nonBlank(r.After), Req: nonBlank(r.Req)}
		if strings.TrimSpace(rule.Group) == "" {
			rule.Group = ""
		}
		if rule.Group == "" && len(rule.After) == 0 && len(rule.Req) == 0 {
			// A rule that places the plugin in no group and orders nothing
			// holds nothing to keep (the survey's kem16d has one: a bare
			// name with an empty after). An incompatibility on it has
			// already raised plugin_rules_incompatible above, which refuses
			// the import, so dropping the rule here loses nothing.
			continue
		}
		p.Rules.Plugins = append(p.Rules.Plugins, rule)
	}
	if len(p.Order) > 0 || !p.Rules.IsZero() {
		features = append(features, CollectionFeaturePluginOrder)
	}
	if incompatible {
		features = append(features, CollectionFeaturePluginRulesIncompatible)
	}
	return p, features
}

// nonBlank is list without its blank names, nil when none is left.
func nonBlank(list []string) []string {
	var out []string
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
