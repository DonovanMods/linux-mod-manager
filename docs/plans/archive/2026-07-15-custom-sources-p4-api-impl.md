# Custom Sources — Phase 4 (Declarative REST API Source) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `type: api` custom sources — a GET+JSON REST API described declaratively (endpoint URL templates + dot-path mappings) becomes a ModSource, including install-by-ID-only definitions with clean capability gaps, plus `lmm source validate --probe` live smoke tests for all custom source types.

**Architecture:** A new `API` type in `internal/source/custom` executes per-operation endpoint templates (`search`, `get_mod`, `mod_files`, `download_url`) with URL-escaped placeholder substitution, decodes JSON, and maps responses onto `domain.Mod`/`domain.DownloadableFile` via configurable dot-paths (engine in `mapping.go`). Undefined endpoints surface as `ErrNotSupported` capability gaps. Auth reuses Phase 3's `AuthConfig` end to end: same-origin credential scoping (vs `base_url`), URL-aware `DownloadHeaders`, startup key attachment, and `auth login/status/logout` all work unchanged via the interfaces that already exist.

**Tech Stack:** Go stdlib (net/http, net/url, encoding/json, strconv), testify, httptest. No new dependencies.

**Spec:** `docs/plans/2026-07-13-custom-sources-design.md` §4 (api type), §6 (auth), §8 (`--probe`), §9 (security). Issue: #49 (epic #45). Note: #49's "extend `lmm auth login` to custom sources" item already shipped in Phase 3 — nothing to do beyond the `API` type implementing `SetAPIKey`/`IsAuthenticated`.

## Global Constraints

- TDD: every task starts with a failing test.
- Error wrapping with context: `fmt.Errorf("doing X: %w", err)` (GO.md); operational errors name the source ID and action (`source %q: searching: %w`).
- `ctx context.Context` first param for I/O paths; no ctx in structs (GO.md).
- No new dependencies.
- `go fmt ./...` and `go vet ./...` clean before every commit.
- GET requests returning JSON only; https enforced for `base_url` unless `allow_http: true`.
- All placeholder substitutions URL-escaped; API keys never appear in logs or error text (the Phase 3 redaction/origin-scoping invariants carry over — reuse, don't reinvent).
- Bounded reads on remote responses (10 MiB, same defense class as manifests).
- Placeholder semantics (design §4, binding): `{page}` = internal 0-based page + `page_start` (default 1); `{offset}` = 0-based page × page_size, independent of `page_start`; `{game_id}` from the query/installed mod (the Service/Updater already pass source-mapped values); required mapping keys are `mod.id`, `mod.name`, and `file.id` — a response missing them is an error; other missing paths yield zero values.
- `GetDependencies` always returns `ErrNotSupported` in v1.
- Commit after each task; conventional commit messages.

---

## Task 1: APIConfig schema and validation

**Files:**
- Modify: `internal/source/custom/definition.go` (replace the stub `APIConfig`)
- Test: `internal/source/custom/definition_test.go` (append)

**Interfaces:**
- Produces (consumed by every later task):

```go
type APIConfig struct {
	BaseURL   string          `yaml:"base_url"`
	PageStart *int            `yaml:"page_start"` // nil = default 1; explicit 0 respected
	Auth      *AuthConfig     `yaml:"auth"`
	Endpoints APIEndpoints    `yaml:"endpoints"`
	Mappings  APIMappings     `yaml:"mappings"`
}

type APIEndpoints struct {
	Search      *EndpointConfig `yaml:"search"`
	GetMod      *EndpointConfig `yaml:"get_mod"`
	ModFiles    *EndpointConfig `yaml:"mod_files"`
	DownloadURL *EndpointConfig `yaml:"download_url"`
}

type EndpointConfig struct {
	Path  string `yaml:"path"`  // required; may contain {placeholders}
	List  string `yaml:"list"`  // dot-path to results array (required for search & mod_files)
	Total string `yaml:"total"` // optional dot-path to total count (search only)
	Field string `yaml:"field"` // dot-path to a scalar (required for download_url)
}

type APIMappings struct {
	Mod  map[string]string `yaml:"mod"`  // domain field key -> JSON dot-path
	File map[string]string `yaml:"file"`
}
```

- Validation rules (in `Validate()`'s `case TypeAPI:`, replacing the current minimal checks; keep the existing base_url http(s)+checkURL logic):
  - `auth` via existing `validateAuth`
  - at least one endpoint defined
  - every defined endpoint: `path` required
  - `search` defined ⇒ `list` required on it; `mod_files` defined ⇒ `list` required on it; `download_url` defined ⇒ `field` required on it
  - `mappings.mod` must contain keys `id` and `name`; `mod_files` defined ⇒ `mappings.file` must contain key `id`
  - unknown mapping keys are errors (typo detection): mod keys ⊆ {`id`,`name`,`version`,`author`,`summary`,`description`,`downloads`,`updated_at`,`url`,`picture_url`}; file keys ⊆ {`id`,`name`,`filename`,`version`,`size`}

- [ ] **Step 1: Write the failing test**

Append to the `tests` table in `TestSourceDefinitionValidate` (`internal/source/custom/definition_test.go`). First add a helper near `validDirectoryDef`:

```go
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
```

Then the table cases (the existing `"valid api"` and `"http api rejected"` cases construct a minimal `&APIConfig{BaseURL: ...}` — UPDATE them to use `validAPIDef()`'s API block with the base_url swapped, since a bare BaseURL is no longer valid):

```go
		{"valid full api", func(d *SourceDefinition) {
			*d = validAPIDef()
		}, ""},
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestSourceDefinitionValidate -v`
Expected: FAIL — `unknown field Endpoints`, etc.

- [ ] **Step 3: Implement**

In `internal/source/custom/definition.go`, replace the stub `APIConfig` with the types from the Interfaces block above, then replace the body of `case TypeAPI:` in `Validate()`:

```go
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
		if err := validateAuth(d.API.Auth); err != nil {
			return fmt.Errorf("api: %w", err)
		}
		if err := d.API.validateEndpointsAndMappings(); err != nil {
			return fmt.Errorf("api: %w", err)
		}
```

Add to definition.go:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS (new cases + all existing; the two updated legacy api cases too).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/definition.go internal/source/custom/definition_test.go
git commit -m "feat(source): expand api source definition schema and validation"
```

---

## Task 2: Dot-path mapping engine

**Files:**
- Create: `internal/source/custom/mapping.go`
- Test: `internal/source/custom/mapping_test.go`

**Interfaces:**
- Produces (unexported, consumed by Tasks 5–7):
  - `lookupPath(doc any, path string) (any, bool)` — traverses `map[string]any` by dot-separated keys; false when any segment is missing or a non-map is traversed into
  - `coerceString(v any) string` — string as-is; float64 via `strconv.FormatFloat(f, 'f', -1, 64)` (JSON numbers arrive as float64; integer ids must render without ".0"); bool/nil/other → ""
  - `coerceInt64(v any) int64` — float64 truncated; string parsed (best effort, 0 on failure); other → 0
  - `mapMod(doc any, mapping map[string]string, sourceID string) (domain.Mod, error)` — error when `id` or `name` resolve empty; `updated_at` parsed RFC3339, unparseable → zero; `url` → `SourceURL`, `picture_url` → `PictureURL`
  - `mapFile(doc any, mapping map[string]string) (domain.DownloadableFile, error)` — error when `id` resolves empty; `filename` → `FileName`

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/mapping_test.go`:

```go
package custom

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonDoc decodes a JSON literal the way the API source does (numbers become
// float64), so tests exercise the real coercion paths.
func jsonDoc(t *testing.T, s string) any {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal([]byte(s), &doc))
	return doc
}

func TestLookupPath(t *testing.T) {
	doc := jsonDoc(t, `{"a": {"b": {"c": 42}}, "top": "x", "arr": [1,2]}`)

	v, ok := lookupPath(doc, "a.b.c")
	require.True(t, ok)
	assert.Equal(t, float64(42), v)

	v, ok = lookupPath(doc, "top")
	require.True(t, ok)
	assert.Equal(t, "x", v)

	_, ok = lookupPath(doc, "a.missing")
	assert.False(t, ok)

	_, ok = lookupPath(doc, "top.deeper") // traversing into a non-map
	assert.False(t, ok)

	_, ok = lookupPath(doc, "arr.0") // array indexing unsupported in v1
	assert.False(t, ok)
}

func TestCoercions(t *testing.T) {
	assert.Equal(t, "hello", coerceString("hello"))
	assert.Equal(t, "123", coerceString(float64(123)))     // integer id, no ".0"
	assert.Equal(t, "1.5", coerceString(float64(1.5)))
	assert.Equal(t, "", coerceString(nil))
	assert.Equal(t, "", coerceString(true))

	assert.Equal(t, int64(42), coerceInt64(float64(42)))
	assert.Equal(t, int64(42), coerceInt64("42"))
	assert.Equal(t, int64(0), coerceInt64("not a number"))
	assert.Equal(t, int64(0), coerceInt64(nil))
}

func TestMapMod(t *testing.T) {
	doc := jsonDoc(t, `{
		"id": 77, "name": "Cool Mod", "latest_version": "1.2.0",
		"author": {"name": "someone"}, "description": "Makes things cooler",
		"download_count": 12345, "updated": "2026-07-01T00:00:00Z",
		"web_url": "https://x.test/mods/77"
	}`)
	mapping := map[string]string{
		"id": "id", "name": "name", "version": "latest_version",
		"author": "author.name", "summary": "description",
		"downloads": "download_count", "updated_at": "updated", "url": "web_url",
	}

	mod, err := mapMod(doc, mapping, "my-api")
	require.NoError(t, err)
	assert.Equal(t, "77", mod.ID) // numeric id coerced without ".0"
	assert.Equal(t, "my-api", mod.SourceID)
	assert.Equal(t, "Cool Mod", mod.Name)
	assert.Equal(t, "1.2.0", mod.Version)
	assert.Equal(t, "someone", mod.Author)
	assert.Equal(t, "Makes things cooler", mod.Summary)
	assert.Equal(t, int64(12345), mod.Downloads)
	assert.Equal(t, 2026, mod.UpdatedAt.Year())
	assert.Equal(t, "https://x.test/mods/77", mod.SourceURL)
}

func TestMapModRequiredFields(t *testing.T) {
	mapping := map[string]string{"id": "id", "name": "name"}

	_, err := mapMod(jsonDoc(t, `{"name": "NoID"}`), mapping, "s")
	assert.ErrorContains(t, err, `required field "id"`)

	_, err = mapMod(jsonDoc(t, `{"id": 1}`), mapping, "s")
	assert.ErrorContains(t, err, `required field "name"`)
}

func TestMapModOptionalMissingYieldsZero(t *testing.T) {
	mapping := map[string]string{"id": "id", "name": "name", "version": "nope.nothere", "updated_at": "bad_time"}
	mod, err := mapMod(jsonDoc(t, `{"id": "x", "name": "X", "bad_time": "yesterday-ish"}`), mapping, "s")
	require.NoError(t, err)
	assert.Empty(t, mod.Version)
	assert.True(t, mod.UpdatedAt.IsZero())
}

func TestMapFile(t *testing.T) {
	doc := jsonDoc(t, `{"id": 900, "title": "Main File", "file_name": "cool-1.2.0.zip", "version": "1.2.0", "size_bytes": 123456}`)
	mapping := map[string]string{"id": "id", "name": "title", "filename": "file_name", "version": "version", "size": "size_bytes"}

	f, err := mapFile(doc, mapping)
	require.NoError(t, err)
	assert.Equal(t, "900", f.ID)
	assert.Equal(t, "Main File", f.Name)
	assert.Equal(t, "cool-1.2.0.zip", f.FileName)
	assert.Equal(t, "1.2.0", f.Version)
	assert.Equal(t, int64(123456), f.Size)

	_, err = mapFile(jsonDoc(t, `{"title": "no id"}`), mapping)
	assert.ErrorContains(t, err, `required field "id"`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run 'TestLookupPath|TestCoercions|TestMapMod|TestMapFile' -v`
Expected: FAIL — `undefined: lookupPath` etc.

- [ ] **Step 3: Implement**

Create `internal/source/custom/mapping.go`:

```go
package custom

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// lookupPath traverses a decoded JSON document (map[string]any tree) by a
// dot-separated path. Returns (value, true) when every segment resolves;
// (nil, false) when any segment is missing or a non-object is traversed
// into. Array indexing is not supported in v1 (design §4).
func lookupPath(doc any, path string) (any, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// coerceString renders a JSON scalar as a string. JSON numbers decode as
// float64, so integer ids must format without a trailing ".0".
func coerceString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// coerceInt64 renders a JSON scalar as an int64, best effort (0 on failure).
func coerceInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// pathString resolves a mapping path to a string; "" when the mapping key or
// the path is absent.
func pathString(doc any, mapping map[string]string, key string) string {
	path, ok := mapping[key]
	if !ok {
		return ""
	}
	v, ok := lookupPath(doc, path)
	if !ok {
		return ""
	}
	return coerceString(v)
}

// mapMod builds a domain.Mod from a decoded JSON object using the definition's
// mod mappings. id and name are required (design §4); everything else is a
// zero value when missing or unmapped.
func mapMod(doc any, mapping map[string]string, sourceID string) (domain.Mod, error) {
	mod := domain.Mod{SourceID: sourceID}

	mod.ID = pathString(doc, mapping, "id")
	if mod.ID == "" {
		return domain.Mod{}, fmt.Errorf(`response is missing required field "id" (mapped from %q)`, mapping["id"])
	}
	mod.Name = pathString(doc, mapping, "name")
	if mod.Name == "" {
		return domain.Mod{}, fmt.Errorf(`response is missing required field "name" (mapped from %q)`, mapping["name"])
	}

	mod.Version = pathString(doc, mapping, "version")
	mod.Author = pathString(doc, mapping, "author")
	mod.Summary = pathString(doc, mapping, "summary")
	mod.Description = pathString(doc, mapping, "description")
	if mod.Description == "" {
		mod.Description = mod.Summary
	}
	mod.SourceURL = pathString(doc, mapping, "url")
	mod.PictureURL = pathString(doc, mapping, "picture_url")

	if path, ok := mapping["downloads"]; ok {
		if v, found := lookupPath(doc, path); found {
			mod.Downloads = coerceInt64(v)
		}
	}
	if ts := pathString(doc, mapping, "updated_at"); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			mod.UpdatedAt = parsed // unparseable -> zero value, by design
		}
	}

	return mod, nil
}

// mapFile builds a domain.DownloadableFile from a decoded JSON object using
// the definition's file mappings. id is required (design §4).
func mapFile(doc any, mapping map[string]string) (domain.DownloadableFile, error) {
	f := domain.DownloadableFile{}

	f.ID = pathString(doc, mapping, "id")
	if f.ID == "" {
		return domain.DownloadableFile{}, fmt.Errorf(`response is missing required field "id" (mapped from %q)`, mapping["id"])
	}
	f.Name = pathString(doc, mapping, "name")
	f.FileName = pathString(doc, mapping, "filename")
	f.Version = pathString(doc, mapping, "version")
	if path, ok := mapping["size"]; ok {
		if v, found := lookupPath(doc, path); found {
			f.Size = coerceInt64(v)
		}
	}
	return f, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/mapping.go internal/source/custom/mapping_test.go
git commit -m "feat(source): add JSON dot-path mapping engine for api sources"
```

---

## Task 3: Shared origin helper (behavior-preserving refactor)

**Files:**
- Create: `internal/source/custom/origin.go`
- Modify: `internal/source/custom/manifest.go` (`sameOrigin` delegates; `normalizedHost` moves)

**Interfaces:**
- Produces: `sameOriginURLs(a, b string) bool` (package-level) — scheme equality + default-port-normalized host equality; false when either URL fails to parse. `Manifest.sameOrigin(fileURL)` becomes `return sameOriginURLs(fileURL, m.url)`. Task 4's `API` uses `sameOriginURLs(fileURL, a.baseURL)`.
- No new tests: the existing `TestManifestDownloadHeaders`, `TestManifestSameOriginNormalizesDefaultPorts`, and query-auth tests are the safety net and must pass untouched.

- [ ] **Step 1: Extract**

Create `internal/source/custom/origin.go` by MOVING (verbatim, adjusting only the receiver-based wrapper) `normalizedHost` out of manifest.go and adding:

```go
package custom

import "net/url"

// sameOriginURLs reports whether two URLs share scheme and host. Ports are
// normalized before comparing: an explicit default port (:443 on https, :80
// on http) matches a URL with no port at all; any other explicit port must
// match exactly. Either URL failing to parse is not same-origin (fail closed).
// Used to scope custom-source API keys to their own origin (design §9).
func sameOriginURLs(a, b string) bool {
	au, err := url.Parse(a)
	if err != nil {
		return false
	}
	bu, err := url.Parse(b)
	if err != nil {
		return false
	}
	return au.Scheme == bu.Scheme && normalizedHost(au) == normalizedHost(bu)
}
```

(Move `normalizedHost` into origin.go unchanged, with its doc comment.) In manifest.go, replace `sameOrigin`'s body with `return sameOriginURLs(fileURL, m.url)` — keep its doc comment, trimmed of the mechanics now documented on `sameOriginURLs`. Remove `net/url` from manifest.go imports ONLY if nothing else there uses it (check: `addQueryParam` uses it — it stays).

- [ ] **Step 2: Verify no behavior change**

Run: `go test ./internal/source/custom/ -race -v && go build ./...`
Expected: PASS, zero test-file changes (`git status` shows only origin.go + manifest.go).

- [ ] **Step 3: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/origin.go internal/source/custom/manifest.go
git commit -m "refactor(source): extract shared same-origin helper"
```

---

## Task 4: API source construction, request layer, identity, capabilities

**Files:**
- Create: `internal/source/custom/api.go`
- Test: `internal/source/custom/api_test.go`

**Interfaces:**
- Consumes: `APIConfig` (Task 1), `sameOriginURLs` (Task 3), `AuthConfig`, `addQueryParam`, `source.ErrNotSupported`/`Capabilities`, `domain.ErrAuthRequired`.
- Produces:
  - `custom.NewAPI(def SourceDefinition) (*API, error)` — pure construction (no I/O); dedicated `&http.Client{Timeout: apiRequestTimeout}` (new const, 30s); `pageStart` resolved (nil → 1)
  - `(a *API) SetAPIKey(key string)`, `IsAuthenticated() bool`, `DownloadHeaders(fileURL string) map[string]string` (header-mode + key + `sameOriginURLs(fileURL, a.baseURL)` only)
  - `ID/Name/AuthURL/ExchangeToken/GetDependencies` (the latter two `ErrNotSupported`); `Capabilities()` = `{Search: eps.Search != nil, Dependencies: false, Updates: eps.GetMod != nil, Auth: auth != nil}`
  - unexported request layer used by Tasks 5–7: `buildEndpointURL(pathTemplate string, vals map[string]string) string` (every `{placeholder}` in vals replaced with its URL-escaped value; unknown placeholders left intact) and `getJSON(ctx context.Context, rawURL string) (any, error)` (GET; auth attach header/query — query via `addQueryParam`; 401 → wraps `domain.ErrAuthRequired`; non-200 → error with status; 10 MiB `maxAPIResponseSize` cap; `*url.Error` unwrapped before wrapping so keys never leak — mirror `fetchRemote`'s redaction exactly)
  - Temporary stubs so the interface compiles: `Search`, `GetMod`, `GetModFiles`, `GetDownloadURL`, `CheckUpdates` all return `ErrNotSupported` (Tasks 5–7 replace them); compile-time assertions `var _ source.ModSource = (*API)(nil)`, `var _ source.CapabilityReporter = (*API)(nil)`, `var _ source.DownloadHeaderProvider = (*API)(nil)`

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/api_test.go`:

```go
package custom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apiDef(baseURL string) SourceDefinition {
	def := validAPIDef()
	def.API.BaseURL = baseURL
	def.AllowHTTP = true // httptest serves plain http
	return def
}

func TestNewAPIIsPure(t *testing.T) {
	a, err := NewAPI(apiDef("https://unreachable.invalid"))
	require.NoError(t, err)
	assert.Equal(t, "my-api", a.ID())
	assert.Equal(t, "My API", a.Name())
	assert.Equal(t, apiRequestTimeout, a.httpClient.Timeout)
	assert.Equal(t, 1, a.pageStart) // default when page_start omitted
}

func TestNewAPIExplicitPageStartZero(t *testing.T) {
	def := apiDef("https://x.test")
	zero := 0
	def.API.PageStart = &zero
	a, err := NewAPI(def)
	require.NoError(t, err)
	assert.Equal(t, 0, a.pageStart)
}

func TestAPIIdentityAndCapabilities(t *testing.T) {
	a, err := NewAPI(apiDef("https://x.test"))
	require.NoError(t, err)
	assert.Equal(t, source.Capabilities{Search: true, Dependencies: false, Updates: true, Auth: false}, a.Capabilities())
	assert.Empty(t, a.AuthURL())

	_, err = a.ExchangeToken(context.Background(), "code")
	assert.True(t, errors.Is(err, source.ErrNotSupported))
	_, err = a.GetDependencies(context.Background(), &domain.Mod{ID: "x"})
	assert.True(t, errors.Is(err, source.ErrNotSupported))

	def := apiDef("https://x.test")
	def.API.Endpoints.Search = nil
	def.API.Endpoints.GetMod = nil
	limited, err := NewAPI(def)
	require.NoError(t, err)
	assert.Equal(t, source.Capabilities{Search: false, Dependencies: false, Updates: false, Auth: false}, limited.Capabilities())
}

func TestBuildEndpointURL(t *testing.T) {
	got := buildEndpointURL("/mods?q={query}&page={page}&x={unknown}", map[string]string{
		"query": "cool mod & more", "page": "2",
	})
	assert.Equal(t, "/mods?q=cool+mod+%26+more&page=2&x={unknown}", got)
}

func TestGetJSONAuthAndErrors(t *testing.T) {
	t.Run("header auth attached and 401 maps to ErrAuthRequired", func(t *testing.T) {
		var gotKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("X-API-Key")
			if gotKey == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"ok": true}`))
		}))
		defer srv.Close()

		def := apiDef(srv.URL)
		def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		a, err := NewAPI(def)
		require.NoError(t, err)

		_, err = a.getJSON(context.Background(), srv.URL+"/mods/1")
		assert.True(t, errors.Is(err, domain.ErrAuthRequired))

		a.SetAPIKey("sekrit")
		doc, err := a.getJSON(context.Background(), srv.URL+"/mods/1")
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotKey)
		assert.Equal(t, map[string]any{"ok": true}, doc)
	})

	t.Run("query auth attached", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query().Get("api_key")
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()

		def := apiDef(srv.URL)
		def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		a, err := NewAPI(def)
		require.NoError(t, err)
		a.SetAPIKey("sekrit")

		_, err = a.getJSON(context.Background(), srv.URL+"/mods/1")
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotQuery)
	})

	t.Run("network error does not leak query key", func(t *testing.T) {
		def := apiDef("http://127.0.0.1:1")
		def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		a, err := NewAPI(def)
		require.NoError(t, err)
		a.SetAPIKey("LEAKME")

		_, err = a.getJSON(context.Background(), "http://127.0.0.1:1/mods/1")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "LEAKME")
		assert.Contains(t, err.Error(), "my-api")
	})

	t.Run("non-200 surfaces status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		a, err := NewAPI(apiDef(srv.URL))
		require.NoError(t, err)
		_, err = a.getJSON(context.Background(), srv.URL+"/x")
		assert.ErrorContains(t, err, "HTTP 500")
	})
}

func TestAPIDownloadHeaders(t *testing.T) {
	def := apiDef("https://api.x.test")
	def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
	a, err := NewAPI(def)
	require.NoError(t, err)
	a.SetAPIKey("sekrit")

	assert.Equal(t, map[string]string{"X-API-Key": "sekrit"}, a.DownloadHeaders("https://api.x.test/dl/1.zip"))
	assert.Nil(t, a.DownloadHeaders("https://cdn.elsewhere.test/dl/1.zip"), "cross-origin downloads must not receive the key")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run 'TestNewAPI|TestAPIIdentity|TestBuildEndpointURL|TestGetJSON|TestAPIDownloadHeaders' -v`
Expected: FAIL — `undefined: NewAPI`.

- [ ] **Step 3: Implement**

Create `internal/source/custom/api.go`:

```go
package custom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// apiRequestTimeout bounds every request to a declarative REST source.
const apiRequestTimeout = 30 * time.Second

// maxAPIResponseSize bounds how much of an API response we read into memory
// (same defense class as maxManifestSize).
const maxAPIResponseSize = 10 << 20 // 10 MiB

// API is a ModSource backed by a declaratively-described GET+JSON REST API
// (design §4). Endpoints that the definition omits surface as ErrNotSupported
// capability gaps rather than errors at load time.
type API struct {
	id        string
	name      string
	baseURL   string
	pageStart int
	auth      *AuthConfig
	endpoints APIEndpoints
	mappings  APIMappings

	apiKey     string
	httpClient *http.Client
}

// NewAPI constructs an api source from a validated definition. It performs
// no I/O — a valid definition always registers; request problems surface as
// operation errors.
func NewAPI(def SourceDefinition) (*API, error) {
	cfg := def.API
	pageStart := 1
	if cfg.PageStart != nil {
		pageStart = *cfg.PageStart
	}
	return &API{
		id:         def.ID,
		name:       def.Name,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		pageStart:  pageStart,
		auth:       cfg.Auth,
		endpoints:  cfg.Endpoints,
		mappings:   cfg.Mappings,
		httpClient: &http.Client{Timeout: apiRequestTimeout},
	}, nil
}

// ID implements source.ModSource.
func (a *API) ID() string { return a.id }

// Name implements source.ModSource.
func (a *API) Name() string { return a.name }

// AuthURL implements source.ModSource; api sources use API keys, not OAuth.
func (a *API) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (a *API) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", a.id, source.ErrNotSupported)
}

// GetDependencies implements source.ModSource; always unsupported in v1
// (design §4).
func (a *API) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", a.id, source.ErrNotSupported)
}

// SetAPIKey provides the API key resolved at startup (env var or token store).
func (a *API) SetAPIKey(key string) { a.apiKey = key }

// IsAuthenticated reports whether an API key is configured.
func (a *API) IsAuthenticated() bool { return a.apiKey != "" }

// Capabilities implements source.CapabilityReporter: an undefined endpoint is
// an unsupported capability (design §4/§7).
func (a *API) Capabilities() source.Capabilities {
	return source.Capabilities{
		Search:       a.endpoints.Search != nil,
		Dependencies: false,
		Updates:      a.endpoints.GetMod != nil,
		Auth:         a.auth != nil,
	}
}

// DownloadHeaders implements source.DownloadHeaderProvider: header-mode keys
// go only to downloads on the API's own origin (design §9).
func (a *API) DownloadHeaders(fileURL string) map[string]string {
	if a.auth == nil || a.auth.APIKey.In != "header" || a.apiKey == "" {
		return nil
	}
	if !sameOriginURLs(fileURL, a.baseURL) {
		return nil
	}
	return map[string]string{a.auth.APIKey.Name: a.apiKey}
}

// buildEndpointURL substitutes {placeholder} tokens in an endpoint path
// template with URL-escaped values. Placeholders without a value are left
// intact (they will typically 404 loudly rather than silently matching).
func buildEndpointURL(pathTemplate string, vals map[string]string) string {
	out := pathTemplate
	for name, value := range vals {
		out = strings.ReplaceAll(out, "{"+name+"}", url.QueryEscape(value))
	}
	return out
}

// getJSON performs an authenticated GET against rawURL and decodes the JSON
// response. 401 maps to domain.ErrAuthRequired; other non-200s surface the
// status. Errors never contain the request URL's query string (keys ride
// there in query mode) — the inner *url.Error is unwrapped, mirroring the
// manifest fetcher's redaction.
func (a *API) getJSON(ctx context.Context, rawURL string) (any, error) {
	reqURL := rawURL
	if a.auth != nil && a.auth.APIKey.In == "query" && a.apiKey != "" {
		u, err := addQueryParam(reqURL, a.auth.APIKey.Name, a.apiKey)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", a.id, err)
		}
		reqURL = u
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("source %q: building request: %w", a.id, err)
	}
	if a.auth != nil && a.auth.APIKey.In == "header" && a.apiKey != "" {
		req.Header.Set(a.auth.APIKey.Name, a.apiKey)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // strip the URL (and any query-mode key) from the message
		}
		return nil, fmt.Errorf("source %q: requesting %s: %w", a.id, redactedURL(rawURL), err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("source %q: %w", a.id, domain.ErrAuthRequired)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source %q: requesting %s: HTTP %d", a.id, redactedURL(rawURL), resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("source %q: reading response: %w", a.id, err)
	}
	if len(data) > maxAPIResponseSize {
		return nil, fmt.Errorf("source %q: response from %s exceeds %d bytes", a.id, redactedURL(rawURL), maxAPIResponseSize)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("source %q: parsing response from %s: %w", a.id, redactedURL(rawURL), err)
	}
	return doc, nil
}

// redactedURL strips the query string from a URL for error messages.
func redactedURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparseable URL)"
	}
	u.RawQuery = ""
	return u.String()
}

var (
	_ source.ModSource              = (*API)(nil)
	_ source.CapabilityReporter     = (*API)(nil)
	_ source.DownloadHeaderProvider = (*API)(nil)
)
```

Add temporary stubs at the bottom (Tasks 5–7 replace them):

```go
// Search is implemented in the search task (replaces this stub).
func (a *API) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", a.id, source.ErrNotSupported)
}

// GetMod is implemented in the read-ops task (replaces this stub).
func (a *API) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, fmt.Errorf("source %q: fetching mod: %w", a.id, source.ErrNotSupported)
}

// GetModFiles is implemented in the read-ops task (replaces this stub).
func (a *API) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, fmt.Errorf("source %q: listing files: %w", a.id, source.ErrNotSupported)
}

// GetDownloadURL is implemented in the read-ops task (replaces this stub).
func (a *API) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", fmt.Errorf("source %q: download URL: %w", a.id, source.ErrNotSupported)
}

// CheckUpdates is implemented in the update-check task (replaces this stub).
func (a *API) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, fmt.Errorf("source %q: update checks: %w", a.id, source.ErrNotSupported)
}
```

Note on `buildEndpointURL` escaping: `url.QueryEscape` is correct for the query-string placeholders this design targets (design §4's examples put placeholders in query strings and simple path segments; QueryEscape's `+` for spaces is acceptable in both for v1).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/api.go internal/source/custom/api_test.go
git commit -m "feat(source): api source construction, request layer, and capabilities"
```

---

## Task 5: API search

**Files:**
- Modify: `internal/source/custom/api.go` (replace the Search stub)
- Test: `internal/source/custom/api_test.go` (append)

**Interfaces:**
- Consumes: `buildEndpointURL`, `getJSON`, `lookupPath`, `coerceInt64`, `mapMod`.
- Produces: real `Search` — placeholder values `{game_id}`=query.GameID, `{query}`=query.Query, `{page}`=strconv(query.Page+a.pageStart), `{page_size}`=strconv(pageSize), `{offset}`=strconv(query.Page×pageSize); pageSize default 20 when ≤0; list dot-path → array → `mapMod` per element (a mapping error on one element fails the operation with the element index); `TotalCount` from the total dot-path (0 when absent/unmapped); missing search endpoint → `ErrNotSupported`; every returned mod gets `GameID = query.GameID`.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/custom/api_test.go`:

```go
const apiSearchResponse = `{
	"results": [
		{"id": 1, "name": "Alpha Mod", "latest_version": "1.0.0"},
		{"id": 2, "name": "Beta Mod", "latest_version": "2.0.0"}
	],
	"pagination": {"total": 41}
}`

func TestAPISearch(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		_, _ = w.Write([]byte(apiSearchResponse))
	}))
	defer srv.Close()

	def := apiDef(srv.URL)
	def.API.Endpoints.Search = &EndpointConfig{
		Path:  "/mods?game={game_id}&q={query}&page={page}&limit={page_size}&skip={offset}",
		List:  "results",
		Total: "pagination.total",
	}
	a, err := NewAPI(def)
	require.NoError(t, err)

	res, err := a.Search(context.Background(), source.SearchQuery{
		GameID: "skyrim", Query: "cool mod", Page: 2, PageSize: 10,
	})
	require.NoError(t, err)

	// {page} = 0-based page + page_start(1) = 3; {offset} = 2*10 = 20.
	assert.Equal(t, "/mods?game=skyrim&q=cool+mod&page=3&limit=10&skip=20", gotPath)
	require.Len(t, res.Mods, 2)
	assert.Equal(t, "1", res.Mods[0].ID)
	assert.Equal(t, "Alpha Mod", res.Mods[0].Name)
	assert.Equal(t, "my-api", res.Mods[0].SourceID)
	assert.Equal(t, "skyrim", res.Mods[0].GameID)
	assert.Equal(t, 41, res.TotalCount)
	assert.Equal(t, 2, res.Page)
	assert.Equal(t, 10, res.PageSize)
}

func TestAPISearchNoEndpoint(t *testing.T) {
	def := apiDef("https://x.test")
	def.API.Endpoints.Search = nil
	a, err := NewAPI(def)
	require.NoError(t, err)

	_, err = a.Search(context.Background(), source.SearchQuery{Query: "x"})
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

func TestAPISearchMissingListPathFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"unexpected": {}}`))
	}))
	defer srv.Close()

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	_, err = a.Search(context.Background(), source.SearchQuery{Query: "x"})
	assert.ErrorContains(t, err, "results")
}

func TestAPISearchTotalAbsentIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer srv.Close()

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	res, err := a.Search(context.Background(), source.SearchQuery{Query: "x"})
	require.NoError(t, err)
	assert.Zero(t, res.TotalCount)
	assert.Empty(t, res.Mods)
}
```

(Note `apiDef`'s `validAPIDef` search endpoint already uses `List: "results"`, `Total: "total"` — the absent-total test relies on `pagination.total`… no: `validAPIDef` sets `Total: "total"`, and the response has no `total` key, so TotalCount is 0. Correct as written.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestAPISearch -v`
Expected: FAIL — stub returns ErrNotSupported for every case.

- [ ] **Step 3: Implement (replace the Search stub)**

```go
// Search implements source.ModSource by executing the search endpoint
// template and mapping the results (design §4). An undefined search endpoint
// is an unsupported capability.
func (a *API) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	ep := a.endpoints.Search
	if ep == nil {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", a.id, source.ErrNotSupported)
	}

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	vals := map[string]string{
		"game_id":   query.GameID,
		"query":     query.Query,
		"page":      strconv.Itoa(query.Page + a.pageStart),
		"page_size": strconv.Itoa(pageSize),
		"offset":    strconv.Itoa(query.Page * pageSize),
	}

	doc, err := a.getJSON(ctx, a.baseURL+buildEndpointURL(ep.Path, vals))
	if err != nil {
		return source.SearchResult{}, fmt.Errorf("searching: %w", err)
	}

	listVal, ok := lookupPath(doc, ep.List)
	if !ok {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: response has no %q array", a.id, ep.List)
	}
	items, ok := listVal.([]any)
	if !ok {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: %q is not an array", a.id, ep.List)
	}

	mods := make([]domain.Mod, 0, len(items))
	for i, item := range items {
		mod, err := mapMod(item, a.mappings.Mod, a.id)
		if err != nil {
			return source.SearchResult{}, fmt.Errorf("source %q: searching: %s[%d]: %w", a.id, ep.List, i, err)
		}
		mod.GameID = query.GameID
		mods = append(mods, mod)
	}

	total := 0
	if ep.Total != "" {
		if v, found := lookupPath(doc, ep.Total); found {
			total = int(coerceInt64(v))
		}
	}

	return source.SearchResult{Mods: mods, TotalCount: total, Page: query.Page, PageSize: pageSize}, nil
}
```

(Add `"strconv"` to api.go imports.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/api.go internal/source/custom/api_test.go
git commit -m "feat(source): api source search with template placeholders and pagination"
```

---

## Task 6: API GetMod, GetModFiles, GetDownloadURL

**Files:**
- Modify: `internal/source/custom/api.go` (replace three stubs)
- Test: `internal/source/custom/api_test.go` (append)

**Interfaces:**
- Produces: real `GetMod` (get_mod endpoint; `{mod_id}` + `{game_id}`; response root mapped via `mapMod`; `GameID` echoed from the param), `GetModFiles` (mod_files endpoint; list path; `mapFile` per element; `{mod_id}`/`{game_id}` from the mod), `GetDownloadURL` (download_url endpoint; `{file_id}`/`{mod_id}`/`{game_id}`; `field` dot-path → string; query-mode key appended ONLY when `sameOriginURLs(downloadURL, a.baseURL)`). Missing endpoints → `ErrNotSupported`.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/custom/api_test.go`:

```go
// newTestAPIServer wires a minimal fake REST API for the read ops.
func newTestAPIServer(t *testing.T) (*httptest.Server, *API) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/mods/77", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}`))
	})
	mux.HandleFunc("/mods/77/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files": [{"id": 900, "file_name": "cool-1.2.0.zip", "version": "1.2.0", "size_bytes": 4}]}`))
	})
	mux.HandleFunc("/files/900/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "` + srv.URL + `/dl/cool-1.2.0.zip"}`))
	})

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	return srv, a
}

func TestAPIGetMod(t *testing.T) {
	_, a := newTestAPIServer(t)

	mod, err := a.GetMod(context.Background(), "skyrim", "77")
	require.NoError(t, err)
	assert.Equal(t, "77", mod.ID)
	assert.Equal(t, "Cool Mod", mod.Name)
	assert.Equal(t, "1.2.0", mod.Version)
	assert.Equal(t, "skyrim", mod.GameID)
	assert.Equal(t, "my-api", mod.SourceID)
}

func TestAPIGetModFiles(t *testing.T) {
	_, a := newTestAPIServer(t)

	files, err := a.GetModFiles(context.Background(), &domain.Mod{ID: "77", GameID: "skyrim"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "900", files[0].ID)
	assert.Equal(t, "cool-1.2.0.zip", files[0].FileName)
	assert.Equal(t, int64(4), files[0].Size)
}

func TestAPIGetDownloadURL(t *testing.T) {
	srv, a := newTestAPIServer(t)

	u, err := a.GetDownloadURL(context.Background(), &domain.Mod{ID: "77", GameID: "skyrim"}, "900")
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/dl/cool-1.2.0.zip", u)
}

func TestAPIGetDownloadURLQueryAuthSameOriginOnly(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sameOrigin := srv.URL + "/dl/a.zip"
	mux.HandleFunc("/files/1/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "` + sameOrigin + `"}`))
	})
	mux.HandleFunc("/files/2/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "https://cdn.elsewhere.test/b.zip"}`))
	})

	def := apiDef(srv.URL)
	def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
	a, err := NewAPI(def)
	require.NoError(t, err)
	a.SetAPIKey("sekrit")

	u, err := a.GetDownloadURL(context.Background(), &domain.Mod{ID: "x"}, "1")
	require.NoError(t, err)
	assert.Contains(t, u, "api_key=sekrit", "same-origin download URL gets the query key")

	u, err = a.GetDownloadURL(context.Background(), &domain.Mod{ID: "x"}, "2")
	require.NoError(t, err)
	assert.NotContains(t, u, "sekrit", "cross-origin download URL must not carry the key")
}

func TestAPIReadOpsMissingEndpoints(t *testing.T) {
	def := apiDef("https://x.test")
	def.API.Endpoints = APIEndpoints{GetMod: &EndpointConfig{Path: "/mods/{mod_id}"}}
	a, err := NewAPI(def)
	require.NoError(t, err)

	_, err = a.GetModFiles(context.Background(), &domain.Mod{ID: "1"})
	assert.True(t, errors.Is(err, source.ErrNotSupported))
	_, err = a.GetDownloadURL(context.Background(), &domain.Mod{ID: "1"}, "f")
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run 'TestAPIGet|TestAPIReadOps' -v`
Expected: FAIL — stubs return ErrNotSupported.

- [ ] **Step 3: Implement (replace the three stubs)**

```go
// GetMod implements source.ModSource via the get_mod endpoint. gameID feeds
// the {game_id} placeholder and is echoed onto the returned mod for
// downstream attribution (the persisted row is normalized by the installer).
func (a *API) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	ep := a.endpoints.GetMod
	if ep == nil {
		return nil, fmt.Errorf("source %q: fetching mod: %w", a.id, source.ErrNotSupported)
	}

	vals := map[string]string{"mod_id": modID, "game_id": gameID}
	doc, err := a.getJSON(ctx, a.baseURL+buildEndpointURL(ep.Path, vals))
	if err != nil {
		return nil, fmt.Errorf("fetching mod %s: %w", modID, err)
	}

	mod, err := mapMod(doc, a.mappings.Mod, a.id)
	if err != nil {
		return nil, fmt.Errorf("source %q: mod %s: %w", a.id, modID, err)
	}
	mod.GameID = gameID
	return &mod, nil
}

// GetModFiles implements source.ModSource via the mod_files endpoint.
func (a *API) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	ep := a.endpoints.ModFiles
	if ep == nil {
		return nil, fmt.Errorf("source %q: listing files: %w", a.id, source.ErrNotSupported)
	}

	vals := map[string]string{"mod_id": mod.ID, "game_id": mod.GameID}
	doc, err := a.getJSON(ctx, a.baseURL+buildEndpointURL(ep.Path, vals))
	if err != nil {
		return nil, fmt.Errorf("listing files for %s: %w", mod.ID, err)
	}

	listVal, ok := lookupPath(doc, ep.List)
	if !ok {
		return nil, fmt.Errorf("source %q: mod %s: response has no %q array", a.id, mod.ID, ep.List)
	}
	items, ok := listVal.([]any)
	if !ok {
		return nil, fmt.Errorf("source %q: mod %s: %q is not an array", a.id, mod.ID, ep.List)
	}

	files := make([]domain.DownloadableFile, 0, len(items))
	for i, item := range items {
		f, err := mapFile(item, a.mappings.File)
		if err != nil {
			return nil, fmt.Errorf("source %q: mod %s: %s[%d]: %w", a.id, mod.ID, ep.List, i, err)
		}
		files = append(files, f)
	}
	if len(files) == 1 {
		files[0].IsPrimary = true // a single file is trivially the primary one
	}
	return files, nil
}

// GetDownloadURL implements source.ModSource via the download_url endpoint.
// Query-mode keys are appended only for same-origin download URLs (design §9).
func (a *API) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	ep := a.endpoints.DownloadURL
	if ep == nil {
		return "", fmt.Errorf("source %q: download URL: %w", a.id, source.ErrNotSupported)
	}

	vals := map[string]string{"file_id": fileID, "mod_id": mod.ID, "game_id": mod.GameID}
	doc, err := a.getJSON(ctx, a.baseURL+buildEndpointURL(ep.Path, vals))
	if err != nil {
		return "", fmt.Errorf("download URL for file %s: %w", fileID, err)
	}

	v, ok := lookupPath(doc, ep.Field)
	if !ok {
		return "", fmt.Errorf("source %q: file %s: response has no %q field", a.id, fileID, ep.Field)
	}
	dlURL := coerceString(v)
	if dlURL == "" {
		return "", fmt.Errorf("source %q: file %s: %q is not a URL string", a.id, fileID, ep.Field)
	}

	if a.auth != nil && a.auth.APIKey.In == "query" && a.apiKey != "" && sameOriginURLs(dlURL, a.baseURL) {
		withKey, err := addQueryParam(dlURL, a.auth.APIKey.Name, a.apiKey)
		if err != nil {
			return "", fmt.Errorf("source %q: file %s: %w", a.id, fileID, err)
		}
		dlURL = withKey
	}
	return dlURL, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/api.go internal/source/custom/api_test.go
git commit -m "feat(source): api source read operations with same-origin key scoping"
```

---

## Task 7: API update checks

**Files:**
- Modify: `internal/source/custom/api.go` (replace the CheckUpdates stub)
- Test: `internal/source/custom/api_test.go` (append)

**Interfaces:**
- Produces: real `CheckUpdates` — per installed mod: ctx-cancellation check, `GetMod(ctx, inst.GameID, inst.ID)` (the Updater already passes source-mapped GameIDs), `domain.IsNewerVersion` comparison. Per-mod fetch errors are collected (`errors.Join`) and returned alongside partial results — never a silent skip. Missing get_mod endpoint → `ErrNotSupported`.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/custom/api_test.go`:

```go
func TestAPICheckUpdates(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/mods/77", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}`))
	})
	mux.HandleFunc("/mods/88", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 88, "name": "Fresh Mod", "latest_version": "0.9.0"}`))
	})
	mux.HandleFunc("/mods/99", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusInternalServerError)
	})

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "77", SourceID: "my-api", Version: "1.0.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "88", SourceID: "my-api", Version: "0.9.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "99", SourceID: "my-api", Version: "1.0", GameID: "skyrim"}},
	}

	updates, err := a.CheckUpdates(context.Background(), installed)
	require.Error(t, err, "the failing mod must surface, not vanish")
	assert.Contains(t, err.Error(), "99")
	require.Len(t, updates, 1, "partial results returned alongside the error")
	assert.Equal(t, "77", updates[0].InstalledMod.ID)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

func TestAPICheckUpdatesNoEndpoint(t *testing.T) {
	def := apiDef("https://x.test")
	def.API.Endpoints = APIEndpoints{Search: &EndpointConfig{Path: "/mods", List: "results"}}
	a, err := NewAPI(def)
	require.NoError(t, err)

	_, err = a.CheckUpdates(context.Background(), []domain.InstalledMod{{Mod: domain.Mod{ID: "1"}}})
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestAPICheckUpdates -v`
Expected: FAIL — stub returns ErrNotSupported for both.

- [ ] **Step 3: Implement (replace the stub)**

```go
// CheckUpdates implements source.ModSource generically via get_mod + version
// comparison (design §4). Per-mod fetch failures are collected and returned
// alongside partial results so a single flaky mod page doesn't hide the rest
// — and doesn't get silently skipped either.
func (a *API) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	if a.endpoints.GetMod == nil {
		return nil, fmt.Errorf("source %q: update checks: %w", a.id, source.ErrNotSupported)
	}

	var updates []domain.Update
	var errs []error
	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}
		current, err := a.GetMod(ctx, inst.GameID, inst.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if domain.IsNewerVersion(inst.Version, current.Version) {
			updates = append(updates, domain.Update{
				InstalledMod: inst,
				NewVersion:   current.Version,
			})
		}
	}
	return updates, errors.Join(errs...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/api.go internal/source/custom/api_test.go
git commit -m "feat(source): api source update checks via get_mod"
```

---

## Task 8: Factory, cmd wiring, and `source validate --probe`

**Files:**
- Modify: `internal/source/custom/custom.go` + `custom_test.go` (factory), `cmd/lmm/source.go` (`isCustomSource`, probe flag + logic)
- Test: `cmd/lmm/source_test.go` (append)

**Interfaces:**
- Produces: `custom.New` handles `TypeAPI` (the "not yet supported" default now only catches unknown strings, which `Validate` already rejects — keep it as defense); `isCustomSource` recognizes `*custom.API`; `lmm source validate <file> --probe [--id <mod-id>]` — after static validation, constructs the source and performs a live smoke test:
  - directory → scan via `Search(ctx, {})`, report mod count
  - manifest → `Search(ctx, {})` (fetch+parse), report mod count
  - api → `Search(ctx, {PageSize: 1})` when the search endpoint exists; else `GetMod(ctx, "", <--id>)` when `--id` given; else a clear error saying this definition needs `--id` for probing
  - probe resolves the API key like startup does: `LMM_<ID>_API_KEY` env var, then the token store (probe runs inside `withService`)
  - probe failure → non-zero exit with the operation's error (which names the source and URL)

- [ ] **Step 1: Write the failing tests**

`internal/source/custom/custom_test.go` — move `TypeAPI` out of the unimplemented loop:

```go
	t.Run("api type constructs a source", func(t *testing.T) {
		def := validAPIDef()
		src, err := New(def)
		assert.NoError(t, err)
		assert.Equal(t, "my-api", src.ID())
	})

	t.Run("unknown type is rejected", func(t *testing.T) {
		def := SourceDefinition{ID: "x", Name: "X", Type: "ftp"}
		_, err := New(def)
		assert.ErrorContains(t, err, "not yet supported")
	})
```

`cmd/lmm/source_test.go` — append (reuse the file's existing `run` helper pattern from `TestSourceValidateCmd`; read it first):

```go
func TestSourceValidateProbe(t *testing.T) {
	t.Run("probe directory source reports mod count", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "SomeMod"), 0755))
		path := filepath.Join(t.TempDir(), "dir.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: probe-dir
name: Probe Dir
type: directory
directory:
  path: `+root+`
`), 0644))

		out, err := runSourceCmd(t, "source", "validate", path, "--probe")
		assert.NoError(t, err)
		assert.Contains(t, out, "probe: ok")
		assert.Contains(t, out, "1 mod(s)")
	})

	t.Run("probe api without search requires --id", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "api.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: probe-api
name: Probe API
type: api
api:
  base_url: https://api.x.test
  endpoints:
    get_mod:
      path: /mods/{mod_id}
  mappings:
    mod:
      id: id
      name: name
`), 0644))

		_, err := runSourceCmd(t, "source", "validate", path, "--probe")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--id")
	})

	t.Run("probe failure exits non-zero", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: probe-bad
name: Probe Bad
type: manifest
manifest:
  url: `+filepath.Join(t.TempDir(), "missing.yaml")+`
`), 0644))

		_, err := runSourceCmd(t, "source", "validate", path, "--probe")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probe-bad")
	})
}
```

Adaptation note: `runSourceCmd` is whatever helper shape the existing `TestSourceValidateCmd` uses to execute the command tree with args and capture output — reuse/extend it rather than duplicating; if the existing helper doesn't reset the `--probe` flag between runs, reset it explicitly (package-level cobra flags persist across tests). The probe path runs `withService` — the existing cmd tests already run against a real temp-config service via the global flag setup; mirror how other service-backed cmd tests arrange that (check `list_test.go`); if arranging a service in this test file is disproportionate, mark the directory-probe subtest to configure `cfgFile`/data dirs the way those tests do.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/custom/ -run TestNew -v; go test ./cmd/lmm/ -run TestSourceValidateProbe -v`
Expected: FAIL — api case errors "not yet supported"; `unknown flag: --probe`.

- [ ] **Step 3: Implement**

`internal/source/custom/custom.go`:

```go
func New(def SourceDefinition) (source.ModSource, error) {
	switch def.Type {
	case TypeDirectory:
		return NewDirectory(def)
	case TypeManifest:
		return NewManifest(def)
	case TypeAPI:
		return NewAPI(def)
	default:
		return nil, fmt.Errorf("source type %q is not yet supported", def.Type)
	}
}
```

`cmd/lmm/source.go`:
1. `isCustomSource`: add `*custom.API` to the type switch.
2. Add flags + probe logic to `sourceValidateCmd`:

```go
var (
	sourceProbe   bool
	sourceProbeID string
)

var sourceValidateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Validate a source definition file",
	Long:  "Parse and validate a user-defined source definition YAML file, reporting any problems. With --probe, also perform a live smoke test (directory scan, manifest fetch+parse, or an API call).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		def, err := config.LoadSourceDefinitionFile(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%s source %q)\n", args[0], def.Type, def.ID)
		if !sourceProbe {
			return nil
		}
		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			return probeSource(ctx, cmd, svc, def)
		})
	},
}

// probeSource constructs the definition's source and performs one live
// operation against it, so users can smoke-test a definition before relying
// on it (design §8).
func probeSource(ctx context.Context, cmd *cobra.Command, svc *core.Service, def custom.SourceDefinition) error {
	src, err := custom.New(def)
	if err != nil {
		return fmt.Errorf("probe: constructing source: %w", err)
	}
	if a, ok := src.(interface{ SetAPIKey(string) }); ok {
		if key := getSourceAPIKey(svc, def.ID, envKeyForSourceID(def.ID)); key != "" {
			a.SetAPIKey(key)
		}
	}

	switch def.Type {
	case custom.TypeDirectory, custom.TypeManifest:
		res, err := src.Search(ctx, source.SearchQuery{PageSize: 1})
		if err != nil {
			return fmt.Errorf("probe: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "probe: ok — %d mod(s) visible\n", res.TotalCount)
	case custom.TypeAPI:
		if def.API.Endpoints.Search != nil {
			res, err := src.Search(ctx, source.SearchQuery{PageSize: 1})
			if err != nil {
				return fmt.Errorf("probe: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "probe: ok — search responded (%d total reported)\n", res.TotalCount)
			return nil
		}
		if sourceProbeID == "" {
			return fmt.Errorf("probe: this definition has no search endpoint; provide a known mod id with --id to probe get_mod")
		}
		mod, err := src.GetMod(ctx, "", sourceProbeID)
		if err != nil {
			return fmt.Errorf("probe: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "probe: ok — get_mod %s returned %q\n", sourceProbeID, mod.Name)
	}
	return nil
}
```

3. In `init()`: `sourceValidateCmd.Flags().BoolVar(&sourceProbe, "probe", false, "perform a live smoke test after validation")` and `sourceValidateCmd.Flags().StringVar(&sourceProbeID, "id", "", "mod id to probe with (api definitions without a search endpoint)")`.
4. Note the directory/manifest probe reports `res.TotalCount` — for the directory source, an empty query matches all mods, so TotalCount is the scan size even with PageSize 1. Check imports: `custom`, `source`, `core`, `context` (some already present).

- [ ] **Step 4: Run tests and full build**

Run: `go test ./internal/source/custom/ ./cmd/lmm/ -v && go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/custom.go internal/source/custom/custom_test.go cmd/lmm/source.go cmd/lmm/source_test.go
git commit -m "feat(cli): construct api sources and add 'source validate --probe'"
```

---

## Task 9: End-to-end test — fake REST API as a full source

**Files:**
- Test: `internal/core/service_api_source_test.go` (create)

Pins #49's acceptance criteria: (1) an install-by-ID-only definition works with search cleanly unsupported; (2) the auth key is attached per the definition and never logged.

- [ ] **Step 1: Write the test**

Create `internal/core/service_api_source_test.go` (`package core_test`, mirroring `service_manifest_source_test.go`'s construction — read it first):

```go
package core_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFakeRESTServer serves a minimal authenticated mod API plus the payload.
func newFakeRESTServer(t *testing.T, requireKey bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if requireKey && r.Header.Get("X-API-Key") != "e2e-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/mods", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results": [{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}], "total": 1}`)
	}))
	mux.HandleFunc("/mods/77", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}`)
	}))
	mux.HandleFunc("/mods/77/files", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"files": [{"id": 900, "file_name": "cool-1.2.0.zip", "version": "1.2.0", "size_bytes": 11}]}`)
	}))
	mux.HandleFunc("/files/900/download", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"url": %q}`, srv.URL+"/dl/cool-1.2.0.zip")
	}))
	mux.HandleFunc("/dl/cool-1.2.0.zip", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mod payload"))
	}))

	return srv
}

func apiSourceDef(baseURL string, withSearch bool) custom.SourceDefinition {
	endpoints := custom.APIEndpoints{
		GetMod:      &custom.EndpointConfig{Path: "/mods/{mod_id}"},
		ModFiles:    &custom.EndpointConfig{Path: "/mods/{mod_id}/files", List: "files"},
		DownloadURL: &custom.EndpointConfig{Path: "/files/{file_id}/download", Field: "url"},
	}
	if withSearch {
		endpoints.Search = &custom.EndpointConfig{Path: "/mods?q={query}&page={page}", List: "results", Total: "total"}
	}
	return custom.SourceDefinition{
		ID: "e2e-api", Name: "E2E API", Type: custom.TypeAPI, AllowHTTP: true,
		API: &custom.APIConfig{
			BaseURL:   baseURL,
			Auth:      &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
			Endpoints: endpoints,
			Mappings: custom.APIMappings{
				Mod:  map[string]string{"id": "id", "name": "name", "version": "latest_version"},
				File: map[string]string{"id": "id", "filename": "file_name", "version": "version", "size": "size_bytes"},
			},
		},
	}
}

func TestAPISourceEndToEnd(t *testing.T) {
	srv := newFakeRESTServer(t, true)

	src, err := custom.New(apiSourceDef(srv.URL, true))
	require.NoError(t, err)
	src.(interface{ SetAPIKey(string) }).SetAPIKey("e2e-key")

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))
	ctx := context.Background()

	res, err := src.Search(ctx, source.SearchQuery{Query: "cool", GameID: "testgame", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]
	assert.Equal(t, "e2e-api", mod.SourceID)
	assert.Equal(t, "testgame", mod.GameID)

	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)

	result, err := svc.DownloadMod(ctx, "e2e-api", game, &mod, &files[0], nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))

	installed := []domain.InstalledMod{{Mod: domain.Mod{ID: "77", SourceID: "e2e-api", Version: "1.0.0", GameID: "testgame"}}}
	updates, err := src.CheckUpdates(ctx, installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

// TestAPISourceInstallByIDOnly pins #49 acceptance criterion 1: a definition
// with no search endpoint works for install-by-ID, and search reports
// unsupported cleanly.
func TestAPISourceInstallByIDOnly(t *testing.T) {
	srv := newFakeRESTServer(t, false)

	src, err := custom.New(apiSourceDef(srv.URL, false))
	require.NoError(t, err)

	ctx := context.Background()
	_, err = src.Search(ctx, source.SearchQuery{Query: "cool"})
	assert.True(t, errors.Is(err, source.ErrNotSupported), "search must be a clean capability gap")
	assert.False(t, source.CapabilitiesOf(src).Search)

	mod, err := src.GetMod(ctx, "testgame", "77")
	require.NoError(t, err)
	files, err := src.GetModFiles(ctx, mod)
	require.NoError(t, err)
	u, err := src.GetDownloadURL(ctx, mod, files[0].ID)
	require.NoError(t, err)
	assert.Contains(t, u, "/dl/cool-1.2.0.zip")
}

// TestAPISourceKeyNeverInErrors pins #49 acceptance criterion 2's "never
// logged" half for the error path.
func TestAPISourceKeyNeverInErrors(t *testing.T) {
	def := apiSourceDef("http://127.0.0.1:1", true)
	def.API.Auth = &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "query", Name: "api_key"}}
	src, err := custom.New(def)
	require.NoError(t, err)
	src.(interface{ SetAPIKey(string) }).SetAPIKey("SUPERSECRET")

	_, err = src.Search(context.Background(), source.SearchQuery{Query: "x"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SUPERSECRET")
}
```

- [ ] **Step 2: Run the tests and the full suite**

Run: `go test ./internal/core/ -run TestAPISource -v && go test ./...`
Expected: PASS everywhere.

- [ ] **Step 3: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/core/service_api_source_test.go
git commit -m "test(core): api source end-to-end coverage"
```

---

## Task 10: Documentation and version bump

**Files:**
- Modify: `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go` (version)

ACCURACY RULE (two docs tasks in this epic were rejected for unbacked claims): verify every sentence against the code on this branch before writing it.

- [ ] **Step 1: Update docs**

`README.md` — Custom Sources section:
- New "API Sources" subsection: the definition YAML (design §4's example, adjusted to the shipped schema), a placeholders table (`{game_id}`, `{query}`, `{page}` with the page_start rule, `{page_size}`, `{offset}` with its independence from page_start, `{mod_id}`, `{file_id}` — all URL-escaped), mapping-keys tables for `mod` and `file` (required keys marked: mod id+name, file id), the capability-gap rule (undefined endpoint = unsupported operation; `GetDependencies` always unsupported), GET+JSON-only and https/`allow_http` guardrails, and the credential-scoping note (same-origin scheme+host vs `base_url` for both auth modes; header keys also stripped on off-origin download redirects — this reuses the v1.8.0 machinery).
- Update the Common Fields `type` row: all three types supported.
- `source validate` docs: add `--probe` (and `--id` for search-less api definitions) with a real captured output example (build the binary; probe a temp directory definition).

`CHANGELOG.md` — under `[Unreleased]` → `### Added`:

```markdown
- API source type: describe a GET+JSON REST API declaratively (endpoint templates + dot-path mappings) and use it as a mod source — search, install (including install-by-ID-only definitions), and update checks
- `lmm source validate --probe` — live smoke test for a definition (directory scan, manifest fetch, or API call; `--id` probes get_mod for search-less API definitions)
```

Then move `[Unreleased]` to `## [1.9.0] - <today>`, update comparison links per convention, and set `version = "1.9.0"` in cmd/lmm/root.go.

- [ ] **Step 2: Final verification and commits**

```bash
go fmt ./... && go vet ./... && go test ./... -race
git add README.md CHANGELOG.md
git commit -m "docs: document api sources and source validate --probe"
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 1.9.0"
```

---

## Out of Scope

- **Phase 5** (#50): aggregate search, TUI Sources screen
- POST/GraphQL/OAuth/scraping APIs; array indexing in dot-paths; per-endpoint rate limiting (design §4's v1 guardrails)
- #52 directory-source polish
