package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lmm/internal/domain"

	"gopkg.in/yaml.v3"
)

// Config holds global application settings
type Config struct {
	DefaultLinkMethod domain.LinkMethod `yaml:"-"`
	LinkMethodStr     string            `yaml:"default_link_method"`
	Keybindings       string            `yaml:"keybindings"`
	CachePath         string            `yaml:"cache_path"`
}

// Load reads configuration from the given directory
func Load(configDir string) (*Config, error) {
	cfg := &Config{
		DefaultLinkMethod: domain.LinkSymlink,
		Keybindings:       "vim",
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
		cfg.DefaultLinkMethod = domain.ParseLinkMethod(cfg.LinkMethodStr)
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
