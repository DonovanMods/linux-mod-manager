package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"gopkg.in/yaml.v3"
)

// ProfileHookConfigYAML uses pointers to distinguish "not set" from "set to empty"
type ProfileHookConfigYAML struct {
	BeforeAll  *string `yaml:"before_all"`
	BeforeEach *string `yaml:"before_each"`
	AfterEach  *string `yaml:"after_each"`
	AfterAll   *string `yaml:"after_all"`
}

// ProfileHooksYAML is the YAML representation of profile hooks
type ProfileHooksYAML struct {
	Install   ProfileHookConfigYAML `yaml:"install"`
	Uninstall ProfileHookConfigYAML `yaml:"uninstall"`
}

// ProfileConfig is the YAML representation of a profile
type ProfileConfig struct {
	Name       string               `yaml:"name"`
	GameID     string               `yaml:"game_id"`
	Mods       []ModReferenceConfig `yaml:"mods"`
	LinkMethod string               `yaml:"link_method,omitempty"`
	IsDefault  bool                 `yaml:"is_default,omitempty"`
	Hooks      ProfileHooksYAML     `yaml:"hooks,omitempty"`
	Overrides  map[string]string    `yaml:"overrides,omitempty"` // path (relative to game install) -> file content (INI tweaks, etc.)
}

// ModReferenceConfig is the YAML representation of a mod reference
type ModReferenceConfig struct {
	SourceID string   `yaml:"source_id"`
	ModID    string   `yaml:"mod_id"`
	Version  string   `yaml:"version,omitempty"`
	FileIDs  []string `yaml:"file_ids,omitempty"`
	Locked   bool     `yaml:"locked,omitempty"`
}

// parseProfileHooks converts YAML hooks to domain types, tracking which were explicitly set
func parseProfileHooks(yaml ProfileHooksYAML) (domain.GameHooks, domain.GameHooksExplicit) {
	hooks := domain.GameHooks{}
	explicit := domain.GameHooksExplicit{}

	// Install hooks
	if yaml.Install.BeforeAll != nil {
		hooks.Install.BeforeAll = ExpandPath(*yaml.Install.BeforeAll)
		explicit.Install.BeforeAll = true
	}
	if yaml.Install.BeforeEach != nil {
		hooks.Install.BeforeEach = ExpandPath(*yaml.Install.BeforeEach)
		explicit.Install.BeforeEach = true
	}
	if yaml.Install.AfterEach != nil {
		hooks.Install.AfterEach = ExpandPath(*yaml.Install.AfterEach)
		explicit.Install.AfterEach = true
	}
	if yaml.Install.AfterAll != nil {
		hooks.Install.AfterAll = ExpandPath(*yaml.Install.AfterAll)
		explicit.Install.AfterAll = true
	}

	// Uninstall hooks
	if yaml.Uninstall.BeforeAll != nil {
		hooks.Uninstall.BeforeAll = ExpandPath(*yaml.Uninstall.BeforeAll)
		explicit.Uninstall.BeforeAll = true
	}
	if yaml.Uninstall.BeforeEach != nil {
		hooks.Uninstall.BeforeEach = ExpandPath(*yaml.Uninstall.BeforeEach)
		explicit.Uninstall.BeforeEach = true
	}
	if yaml.Uninstall.AfterEach != nil {
		hooks.Uninstall.AfterEach = ExpandPath(*yaml.Uninstall.AfterEach)
		explicit.Uninstall.AfterEach = true
	}
	if yaml.Uninstall.AfterAll != nil {
		hooks.Uninstall.AfterAll = ExpandPath(*yaml.Uninstall.AfterAll)
		explicit.Uninstall.AfterAll = true
	}

	return hooks, explicit
}

// stringPtrIfExplicit returns a pointer to value when explicit is true (the
// *string-pointer encoding parseProfileHooks decodes: present-but-empty
// means "explicitly disabled", absent means "inherit from game"), or nil
// otherwise - the reverse of parseProfileHooks's per-field decoding.
func stringPtrIfExplicit(value string, explicit bool) *string {
	if !explicit {
		return nil
	}
	return &value
}

// serializeProfileHooks converts domain hooks/explicit-flags back to their
// YAML representation - the reverse of parseProfileHooks, used by
// SaveProfile so a profile's hooks: overrides survive a load/mutate/save
// cycle (#295).
func serializeProfileHooks(hooks domain.GameHooks, explicit domain.GameHooksExplicit) ProfileHooksYAML {
	return ProfileHooksYAML{
		Install: ProfileHookConfigYAML{
			BeforeAll:  stringPtrIfExplicit(hooks.Install.BeforeAll, explicit.Install.BeforeAll),
			BeforeEach: stringPtrIfExplicit(hooks.Install.BeforeEach, explicit.Install.BeforeEach),
			AfterEach:  stringPtrIfExplicit(hooks.Install.AfterEach, explicit.Install.AfterEach),
			AfterAll:   stringPtrIfExplicit(hooks.Install.AfterAll, explicit.Install.AfterAll),
		},
		Uninstall: ProfileHookConfigYAML{
			BeforeAll:  stringPtrIfExplicit(hooks.Uninstall.BeforeAll, explicit.Uninstall.BeforeAll),
			BeforeEach: stringPtrIfExplicit(hooks.Uninstall.BeforeEach, explicit.Uninstall.BeforeEach),
			AfterEach:  stringPtrIfExplicit(hooks.Uninstall.AfterEach, explicit.Uninstall.AfterEach),
			AfterAll:   stringPtrIfExplicit(hooks.Uninstall.AfterAll, explicit.Uninstall.AfterAll),
		},
	}
}

// validateSegment enforces that value is usable as a single path segment:
// non-empty (after trimming whitespace), no path separators ("/" or "\"),
// and no ".." substring. The rule is deliberately conservative — harmless
// values like "foo..bar" or non-escaping subpaths like "a/b" are rejected
// too — because filepath.Join collapses ".." segments, so anything less
// strict risks resolving outside configDir (e.g. "../../evil"). Failures
// wrap sentinel so callers can branch on which field was invalid.
func validateSegment(value string, sentinel error) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: value is empty", sentinel)
	}
	if strings.ContainsAny(value, `/\`) || strings.Contains(value, "..") {
		return fmt.Errorf("%w: %q must not contain path separators or \"..\"", sentinel, value)
	}
	return nil
}

// validateProfilePath guards both path segments a profile file path is built
// from: the game ID and the profile name.
func validateProfilePath(gameID, profileName string) error {
	if err := validateSegment(gameID, domain.ErrInvalidGameID); err != nil {
		return err
	}
	return validateSegment(profileName, domain.ErrInvalidProfileName)
}

// LoadProfile reads a profile from disk
func LoadProfile(configDir, gameID, profileName string) (*domain.Profile, error) {
	if err := validateProfilePath(gameID, profileName); err != nil {
		return nil, err
	}
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

	linkMethod, ok := domain.ParseLinkMethod(cfg.LinkMethod)
	if !ok {
		return nil, fmt.Errorf("%w: profile %q (game %q): link_method %q (valid: %s)",
			domain.ErrInvalidLinkMethod, profileName, gameID, cfg.LinkMethod, domain.ValidLinkMethods)
	}

	profile := &domain.Profile{
		Name:               cfg.Name,
		GameID:             cfg.GameID,
		LinkMethod:         linkMethod,
		LinkMethodExplicit: cfg.LinkMethod != "",
		IsDefault:          cfg.IsDefault,
		Mods:               make([]domain.ModReference, len(cfg.Mods)),
	}

	for i, m := range cfg.Mods {
		profile.Mods[i] = domain.ModReference{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
			FileIDs:  m.FileIDs,
			Locked:   m.Locked,
		}
	}

	profile.Hooks, profile.HooksExplicit = parseProfileHooks(cfg.Hooks)

	if len(cfg.Overrides) > 0 {
		profile.Overrides = make(map[string][]byte)
		for path, content := range cfg.Overrides {
			profile.Overrides[path] = []byte(content)
		}
	}

	return profile, nil
}

// SaveProfile writes a profile to disk
func SaveProfile(configDir string, profile *domain.Profile) error {
	if err := validateProfilePath(profile.GameID, profile.Name); err != nil {
		return err
	}
	cfg := ProfileConfig{
		Name:      profile.Name,
		GameID:    profile.GameID,
		IsDefault: profile.IsDefault,
		Mods:      make([]ModReferenceConfig, len(profile.Mods)),
		Hooks:     serializeProfileHooks(profile.Hooks, profile.HooksExplicit),
	}
	// Only write link_method if explicitly set: String() never returns "", so
	// assigning it unconditionally defeats `omitempty` and bakes a phantom
	// symlink override into every profile file.
	if profile.LinkMethodExplicit {
		cfg.LinkMethod = profile.LinkMethod.String()
	}

	for i, m := range profile.Mods {
		cfg.Mods[i] = ModReferenceConfig{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
			FileIDs:  m.FileIDs,
			Locked:   m.Locked,
		}
	}

	if len(profile.Overrides) > 0 {
		cfg.Overrides = make(map[string]string)
		for path, content := range profile.Overrides {
			cfg.Overrides[path] = string(content)
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
	if err := validateSegment(gameID, domain.ErrInvalidGameID); err != nil {
		return nil, err
	}
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
	if err := validateProfilePath(gameID, profileName); err != nil {
		return err
	}
	profilePath := filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml")
	if err := os.Remove(profilePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ErrProfileNotFound
		}
		return fmt.Errorf("deleting profile: %w", err)
	}
	return nil
}

// exportedProfileYAML is the exported profile's wire shape - the YAML DTO
// ExportProfile/ImportProfile marshal, so the hooks block uses the SAME
// *string-pointer encoding a profile file does (#296). domain.
// ExportedProfile carries the hooks as domain types; only their "unset vs
// explicitly disabled" distinction needs the pointer form, which no yaml tag
// on domain.GameHooks could express - hence a DTO here rather than tags
// there. Field order is the file's key order, matching ProfileConfig's:
// hooks sits between link_method and overrides.
//
// Mods stays []domain.ModReference (ModReference's own yaml tags), exactly
// as the pre-#296 export marshaled it - the bytes must not move.
type exportedProfileYAML struct {
	Name       string                `yaml:"name"`
	GameID     string                `yaml:"game_id"`
	Mods       []domain.ModReference `yaml:"mods"`
	LinkMethod string                `yaml:"link_method,omitempty"`
	Hooks      ProfileHooksYAML      `yaml:"hooks,omitempty"`
	Overrides  map[string]string     `yaml:"overrides,omitempty"`
}

// ExportProfileValue builds profile's portable domain.ExportedProfile
// value: exactly the fields ExportProfile marshals to YAML (LinkMethod only
// when explicit, Overrides as strings), but with Hooks/HooksExplicit left
// as domain.GameHooks/GameHooksExplicit rather than run through the YAML
// DTO's *string-pointer encoding - domain.ExportedProfile's own pair
// already carries the "unset vs explicitly disabled" tri-state, so no
// round trip through the pointer form is needed here. Shared by
// ExportProfile (YAML) and core.Service.ExportProfile (`lmm profile export
// --json`, #309) so the two document formats cannot drift apart on what
// counts as "the exported profile".
func ExportProfileValue(profile *domain.Profile) *domain.ExportedProfile {
	var linkMethod string
	if profile.LinkMethodExplicit {
		linkMethod = profile.LinkMethod.String()
	}
	var overrides map[string]string
	if len(profile.Overrides) > 0 {
		overrides = make(map[string]string, len(profile.Overrides))
		for path, content := range profile.Overrides {
			overrides[path] = string(content)
		}
	}
	return &domain.ExportedProfile{
		Name:          profile.Name,
		GameID:        profile.GameID,
		Mods:          profile.Mods,
		LinkMethod:    linkMethod,
		Overrides:     overrides,
		Hooks:         profile.Hooks,
		HooksExplicit: profile.HooksExplicit,
	}
}

// ExportProfile exports a profile to a portable format
func ExportProfile(profile *domain.Profile) ([]byte, error) {
	exported := ExportProfileValue(profile)

	// yaml.v3 omits a zero struct under omitempty, so a profile with no
	// explicit override writes no hooks: key at all and its export stays
	// byte-identical to every export made before #296.
	data, err := yaml.Marshal(&exportedProfileYAML{
		Name:       exported.Name,
		GameID:     exported.GameID,
		Mods:       exported.Mods,
		LinkMethod: exported.LinkMethod,
		Hooks:      serializeProfileHooks(exported.Hooks, exported.HooksExplicit),
		Overrides:  exported.Overrides,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling exported profile: %w", err)
	}

	return data, nil
}

// ImportProfile imports a profile from portable format
func ImportProfile(data []byte) (*domain.Profile, error) {
	var wire exportedProfileYAML
	if err := yaml.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("parsing exported profile: %w", err)
	}

	exported := domain.ExportedProfile{
		Name:       wire.Name,
		GameID:     wire.GameID,
		Mods:       wire.Mods,
		LinkMethod: wire.LinkMethod,
		Overrides:  wire.Overrides,
	}
	// An export with no hooks: key (every export made before #296 included)
	// decodes to all-nil pointers, so nothing is marked explicit and the
	// profile keeps inheriting the game's hooks - the pre-#296 behaviour.
	exported.Hooks, exported.HooksExplicit = parseProfileHooks(wire.Hooks)

	linkMethod, ok := domain.ParseLinkMethod(exported.LinkMethod)
	if !ok {
		return nil, fmt.Errorf("%w: imported profile %q (game %q): link_method %q (valid: %s)",
			domain.ErrInvalidLinkMethod, exported.Name, exported.GameID, exported.LinkMethod, domain.ValidLinkMethods)
	}

	p := &domain.Profile{
		Name:               exported.Name,
		GameID:             exported.GameID,
		Mods:               exported.Mods,
		LinkMethod:         linkMethod,
		LinkMethodExplicit: exported.LinkMethod != "",
		Hooks:              exported.Hooks,
		HooksExplicit:      exported.HooksExplicit,
	}
	if len(exported.Overrides) > 0 {
		p.Overrides = make(map[string][]byte)
		for path, content := range exported.Overrides {
			p.Overrides[path] = []byte(content)
		}
	}
	return p, nil
}
