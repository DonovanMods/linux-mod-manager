package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"gopkg.in/yaml.v3"
)

// SourceLoadError describes a source definition file that could not be loaded.
// These are collected rather than returned as hard errors so one broken file
// never prevents lmm from starting.
type SourceLoadError struct {
	File string // base filename within the sources directory
	Err  error
}

func (e SourceLoadError) Error() string {
	return fmt.Sprintf("%s: %v", e.File, e.Err)
}

// LoadSourceDefinitions reads and validates every *.yaml/*.yml file in
// <configDir>/sources. A missing directory yields no definitions and no error.
// Per-file parse/validation failures (including duplicate IDs) are returned as
// SourceLoadErrors; the hard error is reserved for an unreadable directory.
func LoadSourceDefinitions(configDir string) ([]custom.SourceDefinition, []SourceLoadError, error) {
	dir := filepath.Join(configDir, "sources")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading sources directory %s: %w", dir, err)
	}

	var defs []custom.SourceDefinition
	var loadErrs []SourceLoadError
	seen := make(map[string]string) // id -> filename that claimed it

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}

		def, err := LoadSourceDefinitionFile(filepath.Join(dir, name))
		if err != nil {
			loadErrs = append(loadErrs, SourceLoadError{File: name, Err: err})
			continue
		}
		if prev, dup := seen[def.ID]; dup {
			loadErrs = append(loadErrs, SourceLoadError{
				File: name,
				Err:  fmt.Errorf("duplicate source id %q (already defined in %s)", def.ID, prev),
			})
			continue
		}
		seen[def.ID] = name
		defs = append(defs, def)
	}

	return defs, loadErrs, nil
}

// LoadSourceDefinitionFile reads and validates a single source definition file.
func LoadSourceDefinitionFile(path string) (custom.SourceDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return custom.SourceDefinition{}, fmt.Errorf("reading definition: %w", err)
	}

	var def custom.SourceDefinition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return custom.SourceDefinition{}, fmt.Errorf("parsing YAML: %w", err)
	}
	if err := def.Validate(); err != nil {
		return custom.SourceDefinition{}, fmt.Errorf("invalid definition: %w", err)
	}

	return def, nil
}
