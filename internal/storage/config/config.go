package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"go.yaml.in/yaml/v3"
)

// Config holds global application settings
type Config struct {
	DefaultLinkMethod domain.LinkMethod `yaml:"-"`
	LinkMethodStr     string            `yaml:"default_link_method"`
	DefaultGame       string            `yaml:"default_game"`

	// Keybindings is parsed but ignored: it was reserved for the TUI v2
	// removed. `omitempty`, and no default applied at Load, so an existing
	// config.yaml that sets it still parses (and round-trips) while a
	// config.yaml lmm creates is never written with a setting for a
	// feature that does not exist (#390).
	Keybindings string `yaml:"keybindings,omitempty"`

	// CachePath overrides the default mod cache directory. `omitempty` for
	// the same reason: unset IS the default, so a fresh save should not
	// spell it out as an empty string (#390).
	CachePath   string `yaml:"cache_path,omitempty"`
	HookTimeout int    `yaml:"hook_timeout"`

	// AutoSnapshot records a snapshot before each deploy, profile switch
	// and update (#350). OFF by default in 2.0: every one of those
	// operations reads the whole deployed tree to hash it, which on a large
	// install is a real cost to pay on every deploy - and a user who wants
	// the safety net can say so once. The failure of an automatic snapshot
	// is never fatal to the operation it precedes: a backup that blocks
	// what it is protecting is worse than no backup.
	AutoSnapshot bool `yaml:"auto_snapshot"`

	// AutoSnapshotKeep is how many AUTOMATIC snapshots a game keeps: the
	// newest N survive each automatic snapshot, the rest are deleted.
	// Default 10; 0 means unlimited. Only automatic ones are ever pruned -
	// a snapshot you named is yours until you delete it.
	//
	// Coordinator ruling on the #350 review's note 13: "nothing prunes
	// them" was disclosed rather than hidden, but an opt-in that grows a
	// directory without bound for as long as it is on is a slow leak, and
	// the whole point of the automatic ones is that you do not think about
	// them.
	AutoSnapshotKeep int `yaml:"auto_snapshot_keep"`

	// UpdateCheck turns lmm's once-a-day check for a newer lmm release on
	// or off (#568). ON by default, so a pointer: nil (the key absent) is
	// the default and is never written back, while an explicit
	// `update_check: false` is the opt-out and survives every save. Read
	// it through UpdateCheckEnabled.
	UpdateCheck *bool `yaml:"update_check,omitempty"`

	// AutoUpdate lets lmm install a new lmm release by itself (#569,
	// #586): after a CLI command whose release check found one, from `lmm
	// serve`'s background refresh, and from lmm-update.timer. ON by default
	// since #586, so a pointer like UpdateCheck: nil (the key absent) is the
	// default and is never written back, while an explicit `auto_update:
	// false` is the opt-out and survives every save. It applies only to a
	// self-managed install - a package's lmm is never replaced. Read it
	// through AutoUpdateEnabled.
	AutoUpdate *bool `yaml:"auto_update,omitempty"`

	// Cache holds the mod cache's own settings (#678). `omitempty`: a
	// config.yaml that never set one is saved without the block.
	Cache CacheSettings `yaml:"cache,omitempty"`
}

// CacheSettings is config.yaml's cache block (#678).
type CacheSettings struct {
	// KeepPreviousVersions is how many PREVIOUS cached versions of each
	// mod the cache keeps, the newest ones, on top of the installed
	// versions (which are always kept, as is every version a profile or
	// snapshot references). nil (the key absent) is
	// DefaultCacheKeepPreviousVersions; 0 keeps only the installed
	// versions; unlimited (or a negative count) keeps every version. Read
	// it through Config.CacheKeepPreviousVersions.
	KeepPreviousVersions *KeepPreviousVersions `yaml:"keep_previous_versions,omitempty"`
}

// DefaultCacheKeepPreviousVersions is cache.keep_previous_versions' value
// when config.yaml does not set it (#678, the owner's ruling: the
// installed version plus the last 3 updates).
const DefaultCacheKeepPreviousVersions = 3

// UnlimitedPreviousVersions is CacheKeepPreviousVersions' answer for a
// cache that keeps every version: nothing is ever pruned.
const UnlimitedPreviousVersions = -1

// KeepPreviousVersions is cache.keep_previous_versions: a count of
// previous versions, or UnlimitedPreviousVersions. It reads an integer or
// the word "unlimited" (a negative integer is unlimited too) and writes
// unlimited as the word.
type KeepPreviousVersions int

// UnmarshalYAML reads an integer or "unlimited".
func (k *KeepPreviousVersions) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Value == "unlimited" {
		*k = UnlimitedPreviousVersions
		return nil
	}
	n, err := strconv.Atoi(node.Value)
	if node.Kind != yaml.ScalarNode || err != nil {
		return fmt.Errorf("cache.keep_previous_versions: %q is not a number of versions (a count, or unlimited)", node.Value)
	}
	*k = KeepPreviousVersions(max(n, UnlimitedPreviousVersions))
	return nil
}

// MarshalYAML writes unlimited as the word.
func (k KeepPreviousVersions) MarshalYAML() (any, error) {
	if k < 0 {
		return "unlimited", nil
	}
	return int(k), nil
}

// CacheKeepPreviousVersions is how many previous versions of each mod the
// cache keeps on top of the installed ones (#678):
// DefaultCacheKeepPreviousVersions unless config.yaml says otherwise, and
// UnlimitedPreviousVersions for no limit.
func (c *Config) CacheKeepPreviousVersions() int {
	if c.Cache.KeepPreviousVersions == nil {
		return DefaultCacheKeepPreviousVersions
	}
	return max(int(*c.Cache.KeepPreviousVersions), UnlimitedPreviousVersions)
}

// SetCacheKeepPreviousVersions sets cache.keep_previous_versions; any
// negative count is unlimited.
func (c *Config) SetCacheKeepPreviousVersions(n int) {
	k := KeepPreviousVersions(max(n, UnlimitedPreviousVersions))
	c.Cache.KeepPreviousVersions = &k
}

// UpdateCheckEnabled reports whether lmm may check GitHub for a newer
// release: true unless config.yaml says `update_check: false`.
func (c *Config) UpdateCheckEnabled() bool {
	return c.UpdateCheck == nil || *c.UpdateCheck
}

// AutoUpdateEnabled reports whether lmm may install a new release by
// itself: true unless config.yaml says `auto_update: false`.
func (c *Config) AutoUpdateEnabled() bool {
	return c.AutoUpdate == nil || *c.AutoUpdate
}

// DefaultAutoSnapshotKeep is AutoSnapshotKeep's value when config.yaml
// does not set it: enough automatic snapshots to cover a session's worth
// of deploys, few enough that the directory stays legible.
const DefaultAutoSnapshotKeep = 10

// Load reads configuration from the given directory
func Load(configDir string) (*Config, error) {
	cfg := &Config{
		DefaultLinkMethod: domain.LinkSymlink,
		HookTimeout:       60, // Default 60 seconds
		// Pre-set, so an ABSENT auto_snapshot_keep keeps the default
		// while an explicit `auto_snapshot_keep: 0` still means
		// unlimited.
		AutoSnapshotKeep: DefaultAutoSnapshotKeep,
	}

	configPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil // Return defaults
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := safeyaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %s: %w", configPath, err)
	}

	// Convert string to LinkMethod
	if cfg.LinkMethodStr != "" {
		method, ok := domain.ParseLinkMethod(cfg.LinkMethodStr)
		if !ok {
			return nil, fmt.Errorf("%w: config.yaml: default_link_method %q (valid: %s)",
				domain.ErrInvalidLinkMethod, cfg.LinkMethodStr, domain.ValidLinkMethods)
		}
		cfg.DefaultLinkMethod = method
	}

	// Expand ~ in cache path
	if cfg.CachePath != "" {
		cfg.CachePath = ExpandPath(cfg.CachePath)
	}

	return cfg, nil
}

// Save writes configuration to the given directory
func (c *Config) Save(configDir string) error {
	c.LinkMethodStr = c.DefaultLinkMethod.String()

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	return replaceFile(configPath, data, 0644, "config")
}
