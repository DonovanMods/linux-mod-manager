package config

import "github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

// ProfilePluginsConfig is a profile's `plugins:` block as its file records
// it (#616): the load order, and LOOT-userlist-shaped rules.
//
//	plugins:
//	  order:
//	    - name: SkyUI_SE.esp
//	    - name: Optional Thing.esp
//	      enabled: false
//	  rules:
//	    groups:
//	      - { name: Late Loaders, after: [default] }
//	    plugins:
//	      - { name: Patch.esp, group: Late Loaders, after: [SkyUI_SE.esp] }
type ProfilePluginsConfig struct {
	Order []PluginEntryConfig `yaml:"order,omitempty"`
	Rules domain.PluginRules  `yaml:"rules,omitempty"`
}

// PluginEntryConfig is one entry of a profile's plugin order. Enabled is a
// pointer so an entry without the key - the usual case - reads as enabled,
// and only `enabled: false` is ever written.
type PluginEntryConfig struct {
	Name    string `yaml:"name"`
	Enabled *bool  `yaml:"enabled,omitempty"`
}

// pluginsFromConfig is the domain plugin list c records; nil records none.
func pluginsFromConfig(c *ProfilePluginsConfig) domain.ProfilePlugins {
	if c == nil {
		return domain.ProfilePlugins{}
	}
	out := domain.ProfilePlugins{Rules: c.Rules}
	if len(c.Order) > 0 {
		out.Order = make([]domain.PluginEntry, len(c.Order))
		for i, e := range c.Order {
			out.Order[i] = domain.PluginEntry{Name: e.Name, Enabled: e.Enabled == nil || *e.Enabled}
		}
	}
	if out.Rules.IsZero() {
		out.Rules = domain.PluginRules{}
	}
	return out
}

// pluginsConfigOf is p as a profile file records it, or nil when p records
// nothing, so a profile without a plugin list writes no `plugins:` key.
func pluginsConfigOf(p domain.ProfilePlugins) *ProfilePluginsConfig {
	if p.IsZero() {
		return nil
	}
	out := &ProfilePluginsConfig{Rules: p.Rules}
	if len(p.Order) > 0 {
		out.Order = make([]PluginEntryConfig, len(p.Order))
		for i, e := range p.Order {
			out.Order[i] = PluginEntryConfig{Name: e.Name}
			if !e.Enabled {
				disabled := false
				out.Order[i].Enabled = &disabled
			}
		}
	}
	return out
}
