# Custom Sources — Phase 3 (Manifest Source) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `type: manifest` custom sources — a published JSON/YAML mod list (https URL or local path) becomes a full ModSource: search, install (with sha256 verification), within-source dependencies, and update checks.

**Architecture:** A `Manifest` type in `internal/source/custom` fetches the manifest document on demand (in-memory TTL cache for remote URLs; local paths read each time) and implements every ModSource operation client-side over the parsed document. Optional API-key auth (header or query) applies to both the manifest fetch and file downloads. Declared `sha256` values ride on a new `domain.DownloadableFile.SHA256` field and are verified in `Service.DownloadModToCache` before the archive is committed to cache.

**Tech Stack:** Go stdlib (net/http, crypto/sha256, net/url), gopkg.in/yaml.v3 (existing dep — YAML 1.2 is a JSON superset, so one parser covers both formats), testify, httptest.

**Spec:** `docs/plans/2026-07-13-custom-sources-design.md` §3 (manifest), §5 (search semantics), §6 (auth), §9 (security). Issue: #48 (epic #45).

## Global Constraints

- TDD: every task starts with a failing test (`~/.claude/DEV.md`, repo CLAUDE.md).
- Error wrapping with context: `fmt.Errorf("doing X: %w", err)` (GO.md).
- `ctx context.Context` first param for I/O paths; no ctx stored in structs (GO.md).
- No new dependencies.
- `go fmt ./...` and `go vet ./...` clean before every commit.
- Manifest fetch/parse errors must name the manifest URL and fail only the operation — never source registration (a valid definition always registers).
- HTTPS enforced for remote manifest URLs and manifest file URLs unless the definition sets `allow_http: true`; local paths exempt.
- API keys are never logged. Key resolution order: `LMM_<ID>_API_KEY` env var (ID uppercased, `-` → `_`), then DB token store.
- Bounded reads on untrusted remote data (10 MiB manifest cap — same defense class as the P2 zip-bomb cap).
- Commit after each task; conventional commit messages.

---

## Task 1: Auth config block in the definition schema

**Files:**
- Modify: `internal/source/custom/definition.go`
- Test: `internal/source/custom/definition_test.go` (append cases)

**Interfaces:**
- Produces: `custom.AuthConfig{APIKey *APIKeyConfig}`, `custom.APIKeyConfig{In, Name string}`, `ManifestConfig.Auth *AuthConfig`, `(d *SourceDefinition) validateAuth(a *AuthConfig) error`. Phase 4 (`api` type) will reuse `AuthConfig` unchanged.

- [ ] **Step 1: Write the failing test**

Append to the `tests` table in `TestSourceDefinitionValidate` (`internal/source/custom/definition_test.go`):

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestSourceDefinitionValidate -v`
Expected: FAIL — `undefined: AuthConfig`, `unknown field Auth`.

- [ ] **Step 3: Implement**

In `internal/source/custom/definition.go`, extend `ManifestConfig` and add the auth types + validation:

```go
// ManifestConfig configures a manifest source.
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
}

// APIKeyConfig says where the API key is attached on requests.
type APIKeyConfig struct {
	In   string `yaml:"in"`   // "header" or "query"
	Name string `yaml:"name"` // header name or query parameter name
}

// validateAuth checks an optional auth block. Shared by manifest (Phase 3)
// and api (Phase 4) validation.
func validateAuth(a *AuthConfig) error {
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
	return nil
}
```

In `Validate()`'s `case TypeManifest:` branch, after the existing Refresh check, add:

```go
		if err := validateAuth(d.Manifest.Auth); err != nil {
			return fmt.Errorf("manifest: %w", err)
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS (new cases + all existing).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/definition.go internal/source/custom/definition_test.go
git commit -m "feat(source): add auth config to custom source definitions"
```

---

## Task 2: Declared-checksum verification on download

**Files:**
- Modify: `internal/domain/mod.go` (`DownloadableFile`), `internal/core/service.go` (`DownloadModToCache` + new helper)
- Test: `internal/core/service_sha256_test.go` (create)

**Interfaces:**
- Produces: `domain.DownloadableFile.SHA256 string` (optional expected hash, hex; empty = no verification — all built-in sources leave it empty, zero behavior change for them); `core.verifyFileSHA256(path, expectedHex string) error` (unexported). `DownloadModToCache` fails with a "sha256 mismatch" error and does NOT commit to cache when the downloaded bytes don't match a declared hash.
- Consumed by: Task 6 (`GetModFiles` maps manifest `sha256` onto the field), Task 10 (e2e).

- [ ] **Step 1: Write the failing test**

Create `internal/core/service_sha256_test.go`. It is `package core_test` and reuses the existing mock-source pattern from `service_test.go` (`newMockSource`, `mockSourceWithDownloads` with `AddDownload`/`Close`, `core.NewService`, `svc.DownloadMod`) — read those helpers first and match their construction exactly:

```go
package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// downloadWithSHA256 runs one DownloadMod through a mock source serving
// content, with expectedSHA declared on the file. Returns the error.
func downloadWithSHA256(t *testing.T, content []byte, expectedSHA string) (error, *domain.Game, *domain.Mod, func() bool) {
	t.Helper()

	cfg := coreServiceConfig(t) // see note below
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: filepath.Join(t.TempDir(), "mods"), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "m1", SourceID: "test", Name: "Mod", Version: "1.0.0", GameID: "testgame"}
	file := &domain.DownloadableFile{ID: "file1", Name: "File", FileName: "m1.zip", SHA256: expectedSHA}

	mock.AddDownload(file.ID, content)

	_, err = svc.DownloadMod(context.Background(), "test", game, mod, file, nil)
	gameCache := svc.GetGameCache(game)
	cached := func() bool { return gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) }
	return err, game, mod, cached
}

func TestDownloadModVerifiesDeclaredSHA256(t *testing.T) {
	content := []byte("mod archive bytes")
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	t.Run("matching hash passes", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, good)
		require.NoError(t, err)
		assert.True(t, cached())
	})

	t.Run("uppercase hash passes (case-insensitive)", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, strings.ToUpper(good))
		require.NoError(t, err)
		assert.True(t, cached())
	})

	t.Run("mismatched hash fails and nothing is cached", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, strings.Repeat("ab", 32))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sha256 mismatch")
		assert.False(t, cached())
	})

	t.Run("empty hash skips verification", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, "")
		require.NoError(t, err)
		assert.True(t, cached())
	})
}
```

Adaptation notes for the implementer (the assertions stay the same):
- If there is no `coreServiceConfig(t)` helper, inline `core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}` (the pattern used by `TestService_DownloadMod_RejectsFileURLFromNonDirectorySource`).
- Match the real constructor/name of the downloads mock (`mockSourceWithDownloads` and its `AddDownload`); if its constructor differs, adapt the call, not the behavior.
- Add `"strings"` to imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestDownloadModVerifiesDeclaredSHA256 -v`
Expected: FAIL — `unknown field SHA256 in struct literal`.

- [ ] **Step 3: Implement**

`internal/domain/mod.go` — add to `DownloadableFile`:

```go
	SHA256 string // Expected SHA-256 of the download (hex); empty = source declares no checksum
```

`internal/core/service.go` — in `DownloadModToCache`, immediately after the `s.downloader.Download(...)` call and its error check, add:

```go
	if file.SHA256 != "" {
		if err := verifyFileSHA256(archivePath, file.SHA256); err != nil {
			return nil, fmt.Errorf("verifying download of %s: %w", file.FileName, err)
		}
	}
```

Add the helper after `DownloadModToCache` (imports: `crypto/sha256`, `encoding/hex`; `io`, `os`, `strings` are already imported):

```go
// verifyFileSHA256 streams path and compares its SHA-256 against expectedHex
// (case-insensitive). Sources that publish expected checksums (manifest
// sha256) set DownloadableFile.SHA256; built-in sources leave it empty and
// skip this entirely.
func verifyFileSHA256(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening downloaded file: %w", err)
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing downloaded file: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expectedHex) {
		return fmt.Errorf("sha256 mismatch: source declares %s, downloaded file is %s", expectedHex, got)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS (new tests + no regressions — built-ins have empty SHA256).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/domain/mod.go internal/core/service.go internal/core/service_sha256_test.go
git commit -m "feat(core): verify source-declared sha256 checksums on download"
```

---

## Task 3: Manifest document parsing

**Files:**
- Create: `internal/source/custom/manifestdoc.go`
- Test: `internal/source/custom/manifestdoc_test.go`

**Interfaces:**
- Produces (all unexported, used by Tasks 4–7):
  - `manifestDoc{Version int; Mods []manifestMod}`
  - `manifestMod{ID, Name, Version, Author, Summary string; GameIDs []string; URL string; UpdatedAt string; Dependencies []string; Files []manifestFile}`
  - `manifestFile{ID, Name, Filename, Version string; Size int64; URL, SHA256 string; Primary bool}`
  - `parseManifest(data []byte, allowHTTP bool) (*manifestDoc, error)` — one parser for YAML and JSON (yaml.v3 handles both).

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/manifestdoc_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestParseManifest -v`
Expected: FAIL — `undefined: parseManifest`.

- [ ] **Step 3: Implement**

Create `internal/source/custom/manifestdoc.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -run TestParseManifest -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/manifestdoc.go internal/source/custom/manifestdoc_test.go
git commit -m "feat(source): parse lmm manifest documents"
```

---

## Task 4: Manifest source construction and fetching (TTL cache + auth)

**Files:**
- Create: `internal/source/custom/manifest.go`
- Test: `internal/source/custom/manifest_test.go`

**Interfaces:**
- Consumes: `parseManifest` (Task 3), `AuthConfig` (Task 1).
- Produces: `custom.NewManifest(def SourceDefinition) (*Manifest, error)` — pure construction, never touches network/filesystem (a valid definition must always register; fetch errors are operation-time). `(m *Manifest) SetAPIKey(key string)`, `(m *Manifest) IsAuthenticated() bool`, and the unexported `(m *Manifest) fetch(ctx) (*manifestDoc, error)` used by Tasks 6–7. Test hooks: unexported `httpClient *http.Client` and `now func() time.Time` fields.

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/manifest_test.go`:

```go
package custom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testManifest = `
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    author: someone
    summary: Makes things cooler
    game_ids: [skyrim]
    dependencies: [other-mod]
    files:
      - id: main
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 4
        url: https://files.test/cool-mod-1.2.0.zip
        sha256: aabbcc
        primary: true
  - id: other-mod
    name: Other Mod
    version: 0.9.0
    summary: A dependency
    files:
      - id: main
        filename: other-mod.zip
        url: https://files.test/other-mod.zip
`

func manifestDef(url string) SourceDefinition {
	return SourceDefinition{
		ID:       "my-repo",
		Name:     "My Repo",
		Type:     TypeManifest,
		Manifest: &ManifestConfig{URL: url},
	}
}

// newLocalManifest writes testManifest to a temp file and builds a source over it.
func newLocalManifest(t *testing.T) *Manifest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(testManifest), 0644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)
	return m
}

func TestNewManifestIsPure(t *testing.T) {
	// Construction must succeed even when the manifest is unreachable —
	// fetch errors are operation-time, not registration-time.
	m, err := NewManifest(manifestDef("https://unreachable.invalid/mods.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "my-repo", m.ID())
	assert.Equal(t, "My Repo", m.Name())
}

func TestManifestFetchLocal(t *testing.T) {
	m := newLocalManifest(t)
	doc, err := m.fetch(context.Background())
	require.NoError(t, err)
	assert.Len(t, doc.Mods, 2)
}

func TestManifestFetchLocalMissingFileNamesPath(t *testing.T) {
	def := manifestDef(filepath.Join(t.TempDir(), "gone.yaml"))
	m, err := NewManifest(def)
	require.NoError(t, err)
	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gone.yaml")
	assert.Contains(t, err.Error(), "my-repo")
}

func TestManifestFetchRemoteTTL(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true // httptest is plain http
	def.Manifest.Refresh = "15m"
	m, err := NewManifest(def)
	require.NoError(t, err)

	current := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return current }

	ctx := context.Background()
	_, err = m.fetch(ctx)
	require.NoError(t, err)
	_, err = m.fetch(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, hits, "second fetch within TTL must hit the cache")

	current = current.Add(16 * time.Minute)
	_, err = m.fetch(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, hits, "fetch after TTL expiry must re-download")
}

func TestManifestFetchAttachesAuth(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		var gotHeader string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("X-API-Key")
			_, _ = w.Write([]byte(testManifest))
		}))
		defer srv.Close()

		def := manifestDef(srv.URL + "/mods.yaml")
		def.AllowHTTP = true
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")

		_, err = m.fetch(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotHeader)
	})

	t.Run("query", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query().Get("api_key")
			_, _ = w.Write([]byte(testManifest))
		}))
		defer srv.Close()

		def := manifestDef(srv.URL + "/mods.yaml")
		def.AllowHTTP = true
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")

		_, err = m.fetch(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotQuery)
	})
}

func TestManifestFetchRemoteErrorNamesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)

	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), srv.URL)
	assert.Contains(t, err.Error(), "my-repo")
}

func TestManifestIsAuthenticated(t *testing.T) {
	def := manifestDef("https://x.test/mods.yaml")
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
	m, err := NewManifest(def)
	require.NoError(t, err)
	assert.False(t, m.IsAuthenticated())
	m.SetAPIKey("k")
	assert.True(t, m.IsAuthenticated())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestManifest -v`
Expected: FAIL — `undefined: NewManifest`.

- [ ] **Step 3: Implement**

Create `internal/source/custom/manifest.go`:

```go
package custom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultManifestRefresh is the remote-manifest cache TTL when the definition
// does not set manifest.refresh.
const defaultManifestRefresh = 15 * time.Minute

// maxManifestSize bounds how much of a remote manifest we read into memory.
// Real manifests are KBs; 10 MiB is generous and prevents a hostile or broken
// server from exhausting memory.
const maxManifestSize = 10 << 20 // 10 MiB

// Manifest is a ModSource backed by a published mod-list document (design §3).
// Remote manifests are fetched on demand and cached in memory for the
// configured TTL; local paths are read on every operation (cheap).
// Construction is pure: a valid definition always registers, and fetch/parse
// problems surface as operation errors naming the manifest URL.
type Manifest struct {
	id        string
	name      string
	url       string // https URL, or absolute local path (~ expanded)
	isRemote  bool
	refresh   time.Duration
	allowHTTP bool
	auth      *AuthConfig

	apiKey     string
	httpClient *http.Client
	now        func() time.Time // injectable for TTL tests

	mu        sync.Mutex
	cached    *manifestDoc
	fetchedAt time.Time
}

// NewManifest constructs a manifest source from a validated definition. It
// performs no I/O — the manifest is first fetched when an operation needs it.
func NewManifest(def SourceDefinition) (*Manifest, error) {
	cfg := def.Manifest

	refresh := defaultManifestRefresh
	if cfg.Refresh != "" {
		d, err := time.ParseDuration(cfg.Refresh)
		if err != nil {
			return nil, fmt.Errorf("manifest.refresh: %w", err) // unreachable after Validate, kept for safety
		}
		refresh = d
	}

	u := cfg.URL
	isRemote := strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
	if !isRemote {
		if strings.HasPrefix(u, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("expanding %q: %w", u, err)
			}
			u = filepath.Join(home, u[2:])
		}
		abs, err := filepath.Abs(u)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", u, err)
		}
		u = abs
	}

	return &Manifest{
		id:         def.ID,
		name:       def.Name,
		url:        u,
		isRemote:   isRemote,
		refresh:    refresh,
		allowHTTP:  def.AllowHTTP,
		auth:       cfg.Auth,
		httpClient: http.DefaultClient,
		now:        time.Now,
	}, nil
}

// SetAPIKey provides the API key resolved at startup (env var or token store).
func (m *Manifest) SetAPIKey(key string) { m.apiKey = key }

// IsAuthenticated reports whether an API key is configured. Only meaningful
// when the definition declares auth (Capabilities().Auth).
func (m *Manifest) IsAuthenticated() bool { return m.apiKey != "" }

// fetch returns the parsed manifest, honoring the TTL cache for remote URLs.
// Errors name the source and manifest URL so users can act on them.
func (m *Manifest) fetch(ctx context.Context) (*manifestDoc, error) {
	if !m.isRemote {
		data, err := os.ReadFile(m.url)
		if err != nil {
			return nil, fmt.Errorf("source %q: reading manifest %s: %w", m.id, m.url, err)
		}
		doc, err := parseManifest(data, m.allowHTTP)
		if err != nil {
			return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
		}
		return doc, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cached != nil && m.now().Sub(m.fetchedAt) < m.refresh {
		return m.cached, nil
	}

	doc, err := m.fetchRemote(ctx)
	if err != nil {
		return nil, err
	}
	m.cached = doc
	m.fetchedAt = m.now()
	return doc, nil
}

// fetchRemote downloads and parses the manifest document. Callers hold m.mu.
func (m *Manifest) fetchRemote(ctx context.Context) (*manifestDoc, error) {
	reqURL := m.url
	if m.auth != nil && m.auth.APIKey.In == "query" && m.apiKey != "" {
		u, err := addQueryParam(reqURL, m.auth.APIKey.Name, m.apiKey)
		if err != nil {
			return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
		}
		reqURL = u
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
	}
	if m.auth != nil && m.auth.APIKey.In == "header" && m.apiKey != "" {
		req.Header.Set(m.auth.APIKey.Name, m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("source %q: fetching manifest %s: %w", m.id, m.url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source %q: fetching manifest %s: HTTP %d", m.id, m.url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("source %q: reading manifest %s: %w", m.id, m.url, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("source %q: manifest %s exceeds %d bytes", m.id, m.url, maxManifestSize)
	}

	doc, err := parseManifest(data, m.allowHTTP)
	if err != nil {
		return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
	}
	return doc, nil
}
```

Add the `addQueryParam` helper to the same file (also used by Task 6's `GetDownloadURL`):

```go
// addQueryParam returns rawURL with name=value appended to its query string,
// preserving existing parameters. Values are URL-escaped.
func addQueryParam(rawURL, name, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	q := u.Query()
	q.Set(name, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
```

(Add `"net/url"` to imports.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -run TestManifest -v` then `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/manifest.go internal/source/custom/manifest_test.go
git commit -m "feat(source): fetch manifest documents with TTL cache and API-key auth"
```

---

## Task 5: Shared client-side search helper

**Files:**
- Create: `internal/source/custom/search.go`
- Modify: `internal/source/custom/directory.go` (`Search` body only)

**Interfaces:**
- Produces: `searchMods(mods []domain.Mod, query source.SearchQuery) source.SearchResult` (unexported) — the exact semantics currently inside `Directory.Search` (design §5): case-insensitive substring on Name/ID/Summary, empty query matches all, name-matches rank before summary-only matches, then alphabetical by Name; PageSize default 20; negative page clamped; `GameID` stamped onto every returned mod.
- Consumed by: `Directory.Search` (refactor, this task) and `Manifest.Search` (Task 6).

This is a behavior-preserving refactor: the existing `TestDirectorySearch` suite is the safety net — no new tests, no test edits.

- [ ] **Step 1: Extract the helper**

Create `internal/source/custom/search.go` by lifting the body of `Directory.Search` after the scan (everything from `q := strings.ToLower(query.Query)` down) **verbatim**:

```go
package custom

import (
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// searchMods implements the client-side search semantics shared by directory
// and manifest sources (design §5): case-insensitive substring match on
// name/ID/summary, name matches ranked before summary-only matches, then
// alphabetical; local pagination with default page size 20. GameID is stamped
// onto every returned mod so downstream installs are attributed correctly.
func searchMods(mods []domain.Mod, query source.SearchQuery) source.SearchResult {
	q := strings.ToLower(query.Query)
	type ranked struct {
		mod       domain.Mod
		nameMatch bool
	}
	var matches []ranked
	for _, m := range mods {
		nameMatch := q == "" || strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.ID), q)
		summaryMatch := strings.Contains(strings.ToLower(m.Summary), q)
		if !nameMatch && !summaryMatch {
			continue
		}
		matches = append(matches, ranked{mod: m, nameMatch: nameMatch})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].nameMatch != matches[j].nameMatch {
			return matches[i].nameMatch
		}
		return matches[i].mod.Name < matches[j].mod.Name
	})

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	start := query.Page * pageSize
	if start < 0 {
		start = 0
	}
	end := min(start+pageSize, len(matches))
	if start > len(matches) {
		start = len(matches)
	}

	out := make([]domain.Mod, 0, end-start)
	for _, m := range matches[start:end] {
		mod := m.mod
		mod.GameID = query.GameID
		out = append(out, mod)
	}

	return source.SearchResult{
		Mods:       out,
		TotalCount: len(matches),
		Page:       query.Page,
		PageSize:   pageSize,
	}
}
```

Replace `Directory.Search`'s body with:

```go
func (d *Directory) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	scanned, err := d.scan()
	if err != nil {
		return source.SearchResult{}, err
	}
	mods := make([]domain.Mod, 0, len(scanned))
	for _, dm := range scanned {
		mods = append(mods, dm.mod)
	}
	return searchMods(mods, query), nil
}
```

(Keep `Directory.Search`'s doc comment; remove the now-unused `"sort"` import from directory.go if nothing else uses it.)

- [ ] **Step 2: Verify no behavior change**

Run: `go test ./internal/source/custom/ -v && go build ./...`
Expected: PASS — every existing `TestDirectorySearch` case green, untouched.

- [ ] **Step 3: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/search.go internal/source/custom/directory.go
git commit -m "refactor(source): share client-side search across custom source types"
```

---

## Task 6: Manifest read operations

**Files:**
- Modify: `internal/source/custom/manifest.go`
- Test: `internal/source/custom/manifest_test.go` (append)

**Interfaces:**
- Consumes: `m.fetch` (Task 4), `searchMods` (Task 5), `domain.DownloadableFile.SHA256` (Task 2), `source.ErrNotSupported`/`Capabilities` (Phase 1).
- Produces: `*Manifest` implements `source.ModSource` (Search, GetMod, GetModFiles, GetDownloadURL; GetDependencies/CheckUpdates land in Task 7 — stub them here returning `ErrNotSupported` so the interface compiles, Task 7 replaces the stubs) + `source.CapabilityReporter`. Semantics later tasks rely on: `GetModFiles` maps manifest `sha256` → `DownloadableFile.SHA256`; `GetDownloadURL` appends the query-auth parameter when configured; game filtering matches `game_ids` against `query.GameID` (empty `game_ids` or empty `query.GameID` ⇒ match).

- [ ] **Step 1: Write the failing test**

Append to `internal/source/custom/manifest_test.go`:

```go
func TestManifestIdentityAndCapabilities(t *testing.T) {
	m := newLocalManifest(t)
	assert.Equal(t, source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: false}, m.Capabilities())
	assert.Empty(t, m.AuthURL())

	_, err := m.ExchangeToken(context.Background(), "code")
	assert.True(t, errors.Is(err, source.ErrNotSupported))

	def := manifestDef("https://x.test/mods.yaml")
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
	authed, err := NewManifest(def)
	require.NoError(t, err)
	assert.True(t, authed.Capabilities().Auth)
}

func TestManifestSearch(t *testing.T) {
	m := newLocalManifest(t)
	ctx := context.Background()

	t.Run("empty query returns all mods for a matching game", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{GameID: "skyrim"})
		require.NoError(t, err)
		assert.Equal(t, 2, res.TotalCount) // cool-mod matches game_ids; other-mod has no game_ids (all games)
	})

	t.Run("game_ids filters non-matching games", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{GameID: "othergame"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1) // only other-mod (no game_ids = all games)
		assert.Equal(t, "other-mod", res.Mods[0].ID)
	})

	t.Run("empty GameID matches everything", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{})
		require.NoError(t, err)
		assert.Equal(t, 2, res.TotalCount)
	})

	t.Run("query matches and mod fields map", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{Query: "cool", GameID: "skyrim"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		mod := res.Mods[0]
		assert.Equal(t, "cool-mod", mod.ID)
		assert.Equal(t, "my-repo", mod.SourceID)
		assert.Equal(t, "Cool Mod", mod.Name)
		assert.Equal(t, "1.2.0", mod.Version)
		assert.Equal(t, "someone", mod.Author)
		assert.Equal(t, "Makes things cooler", mod.Summary)
		assert.Equal(t, "skyrim", mod.GameID)
	})
}

func TestManifestGetMod(t *testing.T) {
	m := newLocalManifest(t)
	mod, err := m.GetMod(context.Background(), "skyrim", "cool-mod")
	require.NoError(t, err)
	assert.Equal(t, "Cool Mod", mod.Name)
	assert.Equal(t, "skyrim", mod.GameID)

	_, err = m.GetMod(context.Background(), "skyrim", "nope")
	assert.ErrorContains(t, err, "not found")
}

func TestManifestFilesAndDownloadURL(t *testing.T) {
	m := newLocalManifest(t)
	ctx := context.Background()

	mod, err := m.GetMod(ctx, "skyrim", "cool-mod")
	require.NoError(t, err)

	files, err := m.GetModFiles(ctx, mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	f := files[0]
	assert.Equal(t, "main", f.ID)
	assert.Equal(t, "cool-mod-1.2.0.zip", f.FileName)
	assert.Equal(t, "1.2.0", f.Version)
	assert.Equal(t, int64(4), f.Size)
	assert.Equal(t, "aabbcc", f.SHA256)
	assert.True(t, f.IsPrimary)

	u, err := m.GetDownloadURL(ctx, mod, "main")
	require.NoError(t, err)
	assert.Equal(t, "https://files.test/cool-mod-1.2.0.zip", u)

	_, err = m.GetDownloadURL(ctx, mod, "nope")
	assert.ErrorContains(t, err, "not found")
}

func TestManifestDownloadURLQueryAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(testManifest), 0644))
	def := manifestDef(path)
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
	m, err := NewManifest(def)
	require.NoError(t, err)
	m.SetAPIKey("sekrit")

	mod, err := m.GetMod(context.Background(), "skyrim", "cool-mod")
	require.NoError(t, err)
	u, err := m.GetDownloadURL(context.Background(), mod, "main")
	require.NoError(t, err)
	assert.Equal(t, "https://files.test/cool-mod-1.2.0.zip?api_key=sekrit", u)
}
```

Add `"errors"` and `"github.com/DonovanMods/linux-mod-manager/internal/source"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run 'TestManifestIdentity|TestManifestSearch|TestManifestGetMod|TestManifestFiles|TestManifestDownloadURL' -v`
Expected: FAIL — `m.Capabilities undefined`, etc.

- [ ] **Step 3: Implement**

Append to `internal/source/custom/manifest.go` (add `"time"` usage for UpdatedAt parsing; `domain` and `source` imports):

```go
// ID implements source.ModSource.
func (m *Manifest) ID() string { return m.id }

// Name implements source.ModSource.
func (m *Manifest) Name() string { return m.name }

// AuthURL implements source.ModSource; manifest sources use API keys, not OAuth.
func (m *Manifest) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (m *Manifest) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", m.id, source.ErrNotSupported)
}

// Capabilities implements source.CapabilityReporter. Auth reflects whether the
// definition declares an auth block.
func (m *Manifest) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: m.auth != nil}
}

// toMod converts a manifest entry to a domain.Mod. GameID is stamped by
// searchMods / the callers, not here.
func (m *Manifest) toMod(mm manifestMod) domain.Mod {
	mod := domain.Mod{
		ID:        mm.ID,
		SourceID:  m.id,
		Name:      mm.Name,
		Version:   mm.Version,
		Author:    mm.Author,
		Summary:   mm.Summary,
		Description: mm.Summary,
		SourceURL: mm.URL,
	}
	if mm.UpdatedAt != "" {
		if ts, err := time.Parse(time.RFC3339, mm.UpdatedAt); err == nil {
			mod.UpdatedAt = ts // unparseable -> zero value, by design
		}
	}
	for _, dep := range mm.Dependencies {
		mod.Dependencies = append(mod.Dependencies, domain.ModReference{SourceID: m.id, ModID: dep})
	}
	return mod
}

// gameMatches reports whether a manifest entry applies to gameID: an empty
// game_ids list matches every game, and an empty gameID matches every entry.
func gameMatches(mm manifestMod, gameID string) bool {
	if len(mm.GameIDs) == 0 || gameID == "" {
		return true
	}
	for _, g := range mm.GameIDs {
		if g == gameID {
			return true
		}
	}
	return false
}

// Search implements source.ModSource with the shared client-side semantics
// (design §5), filtered by the manifest's per-mod game_ids.
func (m *Manifest) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	doc, err := m.fetch(ctx)
	if err != nil {
		return source.SearchResult{}, err
	}
	mods := make([]domain.Mod, 0, len(doc.Mods))
	for _, mm := range doc.Mods {
		if !gameMatches(mm, query.GameID) {
			continue
		}
		mods = append(mods, m.toMod(mm))
	}
	return searchMods(mods, query), nil
}

// GetMod implements source.ModSource. gameID does not filter (install-by-ID
// works from any game); it is echoed onto the returned mod for attribution.
func (m *Manifest) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	mm, err := m.findMod(ctx, modID)
	if err != nil {
		return nil, err
	}
	mod := m.toMod(*mm)
	mod.GameID = gameID
	return &mod, nil
}

// GetModFiles implements source.ModSource, mapping manifest file entries —
// including declared sha256 checksums — onto DownloadableFiles.
func (m *Manifest) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	mm, err := m.findMod(ctx, mod.ID)
	if err != nil {
		return nil, err
	}
	files := make([]domain.DownloadableFile, 0, len(mm.Files))
	for _, f := range mm.Files {
		files = append(files, domain.DownloadableFile{
			ID:        f.ID,
			Name:      f.Name,
			FileName:  f.Filename,
			Version:   f.Version,
			Size:      f.Size,
			IsPrimary: f.Primary,
			SHA256:    f.SHA256,
		})
	}
	return files, nil
}

// GetDownloadURL implements source.ModSource. Query-mode auth is appended
// here; header-mode auth rides via DownloadHeaders (see DownloadHeaderProvider).
func (m *Manifest) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	mm, err := m.findMod(ctx, mod.ID)
	if err != nil {
		return "", err
	}
	for _, f := range mm.Files {
		if f.ID != fileID {
			continue
		}
		u := f.URL
		if m.auth != nil && m.auth.APIKey.In == "query" && m.apiKey != "" {
			withKey, err := addQueryParam(u, m.auth.APIKey.Name, m.apiKey)
			if err != nil {
				return "", fmt.Errorf("source %q: file %q: %w", m.id, fileID, err)
			}
			u = withKey
		}
		return u, nil
	}
	return "", fmt.Errorf("source %q: mod %q: file not found: %s", m.id, mod.ID, fileID)
}

// findMod fetches the manifest and returns the entry with the given ID.
func (m *Manifest) findMod(ctx context.Context, modID string) (*manifestMod, error) {
	doc, err := m.fetch(ctx)
	if err != nil {
		return nil, err
	}
	for i := range doc.Mods {
		if doc.Mods[i].ID == modID {
			return &doc.Mods[i], nil
		}
	}
	return nil, fmt.Errorf("source %q: mod not found: %s", m.id, modID)
}
```

Also add temporary stubs so `*Manifest` satisfies `source.ModSource` until Task 7 replaces them:

```go
// GetDependencies is implemented in the dependencies task (replaces this stub).
func (m *Manifest) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", m.id, source.ErrNotSupported)
}

// CheckUpdates is implemented in the update-check task (replaces this stub).
func (m *Manifest) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, fmt.Errorf("source %q: update checks: %w", m.id, source.ErrNotSupported)
}
```

And a compile-time interface assertion at the bottom of manifest.go:

```go
var _ source.ModSource = (*Manifest)(nil)
var _ source.CapabilityReporter = (*Manifest)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/manifest.go internal/source/custom/manifest_test.go
git commit -m "feat(source): manifest source read operations"
```

---

## Task 7: Manifest dependencies and update checks

**Files:**
- Modify: `internal/source/custom/manifest.go` (replace the two stubs)
- Test: `internal/source/custom/manifest_test.go` (append)

**Interfaces:**
- Produces: real `GetDependencies` (within-source `ModReference`s) and `CheckUpdates` (via `domain.IsNewerVersion`, removed mods skipped, ctx cancellation respected — same semantics as `Directory.CheckUpdates`).

- [ ] **Step 1: Write the failing test**

Append to `internal/source/custom/manifest_test.go` (add `domain` import):

```go
func TestManifestGetDependencies(t *testing.T) {
	m := newLocalManifest(t)

	mod, err := m.GetMod(context.Background(), "skyrim", "cool-mod")
	require.NoError(t, err)
	deps, err := m.GetDependencies(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, domain.ModReference{SourceID: "my-repo", ModID: "other-mod"}, deps[0])

	other, err := m.GetMod(context.Background(), "skyrim", "other-mod")
	require.NoError(t, err)
	deps, err = m.GetDependencies(context.Background(), other)
	require.NoError(t, err)
	assert.Empty(t, deps)
}

func TestManifestCheckUpdates(t *testing.T) {
	m := newLocalManifest(t) // cool-mod is at 1.2.0

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "cool-mod", SourceID: "my-repo", Version: "1.0.0"}},
		{Mod: domain.Mod{ID: "other-mod", SourceID: "my-repo", Version: "0.9.0"}},
		{Mod: domain.Mod{ID: "removed", SourceID: "my-repo", Version: "1.0"}},
	}

	updates, err := m.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "cool-mod", updates[0].InstalledMod.ID)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run 'TestManifestGetDependencies|TestManifestCheckUpdates' -v`
Expected: FAIL — both currently return `ErrNotSupported`.

- [ ] **Step 3: Implement (replace the stubs)**

```go
// GetDependencies implements source.ModSource: manifest dependencies are IDs
// within this source, returned as ModReferences for the resolver.
func (m *Manifest) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	mm, err := m.findMod(ctx, mod.ID)
	if err != nil {
		return nil, err
	}
	refs := make([]domain.ModReference, 0, len(mm.Dependencies))
	for _, dep := range mm.Dependencies {
		refs = append(refs, domain.ModReference{SourceID: m.id, ModID: dep})
	}
	return refs, nil
}

// CheckUpdates implements source.ModSource by comparing installed versions to
// the current manifest.
func (m *Manifest) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	doc, err := m.fetch(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]manifestMod, len(doc.Mods))
	for _, mm := range doc.Mods {
		byID[mm.ID] = mm
	}

	var updates []domain.Update
	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}
		current, ok := byID[inst.ID]
		if !ok {
			continue // mod removed from the manifest; nothing to offer
		}
		if domain.IsNewerVersion(inst.Version, current.Version) {
			updates = append(updates, domain.Update{
				InstalledMod: inst,
				NewVersion:   current.Version,
			})
		}
	}
	return updates, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/manifest.go internal/source/custom/manifest_test.go
git commit -m "feat(source): manifest dependencies and update checks"
```

---

## Task 8: Header auth on file downloads

**Files:**
- Modify: `internal/source/source.go` (new optional interface), `internal/core/downloader.go` (headers support), `internal/core/service.go` (wiring), `internal/source/custom/manifest.go` (implement provider)
- Test: `internal/core/downloader_test.go` (append), `internal/source/custom/manifest_test.go` (append)

**Interfaces:**
- Produces:
  - `source.DownloadHeaderProvider interface { DownloadHeaders() map[string]string }` — consulted by `DownloadModToCache` before downloading; nil/absent map = no extra headers.
  - `(d *Downloader) DownloadWithHeaders(ctx context.Context, url, destPath string, headers map[string]string, progressFn ProgressFunc) (*DownloadResult, error)`; existing `Download` delegates with nil headers (no caller changes).
  - `(m *Manifest) DownloadHeaders() map[string]string` — non-nil only for header-mode auth with a configured key.

- [ ] **Step 1: Write the failing tests**

Append to `internal/core/downloader_test.go` (match the file's existing package and helper style — read it first):

```go
func TestDownloadWithHeadersSetsHeaders(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-API-Key")
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	d := NewDownloader(nil)
	dest := filepath.Join(t.TempDir(), "out.bin")
	_, err := d.DownloadWithHeaders(context.Background(), srv.URL, dest, map[string]string{"X-API-Key": "sekrit"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "sekrit", got)
}
```

Append to `internal/source/custom/manifest_test.go`:

```go
func TestManifestDownloadHeaders(t *testing.T) {
	t.Run("header auth with key", func(t *testing.T) {
		def := manifestDef("https://x.test/mods.yaml")
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Equal(t, map[string]string{"X-API-Key": "sekrit"}, m.DownloadHeaders())
	})

	t.Run("query auth or no key yields nil", func(t *testing.T) {
		def := manifestDef("https://x.test/mods.yaml")
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Nil(t, m.DownloadHeaders())

		noKey, err := NewManifest(manifestDef("https://x.test/mods.yaml"))
		require.NoError(t, err)
		assert.Nil(t, noKey.DownloadHeaders())
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run TestDownloadWithHeaders -v; go test ./internal/source/custom/ -run TestManifestDownloadHeaders -v`
Expected: FAIL — `undefined: DownloadWithHeaders` / `m.DownloadHeaders undefined`.

- [ ] **Step 3: Implement**

`internal/source/source.go` — append:

```go
// DownloadHeaderProvider is implemented by sources whose file downloads need
// extra HTTP headers (e.g. header-mode API-key auth on a manifest source).
// Service.DownloadModToCache consults it before downloading. A nil map means
// no extra headers.
type DownloadHeaderProvider interface {
	DownloadHeaders() map[string]string
}
```

`internal/core/downloader.go`:
1. Rename the guts: `Download` becomes a delegator, `downloadOnce` gains a `headers map[string]string` param.

```go
// Download fetches a file from the URL and saves it to destPath, with retries
// on transient failures (exponential backoff). Progress updates are sent to
// the optional progressFn callback.
func (d *Downloader) Download(ctx context.Context, url, destPath string, progressFn ProgressFunc) (*DownloadResult, error) {
	return d.DownloadWithHeaders(ctx, url, destPath, nil, progressFn)
}

// DownloadWithHeaders is Download with extra request headers applied to every
// attempt — used for authenticated file downloads from custom sources.
func (d *Downloader) DownloadWithHeaders(ctx context.Context, url, destPath string, headers map[string]string, progressFn ProgressFunc) (*DownloadResult, error) {
```

(The retry loop body moves into `DownloadWithHeaders` unchanged, with the inner call becoming `d.downloadOnce(ctx, url, destPath, headers, progressFn)`.)

2. In `downloadOnce`, after `http.NewRequestWithContext` succeeds:

```go
	for name, value := range headers {
		req.Header.Set(name, value)
	}
```

`internal/core/service.go` — in `DownloadModToCache`, replace the `s.downloader.Download(...)` call with:

```go
	var headers map[string]string
	if hp, ok := src.(source.DownloadHeaderProvider); ok {
		headers = hp.DownloadHeaders()
	}
	downloadResult, err := s.downloader.DownloadWithHeaders(ctx, url, archivePath, headers, progressFn)
```

(`src` is already in scope from the file:// gating; check the `source` package import exists.)

`internal/source/custom/manifest.go` — append:

```go
// DownloadHeaders implements source.DownloadHeaderProvider: header-mode auth
// applies the same key to file downloads as to manifest fetches (design §6).
func (m *Manifest) DownloadHeaders() map[string]string {
	if m.auth == nil || m.auth.APIKey.In != "header" || m.apiKey == "" {
		return nil
	}
	return map[string]string{m.auth.APIKey.Name: m.apiKey}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ ./internal/source/custom/ -v && go build ./...`
Expected: PASS, no regressions (all existing Download callers unchanged).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/source.go internal/core/downloader.go internal/core/downloader_test.go internal/core/service.go internal/source/custom/manifest.go internal/source/custom/manifest_test.go
git commit -m "feat(core): support authenticated file downloads via source-provided headers"
```

---

## Task 9: Factory, startup key attachment, and auth commands for custom sources

**Files:**
- Modify: `internal/source/custom/custom.go` + `custom_test.go` (factory case), `cmd/lmm/root.go` (key attachment), `cmd/lmm/auth.go` (login/logout for custom sources), `cmd/lmm/source.go` (`isCustomSource` gains `*custom.Manifest`)
- Test: `cmd/lmm/auth_test.go` (append or create, following the light cmd-test pattern), `internal/source/custom/custom_test.go`

**Interfaces:**
- Consumes: `NewManifest` (Task 4), `getSourceAPIKey` (existing), `svc.ListSources`/`source.CapabilitiesOf` (existing).
- Produces: `custom.New` handles `TypeManifest`; `envKeyForSourceID(id string) string` in cmd/lmm (`"LMM_" + upper(id, -→_) + "_API_KEY"`); `lmm auth login/logout <custom-id>` accepted for registered custom sources whose `Capabilities().Auth` is true.

- [ ] **Step 1: Write the failing tests**

`internal/source/custom/custom_test.go` — update `TestNew`: move `TypeManifest` from the "unimplemented" loop into its own passing case:

```go
	t.Run("manifest type constructs a source", func(t *testing.T) {
		def := SourceDefinition{
			ID:       "my-repo",
			Name:     "My Repo",
			Type:     TypeManifest,
			Manifest: &ManifestConfig{URL: "https://x.test/mods.yaml"},
		}
		src, err := New(def)
		assert.NoError(t, err)
		assert.Equal(t, "my-repo", src.ID())
	})

	t.Run("unimplemented types return a clear error", func(t *testing.T) {
		def := SourceDefinition{ID: "x", Name: "X", Type: TypeAPI}
		_, err := New(def)
		assert.ErrorContains(t, err, "not yet supported")
	})
```

`cmd/lmm/auth_test.go` — append (create the file with the standard cmd package header if absent):

```go
func TestEnvKeyForSourceID(t *testing.T) {
	assert.Equal(t, "LMM_DONOVAN_MODS_API_KEY", envKeyForSourceID("donovan-mods"))
	assert.Equal(t, "LMM_MY_REPO_API_KEY", envKeyForSourceID("my-repo"))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/custom/ -run TestNew -v; go test ./cmd/lmm/ -run TestEnvKeyForSourceID -v`
Expected: FAIL — manifest case errors "not yet supported"; `undefined: envKeyForSourceID`.

- [ ] **Step 3: Implement**

`internal/source/custom/custom.go`:

```go
func New(def SourceDefinition) (source.ModSource, error) {
	switch def.Type {
	case TypeDirectory:
		return NewDirectory(def)
	case TypeManifest:
		return NewManifest(def)
	default:
		return nil, fmt.Errorf("source type %q is not yet supported", def.Type)
	}
}
```

`cmd/lmm/root.go` — in `registerCustomSources`, after `custom.New(def)` succeeds and before `svc.RegisterSource(src)`:

```go
		if a, ok := src.(interface{ SetAPIKey(string) }); ok {
			if key := getSourceAPIKey(svc, def.ID, envKeyForSourceID(def.ID)); key != "" {
				a.SetAPIKey(key)
			}
		}
```

`cmd/lmm/auth.go`:

1. Add the env-key helper (design §6 naming rule):

```go
// envKeyForSourceID derives the env var that can supply a custom source's API
// key: LMM_<ID>_API_KEY with the ID uppercased and dashes as underscores.
func envKeyForSourceID(sourceID string) string {
	return "LMM_" + strings.ReplaceAll(strings.ToUpper(sourceID), "-", "_") + "_API_KEY"
}
```

2. `getEnvKeyForSource`: change the `default:` branch to `return envKeyForSourceID(sourceID)`.
3. Custom-source acceptance: the login/logout paths currently gate on the hardcoded `supportedSources` list. Rework the gate to also accept any *registered* source whose capabilities report auth. `runAuthLogin`/`runAuthLogout` already run inside `withService`, so thread the service into the check:

```go
// isAuthCapableSource reports whether sourceID can hold an API key: either a
// built-in from supportedSources, or a registered custom source whose
// definition declares auth.
func isAuthCapableSource(service *core.Service, sourceID string) bool {
	if isSupportedSource(sourceID) {
		return true
	}
	src, err := service.GetSource(sourceID)
	if err != nil {
		return false
	}
	return source.CapabilitiesOf(src).Auth
}
```

Update `selectAuthSource` to take the service (`selectAuthSource(service, args)`) and use `isAuthCapableSource` for the args path; update both call sites (`runAuthLogin`, `runAuthLogout` — check how logout validates and apply the same gate). The interactive `promptForSource()` path keeps offering built-ins only (custom sources are named explicitly).
4. `validateAPIKey`: built-ins keep their live validation; for custom sources there is no generic validation endpoint — add a `default:` that prints nothing and returns nil (the key is exercised on first fetch). Check the switch already has a default; adjust it to return nil for registered custom sources.
5. `printAuthInstructions`: add a default case that prints the env var alternative:

```go
	default:
		fmt.Printf("Enter the API key for %s.\n", sourceID)
		fmt.Printf("(Alternatively, set the %s environment variable.)\n", envKeyForSourceID(sourceID))
```

`cmd/lmm/source.go` — extend `isCustomSource` (the type-switch/assertion added in the P2 fix wave) to also recognize `*custom.Manifest`.

- [ ] **Step 4: Verify build and full suite**

Run: `go build ./... && go test ./...`
Expected: PASS.

Manual check:

```bash
go build -o lmm ./cmd/lmm
mkdir -p /tmp/lmm-p3-smoke && cat > /tmp/lmm-p3-smoke/repo.yaml <<'EOF'
id: smoke-repo
name: Smoke Repo
type: manifest
manifest:
  url: /tmp/lmm-p3-smoke/mods.yaml
EOF
cat > /tmp/lmm-p3-smoke/mods.yaml <<'EOF'
version: 1
mods:
  - id: hello
    name: Hello Mod
    version: 1.0.0
    files:
      - id: main
        filename: hello.zip
        url: https://example.test/hello.zip
EOF
./lmm source validate /tmp/lmm-p3-smoke/repo.yaml   # expect: valid (manifest source "smoke-repo")
```

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/custom.go internal/source/custom/custom_test.go cmd/lmm/root.go cmd/lmm/auth.go cmd/lmm/auth_test.go cmd/lmm/source.go
git commit -m "feat(cli): register manifest sources and extend auth commands to custom sources"
```

---

## Task 10: End-to-end test — static file server as a full source

**Files:**
- Test: `internal/core/service_manifest_source_test.go` (create)

Acceptance criterion #48-1: "A static file server (or local file) publishing one manifest works as a full source, including dependency resolution and update checks."

- [ ] **Step 1: Write the test**

Create `internal/core/service_manifest_source_test.go` (`package core_test`; reuse `core.NewService` construction from `service_test.go`):

```go
package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifestSourceEndToEnd drives the full loop against a static file
// server: manifest fetch -> search -> files (sha256) -> download+verify ->
// cache, plus dependency resolution and update checks (issue #48 acceptance).
func TestManifestSourceEndToEnd(t *testing.T) {
	archive := []byte("mod payload bytes")
	sum := sha256.Sum256(archive)
	archiveSHA := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    summary: Makes things cooler
    dependencies: [dep-mod]
    files:
      - id: main
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        url: %s/files/cool-mod-1.2.0.zip
        sha256: %s
        primary: true
  - id: dep-mod
    name: Dep Mod
    version: 0.5.0
    files:
      - id: main
        filename: dep-mod.zip
        url: %s/files/dep-mod.zip
`, srv.URL, archiveSHA, srv.URL)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })

	src, err := custom.New(custom.SourceDefinition{
		ID:        "e2e-repo",
		Name:      "E2E Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true, // httptest serves plain http
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))
	ctx := context.Background()

	// Search finds the mod and stamps identity.
	res, err := src.Search(ctx, source.SearchQuery{Query: "cool", GameID: "testgame", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]
	assert.Equal(t, "e2e-repo", mod.SourceID)
	assert.Equal(t, "testgame", mod.GameID)

	// Dependencies resolve within the source.
	deps, err := src.GetDependencies(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, domain.ModReference{SourceID: "e2e-repo", ModID: "dep-mod"}, deps[0])

	// Files carry the declared sha256; download verifies it and lands in cache.
	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, archiveSHA, files[0].SHA256)

	result, err := svc.DownloadMod(ctx, "e2e-repo", game, &mod, &files[0], nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))

	// Update checks: installed 1.0.0 -> manifest 1.2.0 offers an update.
	installed := []domain.InstalledMod{{Mod: domain.Mod{ID: "cool-mod", SourceID: "e2e-repo", Version: "1.0.0"}}}
	updates, err := src.CheckUpdates(ctx, installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

// TestManifestSourceEndToEndCorruptDownload pins acceptance criterion 2 of
// the sha256 wiring: a server whose file bytes don't match the declared hash
// must fail the install and leave the cache empty.
func TestManifestSourceEndToEndCorruptDownload(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wrongSHA := strings.Repeat("de", 32) // 64 hex chars that won't match the served bytes
	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: bad-mod
    name: Bad Mod
    version: 1.0.0
    files:
      - id: main
        filename: bad-mod.zip
        url: %s/files/bad-mod.zip
        sha256: %s
`, srv.URL, wrongSHA)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tampered content")) })

	src, err := custom.New(custom.SourceDefinition{
		ID: "bad-repo", Name: "Bad Repo", Type: custom.TypeManifest, AllowHTTP: true,
		Manifest: &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))
	ctx := context.Background()

	mod, err := src.GetMod(ctx, "testgame", "bad-mod")
	require.NoError(t, err)
	files, err := src.GetModFiles(ctx, mod)
	require.NoError(t, err)

	_, err = svc.DownloadMod(ctx, "bad-repo", game, mod, &files[0], nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256 mismatch")
	gameCache := svc.GetGameCache(game)
	assert.False(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))
}
```

- [ ] **Step 2: Run the test and the full suite**

Run: `go test ./internal/core/ -run TestManifestSourceEndToEnd -v && go test ./...`
Expected: PASS everywhere.

- [ ] **Step 3: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/core/service_manifest_source_test.go
git commit -m "test(core): manifest source end-to-end coverage"
```

---

## Task 11: Documentation and version bump

**Files:**
- Modify: `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go` (version)

- [ ] **Step 1: Update docs**

`README.md` — in the Custom Sources section:
- New "Manifest Sources" subsection: the definition YAML (design §3 example), the manifest document format — a fields table for `mods[]` (id, name, version, author, summary, game_ids, url, updated_at, dependencies, files) and `files[]` (id, name, filename, version, size, url, sha256, primary) with required fields marked — plus the semantics: remote manifests cached for `refresh` (default 15m), local paths read live; `game_ids` matched against the game's mapped `sources:` value (empty = all games); `sha256` verified on download; dependencies resolve within the source.
- New "Authentication" subsection: `auth.api_key` block (`in: header|query`, `name`), key resolution (`LMM_<ID>_API_KEY` env var → `lmm auth login <id>` stored token), key applies to both manifest fetch and file downloads.
- Update the Common Fields `type` row: `directory` and `manifest` are supported; `api` is planned.

`CHANGELOG.md` — under `[Unreleased]` → `### Added`:

```markdown
- Manifest source type: publish a JSON/YAML mod list (https URL or local file) and use it as a full source — search, install, within-source dependencies, and update checks
- Declared `sha256` checksums in manifests are verified on download
- API-key authentication for custom sources (`auth.api_key` in the definition; `LMM_<ID>_API_KEY` env var or `lmm auth login <id>`)
```

- [ ] **Step 2: Version bump and final verification**

- `CHANGELOG.md`: move `[Unreleased]` items to `## [1.7.0] - <today>`; update comparison links.
- `cmd/lmm/root.go`: `version = "1.7.0"`.

```bash
go fmt ./... && go vet ./... && go test ./...
git add README.md CHANGELOG.md
git commit -m "docs: document manifest sources and custom-source authentication"
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 1.7.0"
```

---

## Out of Scope (tracked separately)

- **Phase 4** — declarative REST `api` type (#49) — reuses `AuthConfig`, `DownloadHeaderProvider`, and `searchMods` from this phase
- **Phase 5** — aggregate search, TUI Sources screen, `source validate --probe` (#50)
- Deferred polish items — issue #52
