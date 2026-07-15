package custom

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// manifestDoc is the lmm-defined manifest format (design §3), version 1.
// YAML 1.2 is a superset of JSON, so yaml.v3 parses both encodings.
type manifestDoc struct {
	Version int           `yaml:"version"`
	Mods    []manifestMod `yaml:"mods"`
}

type manifestMod struct {
	ID           string         `yaml:"id"`
	Name         string         `yaml:"name"`
	Version      string         `yaml:"version"`
	Author       string         `yaml:"author"`
	Summary      string         `yaml:"summary"`
	GameIDs      []string       `yaml:"game_ids"` // matched against the game's mapped value; empty = all games
	URL          string         `yaml:"url"`
	UpdatedAt    string         `yaml:"updated_at"` // RFC 3339; unparseable -> zero value (design §4 rule)
	Dependencies []string       `yaml:"dependencies"`
	Files        []manifestFile `yaml:"files"`
}

type manifestFile struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Filename string `yaml:"filename"`
	Version  string `yaml:"version"`
	Size     int64  `yaml:"size"`
	URL      string `yaml:"url"`
	SHA256   string `yaml:"sha256"` // optional; verified on download when present
	Primary  bool   `yaml:"primary"`
}

// parseManifest decodes and validates a manifest document. allowHTTP mirrors
// the definition's allow_http flag: file URLs must be https unless it is set.
// Local file paths never appear here — manifest files reference downloads by
// URL only.
func parseManifest(data []byte, allowHTTP bool) (*manifestDoc, error) {
	var doc manifestDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	if doc.Version != 1 {
		return nil, fmt.Errorf("unsupported manifest version %d (expected 1)", doc.Version)
	}

	seen := make(map[string]bool, len(doc.Mods))
	for i, m := range doc.Mods {
		if m.ID == "" {
			return nil, fmt.Errorf("mods[%d]: id is required", i)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("duplicate mod id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Name == "" {
			return nil, fmt.Errorf("mod %q: name is required", m.ID)
		}
		for j, f := range m.Files {
			if f.ID == "" {
				return nil, fmt.Errorf("mod %q: files[%d]: id is required", m.ID, j)
			}
			if f.Filename == "" {
				return nil, fmt.Errorf("mod %q: file %q: filename is required", m.ID, f.ID)
			}
			if f.URL == "" {
				return nil, fmt.Errorf("mod %q: file %q: url is required", m.ID, f.ID)
			}
			if strings.HasPrefix(f.URL, "http://") && !allowHTTP {
				return nil, fmt.Errorf("mod %q: file %q: plain http is disabled; use https or set allow_http: true", m.ID, f.ID)
			}
		}
	}

	return &doc, nil
}
