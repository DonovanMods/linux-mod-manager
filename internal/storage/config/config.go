package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"gopkg.in/yaml.v3"
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

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
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
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
