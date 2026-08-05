// Package pakconvert is a THROWAWAY spike (#220): converting prebuilt Icarus
// PAK mods into synthesized .exmodz form. Never merged into develop.
package pakconvert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// CorpusMod is one downloaded mod in the local corpus. Pak/Exmodz are
// corpus-relative paths ("" when the catalog has no such file).
type CorpusMod struct {
	ID      string
	Name    string
	Version string
	Week    string
	Pak     string
	Exmodz  string
}

// Manifest indexes the corpus dir; written by cmd/fetchcorpus, read by the
// env-gated integration tests.
type Manifest struct {
	Mods []CorpusMod
}

// CensusEntry records pak/exmodz availability for one catalog mod.
type CensusEntry struct {
	ID        string
	Name      string
	HasPak    bool
	HasExmodz bool
}

// MatchTargets returns mods whose Name contains any of the given substrings,
// case-insensitively. Order follows the input mods slice.
func MatchTargets(mods []domain.Mod, substrings []string) []domain.Mod {
	var out []domain.Mod
	for _, m := range mods {
		lower := strings.ToLower(m.Name)
		for _, s := range substrings {
			if strings.Contains(lower, strings.ToLower(s)) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// SaveJSON writes v as indented JSON, creating parent dirs.
func SaveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadManifest reads <dir>/manifest.json.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("reading corpus manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing corpus manifest: %w", err)
	}
	return &m, nil
}
