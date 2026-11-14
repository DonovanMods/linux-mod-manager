package config

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"go.yaml.in/yaml/v3"
)

// overridesYAML is an overrides: map (config-file path -> content) as every
// writer emits it: the profile file (whole and in-place), the version-0
// export and the collection document. It reads like any map[string]string;
// on write, each content is overrideText (#640).
type overridesYAML map[string]string

// MarshalYAML writes each content as overrideText.
func (m overridesYAML) MarshalYAML() (any, error) {
	out := make(map[string]overrideText, len(m))
	for path, content := range m {
		out[path] = overrideText(content)
	}
	return out, nil
}

// overrideText is one override's content. yaml.v3 writes it in its usual
// form - a literal block for multi-line text - whenever that reads back as
// the same bytes, so every document that already round-tripped is written
// exactly as before. When it does not (a first line starting with a tab is
// written as a block scalar the decoder then rejects, #640), it is written
// double-quoted instead, which escapes every byte that could be misread.
type overrideText string

// MarshalYAML is the content as yaml.v3 writes it, or double-quoted when
// that would not read back unchanged.
func (t overrideText) MarshalYAML() (any, error) {
	s := string(t)
	if roundTrips(s) {
		return s, nil
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: s}, nil
}

// roundTrips reports whether s, written by yaml.v3 as a mapping value,
// reads back as s.
func roundTrips(s string) bool {
	out, err := yaml.Marshal(map[string]string{"k": s})
	if err != nil {
		return false
	}
	var back map[string]string
	if err := safeyaml.Unmarshal(out, &back); err != nil {
		return false
	}
	got, ok := back["k"]
	return ok && got == s
}
