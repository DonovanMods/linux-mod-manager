package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lmm/internal/domain"

	"gopkg.in/yaml.v3"
)

// ProfileConfig is the YAML representation of a profile
type ProfileConfig struct {
	Name       string               `yaml:"name"`
	GameID     string               `yaml:"game_id"`
	Mods       []ModReferenceConfig `yaml:"mods"`
	LinkMethod string               `yaml:"link_method,omitempty"`
	IsDefault  bool                 `yaml:"is_default,omitempty"`
}

// ModReferenceConfig is the YAML representation of a mod reference
type ModReferenceConfig struct {
	SourceID string `yaml:"source_id"`
	ModID    string `yaml:"mod_id"`
	Version  string `yaml:"version,omitempty"`
}

// LoadProfile reads a profile from disk
func LoadProfile(configDir, gameID, profileName string) (*domain.Profile, error) {
	profilePath := filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.ErrProfileNotFound
		}
		return nil, fmt.Errorf("reading profile: %w", err)
	}

	var cfg ProfileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}

	profile := &domain.Profile{
		Name:       cfg.Name,
		GameID:     cfg.GameID,
		LinkMethod: domain.ParseLinkMethod(cfg.LinkMethod),
		IsDefault:  cfg.IsDefault,
		Mods:       make([]domain.ModReference, len(cfg.Mods)),
	}

	for i, m := range cfg.Mods {
		profile.Mods[i] = domain.ModReference{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
		}
	}

	return profile, nil
}

// SaveProfile writes a profile to disk
func SaveProfile(configDir string, profile *domain.Profile) error {
	cfg := ProfileConfig{
		Name:       profile.Name,
		GameID:     profile.GameID,
		LinkMethod: profile.LinkMethod.String(),
		IsDefault:  profile.IsDefault,
		Mods:       make([]ModReferenceConfig, len(profile.Mods)),
	}

	for i, m := range profile.Mods {
		cfg.Mods[i] = ModReferenceConfig{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
		}
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshaling profile: %w", err)
	}

	profileDir := filepath.Join(configDir, "games", profile.GameID, "profiles")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("creating profiles dir: %w", err)
	}

	profilePath := filepath.Join(profileDir, profile.Name+".yaml")
	if err := os.WriteFile(profilePath, data, 0644); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}

	return nil
}

// ListProfiles returns all profile names for a game
func ListProfiles(configDir, gameID string) ([]string, error) {
	profileDir := filepath.Join(configDir, "games", gameID, "profiles")
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading profiles dir: %w", err)
	}

	var profiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") {
			profiles = append(profiles, strings.TrimSuffix(name, ".yaml"))
		}
	}

	return profiles, nil
}

// DeleteProfile removes a profile from disk
func DeleteProfile(configDir, gameID, profileName string) error {
	profilePath := filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml")
	if err := os.Remove(profilePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ErrProfileNotFound
		}
		return fmt.Errorf("deleting profile: %w", err)
	}
	return nil
}

// ExportProfile exports a profile to a portable format
func ExportProfile(profile *domain.Profile) ([]byte, error) {
	exported := domain.ExportedProfile{
		Name:       profile.Name,
		GameID:     profile.GameID,
		Mods:       profile.Mods,
		LinkMethod: profile.LinkMethod.String(),
	}

	data, err := yaml.Marshal(&exported)
	if err != nil {
		return nil, fmt.Errorf("marshaling exported profile: %w", err)
	}

	return data, nil
}

// ImportProfile imports a profile from portable format
func ImportProfile(data []byte) (*domain.Profile, error) {
	var exported domain.ExportedProfile
	if err := yaml.Unmarshal(data, &exported); err != nil {
		return nil, fmt.Errorf("parsing exported profile: %w", err)
	}

	return &domain.Profile{
		Name:       exported.Name,
		GameID:     exported.GameID,
		Mods:       exported.Mods,
		LinkMethod: domain.ParseLinkMethod(exported.LinkMethod),
	}, nil
}
