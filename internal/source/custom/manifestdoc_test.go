package custom

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validManifestYAML = `
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    author: someone
    summary: Makes things cooler
    game_ids: [skyrimspecialedition]
    url: https://example.com/mods/cool-mod
    updated_at: 2026-07-01T00:00:00Z
    dependencies: [other-mod]
    files:
      - id: main
        name: Main File
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 123456
        url: https://example.com/files/cool-mod-1.2.0.zip
        sha256: aabbcc
        primary: true
  - id: other-mod
    name: Other Mod
    version: 0.9.0
    files:
      - id: main
        filename: other-mod.zip
        url: https://example.com/files/other-mod.zip
`

func TestParseManifestYAML(t *testing.T) {
	doc, err := parseManifest([]byte(validManifestYAML), false)
	require.NoError(t, err)
	require.Len(t, doc.Mods, 2)
	m := doc.Mods[0]
	assert.Equal(t, "cool-mod", m.ID)
	assert.Equal(t, "Cool Mod", m.Name)
	assert.Equal(t, []string{"skyrimspecialedition"}, m.GameIDs)
	assert.Equal(t, []string{"other-mod"}, m.Dependencies)
	require.Len(t, m.Files, 1)
	assert.Equal(t, "main", m.Files[0].ID)
	assert.Equal(t, "cool-mod-1.2.0.zip", m.Files[0].Filename)
	assert.Equal(t, int64(123456), m.Files[0].Size)
	assert.Equal(t, "aabbcc", m.Files[0].SHA256)
	assert.True(t, m.Files[0].Primary)
}

func TestParseManifestJSON(t *testing.T) {
	doc, err := parseManifest([]byte(`{"version":1,"mods":[{"id":"j","name":"J","files":[{"id":"main","filename":"j.zip","url":"https://x.test/j.zip"}]}]}`), false)
	require.NoError(t, err)
	require.Len(t, doc.Mods, 1)
	assert.Equal(t, "j", doc.Mods[0].ID)
}

func TestParseManifestErrors(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"bad syntax", "version: [unclosed", "parsing manifest"},
		{"wrong version", "version: 2\nmods: []", "unsupported manifest version 2"},
		{"missing version", "mods: []", "unsupported manifest version 0"},
		{"mod missing id", "version: 1\nmods:\n  - name: X\n    files: [{id: main, filename: x.zip, url: https://x.test/x.zip}]", "mods[0]: id is required"},
		{"mod missing name", "version: 1\nmods:\n  - id: x\n    files: [{id: main, filename: x.zip, url: https://x.test/x.zip}]", `mod "x": name is required`},
		{"duplicate mod ids", "version: 1\nmods:\n  - {id: x, name: X}\n  - {id: x, name: X2}", `duplicate mod id "x"`},
		{"file missing id", "version: 1\nmods:\n  - id: x\n    name: X\n    files: [{filename: x.zip, url: https://x.test/x.zip}]", `mod "x": files[0]: id is required`},
		{"file missing filename", "version: 1\nmods:\n  - id: x\n    name: X\n    files: [{id: main, url: https://x.test/x.zip}]", `file "main": filename is required`},
		{"file missing url", "version: 1\nmods:\n  - id: x\n    name: X\n    files: [{id: main, filename: x.zip}]", `file "main": url is required`},
		{"http file url rejected", "version: 1\nmods:\n  - id: x\n    name: X\n    files: [{id: main, filename: x.zip, url: http://x.test/x.zip}]", "plain http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseManifest([]byte(tt.doc), false)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParseManifestAllowHTTP(t *testing.T) {
	doc := "version: 1\nmods:\n  - id: x\n    name: X\n    files: [{id: main, filename: x.zip, url: http://x.test/x.zip}]"
	_, err := parseManifest([]byte(doc), true)
	assert.NoError(t, err)
}
