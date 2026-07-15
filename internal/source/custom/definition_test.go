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

func validAPIDef() SourceDefinition {
	return SourceDefinition{
		ID:   "my-api",
		Name: "My API",
		Type: TypeAPI,
		API: &APIConfig{
			BaseURL: "https://api.x.test",
			Endpoints: APIEndpoints{
				Search:      &EndpointConfig{Path: "/mods?q={query}&page={page}", List: "results", Total: "total"},
				GetMod:      &EndpointConfig{Path: "/mods/{mod_id}"},
				ModFiles:    &EndpointConfig{Path: "/mods/{mod_id}/files", List: "files"},
				DownloadURL: &EndpointConfig{Path: "/files/{file_id}/download", Field: "url"},
			},
			Mappings: APIMappings{
				Mod:  map[string]string{"id": "id", "name": "name", "version": "latest_version"},
				File: map[string]string{"id": "id", "filename": "file_name"},
			},
		},
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
		{"ftp manifest url rejected", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "ftp://x.test/m.yaml"}
		}, "unsupported scheme"},
		{"bad manifest refresh", func(d *SourceDefinition) {
			d.Type = TypeManifest
			d.Directory = nil
			d.Manifest = &ManifestConfig{URL: "https://x.test/m.yaml", Refresh: "soon"}
		}, "refresh"},
		{"valid full api", func(d *SourceDefinition) {
			*d = validAPIDef()
		}, ""},
		{"http api rejected", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.BaseURL = "http://api.x.test"
		}, "https"},
		{"api no endpoints", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Endpoints = APIEndpoints{}
		}, "at least one endpoint"},
		{"api endpoint missing path", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Endpoints.GetMod = &EndpointConfig{}
		}, "get_mod: path is required"},
		{"api search missing list", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Endpoints.Search.List = ""
		}, "search: list is required"},
		{"api mod_files missing list", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Endpoints.ModFiles.List = ""
		}, "mod_files: list is required"},
		{"api download_url missing field", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Endpoints.DownloadURL.Field = ""
		}, "download_url: field is required"},
		{"api mappings missing mod id", func(d *SourceDefinition) {
			*d = validAPIDef()
			delete(d.API.Mappings.Mod, "id")
		}, `mappings.mod: "id" is required`},
		{"api mappings missing mod name", func(d *SourceDefinition) {
			*d = validAPIDef()
			delete(d.API.Mappings.Mod, "name")
		}, `mappings.mod: "name" is required`},
		{"api mod_files without file id mapping", func(d *SourceDefinition) {
			*d = validAPIDef()
			delete(d.API.Mappings.File, "id")
		}, `mappings.file: "id" is required`},
		{"api unknown mod mapping key", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Mappings.Mod["fancyness"] = "x"
		}, `mappings.mod: unknown key "fancyness"`},
		{"api unknown file mapping key", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Mappings.File["sha512"] = "x"
		}, `mappings.file: unknown key "sha512"`},
		{"api with auth", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		}, ""},
		{"api bad auth", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "body", Name: "k"}}
		}, `auth.api_key.in must be "header" or "query"`},
		{"api install-by-id only is valid", func(d *SourceDefinition) {
			*d = validAPIDef()
			d.API.Endpoints.Search = nil
		}, ""},
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
