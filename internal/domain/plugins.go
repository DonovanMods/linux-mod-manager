package domain

import "slices"

// The profile's plugin list (#616, design docs/plans/2026-11-10-collections-
// design.md §5): a second ordering axis beside the profile's mods. Mod order
// decides which mod's FILE wins at deploy; plugin order decides which
// .esm/.esp/.esl files a Bethesda game loads, in what order, and which are
// active. One mod can ship several plugins, so the one cannot be derived
// from the other.
//
// The list is the user's INTENT, not the effective order: core derives the
// order it writes to the game's Plugins.txt from it, the profile's rules and
// the plugins actually deployed, and never writes that result back here.

// ProfilePlugins is a profile's `plugins:` block.
type ProfilePlugins struct {
	// Order is the load order the user chose, first loads first. Implicit
	// plugins (the game's own masters) are never listed. An entry whose
	// plugin is not deployed is kept, so disabling and re-enabling a mod
	// does not lose its plugin's place.
	Order []PluginEntry `json:"order,omitempty"`
	// Rules are LOOT-userlist-shaped ordering rules, so a collection's
	// plugin rules land verbatim.
	Rules PluginRules `json:"rules,omitzero"`
}

// PluginEntry is one plugin in a profile's plugin order.
type PluginEntry struct {
	// Name is the plugin's file name ("SkyUI_SE.esp"), compared
	// case-insensitively, as the game does.
	Name string `json:"name"`
	// Enabled is whether the plugin is active. A profile file records only
	// `enabled: false`; an entry without the key is enabled.
	Enabled bool `json:"enabled"`
}

// PluginRules are a profile's plugin ordering rules, in the shape of a LOOT
// userlist: groups that load after other groups, and per-plugin rules.
type PluginRules struct {
	// Groups are the rule groups. A plugin no rule places in a group is in
	// the group named PluginGroupDefault.
	Groups []PluginGroup `yaml:"groups,omitempty" json:"groups,omitempty"`
	// Plugins are the per-plugin rules.
	Plugins []PluginRule `yaml:"plugins,omitempty" json:"plugins,omitempty"`
}

// PluginGroupDefault is the group every plugin is in unless a rule says
// otherwise, as in LOOT.
const PluginGroupDefault = "default"

// PluginGroup is a named group of plugins that loads after other groups.
type PluginGroup struct {
	Name string `yaml:"name" json:"name"`
	// After names the groups whose plugins all load before this group's.
	After []string `yaml:"after,omitempty" json:"after,omitempty"`
}

// PluginRule is one plugin's rule.
type PluginRule struct {
	// Name is the plugin the rule is about.
	Name string `yaml:"name" json:"name"`
	// Group places the plugin in a group; empty is PluginGroupDefault.
	Group string `yaml:"group,omitempty" json:"group,omitempty"`
	// After names plugins this one loads after, when they are present.
	After []string `yaml:"after,omitempty" json:"after,omitempty"`
	// Req names plugins this one requires: it loads after them, as with
	// After, and a requirement that is not active is reported.
	Req []string `yaml:"req,omitempty" json:"req,omitempty"`
}

// IsZero reports whether r holds no rule.
func (r PluginRules) IsZero() bool {
	return len(r.Groups) == 0 && len(r.Plugins) == 0
}

// IsZero reports whether p records nothing: no order and no rules.
func (p ProfilePlugins) IsZero() bool {
	return len(p.Order) == 0 && p.Rules.IsZero()
}

// Equal reports whether p and other record the same list, an empty list
// and none alike.
func (p ProfilePlugins) Equal(other ProfilePlugins) bool {
	if !slices.Equal(p.Order, other.Order) {
		return false
	}
	return slices.EqualFunc(p.Rules.Groups, other.Rules.Groups, func(a, b PluginGroup) bool {
		return a.Name == b.Name && slices.Equal(a.After, b.After)
	}) && slices.EqualFunc(p.Rules.Plugins, other.Rules.Plugins, func(a, b PluginRule) bool {
		return a.Name == b.Name && a.Group == b.Group && slices.Equal(a.After, b.After) && slices.Equal(a.Req, b.Req)
	})
}

// Clone returns a copy of p that shares no slice with it.
func (p ProfilePlugins) Clone() ProfilePlugins {
	out := ProfilePlugins{Order: slices.Clone(p.Order)}
	if len(p.Rules.Groups) > 0 {
		out.Rules.Groups = make([]PluginGroup, len(p.Rules.Groups))
		for i, g := range p.Rules.Groups {
			out.Rules.Groups[i] = PluginGroup{Name: g.Name, After: slices.Clone(g.After)}
		}
	}
	if len(p.Rules.Plugins) > 0 {
		out.Rules.Plugins = make([]PluginRule, len(p.Rules.Plugins))
		for i, r := range p.Rules.Plugins {
			out.Rules.Plugins[i] = PluginRule{Name: r.Name, Group: r.Group, After: slices.Clone(r.After), Req: slices.Clone(r.Req)}
		}
	}
	return out
}
