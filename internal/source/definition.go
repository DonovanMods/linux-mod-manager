package source

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Source type identifiers for SourceDefinition.Type.
const (
	TypeDirectory = "directory"
	TypeManifest  = "manifest"
	TypeAPI       = "api"
)

// SourceDefinition is one user-defined source, parsed from a YAML file in
// <configDir>/sources/. Exactly one of Directory/Manifest/API must be set,
// matching Type. Lives here (the interface package) rather than in
// internal/source/custom (its constructors) so a frontend can inspect and
// validate a definition without importing custom's concrete source
// implementations (v2 Phase 2 Task 22).
type SourceDefinition struct {
	ID        string           `yaml:"id"`
	Name      string           `yaml:"name"`
	Type      string           `yaml:"type"`
	AllowHTTP bool             `yaml:"allow_http"`
	Directory *DirectoryConfig `yaml:"directory"`
	Manifest  *ManifestConfig  `yaml:"manifest"`
	API       *APIConfig       `yaml:"api"`
}

// DirectoryConfig configures a local-directory source.
type DirectoryConfig struct {
	Path string `yaml:"path"`
}

// ManifestConfig configures a manifest source (Phase 3).
type ManifestConfig struct {
	URL     string      `yaml:"url"`
	Refresh string      `yaml:"refresh"` // Go duration string, e.g. "15m"; empty = default
	Auth    *AuthConfig `yaml:"auth"`
}

// AuthConfig configures optional API-key authentication for a custom source.
// The key itself is never stored in the definition; it comes from the
// LMM_<ID>_API_KEY env var or the DB token store at startup.
type AuthConfig struct {
	APIKey *APIKeyConfig `yaml:"api_key"`
	// Validate declares the live key-check probe `lmm auth login` runs
	// before storing a key (#121). Absent: the key is stored unvalidated
	// and exercised on first use, which is all a custom source could
	// promise before this existed. Supported on api sources only - a
	// manifest source has one document URL, not a base URL to hang a probe
	// path off.
	Validate *AuthValidateConfig `yaml:"validate"`
}

// AuthValidateConfig describes a request that succeeds only for a valid API
// key, so a custom api source can implement the same live check
// (source.KeyValidator) the built-in sources do (#121). It is declarative on
// purpose: a YAML-defined source cannot ship code, so the probe has to be
// expressible as "call this, expect that".
//
// The request goes to the api source's own base URL plus Path, carrying the
// candidate key exactly the way every other request to that source carries
// it (AuthConfig.APIKey's header or query placement).
type AuthValidateConfig struct {
	// Method is the HTTP method for the probe: GET (the default when
	// empty), HEAD, or POST. Nothing that could mutate state is accepted -
	// a key check must be safe to run on every `lmm auth login`.
	Method string `yaml:"method"`
	// Path is the probe's path, appended to the api source's base_url
	// exactly as an endpoint path is. Required.
	Path string `yaml:"path"`
	// Status is the exact HTTP status a valid key must produce. Zero (the
	// default) accepts any 2xx, which is what most APIs answer.
	Status int `yaml:"status"`
	// Field is an optional JSON dot-path that must be PRESENT in the
	// response for the key to count as valid - for an API that answers 200
	// with an anonymous document rather than 401 when the key is missing.
	// Same dot-path vocabulary as the endpoint mappings; no array indexing.
	Field string `yaml:"field"`
}

// APIKeyConfig says where the API key is attached on requests.
type APIKeyConfig struct {
	In   string `yaml:"in"`   // "header" or "query"
	Name string `yaml:"name"` // header name or query parameter name
}

// APIConfig configures a declarative REST source (expanded in Phase 4).
type APIConfig struct {
	BaseURL   string       `yaml:"base_url"`
	PageStart *int         `yaml:"page_start"` // nil = default 1; explicit 0 respected
	Auth      *AuthConfig  `yaml:"auth"`
	Endpoints APIEndpoints `yaml:"endpoints"`
	Mappings  APIMappings  `yaml:"mappings"`
}

// APIEndpoints defines optional endpoint configurations for an API source.
type APIEndpoints struct {
	Search      *EndpointConfig `yaml:"search"`
	GetMod      *EndpointConfig `yaml:"get_mod"`
	ModFiles    *EndpointConfig `yaml:"mod_files"`
	DownloadURL *EndpointConfig `yaml:"download_url"`
}

// EndpointConfig configures a single API endpoint.
type EndpointConfig struct {
	Path  string `yaml:"path"`  // required; may contain {placeholders}
	List  string `yaml:"list"`  // dot-path to results array (required for search & mod_files)
	Total string `yaml:"total"` // optional dot-path to total count (search only)
	Field string `yaml:"field"` // dot-path to a scalar (required for download_url)
}

// APIMappings maps domain field keys to JSON dot-paths.
type APIMappings struct {
	Mod  map[string]string `yaml:"mod"` // domain field key -> JSON dot-path
	File map[string]string `yaml:"file"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// knownModMappingKeys / knownFileMappingKeys are the domain fields a mapping
// may target. Unknown keys are validation errors so typos surface at load
// time instead of silently producing empty fields.
var knownModMappingKeys = map[string]bool{
	"id": true, "name": true, "version": true, "author": true, "summary": true,
	"description": true, "downloads": true, "updated_at": true, "url": true, "picture_url": true,
}

var knownFileMappingKeys = map[string]bool{
	"id": true, "name": true, "filename": true, "version": true, "size": true,
}

// validateEndpointsAndMappings checks the api block's endpoint/mapping rules
// (design §4): at least one endpoint, per-endpoint required fields, required
// mapping keys, and no unknown mapping keys.
func (c *APIConfig) validateEndpointsAndMappings() error {
	eps := []struct {
		name string
		ep   *EndpointConfig
	}{
		{"search", c.Endpoints.Search},
		{"get_mod", c.Endpoints.GetMod},
		{"mod_files", c.Endpoints.ModFiles},
		{"download_url", c.Endpoints.DownloadURL},
	}

	defined := false
	for _, e := range eps {
		if e.ep == nil {
			continue
		}
		defined = true
		if e.ep.Path == "" {
			return fmt.Errorf("endpoints.%s: path is required", e.name)
		}
	}
	if !defined {
		return errors.New("endpoints: at least one endpoint must be defined")
	}
	if c.Endpoints.Search != nil && c.Endpoints.Search.List == "" {
		return errors.New("endpoints.search: list is required")
	}
	if c.Endpoints.ModFiles != nil && c.Endpoints.ModFiles.List == "" {
		return errors.New("endpoints.mod_files: list is required")
	}
	if c.Endpoints.DownloadURL != nil && c.Endpoints.DownloadURL.Field == "" {
		return errors.New("endpoints.download_url: field is required")
	}

	if c.Mappings.Mod["id"] == "" {
		return errors.New(`mappings.mod: "id" is required`)
	}
	if c.Mappings.Mod["name"] == "" {
		return errors.New(`mappings.mod: "name" is required`)
	}
	if c.Endpoints.ModFiles != nil && c.Mappings.File["id"] == "" {
		return errors.New(`mappings.file: "id" is required when mod_files is defined`)
	}
	for k := range c.Mappings.Mod {
		if !knownModMappingKeys[k] {
			return fmt.Errorf("mappings.mod: unknown key %q", k)
		}
	}
	for k := range c.Mappings.File {
		if !knownFileMappingKeys[k] {
			return fmt.Errorf("mappings.file: unknown key %q", k)
		}
	}
	return nil
}

// validateAuth checks an optional auth block. Shared by manifest (Phase 3)
// and api (Phase 4) validation; allowValidate is false for the manifest
// type, which has no base URL for a probe path to hang off (#121).
func validateAuth(a *AuthConfig, allowValidate bool) error {
	if a == nil {
		return nil
	}
	if a.APIKey == nil {
		return errors.New("auth.api_key is required when auth is set")
	}
	if a.APIKey.In != "header" && a.APIKey.In != "query" {
		return fmt.Errorf(`auth.api_key.in must be "header" or "query", got %q`, a.APIKey.In)
	}
	if a.APIKey.Name == "" {
		return errors.New("auth.api_key.name is required")
	}
	if a.Validate == nil {
		return nil
	}
	if !allowValidate {
		return errors.New("auth.validate is only supported on api sources")
	}
	return a.Validate.validate()
}

// validate checks the probe's own fields (#121). Nothing here touches the
// network: a probe is only exercised by an explicit `lmm auth login`.
func (v *AuthValidateConfig) validate() error {
	if v.Path == "" {
		return errors.New("auth.validate.path is required")
	}
	switch strings.ToUpper(v.Method) {
	case "", http.MethodGet, http.MethodHead, http.MethodPost:
	default:
		return fmt.Errorf(`auth.validate.method must be GET, HEAD or POST, got %q`, v.Method)
	}
	if v.Status != 0 && (v.Status < 100 || v.Status > 599) {
		return fmt.Errorf("auth.validate.status must be a valid HTTP status code, got %d", v.Status)
	}
	return nil
}

// Validate checks the definition for structural errors. It does not touch the
// filesystem or network; existence checks happen when the source is constructed.
func (d *SourceDefinition) Validate() error {
	if d.ID == "" {
		return errors.New("id is required")
	}
	if !idPattern.MatchString(d.ID) {
		return fmt.Errorf("id %q must match ^[a-z0-9-]+$", d.ID)
	}
	if d.Name == "" {
		return errors.New("name is required")
	}
	if d.Type == "" {
		return errors.New("type is required")
	}

	blocks := 0
	if d.Directory != nil {
		blocks++
	}
	if d.Manifest != nil {
		blocks++
	}
	if d.API != nil {
		blocks++
	}
	if blocks > 1 {
		return errors.New("exactly one of directory/manifest/api may be set")
	}

	switch d.Type {
	case TypeDirectory:
		if d.Directory == nil {
			return fmt.Errorf(`type %q requires a "directory" block`, d.Type)
		}
		if d.Directory.Path == "" {
			return errors.New("directory.path is required")
		}
	case TypeManifest:
		if d.Manifest == nil {
			return fmt.Errorf(`type %q requires a "manifest" block`, d.Type)
		}
		if d.Manifest.URL == "" {
			return errors.New("manifest.url is required")
		}
		if err := d.checkURL(d.Manifest.URL); err != nil {
			return fmt.Errorf("manifest.url: %w", err)
		}
		if strings.Contains(d.Manifest.URL, "://") &&
			!strings.HasPrefix(d.Manifest.URL, "http://") && !strings.HasPrefix(d.Manifest.URL, "https://") {
			return errors.New("manifest.url: unsupported scheme (use https://, http:// with allow_http, or a local path)")
		}
		if d.Manifest.Refresh != "" {
			if _, err := time.ParseDuration(d.Manifest.Refresh); err != nil {
				return fmt.Errorf("manifest.refresh: %w", err)
			}
		}
		if err := validateAuth(d.Manifest.Auth, false); err != nil {
			return fmt.Errorf("manifest: %w", err)
		}
	case TypeAPI:
		if d.API == nil {
			return fmt.Errorf(`type %q requires an "api" block`, d.Type)
		}
		if d.API.BaseURL == "" {
			return errors.New("api.base_url is required")
		}
		if !strings.HasPrefix(d.API.BaseURL, "https://") && !strings.HasPrefix(d.API.BaseURL, "http://") {
			return errors.New("api.base_url must be an http(s) URL")
		}
		if err := d.checkURL(d.API.BaseURL); err != nil {
			return fmt.Errorf("api.base_url: %w", err)
		}
		if err := validateAuth(d.API.Auth, true); err != nil {
			return fmt.Errorf("api: %w", err)
		}
		if err := d.API.validateEndpointsAndMappings(); err != nil {
			return fmt.Errorf("api: %w", err)
		}
	default:
		return fmt.Errorf("unknown type %q (expected %s, %s, or %s)", d.Type, TypeDirectory, TypeManifest, TypeAPI)
	}

	return nil
}

// checkURL rejects plain-http URLs unless allow_http is set. Non-URL values
// (local paths) pass through untouched.
func (d *SourceDefinition) checkURL(u string) error {
	if strings.HasPrefix(u, "http://") && !d.AllowHTTP {
		return errors.New("plain http is disabled; use https or set allow_http: true")
	}
	return nil
}
