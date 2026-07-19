# Custom Sources — Phases 1 & 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Users can define custom mod sources in `~/.config/lmm/sources/*.yaml`; the `directory` type turns a local folder of mods (e.g. 7D2D modlets) into a first-class source — searchable, installable, and update-checked.

**Architecture:** New `internal/source/custom` package implements `ModSource` from declarative YAML definitions loaded by `internal/storage/config`. A `Capabilities`/`ErrNotSupported` mechanism lets partial sources degrade gracefully. Local installs reuse the existing staging cache flow via a `file://` hook in `Service.DownloadModToCache`.

**Tech Stack:** Go, gopkg.in/yaml.v3 (existing dep), encoding/xml (stdlib), testify.

**Spec:** `docs/plans/2026-07-13-custom-sources-design.md` (Phases 3–5 — manifest, api, aggregate search — are separate plans/issues.)

## Global Constraints

- TDD: every task starts with a failing test (`~/.claude/DEV.md`, repo CLAUDE.md).
- Error wrapping with context: `fmt.Errorf("doing X: %w", err)` (GO.md).
- `ctx context.Context` first param for I/O paths; no ctx in structs (GO.MD).
- No new dependencies.
- `go fmt ./...` and `go vet ./...` clean before every commit.
- Custom source definition IDs: `^[a-z0-9-]+$`; a broken definition file must never break lmm startup (warn + skip).
- Definitions live in `<configDir>/sources/*.yaml` (default `~/.config/lmm/sources/`).
- Commit after each task; conventional commit messages.

---

## Phase 1 — Foundation

### Task 1: Capabilities & ErrNotSupported

**Files:**
- Modify: `internal/source/source.go`
- Test: `internal/source/source_test.go` (create)

**Interfaces:**
- Consumes: existing `ModSource` interface.
- Produces: `source.ErrNotSupported` (sentinel error), `source.Capabilities{Search, Dependencies, Updates, Auth bool}`, `source.CapabilityReporter` interface, `source.CapabilitiesOf(src ModSource) Capabilities`.

- [ ] **Step 1: Write the failing test**

Create `internal/source/source_test.go`:

```go
package source

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
)

// fullSource implements ModSource but not CapabilityReporter.
type fullSource struct{}

func (fullSource) ID() string      { return "full" }
func (fullSource) Name() string    { return "Full" }
func (fullSource) AuthURL() string { return "" }
func (fullSource) ExchangeToken(context.Context, string) (*Token, error) { return nil, nil }
func (fullSource) Search(context.Context, SearchQuery) (SearchResult, error) {
	return SearchResult{}, nil
}
func (fullSource) GetMod(context.Context, string, string) (*domain.Mod, error) { return nil, nil }
func (fullSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (fullSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (fullSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (fullSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// partialSource additionally reports limited capabilities.
type partialSource struct{ fullSource }

func (partialSource) Capabilities() Capabilities {
	return Capabilities{Search: true, Updates: true}
}

func TestCapabilitiesOf(t *testing.T) {
	t.Run("defaults to fully capable", func(t *testing.T) {
		caps := CapabilitiesOf(fullSource{})
		assert.Equal(t, Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true}, caps)
	})

	t.Run("uses CapabilityReporter when implemented", func(t *testing.T) {
		caps := CapabilitiesOf(partialSource{})
		assert.Equal(t, Capabilities{Search: true, Dependencies: false, Updates: true, Auth: false}, caps)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestCapabilitiesOf -v`
Expected: FAIL — `undefined: Capabilities`, `undefined: CapabilitiesOf`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/source/source.go` (add `"errors"` to imports):

```go
// ErrNotSupported indicates a source does not support the requested operation.
// Callers should branch with errors.Is(err, ErrNotSupported) and degrade
// gracefully (hide the action, show a notice) rather than treat it as a failure.
var ErrNotSupported = errors.New("operation not supported by this source")

// Capabilities reports which optional operations a source supports.
type Capabilities struct {
	Search       bool
	Dependencies bool
	Updates      bool
	Auth         bool
}

// CapabilityReporter is implemented by sources that support only a subset of
// ModSource operations. Sources that do not implement it are assumed fully
// capable.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// CapabilitiesOf returns src's capabilities, assuming full capability for
// sources that do not implement CapabilityReporter (all built-in sources).
func CapabilitiesOf(src ModSource) Capabilities {
	if cr, ok := src.(CapabilityReporter); ok {
		return cr.Capabilities()
	}
	return Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -v`
Expected: PASS (all, including existing registry tests).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/source.go internal/source/source_test.go
git commit -m "feat(source): add ErrNotSupported sentinel and Capabilities reporting"
```

---

### Task 2: SourceDefinition schema + validation

**Files:**
- Create: `internal/source/custom/definition.go`
- Test: `internal/source/custom/definition_test.go`

**Interfaces:**
- Produces:
  - `custom.SourceDefinition{ID, Name, Type string; AllowHTTP bool; Directory *DirectoryConfig; Manifest *ManifestConfig; API *APIConfig}` with yaml tags
  - `custom.DirectoryConfig{Path string}`
  - `custom.ManifestConfig{URL string; Refresh string}` (Refresh parsed/validated as duration; used in Phase 3)
  - `custom.APIConfig{BaseURL string}` (expanded in Phase 4)
  - `(d *SourceDefinition) Validate() error`
  - Type constants: `custom.TypeDirectory = "directory"`, `custom.TypeManifest = "manifest"`, `custom.TypeAPI = "api"`

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/definition_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -v`
Expected: FAIL — package does not exist / undefined types.

- [ ] **Step 3: Write minimal implementation**

Create `internal/source/custom/definition.go`:

```go
// Package custom implements user-defined mod sources configured declaratively
// via YAML files in <configDir>/sources/. See the design doc:
// docs/plans/2026-07-13-custom-sources-design.md
package custom

import (
	"errors"
	"fmt"
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
// matching Type.
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
	URL     string `yaml:"url"`
	Refresh string `yaml:"refresh"` // Go duration string, e.g. "15m"; empty = default
}

// APIConfig configures a declarative REST source (expanded in Phase 4).
type APIConfig struct {
	BaseURL string `yaml:"base_url"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

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
		if d.Manifest.Refresh != "" {
			if _, err := time.ParseDuration(d.Manifest.Refresh); err != nil {
				return fmt.Errorf("manifest.refresh: %w", err)
			}
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS (all table cases).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/
git commit -m "feat(source): add custom source definition schema and validation"
```

---

### Task 3: Definition loader

**Files:**
- Create: `internal/storage/config/sources.go`
- Test: `internal/storage/config/sources_test.go`

**Interfaces:**
- Consumes: `custom.SourceDefinition`, `(*SourceDefinition).Validate()` from Task 2.
- Produces:
  - `config.SourceLoadError{File string; Err error}` with `Error() string`
  - `config.LoadSourceDefinitions(configDir string) ([]custom.SourceDefinition, []SourceLoadError, error)` — missing dir ⇒ `(nil, nil, nil)`; per-file problems land in `[]SourceLoadError`, only unreadable-directory is a hard error
  - `config.LoadSourceDefinitionFile(path string) (custom.SourceDefinition, error)` — used by `lmm source validate`

- [ ] **Step 1: Write the failing test**

Create `internal/storage/config/sources_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSourceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

func TestLoadSourceDefinitions(t *testing.T) {
	t.Run("missing sources dir is not an error", func(t *testing.T) {
		defs, loadErrs, err := LoadSourceDefinitions(t.TempDir())
		assert.NoError(t, err)
		assert.Empty(t, defs)
		assert.Empty(t, loadErrs)
	})

	t.Run("loads valid definitions and collects per-file errors", func(t *testing.T) {
		configDir := t.TempDir()
		srcDir := filepath.Join(configDir, "sources")
		writeSourceFile(t, srcDir, "good.yaml", `
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`)
		writeSourceFile(t, srcDir, "bad-yaml.yaml", "id: [unclosed")
		writeSourceFile(t, srcDir, "invalid.yaml", `
id: BAD_ID
name: Bad
type: directory
directory:
  path: ~/x
`)
		writeSourceFile(t, srcDir, "notes.txt", "not yaml, ignored")

		defs, loadErrs, err := LoadSourceDefinitions(configDir)
		assert.NoError(t, err)
		require.Len(t, defs, 1)
		assert.Equal(t, "my-mods", defs[0].ID)
		require.Len(t, loadErrs, 2)
		files := []string{loadErrs[0].File, loadErrs[1].File}
		assert.Contains(t, files, "bad-yaml.yaml")
		assert.Contains(t, files, "invalid.yaml")
	})

	t.Run("duplicate ids across files are rejected", func(t *testing.T) {
		configDir := t.TempDir()
		srcDir := filepath.Join(configDir, "sources")
		def := `
id: dupe
name: Dupe
type: directory
directory:
  path: ~/mods
`
		writeSourceFile(t, srcDir, "a.yaml", def)
		writeSourceFile(t, srcDir, "b.yaml", def)

		defs, loadErrs, err := LoadSourceDefinitions(configDir)
		assert.NoError(t, err)
		assert.Len(t, defs, 1)
		require.Len(t, loadErrs, 1)
		assert.ErrorContains(t, loadErrs[0].Err, "duplicate")
	})
}

func TestLoadSourceDefinitionFile(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "s.yaml", `
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`)

	def, err := LoadSourceDefinitionFile(filepath.Join(dir, "s.yaml"))
	assert.NoError(t, err)
	assert.Equal(t, "my-mods", def.ID)

	_, err = LoadSourceDefinitionFile(filepath.Join(dir, "missing.yaml"))
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/config/ -run 'TestLoadSourceDefinition' -v`
Expected: FAIL — `undefined: LoadSourceDefinitions`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/storage/config/sources.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/config/ -v`
Expected: PASS (new and existing config tests).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/storage/config/sources.go internal/storage/config/sources_test.go
git commit -m "feat(config): load custom source definitions from sources directory"
```

---

### Task 4: Factory + startup registration

**Files:**
- Create: `internal/source/custom/custom.go`
- Test: `internal/source/custom/custom_test.go`
- Modify: `cmd/lmm/root.go` (registerSources + initService call site)

**Interfaces:**
- Consumes: `config.LoadSourceDefinitions` (Task 3), `svc.RegisterSource`/`svc.GetSource` (existing).
- Produces: `custom.New(def SourceDefinition) (source.ModSource, error)` — the single construction entry point; Phase 2 adds the directory case, Phases 3–4 add theirs.

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/custom_test.go`:

```go
package custom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	t.Run("unimplemented types return a clear error", func(t *testing.T) {
		for _, typ := range []string{TypeDirectory, TypeManifest, TypeAPI} {
			def := SourceDefinition{ID: "x", Name: "X", Type: typ}
			_, err := New(def)
			assert.ErrorContains(t, err, "not yet supported", "type %s", typ)
		}
	})
}
```

(Phase 2 flips the `TypeDirectory` expectation; that change is written into Task 11.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestNew -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/source/custom/custom.go`:

```go
package custom

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// New constructs a ModSource from a validated definition. It returns an error
// for definition types whose implementation has not shipped yet, so startup
// can warn-and-skip instead of failing.
func New(def SourceDefinition) (source.ModSource, error) {
	switch def.Type {
	default:
		return nil, fmt.Errorf("source type %q is not yet supported", def.Type)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Wire into startup**

In `cmd/lmm/root.go`:

1. Add imports: `"github.com/DonovanMods/linux-mod-manager/internal/source/custom"` (config is already imported).
2. In `initService()`, change the call `registerSources(svc)` to `registerSources(svc, cfg.ConfigDir)`.
3. Replace `registerSources` and add `registerCustomSources`:

```go
// registerSources registers all available mod sources with the service.
// Built-ins first, then user-defined sources from <configDir>/sources/.
func registerSources(svc *core.Service, cfgDir string) {
	// NexusMods
	nexusKey := getSourceAPIKey(svc, "nexusmods", "NEXUSMODS_API_KEY")
	svc.RegisterSource(nexusmods.New(nil, nexusKey))

	// CurseForge
	curseKey := getSourceAPIKey(svc, "curseforge", "CURSEFORGE_API_KEY")
	svc.RegisterSource(curseforge.New(nil, curseKey))

	registerCustomSources(svc, cfgDir)
}

// registerCustomSources loads user-defined source definitions and registers
// the valid ones. Broken definitions warn on stderr and are skipped — a bad
// file must never prevent lmm from starting.
func registerCustomSources(svc *core.Service, cfgDir string) {
	defs, loadErrs, err := config.LoadSourceDefinitions(cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading custom sources: %v\n", err)
		return
	}
	for _, le := range loadErrs {
		fmt.Fprintf(os.Stderr, "warning: skipping source definition %v\n", le)
	}
	for _, def := range defs {
		if _, err := svc.GetSource(def.ID); err == nil {
			fmt.Fprintf(os.Stderr, "warning: skipping source %q: id already in use\n", def.ID)
			continue
		}
		src, err := custom.New(def)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping source %q: %v\n", def.ID, err)
			continue
		}
		svc.RegisterSource(src)
	}
}
```

- [ ] **Step 6: Verify build and full test suite**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/custom.go internal/source/custom/custom_test.go cmd/lmm/root.go
git commit -m "feat(cli): register user-defined sources at startup with warn-and-skip"
```

---

### Task 5: `lmm source list` and `lmm source validate`

**Files:**
- Create: `cmd/lmm/source.go`
- Test: `cmd/lmm/source_test.go`

**Interfaces:**
- Consumes: `svc.ListSources()`, `source.CapabilitiesOf`, `config.LoadSourceDefinitions`, `config.LoadSourceDefinitionFile`, `withService`, `getServiceConfig` (all existing/prior tasks).
- Produces: `lmm source list` (table + `--json`), `lmm source validate <file>`. Exit code 1 when validate fails.

- [ ] **Step 1: Write the failing test**

Create `cmd/lmm/source_test.go` (follows the light cmd-test pattern used by `list_test.go` — heavy logic was tested in internal packages):

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceCmd_Structure(t *testing.T) {
	assert.Equal(t, "source", sourceCmd.Use)
	assert.NotEmpty(t, sourceCmd.Short)

	names := make([]string, 0)
	for _, c := range sourceCmd.Commands() {
		names = append(names, c.Name())
	}
	assert.Contains(t, names, "list")
	assert.Contains(t, names, "validate")
}

func TestSourceValidateCmd(t *testing.T) {
	run := func(args ...string) (string, error) {
		cmd := &cobra.Command{Use: "test"}
		cmd.AddCommand(sourceCmd)
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return buf.String(), err
	}

	t.Run("valid file passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "good.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`), 0644))

		out, err := run("source", "validate", path)
		assert.NoError(t, err)
		assert.Contains(t, out, "valid")
	})

	t.Run("invalid file fails with reason", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: BAD_ID
name: Bad
type: directory
directory:
  path: ~/x
`), 0644))

		_, err := run("source", "validate", path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must match")
	})

	t.Run("missing argument errors", func(t *testing.T) {
		_, err := run("source", "validate")
		assert.Error(t, err)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/lmm/ -run 'TestSource' -v`
Expected: FAIL — `undefined: sourceCmd`.

- [ ] **Step 3: Write the implementation**

Create `cmd/lmm/source.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Manage mod sources",
	Long:  "List registered mod sources and validate user-defined source definitions.",
}

// sourceInfo is one row of `lmm source list` output.
type sourceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"` // "built-in", "directory", "manifest", "api", or "error"
	Auth         string `json:"auth"` // "yes", "no", "n/a"
	Capabilities string `json:"capabilities"`
	Error        string `json:"error,omitempty"`
}

var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all mod sources",
	Long:  "List built-in and user-defined mod sources, including definitions that failed to load.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			cfg, err := getServiceConfig()
			if err != nil {
				return err
			}
			defs, loadErrs, err := config.LoadSourceDefinitions(cfg.ConfigDir)
			if err != nil {
				return fmt.Errorf("loading source definitions: %w", err)
			}

			defTypes := make(map[string]string, len(defs))
			for _, d := range defs {
				defTypes[d.ID] = d.Type
			}

			var rows []sourceInfo
			for _, src := range svc.ListSources() {
				typ, isCustom := defTypes[src.ID()]
				if !isCustom {
					typ = "built-in"
				}
				rows = append(rows, sourceInfo{
					ID:           src.ID(),
					Name:         src.Name(),
					Type:         typ,
					Auth:         authState(src),
					Capabilities: capabilitySummary(source.CapabilitiesOf(src)),
				})
			}
			for _, le := range loadErrs {
				rows = append(rows, sourceInfo{
					ID:    le.File,
					Type:  "error",
					Error: le.Err.Error(),
				})
			}

			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tTYPE\tAUTH\tCAPABILITIES\tERROR")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Type, r.Auth, r.Capabilities, r.Error)
			}
			return w.Flush()
		})
	},
}

var sourceValidateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Validate a source definition file",
	Long:  "Parse and validate a user-defined source definition YAML file, reporting any problems.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		def, err := config.LoadSourceDefinitionFile(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%s source %q)\n", args[0], def.Type, def.ID)
		return nil
	},
}

// authState reports a source's authentication status for display.
func authState(src source.ModSource) string {
	if !source.CapabilitiesOf(src).Auth {
		return "n/a"
	}
	if a, ok := src.(interface{ IsAuthenticated() bool }); ok {
		if a.IsAuthenticated() {
			return "yes"
		}
		return "no"
	}
	return "yes"
}

// capabilitySummary renders capabilities as a compact list, e.g. "search,updates".
func capabilitySummary(c source.Capabilities) string {
	out := ""
	add := func(enabled bool, name string) {
		if !enabled {
			return
		}
		if out != "" {
			out += ","
		}
		out += name
	}
	add(c.Search, "search")
	add(c.Dependencies, "deps")
	add(c.Updates, "updates")
	add(c.Auth, "auth")
	return out
}

func init() {
	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceValidateCmd)
	rootCmd.AddCommand(sourceCmd)
}
```

- [ ] **Step 4: Run tests, then verify manually**

Run: `go test ./cmd/lmm/ -v`
Expected: PASS.

Run: `go build -o lmm ./cmd/lmm && ./lmm source list`
Expected: table showing `nexusmods` and `curseforge` as `built-in` with full capabilities.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add cmd/lmm/source.go cmd/lmm/source_test.go
git commit -m "feat(cli): add 'lmm source list' and 'lmm source validate' commands"
```

---

### Task 6: Phase 1 documentation

**Files:**
- Modify: `README.md` (commands section), `CHANGELOG.md` (`[Unreleased]`)

- [ ] **Step 1: Update docs**

`CHANGELOG.md` — add under `[Unreleased]` → `### Added`:

```markdown
- `lmm source list` — list built-in and user-defined mod sources
- `lmm source validate <file>` — validate a user-defined source definition
- User-defined source definitions loaded from `~/.config/lmm/sources/*.yaml`
```

`README.md` — add a "Custom Sources" section documenting: the `sources/` directory location, the common definition fields (`id`, `name`, `type`, `allow_http`), warn-and-skip behavior, and both new commands with example output. Copy the YAML example from the design doc §1.

- [ ] **Step 2: Verify and commit**

Run: `go test ./...`
Expected: PASS.

```bash
git add README.md CHANGELOG.md
git commit -m "docs: document custom source definitions and source commands"
```

---

## Phase 2 — Directory Source

### Task 7: Shared version extraction in domain

**Files:**
- Modify: `internal/domain/mod.go`, `internal/core/importer.go`
- Test: `internal/domain/mod_test.go` (append)

**Interfaces:**
- Produces: `domain.ExtractVersionFromName(name string) string` — returns the **last** version-like pattern (e.g. `"jei-1.20.1-15.3.0"` → `"15.3.0"`), or `""`.
- Consumes/refactors: `extractVersionFromFilename` + `versionRegexImporter` in `internal/core/importer.go` are deleted; call sites switch to the domain function.

- [ ] **Step 1: Write the failing test**

Append to `internal/domain/mod_test.go`:

```go
func TestExtractVersionFromName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "BiggerBackpack-1.2.0", "1.2.0"},
		{"v prefix", "cool-mod-v2.1", "2.1"},
		{"last version wins", "jei-1.20.1-15.3.0", "15.3.0"},
		{"prerelease suffix", "mod-1.0.0-beta2", "1.0.0-beta2"},
		{"no version", "JustAName", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractVersionFromName(tt.in))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestExtractVersionFromName -v`
Expected: FAIL — `undefined: ExtractVersionFromName`.

- [ ] **Step 3: Implement and refactor**

Append to `internal/domain/mod.go` (add `"regexp"` to imports):

```go
// versionPattern matches version-like strings such as 1.2.3, v1.2.3, or
// 1.0.0-beta2. The optional suffix must start with a letter so compound
// strings like "1.20.1-15.3.0" parse as two versions, not one.
var versionPattern = regexp.MustCompile(`[vV]?(\d+\.\d+(?:\.\d+)?(?:\.\d+)?(?:[-+][a-zA-Z][\w.]*)?)`)

// ExtractVersionFromName extracts the last version-like pattern from a file or
// directory name (mod version typically follows the game version in names like
// "jei-1.20.1-15.3.0"). Returns "" when no version is present.
func ExtractVersionFromName(name string) string {
	matches := versionPattern.FindAllStringSubmatch(name, -1)
	if len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	return ""
}
```

In `internal/core/importer.go`: delete `versionRegexImporter` and `extractVersionFromFilename`, and replace every call `extractVersionFromFilename(x)` with `domain.ExtractVersionFromName(x)` (call sites: `Import`, `detectModFromFilename` — find them all with `grep -n extractVersionFromFilename internal/core/`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/domain/ ./internal/core/ -v`
Expected: PASS (domain new test + all existing importer tests).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/domain/mod.go internal/domain/mod_test.go internal/core/importer.go
git commit -m "refactor(domain): promote filename version extraction to domain"
```

---

### Task 8: Metadata readers (ModInfo.xml)

**Files:**
- Create: `internal/source/custom/metadata/reader.go`, `internal/source/custom/metadata/modinfo.go`
- Test: `internal/source/custom/metadata/metadata_test.go`

**Interfaces:**
- Produces:
  - `metadata.Info{Name, DisplayName, Version, Summary, Author string}`
  - `metadata.Reader` interface: `Detect(modDir string) string` (path or `""`), `Read(path string) (*Info, error)`
  - `metadata.Resolve(modDir string) *Info` — tries readers in order; `nil` when none match or reading fails
- Consumed by: Task 9 (directory scan).

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/metadata/metadata_test.go`:

```go
package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 7D2D "V2" layout: fields directly under <xml>.
const modInfoV2 = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<Name value="BiggerBackpack"/>
	<DisplayName value="Bigger Backpack"/>
	<Version value="1.2.0"/>
	<Description value="Carry more stuff"/>
	<Author value="Donovan"/>
</xml>`

// 7D2D "V1" layout: fields nested in <ModInfo>.
const modInfoV1 = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<ModInfo>
		<Name value="OldMod"/>
		<Version value="0.9"/>
		<Description value="Legacy layout"/>
		<Author value="Someone"/>
	</ModInfo>
</xml>`

func writeModDir(t *testing.T, xml string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ModInfo.xml"), []byte(xml), 0644))
	return dir
}

func TestResolveModInfoV2(t *testing.T) {
	info := Resolve(writeModDir(t, modInfoV2))
	require.NotNil(t, info)
	assert.Equal(t, "BiggerBackpack", info.Name)
	assert.Equal(t, "Bigger Backpack", info.DisplayName)
	assert.Equal(t, "1.2.0", info.Version)
	assert.Equal(t, "Carry more stuff", info.Summary)
	assert.Equal(t, "Donovan", info.Author)
}

func TestResolveModInfoV1(t *testing.T) {
	info := Resolve(writeModDir(t, modInfoV1))
	require.NotNil(t, info)
	assert.Equal(t, "OldMod", info.Name)
	assert.Equal(t, "0.9", info.Version)
}

func TestResolveNoMetadata(t *testing.T) {
	assert.Nil(t, Resolve(t.TempDir()))
}

func TestResolveMalformedXML(t *testing.T) {
	assert.Nil(t, Resolve(writeModDir(t, "<xml><unclosed")))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/metadata/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/source/custom/metadata/reader.go`:

```go
// Package metadata extracts mod metadata from well-known files inside a mod
// directory (e.g. 7 Days to Die's ModInfo.xml). Adding a format means adding
// one Reader implementation and listing it in readers.
package metadata

// Info is metadata extracted from a well-known mod metadata file. Zero-value
// fields mean the file did not provide them.
type Info struct {
	Name        string
	DisplayName string
	Version     string
	Summary     string
	Author      string
}

// Reader parses one well-known metadata file format.
type Reader interface {
	// Detect returns the metadata file path within modDir, or "" if absent.
	Detect(modDir string) string
	// Read parses the metadata file at path.
	Read(path string) (*Info, error)
}

// readers lists all known formats in priority order.
var readers = []Reader{ModInfoXML{}}

// Resolve extracts metadata from modDir using the first matching reader.
// Returns nil when no known metadata file exists or it cannot be parsed;
// callers fall back to name-based detection.
func Resolve(modDir string) *Info {
	for _, r := range readers {
		path := r.Detect(modDir)
		if path == "" {
			continue
		}
		info, err := r.Read(path)
		if err != nil {
			continue // malformed metadata falls back, it doesn't fail the scan
		}
		return info
	}
	return nil
}
```

Create `internal/source/custom/metadata/modinfo.go`:

```go
package metadata

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// ModInfoXML reads 7 Days to Die ModInfo.xml files. Two layouts exist:
// V2 puts fields directly under <xml>; V1 nests them in <ModInfo>.
type ModInfoXML struct{}

// Detect implements Reader.
func (ModInfoXML) Detect(modDir string) string {
	path := filepath.Join(modDir, "ModInfo.xml")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

type modInfoFields struct {
	Name        attrValue `xml:"Name"`
	DisplayName attrValue `xml:"DisplayName"`
	Version     attrValue `xml:"Version"`
	Description attrValue `xml:"Description"`
	Author      attrValue `xml:"Author"`
}

type modInfoDoc struct {
	modInfoFields
	ModInfo *modInfoFields `xml:"ModInfo"`
}

type attrValue struct {
	Value string `xml:"value,attr"`
}

// Read implements Reader.
func (ModInfoXML) Read(path string) (*Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ModInfo.xml: %w", err)
	}

	var doc modInfoDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing ModInfo.xml: %w", err)
	}

	fields := doc.modInfoFields
	if doc.ModInfo != nil && doc.ModInfo.Name.Value != "" {
		fields = *doc.ModInfo // V1 layout
	}
	if fields.Name.Value == "" {
		return nil, fmt.Errorf("ModInfo.xml has no Name element")
	}

	return &Info{
		Name:        fields.Name.Value,
		DisplayName: fields.DisplayName.Value,
		Version:     fields.Version.Value,
		Summary:     fields.Description.Value,
		Author:      fields.Author.Value,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/metadata/ -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/metadata/
git commit -m "feat(source): add well-known metadata readers with 7D2D ModInfo.xml support"
```

---

### Task 9: Directory source — construction, scan, and read operations

**Files:**
- Create: `internal/source/custom/directory.go`
- Test: `internal/source/custom/directory_test.go`

**Interfaces:**
- Consumes: `metadata.Resolve` (Task 8), `domain.ExtractVersionFromName` (Task 7), `source.ErrNotSupported`/`Capabilities` (Task 1).
- Produces:
  - `custom.NewDirectory(def SourceDefinition) (*Directory, error)` — expands `~`, requires existing directory
  - `*Directory` implements `source.ModSource` + `source.CapabilityReporter`
  - Semantics later tasks rely on: mod ID = subdirectory name (or archive base name); `GetModFiles` returns exactly one file with `ID: "main"`; `GetDownloadURL` returns `"file://" + <absolute path>`; `GetDependencies`/`ExchangeToken` return `ErrNotSupported`; `Capabilities() = {Search: true, Updates: true}`

- [ ] **Step 1: Write the failing test**

Create `internal/source/custom/directory_test.go`:

```go
package custom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testModInfo = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<Name value="BiggerBackpack"/>
	<DisplayName value="Bigger Backpack"/>
	<Version value="1.2.0"/>
	<Description value="Carry more stuff"/>
	<Author value="Donovan"/>
</xml>`

// newTestDirectory builds a source over a temp dir containing:
//   BiggerBackpack/        (with ModInfo.xml)
//   PlainMod-0.5/          (no metadata; version from dirname)
//   archived-mod-2.0.zip   (archive mod)
//   README.md              (ignored: not a dir or archive)
func newTestDirectory(t *testing.T) *Directory {
	t.Helper()
	root := t.TempDir()

	bb := filepath.Join(root, "BiggerBackpack")
	require.NoError(t, os.MkdirAll(bb, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bb, "ModInfo.xml"), []byte(testModInfo), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bb, "readme.txt"), []byte("hi"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "PlainMod-0.5"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "archived-mod-2.0.zip"), []byte("zipbytes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("ignored"), 0644))

	def := SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: root},
	}
	d, err := NewDirectory(def)
	require.NoError(t, err)
	return d
}

func TestNewDirectoryValidation(t *testing.T) {
	def := SourceDefinition{
		ID:        "x",
		Name:      "X",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: filepath.Join(t.TempDir(), "missing")},
	}
	_, err := NewDirectory(def)
	assert.ErrorContains(t, err, "missing")
}

func TestDirectoryIdentityAndCapabilities(t *testing.T) {
	d := newTestDirectory(t)
	assert.Equal(t, "my-mods", d.ID())
	assert.Equal(t, "My Mods", d.Name())
	assert.Equal(t, source.Capabilities{Search: true, Updates: true}, d.Capabilities())
	assert.Empty(t, d.AuthURL())

	_, err := d.ExchangeToken(context.Background(), "code")
	assert.True(t, errors.Is(err, source.ErrNotSupported))

	_, err = d.GetDependencies(context.Background(), nil)
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

func TestDirectorySearch(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	t.Run("empty query returns all mods", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{GameID: "anything"})
		require.NoError(t, err)
		assert.Equal(t, 3, res.TotalCount)
		require.Len(t, res.Mods, 3)
	})

	t.Run("metadata takes priority over dirname", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "backpack"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		m := res.Mods[0]
		assert.Equal(t, "BiggerBackpack", m.ID)
		assert.Equal(t, "Bigger Backpack", m.Name)
		assert.Equal(t, "1.2.0", m.Version)
		assert.Equal(t, "Carry more stuff", m.Summary)
		assert.Equal(t, "Donovan", m.Author)
		assert.Equal(t, "my-mods", m.SourceID)
	})

	t.Run("fallback parses version from name", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "plainmod"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		assert.Equal(t, "PlainMod-0.5", res.Mods[0].ID)
		assert.Equal(t, "PlainMod", res.Mods[0].Name)
		assert.Equal(t, "0.5", res.Mods[0].Version)
	})

	t.Run("summary matches rank after name matches", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "stuff"}) // only in summary
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		assert.Equal(t, "BiggerBackpack", res.Mods[0].ID)
	})

	t.Run("pagination", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Page: 0, PageSize: 2})
		require.NoError(t, err)
		assert.Len(t, res.Mods, 2)
		assert.Equal(t, 3, res.TotalCount)

		res, err = d.Search(ctx, source.SearchQuery{Page: 1, PageSize: 2})
		require.NoError(t, err)
		assert.Len(t, res.Mods, 1)
	})
}

func TestDirectoryGetMod(t *testing.T) {
	d := newTestDirectory(t)

	mod, err := d.GetMod(context.Background(), "ignored", "BiggerBackpack")
	require.NoError(t, err)
	assert.Equal(t, "Bigger Backpack", mod.Name)

	_, err = d.GetMod(context.Background(), "ignored", "nope")
	assert.ErrorContains(t, err, "not found")
}

func TestDirectoryFilesAndDownloadURL(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	mod, err := d.GetMod(ctx, "", "BiggerBackpack")
	require.NoError(t, err)

	files, err := d.GetModFiles(ctx, mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "main", files[0].ID)
	assert.Equal(t, "BiggerBackpack", files[0].FileName)
	assert.True(t, files[0].IsPrimary)

	url, err := d.GetDownloadURL(ctx, mod, files[0].ID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "file://"))
	assert.True(t, strings.HasSuffix(url, "/BiggerBackpack"))

	// Archive mods point at the archive file.
	zipMod, err := d.GetMod(ctx, "", "archived-mod-2.0")
	require.NoError(t, err)
	zipFiles, err := d.GetModFiles(ctx, zipMod)
	require.NoError(t, err)
	require.Len(t, zipFiles, 1)
	assert.Equal(t, "archived-mod-2.0.zip", zipFiles[0].FileName)
	assert.Equal(t, int64(8), zipFiles[0].Size) // len("zipbytes")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestDirectory -v`
Expected: FAIL — `undefined: NewDirectory`.

- [ ] **Step 3: Write the implementation**

Create `internal/source/custom/directory.go`:

```go
package custom

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom/metadata"
)

// Directory is a ModSource backed by a local directory: each subdirectory (or
// .zip/.jar archive) is one mod. The directory is rescanned on each operation
// so edits show up without restarting lmm; scans are local and cheap.
type Directory struct {
	id   string
	name string
	path string // absolute, verified at construction
}

// NewDirectory constructs a directory source from a validated definition.
// The configured path must exist and be a directory.
func NewDirectory(def SourceDefinition) (*Directory, error) {
	path := def.Directory.Path
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("expanding %q: %w", path, err)
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("directory source path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("directory source path %s is not a directory", abs)
	}

	return &Directory{id: def.ID, name: def.Name, path: abs}, nil
}

// ID implements source.ModSource.
func (d *Directory) ID() string { return d.id }

// Name implements source.ModSource.
func (d *Directory) Name() string { return d.name }

// AuthURL implements source.ModSource; directory sources need no auth.
func (d *Directory) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (d *Directory) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", d.id, source.ErrNotSupported)
}

// Capabilities implements source.CapabilityReporter.
func (d *Directory) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Updates: true}
}

// dirMod pairs a scanned mod with its filesystem location.
type dirMod struct {
	mod       domain.Mod
	path      string // absolute path to the mod directory or archive
	isArchive bool
	size      int64 // archive size in bytes; 0 for directories
}

// scan reads the source directory. Subdirectories are directory mods;
// .zip/.jar files are archive mods; everything else is ignored.
func (d *Directory) scan() ([]dirMod, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, fmt.Errorf("source %q: scanning %s: %w", d.id, d.path, err)
	}

	var mods []dirMod
	for _, entry := range entries {
		entryPath := filepath.Join(d.path, entry.Name())

		if entry.IsDir() {
			mods = append(mods, d.scanDir(entry.Name(), entryPath))
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".zip" && ext != ".jar" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("source %q: stat %s: %w", d.id, entryPath, err)
		}
		base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		name, version := nameAndVersionFrom(base)
		mods = append(mods, dirMod{
			mod: domain.Mod{
				ID:       base,
				SourceID: d.id,
				Name:     name,
				Version:  version,
			},
			path:      entryPath,
			isArchive: true,
			size:      info.Size(),
		})
	}

	return mods, nil
}

// scanDir builds a dirMod for a mod directory, preferring well-known metadata
// files over dirname parsing.
func (d *Directory) scanDir(dirName, dirPath string) dirMod {
	mod := domain.Mod{ID: dirName, SourceID: d.id}

	if info := metadata.Resolve(dirPath); info != nil {
		mod.Name = info.DisplayName
		if mod.Name == "" {
			mod.Name = info.Name
		}
		mod.Version = info.Version
		mod.Summary = info.Summary
		mod.Description = info.Summary
		mod.Author = info.Author
	} else {
		mod.Name, mod.Version = nameAndVersionFrom(dirName)
	}

	return dirMod{mod: mod, path: dirPath}
}

// nameAndVersionFrom splits a directory/file base name into a display name and
// version ("PlainMod-0.5" -> "PlainMod", "0.5").
func nameAndVersionFrom(base string) (string, string) {
	version := domain.ExtractVersionFromName(base)
	name := base
	if version != "" {
		if idx := strings.LastIndex(base, version); idx > 0 {
			name = strings.TrimRight(base[:idx], "-_ vV")
		}
	}
	return name, version
}

// Search implements source.ModSource with client-side matching: case-insensitive
// substring on name and summary; name matches rank first, then alphabetical.
// GameID is ignored — a directory source applies to any game that maps it.
func (d *Directory) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	scanned, err := d.scan()
	if err != nil {
		return source.SearchResult{}, err
	}

	q := strings.ToLower(query.Query)
	type ranked struct {
		mod       domain.Mod
		nameMatch bool
	}
	var matches []ranked
	for _, dm := range scanned {
		nameMatch := q == "" || strings.Contains(strings.ToLower(dm.mod.Name), q) || strings.Contains(strings.ToLower(dm.mod.ID), q)
		summaryMatch := strings.Contains(strings.ToLower(dm.mod.Summary), q)
		if !nameMatch && !summaryMatch {
			continue
		}
		matches = append(matches, ranked{mod: dm.mod, nameMatch: nameMatch})
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
	end := min(start+pageSize, len(matches))
	if start > len(matches) {
		start = len(matches)
	}

	mods := make([]domain.Mod, 0, end-start)
	for _, m := range matches[start:end] {
		mods = append(mods, m.mod)
	}

	return source.SearchResult{
		Mods:       mods,
		TotalCount: len(matches),
		Page:       query.Page,
		PageSize:   pageSize,
	}, nil
}

// GetMod implements source.ModSource. gameID is ignored (see Search).
func (d *Directory) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	dm, err := d.find(modID)
	if err != nil {
		return nil, err
	}
	mod := dm.mod
	return &mod, nil
}

// GetDependencies implements source.ModSource; directory mods declare none.
func (d *Directory) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", d.id, source.ErrNotSupported)
}

// GetModFiles implements source.ModSource: every mod has exactly one synthetic
// file ("main") representing its directory or archive.
func (d *Directory) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	dm, err := d.find(mod.ID)
	if err != nil {
		return nil, err
	}
	return []domain.DownloadableFile{{
		ID:        "main",
		Name:      dm.mod.Name,
		FileName:  filepath.Base(dm.path),
		Version:   dm.mod.Version,
		Size:      dm.size,
		IsPrimary: true,
	}}, nil
}

// GetDownloadURL implements source.ModSource, returning a file:// URL that
// Service.DownloadModToCache ingests by local copy instead of HTTP download.
func (d *Directory) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	dm, err := d.find(mod.ID)
	if err != nil {
		return "", err
	}
	return "file://" + dm.path, nil
}

// CheckUpdates implements source.ModSource by comparing installed versions to
// the current scan.
func (d *Directory) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	scanned, err := d.scan()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.Mod, len(scanned))
	for _, dm := range scanned {
		byID[dm.mod.ID] = dm.mod
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
			continue // mod removed from the directory; nothing to offer
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

// find scans and returns the mod with the given ID.
func (d *Directory) find(modID string) (dirMod, error) {
	scanned, err := d.scan()
	if err != nil {
		return dirMod{}, err
	}
	for _, dm := range scanned {
		if dm.mod.ID == modID {
			return dm, nil
		}
	}
	return dirMod{}, fmt.Errorf("source %q: mod not found: %s", d.id, modID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/directory.go internal/source/custom/directory_test.go
git commit -m "feat(source): add directory source type for local mod folders"
```

---

### Task 10: Directory CheckUpdates behavior test

**Files:**
- Test: `internal/source/custom/directory_test.go` (append)

(The implementation shipped in Task 9; this task pins the update semantics with focused tests before wiring — reject-able independently if the semantics are wrong.)

- [ ] **Step 1: Write the test**

Append to `internal/source/custom/directory_test.go`:

```go
func TestDirectoryCheckUpdates(t *testing.T) {
	d := newTestDirectory(t) // BiggerBackpack is at 1.2.0

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Name: "Bigger Backpack", Version: "1.0.0"}},
		{Mod: domain.Mod{ID: "PlainMod-0.5", SourceID: "my-mods", Name: "PlainMod", Version: "0.5"}},
		{Mod: domain.Mod{ID: "Removed", SourceID: "my-mods", Name: "Removed", Version: "1.0"}},
	}

	updates, err := d.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "BiggerBackpack", updates[0].InstalledMod.ID)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}
```

Add `"github.com/DonovanMods/linux-mod-manager/internal/domain"` to the test file's imports.

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/source/custom/ -run TestDirectoryCheckUpdates -v`
Expected: PASS (up-to-date mods and removed mods produce no update; outdated mod produces one).

- [ ] **Step 3: Commit**

```bash
git add internal/source/custom/directory_test.go
git commit -m "test(source): pin directory source update-check semantics"
```

---

### Task 11: Enable directory type in the factory

**Files:**
- Modify: `internal/source/custom/custom.go`, `internal/source/custom/custom_test.go`

**Interfaces:**
- Produces: `custom.New` returns a working `*Directory` for `type: directory` definitions.

- [ ] **Step 1: Update the test**

Replace the body of `TestNew` in `internal/source/custom/custom_test.go`:

```go
func TestNew(t *testing.T) {
	t.Run("directory type constructs a source", func(t *testing.T) {
		def := SourceDefinition{
			ID:        "my-mods",
			Name:      "My Mods",
			Type:      TypeDirectory,
			Directory: &DirectoryConfig{Path: t.TempDir()},
		}
		src, err := New(def)
		assert.NoError(t, err)
		assert.Equal(t, "my-mods", src.ID())
	})

	t.Run("unimplemented types return a clear error", func(t *testing.T) {
		for _, typ := range []string{TypeManifest, TypeAPI} {
			def := SourceDefinition{ID: "x", Name: "X", Type: typ}
			_, err := New(def)
			assert.ErrorContains(t, err, "not yet supported", "type %s", typ)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestNew -v`
Expected: FAIL — directory case returns "not yet supported".

- [ ] **Step 3: Implement**

In `internal/source/custom/custom.go`, add the case:

```go
func New(def SourceDefinition) (source.ModSource, error) {
	switch def.Type {
	case TypeDirectory:
		return NewDirectory(def)
	default:
		return nil, fmt.Errorf("source type %q is not yet supported", def.Type)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/custom/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/custom.go internal/source/custom/custom_test.go
git commit -m "feat(source): construct directory sources from definitions"
```

---

### Task 12: Local-path ingest in Service.DownloadModToCache

**Files:**
- Modify: `internal/core/service.go` (`DownloadModToCache`, new `ingestLocalToCache`)
- Test: `internal/core/service_download_local_test.go` (create)

**Interfaces:**
- Consumes: existing `copyDir`, `copyFileStreaming`, `commitStagedCache`, `s.extractor` in package `core`; `file://` URLs from `Directory.GetDownloadURL` (Task 9).
- Produces: `DownloadModToCache` transparently ingests `file://` URLs — directories are copied recursively; archive files honor the existing DeployCopy/extract split. `DownloadModResult.Checksum` is empty for local ingests.

- [ ] **Step 1: Write the failing test**

Create `internal/core/service_download_local_test.go`:

```go
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLocalIngestService(t *testing.T) (*Service, *cache.Cache) {
	t.Helper()
	svc := &Service{extractor: NewExtractor()}
	return svc, cache.New(t.TempDir())
}

func TestIngestLocalToCacheDirectory(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "BiggerBackpack")
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "Config"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte("<xml/>"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items/>"), 0644))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Version: "1.2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "BiggerBackpack"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, 2, result.FilesExtracted)
	assert.Empty(t, result.Checksum)

	files, err := gameCache.ListFiles("7dtd", "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

func TestIngestLocalToCacheArchiveCopyMode(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	archive := filepath.Join(t.TempDir(), "coolmod-2.0.zip")
	require.NoError(t, os.WriteFile(archive, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "coolmod-2.0.zip"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, archive)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	cached := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "coolmod-2.0.zip")
	_, err = os.Stat(cached)
	assert.NoError(t, err)
}

func TestIngestLocalToCacheMissingPath(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	game := &domain.Game{ID: "7dtd"}
	mod := &domain.Mod{ID: "x", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "x"}

	_, err := svc.ingestLocalToCache(gameCache, game, mod, file, filepath.Join(t.TempDir(), "gone"))
	assert.Error(t, err)
}
```

Note: if `Service{extractor: ...}` literal construction fails because of unexported invariants, use the package's existing test helper for building a Service (check `service_test.go`) — the assertions stay the same.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestIngestLocal -v`
Expected: FAIL — `undefined: svc.ingestLocalToCache`.

- [ ] **Step 3: Implement**

In `internal/core/service.go` (add `"strings"` to imports if absent):

1. At the top of `DownloadModToCache`, right after the `GetDownloadURL` call and its error check, add:

```go
	if localPath, ok := strings.CutPrefix(url, "file://"); ok {
		return s.ingestLocalToCache(gameCache, game, mod, file, localPath)
	}
```

2. Add the new method after `DownloadModToCache`:

```go
// ingestLocalToCache copies a local mod (directory or archive) into the cache
// using the same staging/commit flow as downloaded mods. Local ingests have no
// download checksum, so DownloadModResult.Checksum is empty.
func (s *Service) ingestLocalToCache(gameCache *cache.Cache, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, localPath string) (*DownloadModResult, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("local mod path: %w", err)
	}

	cachePath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	stagePath := cachePath + ".staging"
	if err := os.RemoveAll(stagePath); err != nil {
		return nil, fmt.Errorf("clearing staging cache: %w", err)
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck
	if gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		if err := copyDir(cachePath, stagePath); err != nil {
			return nil, fmt.Errorf("staging existing cache: %w", err)
		}
	}

	switch {
	case info.IsDir():
		if err := copyDir(localPath, stagePath); err != nil {
			return nil, fmt.Errorf("copying mod directory: %w", err)
		}
	case game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(localPath):
		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}
		if err := copyFileStreaming(localPath, filepath.Join(stagePath, filepath.Base(localPath))); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
	default:
		if err := s.extractor.Extract(localPath, stagePath); err != nil {
			return nil, fmt.Errorf("extracting mod: %w", err)
		}
	}

	if err := commitStagedCache(cachePath, stagePath); err != nil {
		return nil, err
	}

	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, err
	}
	return &DownloadModResult{FilesExtracted: len(files)}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS (new tests + all existing service/installer tests).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/core/service.go internal/core/service_download_local_test.go
git commit -m "feat(core): ingest file:// download URLs by local copy"
```

---

### Task 13: End-to-end verification, docs, and version bump

**Files:**
- Test: `internal/core/service_directory_source_test.go` (create)
- Modify: `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go` (version)

- [ ] **Step 1: Write the end-to-end test**

Create `internal/core/service_directory_source_test.go` — the full loop: definition → source → search → files → download URL → cache ingest:

```go
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectorySourceEndToEnd(t *testing.T) {
	// A modlets directory with one mod.
	root := t.TempDir()
	modDir := filepath.Join(root, "BiggerBackpack")
	require.NoError(t, os.MkdirAll(modDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte(
		`<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`), 0644))

	src, err := custom.New(custom.SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: root},
	})
	require.NoError(t, err)

	svc := &Service{extractor: NewExtractor()}
	gameCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	ctx := context.Background()

	// Search finds the mod.
	res, err := src.Search(ctx, sourceSearchQuery("backpack"))
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]

	// Files + download URL + local ingest land it in the cache.
	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)

	url, err := src.GetDownloadURL(ctx, &mod, files[0].ID)
	require.NoError(t, err)

	result, err := svc.ingestLocalToCache(gameCache, game, &mod, &files[0], url[len("file://"):])
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	assert.True(t, gameCache.Exists("7dtd", "my-mods", "BiggerBackpack", "1.2.0"))
}
```

Add this helper to the same file (keeps the test readable):

```go
func sourceSearchQuery(q string) source.SearchQuery {
	return source.SearchQuery{Query: q, PageSize: 20}
}
```

with import `"github.com/DonovanMods/linux-mod-manager/internal/source"`.

- [ ] **Step 2: Run the full suite**

Run: `go test ./...`
Expected: PASS everywhere.

- [ ] **Step 3: Manual smoke test against the real modlets directory**

```bash
go build -o lmm ./cmd/lmm
mkdir -p ~/.config/lmm/sources
cat > ~/.config/lmm/sources/donovan-mods.yaml <<'EOF'
id: donovan-mods
name: Donovan's 7D2D Modlets
type: directory
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
EOF
./lmm source list                    # expect donovan-mods, type=directory, auth=n/a
./lmm search backpack -g 7dtd --source donovan-mods   # expect modlet hits (game must exist in games.yaml)
```

Expected: the custom source lists and searches. (Remove the test definition afterward if undesired.)

- [ ] **Step 4: Update docs and bump version**

- `CHANGELOG.md`: move `[Unreleased]` items to a new `## [1.6.0] - <today>` section; add the directory-source feature line: `- Directory source type: use a local folder of mods as a first-class source`; update comparison links at the bottom.
- `README.md`: extend the Custom Sources section with the `type: directory` example, metadata resolution rules (ModInfo.xml → dirname fallback), and the mod-ID-is-directory-name note.
- `cmd/lmm/root.go`: `version = "1.6.0"`.

- [ ] **Step 5: Final verification and commits**

```bash
go fmt ./... && go vet ./... && go test ./...
git add internal/core/service_directory_source_test.go README.md CHANGELOG.md
git commit -m "feat(source): directory source end-to-end test and docs"
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 1.6.0"
```

---

## Out of Scope (tracked separately)

- **Phase 3** — manifest source type (issue + plan of its own)
- **Phase 4** — declarative REST `api` type, custom-source auth via `lmm auth login <id>` / `LMM_<ID>_API_KEY` (issue + plan of its own)
- **Phase 5** — aggregate search (core + CLI + TUI), TUI Sources screen, `source validate --probe` (issue + plan of its own)
