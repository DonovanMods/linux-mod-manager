package custom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func validDirectoryDef() SourceDefinition {
	return SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: "~/mods"},
	}
}

func TestSourceDefinitionValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*SourceDefinition)
		wantErr string // empty = valid
	}{
		{"valid directory", func(d *SourceDefinition) {}, ""},
		{"missing id", func(d *SourceDefinition) { d.ID = "" }, "id is required"},
		{"bad id chars", func(d *SourceDefinition) { d.ID = "My_Mods" }, "must match"},
		{"missing name", func(d *SourceDefinition) { d.Name = "" }, "name is required"},
		{"missing type", func(d *SourceDefinition) { d.Type = "" }, "type is required"},
		{"unknown type", func(d *SourceDefinition) { d.Type = "ftp" }, "unknown type"},
		{"directory without block", func(d *SourceDefinition) { d.Directory = nil }, `requires a "directory" block`},
		{"directory with empty path", func(d *SourceDefinition) { d.Directory.Path = "" }, "directory.path is required"},
		{"two type blocks", func(d *SourceDefinition) {
			d.Manifest = &ManifestConfig{URL: "https://x.test/m.yaml"}
		}, "exactly one"},
		{"valid https manifest", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "https://x.test/m.yaml"}
		}, ""},
		{"valid local manifest path", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "~/mods/manifest.yaml"}
		}, ""},
		{"http manifest rejected", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "http://x.test/m.yaml"}
		}, "https"},
		{"http manifest allowed with allow_http", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.AllowHTTP = true
			d.Manifest = &ManifestConfig{URL: "http://x.test/m.yaml"}
		}, ""},
		{"bad manifest refresh", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "https://x.test/m.yaml", Refresh: "soon"}
		}, "refresh"},
		{"valid api", func(d *SourceDefinition) {
			d.Type = TypeAPI
			d.Directory = nil
			d.API = &APIConfig{BaseURL: "https://api.x.test"}
		}, ""},
		{"http api rejected", func(d *SourceDefinition) {
			d.Type = TypeAPI
			d.Directory = nil
			d.API = &APIConfig{BaseURL: "http://api.x.test"}
		}, "https"},
		{"valid manifest auth header", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{
				URL:  "https://x.test/m.yaml",
				Auth: &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}},
			}
		}, ""},
		{"valid manifest auth query", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{
				URL:  "https://x.test/m.yaml",
				Auth: &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}},
			}
		}, ""},
		{"auth without api_key block", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "https://x.test/m.yaml", Auth: &AuthConfig{}}
		}, "auth.api_key is required"},
		{"auth bad in", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{
				URL:  "https://x.test/m.yaml",
				Auth: &AuthConfig{APIKey: &APIKeyConfig{In: "body", Name: "k"}},
			}
		}, `auth.api_key.in must be "header" or "query"`},
		{"auth missing name", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{
				URL:  "https://x.test/m.yaml",
				Auth: &AuthConfig{APIKey: &APIKeyConfig{In: "header"}},
			}
		}, "auth.api_key.name is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validDirectoryDef()
			tt.mutate(&def)
			err := def.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}
