# Issue #54 Hardening (Manifest Sources + Auth) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every checkbox in issue #54 — the Phase 5 concurrency prerequisite (manifest fetch mutex/timeout/defensive copy), the pre-existing NexusMods update-domain bug, the same-origin download-key guard, auth CLI polish (status for custom sources, logout for unregistered sources, test pin cleanup), and single-pass download hashing.

**Architecture:** The manifest fetcher stops holding its mutex across the network (lock only guards cache state; duplicate concurrent fetches are acceptable), gains a timeout-bounded dedicated HTTP client, and returns deep copies of the cached document. `Updater.CheckUpdates` gains the game so it can translate installed mods' GameIDs to source-mapped values before calling each source. `DownloadHeaderProvider` becomes URL-aware so manifest sources attach header keys only to same-origin file downloads. The downloader computes SHA-256 alongside MD5 in its existing single streaming pass, letting the service drop its second file read.

**Tech Stack:** Go stdlib only (net/http, net/url, crypto/sha256, sync). No new dependencies.

**Spec:** GitHub issue #54 (checkboxes are the requirements). Design context: docs/plans/2026-07-13-custom-sources-design.md §6/§9.

## Global Constraints

- TDD: every task starts with a failing test.
- Error wrapping with context: `fmt.Errorf("doing X: %w", err)` (GO.md).
- `ctx context.Context` first param for I/O paths; no ctx in structs (GO.md).
- No new dependencies (`golang.org/x/sync` is NOT allowed — hand-roll or accept duplicate fetches).
- `go fmt ./...` and `go vet ./...` clean before every commit.
- API keys never appear in logs or error text (preserved invariant from Phase 3).
- Existing error-message contracts that tests pin must keep working — notably `"sha256 mismatch: source declares %s, downloaded file is %s"`.
- Commit after each task; conventional commit messages.

---

## Task 1: Manifest fetch — timeout, lock-free network, defensive copy

**Files:**
- Modify: `internal/source/custom/manifest.go`
- Test: `internal/source/custom/manifest_test.go` (append)

**Interfaces:**
- Consumes: existing `Manifest` struct fields (`mu`, `cached`, `fetchedAt`, `httpClient`, `now`), `fetchRemote`.
- Produces: `fetch(ctx)` returns a **deep copy** of the document (callers may mutate freely); `NewManifest` builds a dedicated `&http.Client{Timeout: manifestFetchTimeout}`; the mutex is never held across a network call. `deepCopyManifest(doc *manifestDoc) *manifestDoc` (unexported helper).

- [ ] **Step 1: Write the failing tests**

Append to `internal/source/custom/manifest_test.go`:

```go
func TestManifestFetchReturnsDefensiveCopy(t *testing.T) {
	m := newLocalManifest(t)
	ctx := context.Background()

	first, err := m.fetch(ctx)
	require.NoError(t, err)
	// Mutate everything a caller could plausibly touch.
	first.Mods[0].Name = "MUTATED"
	first.Mods[0].GameIDs[0] = "MUTATED"
	first.Mods[0].Files[0].URL = "MUTATED"
	first.Mods = nil

	second, err := m.fetch(ctx)
	require.NoError(t, err)
	require.Len(t, second.Mods, 2)
	assert.Equal(t, "Cool Mod", second.Mods[0].Name)
	assert.Equal(t, "skyrim", second.Mods[0].GameIDs[0])
	assert.NotEqual(t, "MUTATED", second.Mods[0].Files[0].URL)
}

func TestManifestFetchConcurrent(t *testing.T) {
	// Race-detector safety net: concurrent fetches (cache hits and misses)
	// must be data-race free. Run with -race in CI/the suite.
	hits := 0
	var srvMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srvMu.Lock()
		hits++
		srvMu.Unlock()
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ferr := m.fetch(context.Background())
			assert.NoError(t, ferr)
		}()
	}
	wg.Wait()
	srvMu.Lock()
	defer srvMu.Unlock()
	assert.GreaterOrEqual(t, hits, 1)
}

func TestManifestFetchDoesNotBlockOnHungServer(t *testing.T) {
	// A hung server must be bounded by the client timeout, not hang forever.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until test cleanup
	}))
	defer func() { close(release); srv.Close() }()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)
	m.httpClient = &http.Client{Timeout: 200 * time.Millisecond}

	start := time.Now()
	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.NotContains(t, err.Error(), "api_key")
}

func TestNewManifestClientHasTimeout(t *testing.T) {
	m, err := NewManifest(manifestDef("https://x.test/mods.yaml"))
	require.NoError(t, err)
	require.NotNil(t, m.httpClient)
	assert.Equal(t, manifestFetchTimeout, m.httpClient.Timeout)
}
```

Add `"sync"` to the test file's imports (`time`, `net/http`, `httptest` already present).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/source/custom/ -run 'TestManifestFetchReturnsDefensiveCopy|TestManifestFetchConcurrent|TestManifestFetchDoesNotBlockOnHungServer|TestNewManifestClientHasTimeout' -race -v`
Expected: FAIL — `undefined: manifestFetchTimeout`; the defensive-copy test fails (mutations visible on refetch); the hung-server test hangs briefly then fails on `http.DefaultClient` having no timeout (it will actually hang until test timeout — that IS the failure).

- [ ] **Step 3: Implement**

In `internal/source/custom/manifest.go`:

1. Add the timeout constant next to the existing constants:

```go
// manifestFetchTimeout bounds a remote manifest fetch. Without it a hung
// server would block the fetching goroutine indefinitely (and, before the
// lock rework, every other operation on this source).
const manifestFetchTimeout = 30 * time.Second
```

2. In `NewManifest`, replace `httpClient: http.DefaultClient,` with:

```go
		httpClient: &http.Client{Timeout: manifestFetchTimeout},
```

3. Replace the remote branch of `fetch` so the lock never spans the network. Current shape (lock → TTL check → fetchRemote → store → unlock) becomes:

```go
	m.mu.Lock()
	if m.cached != nil && m.now().Sub(m.fetchedAt) < m.refresh {
		doc := m.cached
		m.mu.Unlock()
		return deepCopyManifest(doc), nil
	}
	m.mu.Unlock()

	// Network I/O happens outside the lock. Two goroutines racing past the
	// TTL check may both fetch — an acceptable, idempotent duplication that
	// keeps a slow server from blocking readers of the cached copy.
	doc, err := m.fetchRemote(ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cached = doc
	m.fetchedAt = m.now()
	m.mu.Unlock()
	return deepCopyManifest(doc), nil
```

Also make the local branch return a copy for symmetry — it re-reads the file each time, so it already returns a fresh doc; leave it as-is but add a comment noting parse output is caller-owned. Remove the now-stale doc comment sentence about the lock covering fetchRemote (update the `fetch` doc comment to describe the new semantics).

4. Add the deep copy helper (manifestDoc contains only value types, strings, and slices — copy every slice):

```go
// deepCopyManifest returns a copy of doc that shares no mutable memory with
// the cached original, so callers can never corrupt the cache between TTL
// refreshes.
func deepCopyManifest(doc *manifestDoc) *manifestDoc {
	out := &manifestDoc{Version: doc.Version, Mods: make([]manifestMod, len(doc.Mods))}
	for i, m := range doc.Mods {
		cm := m // struct copy; now fix up slice fields
		cm.GameIDs = append([]string(nil), m.GameIDs...)
		cm.Dependencies = append([]string(nil), m.Dependencies...)
		cm.Files = append([]manifestFile(nil), m.Files...)
		out.Mods[i] = cm
	}
	return out
}
```

Note: `findMod` (used by GetMod/GetModFiles/GetDownloadURL/GetDependencies) calls `fetch` and takes `&doc.Mods[i]` of the returned copy — with copies this is now always safe by construction; no change needed there.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/custom/ -race -v`
Expected: PASS (new tests + entire existing manifest suite under -race).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/custom/manifest.go internal/source/custom/manifest_test.go
git commit -m "fix(source): bound manifest fetches with a timeout and stop locking across the network"
```

---

## Task 2: Updater translates installed GameIDs to source-mapped values

**Files:**
- Modify: `internal/core/updater.go` (`CheckUpdates` signature + translation), `cmd/lmm/update.go:140` and `cmd/lmm/update.go:285` (call sites)
- Test: `internal/core/updater_test.go` (append; read the file first for its mock conventions)

**Interfaces:**
- Consumes: `domain.Game.SourceIDs map[string]string`.
- Produces: `(u *Updater) CheckUpdates(ctx context.Context, game *domain.Game, installed []domain.InstalledMod) ([]domain.Update, error)` — before calling each source, mods in that source's group get `GameID` set to `game.SourceIDs[sourceID]` when that mapping is non-empty (copies only; caller slices untouched). `game == nil` skips translation (defensive; both real call sites pass a game).

Background: installed rows persist the lmm `game.ID` (the Phase 3 fix). NexusMods' `CheckUpdates` uses `inst.GameID` as its API game domain, so a game whose mapping differs (e.g. lmm id `skyrim-se`, nexus domain `skyrimspecialedition`) needs translation at the boundary — exactly mirroring what `Service.SearchMods`/`GetMod` do on the query side.

- [ ] **Step 1: Write the failing test**

Append to `internal/core/updater_test.go` (adapt the mock to the file's existing mock-source pattern — read it first; the assertions stay):

```go
// gameIDCapturingSource records the GameIDs it receives in CheckUpdates.
type gameIDCapturingSource struct {
	source.ModSource // embed an existing test mock for the other methods
	id       string
	received []string
}

func (g *gameIDCapturingSource) ID() string { return g.id }
func (g *gameIDCapturingSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, inst := range installed {
		g.received = append(g.received, inst.GameID)
	}
	return nil, nil
}

func TestCheckUpdatesTranslatesGameIDPerSourceMapping(t *testing.T) {
	reg := source.NewRegistry()
	mapped := &gameIDCapturingSource{id: "nexusmods"}
	unmapped := &gameIDCapturingSource{id: "my-repo"}
	reg.Register(mapped)
	reg.Register(unmapped)
	u := NewUpdater(reg)

	game := &domain.Game{
		ID: "skyrim-se",
		SourceIDs: map[string]string{
			"nexusmods": "skyrimspecialedition",
			"my-repo":   "", // empty mapping: keep the lmm game id
		},
	}
	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "a", SourceID: "nexusmods", GameID: "skyrim-se", Version: "1.0"}},
		{Mod: domain.Mod{ID: "b", SourceID: "my-repo", GameID: "skyrim-se", Version: "1.0"}},
	}

	_, err := u.CheckUpdates(context.Background(), game, installed)
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrimspecialedition"}, mapped.received)
	assert.Equal(t, []string{"skyrim-se"}, unmapped.received)
	// Caller's slice must be untouched.
	assert.Equal(t, "skyrim-se", installed[0].GameID)
}
```

Notes for the implementer: check how existing updater tests construct mocks (there is likely a `mockSource` implementing `source.ModSource` — embed or copy its pattern rather than embedding the interface if that reads better in context). If `source.NewRegistry()`/`Register` have different names, match reality.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestCheckUpdatesTranslatesGameID -v`
Expected: FAIL — compile error (`CheckUpdates` takes 2 args today).

- [ ] **Step 3: Implement**

In `internal/core/updater.go`, change the signature and add translation inside the per-source loop, right before `src.CheckUpdates(ctx, mods)`:

```go
// CheckUpdates checks for available updates for installed mods. game supplies
// the source-ID mapping: installed rows persist the lmm game ID, but sources
// like NexusMods address games by their own domain, so each source's batch is
// translated via game.SourceIDs before the call (empty mapping = keep the lmm
// id, matching the search-side semantics in Service.SearchMods/GetMod).
func (u *Updater) CheckUpdates(ctx context.Context, game *domain.Game, installed []domain.InstalledMod) ([]domain.Update, error) {
```

Inside the loop, before `src.CheckUpdates(ctx, mods)`:

```go
		if game != nil {
			if mappedID, ok := game.SourceIDs[sourceID]; ok && mappedID != "" {
				translated := make([]domain.InstalledMod, len(mods))
				copy(translated, mods)
				for i := range translated {
					translated[i].GameID = mappedID
				}
				mods = translated
			}
		}
```

Update both call sites in `cmd/lmm/update.go` (`game` is already in scope at both):

```go
	updates, err := updater.CheckUpdates(ctx, game, installed)
```

and

```go
	updates, err := updater.CheckUpdates(ctx, game, []domain.InstalledMod{*mod})
```

Fix any other compile-breaking callers found by `go build ./...` (tests included — update existing updater tests to pass a game or nil, preserving their assertions).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ ./cmd/lmm/ -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/core/updater.go internal/core/updater_test.go cmd/lmm/update.go
git commit -m "fix(core): translate installed GameIDs to source-mapped values in update checks"
```

---

## Task 3: Same-origin guard for header-mode download keys

**Files:**
- Modify: `internal/source/source.go` (`DownloadHeaderProvider`), `internal/source/custom/manifest.go` (`DownloadHeaders`), `internal/core/service.go` (call site)
- Test: `internal/source/custom/manifest_test.go` (modify `TestManifestDownloadHeaders`)

**Interfaces:**
- Produces: `DownloadHeaderProvider` becomes `DownloadHeaders(fileURL string) map[string]string`. Manifest semantics: header-mode auth with a key attaches ONLY when the file URL's host equals the manifest URL's host (remote manifests); local-path manifests are user-authored and trusted — headers attach to any host. Query-mode/no-key behavior unchanged (nil).

- [ ] **Step 1: Update the tests (failing first)**

Replace `TestManifestDownloadHeaders` in `internal/source/custom/manifest_test.go`:

```go
func TestManifestDownloadHeaders(t *testing.T) {
	headerAuth := &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}

	t.Run("remote manifest: same-origin file URL gets the key", func(t *testing.T) {
		def := manifestDef("https://repo.test/mods.yaml")
		def.Manifest.Auth = headerAuth
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Equal(t, map[string]string{"X-API-Key": "sekrit"}, m.DownloadHeaders("https://repo.test/files/a.zip"))
	})

	t.Run("remote manifest: cross-origin file URL gets nothing", func(t *testing.T) {
		def := manifestDef("https://repo.test/mods.yaml")
		def.Manifest.Auth = headerAuth
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Nil(t, m.DownloadHeaders("https://cdn.example.com/files/a.zip"),
			"the repo's API key must not be shipped to third-party hosts")
	})

	t.Run("local manifest: any host gets the key (user-authored, trusted)", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mods.yaml")
		require.NoError(t, os.WriteFile(path, []byte(testManifest), 0644))
		def := manifestDef(path)
		def.Manifest.Auth = headerAuth
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Equal(t, map[string]string{"X-API-Key": "sekrit"}, m.DownloadHeaders("https://anywhere.test/a.zip"))
	})

	t.Run("query auth or no key yields nil", func(t *testing.T) {
		def := manifestDef("https://repo.test/mods.yaml")
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Nil(t, m.DownloadHeaders("https://repo.test/a.zip"))

		noKey, err := NewManifest(manifestDef("https://repo.test/mods.yaml"))
		require.NoError(t, err)
		assert.Nil(t, noKey.DownloadHeaders("https://repo.test/a.zip"))
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/custom/ -run TestManifestDownloadHeaders -v`
Expected: FAIL — compile error (DownloadHeaders takes no argument today).

- [ ] **Step 3: Implement**

`internal/source/source.go` — update the interface and doc:

```go
// DownloadHeaderProvider is implemented by sources whose file downloads need
// extra HTTP headers (e.g. header-mode API-key auth on a manifest source).
// Service.DownloadModToCache consults it with the resolved download URL so
// the source can scope credentials (e.g. same-origin only). A nil map means
// no extra headers.
type DownloadHeaderProvider interface {
	DownloadHeaders(fileURL string) map[string]string
}
```

`internal/source/custom/manifest.go` — replace `DownloadHeaders`:

```go
// DownloadHeaders implements source.DownloadHeaderProvider. Header-mode auth
// applies the same key to file downloads as to manifest fetches (design §6),
// but for remote manifests only when the file URL is same-origin with the
// manifest — a manifest pointing files at a third-party CDN must not ship the
// repo's key there. Local-path manifests are user-authored and trusted, so
// their configured key attaches regardless of host.
func (m *Manifest) DownloadHeaders(fileURL string) map[string]string {
	if m.auth == nil || m.auth.APIKey.In != "header" || m.apiKey == "" {
		return nil
	}
	if m.isRemote {
		fu, err := url.Parse(fileURL)
		if err != nil {
			return nil
		}
		mu, err := url.Parse(m.url)
		if err != nil {
			return nil
		}
		if fu.Host != mu.Host {
			return nil
		}
	}
	return map[string]string{m.auth.APIKey.Name: m.apiKey}
}
```

`internal/core/service.go` — the call site in `DownloadModToCache` passes the resolved URL:

```go
	if hp, ok := src.(source.DownloadHeaderProvider); ok {
		headers = hp.DownloadHeaders(url)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/custom/ ./internal/core/ -v && go build ./...`
Expected: PASS (the Task-8-era e2e/auth tests still pass — their file URLs are same-origin with their httptest manifests; verify, and if any e2e used a second host, adapt per the new semantics with a comment).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/source/source.go internal/source/custom/manifest.go internal/source/custom/manifest_test.go internal/core/service.go
git commit -m "fix(security): scope header-mode download keys to same-origin file URLs"
```

---

## Task 4: Single-pass download hashing

**Files:**
- Modify: `internal/core/downloader.go` (`DownloadResult`, `downloadOnce`), `internal/core/service.go` (verification uses the result; delete `verifyFileSHA256`)
- Test: `internal/core/downloader_test.go` (append), `internal/core/service_sha256_test.go` (unchanged — it pins the behavior contract)

**Interfaces:**
- Produces: `DownloadResult.SHA256 string` (hex, lowercase) computed in the same streaming pass as the existing MD5 `Checksum`. `DownloadModToCache` compares `file.SHA256` against `downloadResult.SHA256` with `strings.EqualFold` and keeps the exact error text `sha256 mismatch: source declares %s, downloaded file is %s`. `verifyFileSHA256` is deleted (no remaining callers).

- [ ] **Step 1: Write the failing test**

Append to `internal/core/downloader_test.go`:

```go
func TestDownloadComputesSHA256(t *testing.T) {
	content := []byte("payload for hashing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	d := NewDownloader(nil)
	dest := filepath.Join(t.TempDir(), "out.bin")
	result, err := d.Download(context.Background(), srv.URL, dest, nil)
	require.NoError(t, err)

	sum := sha256.Sum256(content)
	assert.Equal(t, hex.EncodeToString(sum[:]), result.SHA256)
	assert.NotEmpty(t, result.Checksum) // MD5 still present
}
```

Add `"crypto/sha256"` and `"encoding/hex"` to the test imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestDownloadComputesSHA256 -v`
Expected: FAIL — `result.SHA256 undefined`.

- [ ] **Step 3: Implement**

`internal/core/downloader.go`:

1. Extend the result type:

```go
// DownloadResult contains the outcome of a download
type DownloadResult struct {
	Path     string // Final file path
	Size     int64  // Bytes downloaded
	Checksum string // MD5 hash of downloaded file (recorded in the DB)
	SHA256   string // SHA-256 of downloaded file (compared against source-declared checksums)
}
```

2. In `downloadOnce`, hash both digests in the one existing pass (add `"crypto/sha256"` import):

```go
	md5Hasher := md5.New()
	shaHasher := sha256.New()
	reader := &progressReader{
		reader:     resp.Body,
		totalBytes: totalBytes,
		progressFn: progressFn,
	}
	teeReader := io.TeeReader(reader, io.MultiWriter(md5Hasher, shaHasher))
```

and in the return:

```go
	return &DownloadResult{
		Path:     destPath,
		Size:     written,
		Checksum: hex.EncodeToString(md5Hasher.Sum(nil)),
		SHA256:   hex.EncodeToString(shaHasher.Sum(nil)),
	}, nil
```

(Rename the existing `hasher` variable to `md5Hasher` throughout the function.)

3. `internal/core/service.go` — in `DownloadModToCache`, replace the post-download verification block:

```go
	if file.SHA256 != "" && !strings.EqualFold(downloadResult.SHA256, file.SHA256) {
		return nil, fmt.Errorf("verifying download of %s: sha256 mismatch: source declares %s, downloaded file is %s",
			file.FileName, file.SHA256, downloadResult.SHA256)
	}
```

Delete `verifyFileSHA256` and drop now-unused imports (`crypto/sha256`, `encoding/hex` — verify with `go build`; `strings`/`io`/`os` are used elsewhere).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: PASS — including the untouched `service_sha256_test.go` (all four subtests: match, uppercase match via EqualFold, mismatch text intact, empty-skip) and the manifest e2e corrupt-download test.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add internal/core/downloader.go internal/core/downloader_test.go internal/core/service.go
git commit -m "perf(core): compute download sha256 in the existing streaming pass"
```

---

## Task 5: `lmm auth status` covers custom sources

**Files:**
- Modify: `cmd/lmm/auth.go` (`doAuthStatus`)
- Test: `cmd/lmm/auth_status_test.go` (create)

**Interfaces:**
- Consumes: `service.ListSources()`, `source.CapabilitiesOf`, `service.GetSourceToken`, `envKeyForSourceID`, `maskAPIKey`, `isSupportedSource` (all existing).
- Produces: after the built-ins loop, `doAuthStatus` also reports every registered non-builtin source whose `Capabilities().Auth` is true: `authenticated (key: ab…yz)` from the token store, `authenticated via LMM_<ID>_API_KEY (key: …)` from the env, else `not authenticated (run: lmm auth login <id>)`. Non-auth custom sources (directory, auth-less manifest) are not listed.

- [ ] **Step 1: Write the failing test**

Create `cmd/lmm/auth_status_test.go`. `doAuthStatus` prints to stdout via `fmt.Printf` — capture it the way existing cmd tests capture stdout if a helper exists (grep for `os.Pipe` in cmd/lmm tests); otherwise use this pattern:

```go
package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()
	require.NoError(t, fn())
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestDoAuthStatusIncludesCustomSources(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// Auth-capable manifest source, key provided via env.
	withAuth, err := custom.NewManifest(custom.SourceDefinition{
		ID: "my-repo", Name: "My Repo", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://repo.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(withAuth)

	// Auth-capable manifest source with no key anywhere.
	noKey, err := custom.NewManifest(custom.SourceDefinition{
		ID: "keyless-repo", Name: "Keyless", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{
			URL:  "https://other.test/mods.yaml",
			Auth: &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
		},
	})
	require.NoError(t, err)
	svc.RegisterSource(noKey)

	// Directory source: no auth capability, must not be listed.
	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID: "local-mods", Name: "Local", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)

	t.Setenv("LMM_MY_REPO_API_KEY", "supersecretkey")

	out := captureStdout(t, func() error { return doAuthStatus(svc) })

	assert.Contains(t, out, "my-repo: authenticated via LMM_MY_REPO_API_KEY")
	assert.NotContains(t, out, "supersecretkey")
	assert.Contains(t, out, "keyless-repo: not authenticated")
	assert.NotContains(t, out, "local-mods")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/lmm/ -run TestDoAuthStatusIncludesCustomSources -v`
Expected: FAIL — output contains neither custom source.

- [ ] **Step 3: Implement**

In `cmd/lmm/auth.go`, at the end of `doAuthStatus` (after the built-ins loop, before the final `return nil`), append:

```go
	// Custom sources that declare auth get the same treatment as built-ins.
	for _, src := range service.ListSources() {
		id := src.ID()
		if isSupportedSource(id) {
			continue // already reported above
		}
		if !source.CapabilitiesOf(src).Auth {
			continue // directory sources, auth-less manifests: nothing to report
		}

		token, err := service.GetSourceToken(id)
		if err != nil {
			return fmt.Errorf("checking %s: %w", id, err)
		}
		if token != nil {
			fmt.Printf("%s: authenticated (key: %s)\n", id, maskAPIKey(token.APIKey))
			continue
		}
		envKey := envKeyForSourceID(id)
		if apiKey := os.Getenv(envKey); apiKey != "" {
			fmt.Printf("%s: authenticated via %s (key: %s)\n", id, envKey, maskAPIKey(apiKey))
			continue
		}
		fmt.Printf("%s: not authenticated (run: lmm auth login %s)\n", id, id)
	}
```

Check the `source` package import in auth.go (present since the Phase 3 gating work; verify).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/lmm/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add cmd/lmm/auth.go cmd/lmm/auth_status_test.go
git commit -m "feat(cli): report custom-source auth state in 'lmm auth status'"
```

---

## Task 6: Logout for unregistered sources + env-key test pin cleanup

**Files:**
- Modify: `cmd/lmm/auth.go` (`runAuthLogout`), `cmd/lmm/auth_test.go` (env-key test)
- Test: `cmd/lmm/auth_test.go` (append logout coverage if a service-level test is tractable; otherwise the runAuthLogout unit shape below)

**Interfaces:**
- Produces: `lmm auth logout <id>` succeeds for ANY source ID that has a stored token — even if the definition file was deleted and the source is no longer registered. An ID with no stored token and no auth capability still errors. `TestGetEnvKeyForSource` no longer pins `LMM__API_KEY` for empty IDs.

- [ ] **Step 1: Write the failing test**

Append to `cmd/lmm/auth_test.go`:

```go
func TestResolveLogoutSource(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// A token stored for a source whose definition file has been deleted:
	// not registered, but logout must still be able to remove it.
	require.NoError(t, svc.SaveSourceToken("ghost-repo", "leftover-key"))

	id, err := resolveLogoutSource(svc, []string{"ghost-repo"})
	require.NoError(t, err)
	assert.Equal(t, "ghost-repo", id)

	// Unknown ID with no token and no registration still errors.
	_, err = resolveLogoutSource(svc, []string{"never-existed"})
	assert.Error(t, err)

	// Built-ins keep working.
	id, err = resolveLogoutSource(svc, []string{"nexusmods"})
	require.NoError(t, err)
	assert.Equal(t, "nexusmods", id)
}
```

And replace the empty/unknown-ID expectations in `TestGetEnvKeyForSource` (and the equivalent cases in `TestEnvKeyForSourceID` if present): drop any case asserting `LMM__API_KEY` for `""`; keep/add real-ID cases only (`nexusmods` → `NEXUSMODS_API_KEY`, `curseforge` → `CURSEFORGE_API_KEY`, `my-repo` → `LMM_MY_REPO_API_KEY`). Note: `getEnvKeyForSource("unknown-source")` → `LMM_UNKNOWN_SOURCE_API_KEY` is real derivation behavior for custom sources — keep such a case; only the empty-ID pin goes.

(Check `svc.SaveSourceToken`'s real name/signature — grep `SaveSourceToken` in internal/core; adapt the call, not the behavior.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/lmm/ -run TestResolveLogoutSource -v`
Expected: FAIL — `undefined: resolveLogoutSource`.

- [ ] **Step 3: Implement**

In `cmd/lmm/auth.go`, add the resolver and use it in `runAuthLogout`:

```go
// resolveLogoutSource picks the source to log out. Unlike login, logout must
// also work for sources that are no longer registered (definition file
// deleted after a key was stored) — otherwise the stored token becomes
// unremovable via the CLI.
func resolveLogoutSource(service *core.Service, args []string) (string, error) {
	if len(args) == 0 {
		return selectAuthSource(service, args) // interactive prompt path unchanged
	}
	sourceID := args[0]
	if isAuthCapableSource(service, sourceID) {
		return sourceID, nil
	}
	token, err := service.GetSourceToken(sourceID)
	if err != nil {
		return "", fmt.Errorf("checking stored credentials for %s: %w", sourceID, err)
	}
	if token != nil {
		return sourceID, nil
	}
	return "", fmt.Errorf("no stored credentials for %q and it is not a registered auth-capable source", sourceID)
}
```

In `runAuthLogout`, replace `selectAuthSource(service, args)` with `resolveLogoutSource(service, args)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/lmm/ -v`
Expected: PASS (including the updated env-key expectations).

- [ ] **Step 5: Commit**

```bash
go fmt ./... && go vet ./...
git add cmd/lmm/auth.go cmd/lmm/auth_test.go
git commit -m "fix(cli): allow auth logout for unregistered sources with stored tokens"
```

---

## Task 7: Docs, changelog, version 1.8.0

**Files:**
- Modify: `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go` (version)

- [ ] **Step 1: Update docs (accuracy rule: every claim must match the code on this branch — verify before writing)**

README.md:
- Authentication subsection (Custom Sources): add the same-origin rule — for remote manifests, header-mode keys are sent only to file URLs on the manifest's own host; local-path manifests attach the key to any host (user-authored). Note `lmm auth status` now lists auth-capable custom sources, and `lmm auth logout <id>` works even after the definition file is removed.
- Manifest Sources subsection: one sentence noting remote fetches are bounded by a 30s timeout.

CHANGELOG.md — under `[Unreleased]`:

```markdown
### Added
- `lmm auth status` reports auth-capable custom sources (stored token or env var, masked)

### Fixed
- `lmm auth logout` works for sources whose definition file was removed
- Update checks translate installed mods to each source's mapped game ID (fixes NexusMods update checks for games whose lmm ID differs from the Nexus domain)
- Remote manifest fetches are bounded by a 30-second timeout and no longer block other operations on the same source

### Security
- Header-mode API keys are only sent to file downloads on the manifest's own host

### Changed
- Download checksums (MD5 + SHA-256) are computed in a single streaming pass
```

Then move `[Unreleased]` content to `## [1.8.0] - <today>`, update comparison links per the file's convention, and set `version = "1.8.0"` in cmd/lmm/root.go.

- [ ] **Step 2: Final verification and commits**

```bash
go fmt ./... && go vet ./... && go test ./... -race
git add README.md CHANGELOG.md
git commit -m "docs: document same-origin download keys, auth status coverage, and fetch timeout"
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 1.8.0"
```

(Note the `-race` run here — Task 1's concurrency work makes this the cheap moment to catch races across the whole suite.)

---

## Out of Scope

- `#50` aggregate search itself (this plan clears its prerequisite)
- `#49` api source type
- Issue #52 items (directory-source polish from Phase 2)
