# #96 Version→File Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Map a version string to downloadable files (the missing primitive for version locking), make `ModReference.Version` authoritative in every deploy-shaped flow (converging installed mods to the recorded version, downgrades included), make `lmm install --version` real (closes #93), and reconcile the update-apply path's version recording with the #94 invariant.

**Architecture:** One PR (branch `feat/96-version-resolution`). A new exported pure resolver in `internal/core` filters a source's raw (unfiltered — archived included) file list by exact version match. A `Versions` capability field advertises per-source support; the resolver itself degrades dynamically (a file list with no version info → `source.ErrNotSupported`). Deploy-shaped flows switch from `selectDeployFiles` to a version-aware wrapper that keeps the stored-FileIDs fast path, heals stored-IDs-gone drift **only to the same recorded version** (never latest — #95's rule is preserved and extended), and hard-errors naming the version otherwise. The two profile diff computations (`PlanProfileSwitch`, `doProfileApply`) gain a version-drift case that schedules reinstall at the profile's version — this is the convergence/downgrade mechanism. `ApplyUpdate` starts stamping `EffectiveInstalledVersion` like every other recording flow, closing the verify↔update mismatch noted on #94.

**Tech Stack:** Go, cobra, modernc.org/sqlite (`:memory:` in tests), testify, `t.TempDir()` for cache dirs, existing core test helpers (`newFlowsTestService`, `mockSourceWithDownloads`, `registerDownloadableMod`, `seedInstalledMod`, `seedProfileWithMod`).

## Design decisions locked for this PR

Settled with the user (2026-07-29) in `docs/plans/2026-07-29-lock-vs-pinned-design.md` plus this planning round:

1. **`ModReference.Version` is a *record*, not a lock marker.** Since #94, every install stamps the actual installed version into the profile ref — so "version present = locked" is impossible. Deploy reproduces the record; `lmm update` moves the record. The explicit `locked:` marker (and the `lmm update <locked-id>` refusal, the `Locked` UpdateSkips category, and lock-aware `verify`) are **#97**, not this PR. Task 8 records this as an addendum on the design note.
2. **Convergence is unconditional where drift exists.** A profile saying `1.2.3` while the DB row says `1.5` means deploy/apply/switch moves the mod to `1.2.3` — hand-edited YAML and imported profiles are exactly the intended triggers. `version: ""` (legacy refs) preserves today's behavior byte-for-byte.
3. **Exact string matching only.** No semver awareness. `DownloadableFile.Version` is compared with `==`.
4. **Version-less file lists pass vacuously** (the #130 precedent): if no file in the list carries a non-empty `Version`, every version-aware path falls back to today's FileIDs-based behavior. CurseForge's regex-extracted versions count as versions (best-effort, declared capability).
5. **Healing is bounded to the recorded version.** When stored FileIDs are gone upstream, we may substitute files whose `Version` exactly equals the recorded version — never anything else. If that fails too, the #95 hard error fires, now naming the version.
6. **Dependencies install at latest.** `--version` applies to the named mod only; its dependency chain resolves as today.
7. **`PlanInstall` keeps its signature.** The CLI resolves `--version` itself and overrides `plan.Files` before `ApplyInstall` — the documented contract ("a CLI/TUI caller that resolves a different selection overrides plan.Files", `internal/core/flows.go:2113-2119`). This avoids 48 mechanical test-site edits for zero behavior.
8. **TUI surface deferred to #97.** The TUI has no file/version choice anywhere today; the version picker is #97's deliverable. Core parity is preserved because everything behavioral lands in `internal/core` — the TUI's profile/switch/import flows inherit convergence automatically in this PR.

## Global Constraints

- TDD strictly: every behavioral task starts with a failing test, run to observe the failure, then implement, then re-run to green. (`~/.claude/CLAUDE.md`)
- NEVER pipe `go test` output inside a `&&` chain (`go test ... | tail` masks failures). Run bare or capture `$?` explicitly. (project memory)
- `git add` files BY NAME — never `git add -A` (untracked `IDEAS.md` must stay untracked). (project memory)
- gofmt formatting (tabs); `go vet ./...` clean before each commit.
- Error wrapping with `%w`; sentinels follow the existing pattern (`internal/core/flows.go:923-937`: unexported var, doc comment, declared above its function). New *exported* sentinel `ErrVersionNotFound` gets a full doc comment.
- Byte-fidelity caution: `internal/core/flows.go` and `cmd/lmm/profile.go` pin user-facing strings via tests; when changing a message, update the pinning test in the same task, never "approximately". The cmd/core twin helpers (`filterAndSortFiles`/`filterAndSortInstallFiles`, `selectFilesToDownload`/`selectDeployFiles`) must stay byte-identical in wording — the duplication is deliberate (`cmd/lmm` is package main; `internal/tui` cannot import it either — see the CANONICAL NOTE at `internal/tui/service_core.go:254-261`).
- `internal/core` tests are package `core_test` — unexported helpers are not directly testable; anything needing direct unit tests must be exported.
- Version bump (MINOR → **1.25.0**) + CHANGELOG as the final task; `make man` must be run in the same task (the genman drift test fails otherwise — project memory). Tag on the MERGE COMMIT after merge.
- PR body: `Closes #96` and `Closes #93`, standard Claude Code attribution. Copilot triage rounds after push; read suppressed low-confidence findings too (7 of 8 were real on PR #128).
- Archive this plan doc into `docs/plans/archive/` IN this PR (repo convention).

## File Structure

| File | Role in this plan |
|---|---|
| `internal/core/resolve.go` (new) | `ErrVersionNotFound`, `ResolveVersionFiles`, `availableVersions`, `anyFileHasVersion` (T1) |
| `internal/core/resolve_test.go` (new) | Pure resolver tests (T1) |
| `internal/source/source.go` | `Capabilities.Versions` field + fallback literal (T2) |
| `internal/source/{nexusmods,curseforge}/…`, `internal/source/custom/{directory,manifest,api}.go` | Per-source `Versions` declarations (T2) |
| `cmd/lmm/source.go`, `internal/tui/service_core.go` | `"versions"` token in both capability summaries (T2) |
| `internal/core/service.go` | `Service.ResolveModVersion` (T3) |
| `cmd/lmm/install.go` | Guard removed, `--version` wired, flag help updated (T4) |
| `cmd/lmm/install_test.go` | Guard test replaced with behavior tests (T4) |
| `internal/core/flows.go` | `selectVersionedDeployFiles` + wiring at :1329/:1923/:3824 (T5); `PlanProfileSwitch` drift case + switch-loop cache-first (T6); `ApplyUpdate` stamp (T7) |
| `internal/core/flows_test.go`, `flows_install_test.go`, `flows_update_test.go`, `flows_import_test.go` | Core flow tests (T5–T7) |
| `cmd/lmm/profile.go` | `selectFilesToDownload` version param (T5); `doProfileApply` drift case (T6) |
| `cmd/lmm/profile_test.go` | cmd twin tests (T5–T6) |
| `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go`, `docs/man/**`, `docs/plans/2026-07-29-lock-vs-pinned-design.md` | Docs, version, man regen, design-note addendum (T8) |

---

### Task 1: Pure resolver — `core.ResolveVersionFiles`

**Files:**
- Create: `internal/core/resolve.go`
- Test: `internal/core/resolve_test.go`

**Interfaces:**
- Produces: `var ErrVersionNotFound = errors.New("version not found")` (exported, in `internal/core`); `func ResolveVersionFiles(sourceID string, files []domain.DownloadableFile, version string) ([]domain.DownloadableFile, error)`; unexported `availableVersions(files []domain.DownloadableFile) []string` and `anyFileHasVersion(files []domain.DownloadableFile) bool` (consumed by T5).
- Consumes: `domain.DownloadableFile` (`internal/domain/mod.go:37-48`), `source.ErrNotSupported` (`internal/source/source.go:59-62`), `installFileCategoryPriority` (`internal/core/flows.go:969-984`).

Semantics: operates on the **raw** `GetModFiles` list — archived/old/deleted files are *in scope by design* (a version pin usually targets an archived file). Returns ALL exact matches, category-sorted (MAIN first), so callers can sub-select (`--file`, primary heuristic).

- [ ] **Step 1: Write the failing tests** in `internal/core/resolve_test.go` (package `core_test`):

```go
package core_test

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveVersionFiles(t *testing.T) {
	f := func(id, version, category string, primary bool) domain.DownloadableFile {
		return domain.DownloadableFile{ID: id, Version: version, Category: category, IsPrimary: primary}
	}

	tests := []struct {
		name    string
		files   []domain.DownloadableFile
		version string
		wantIDs []string
		wantErr error // sentinel matched with errors.Is; nil = success
	}{
		{
			name:    "exact match returns the matching file",
			files:   []domain.DownloadableFile{f("10", "1.5", "MAIN", true), f("9", "1.0", "OLD_VERSION", false)},
			version: "1.0",
			wantIDs: []string{"9"},
		},
		{
			name:    "archived files are eligible - no filtering",
			files:   []domain.DownloadableFile{f("10", "1.5", "MAIN", true), f("9", "1.0", "ARCHIVED", false)},
			version: "1.0",
			wantIDs: []string{"9"},
		},
		{
			name: "multiple files of one version all returned, category-sorted MAIN first",
			files: []domain.DownloadableFile{
				f("11", "1.0", "OPTIONAL", false),
				f("10", "1.0", "MAIN", true),
				f("12", "1.5", "MAIN", false),
			},
			version: "1.0",
			wantIDs: []string{"10", "11"},
		},
		{
			name:    "no match is ErrVersionNotFound",
			files:   []domain.DownloadableFile{f("10", "1.5", "MAIN", true), f("9", "1.0", "MAIN", false)},
			version: "2.0",
			wantErr: core.ErrVersionNotFound,
		},
		{
			name:    "version-less list is ErrNotSupported",
			files:   []domain.DownloadableFile{f("main", "", "", true)},
			version: "1.0",
			wantErr: source.ErrNotSupported,
		},
		{
			name:    "empty list is ErrNotSupported",
			files:   nil,
			version: "1.0",
			wantErr: source.ErrNotSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ResolveVersionFiles("src", tt.files, tt.version)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			ids := make([]string, len(got))
			for i, g := range got {
				ids[i] = g.ID
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestResolveVersionFiles_NotFoundListsAvailableVersions(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "10", Version: "1.5"},
		{ID: "9", Version: "1.0"},
		{ID: "8", Version: "1.5"}, // duplicate version - listed once
		{ID: "7"},                 // version-less file - not listed
	}
	_, err := core.ResolveVersionFiles("src", files, "2.0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrVersionNotFound))
	assert.Contains(t, err.Error(), `version "2.0"`)
	assert.Contains(t, err.Error(), "available: 1.5, 1.0")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/core/ -run 'TestResolveVersionFiles' -v`
Expected: FAIL — `undefined: core.ResolveVersionFiles` / `core.ErrVersionNotFound`.

- [ ] **Step 3: Implement** `internal/core/resolve.go`:

```go
package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// ErrVersionNotFound reports that version->file resolution ran against a
// source that does carry per-file version info, but no file matched the
// requested version exactly. Callers branch with errors.Is; the message
// names the requested version and the distinct versions that ARE available.
var ErrVersionNotFound = errors.New("version not found")

// ResolveVersionFiles selects the files whose Version exactly matches
// version, from a source's raw (unfiltered) file list - archived/old/deleted
// files are eligible by design, since a version pin usually targets one
// (#96). Matches are returned category-sorted (MAIN first, mirroring
// filterAndSortInstallFiles' ordering) so callers can apply their own
// sub-selection (--file, the primary heuristic).
//
// Degradation is dynamic rather than capability-driven: a list in which no
// file carries a non-empty Version cannot resolve any version, and returns
// source.ErrNotSupported wrapped with the sourceID - the same contract as a
// source that lacks the operation entirely (#130's vacuous-version
// precedent: no version info means nothing to compare, not a mismatch).
func ResolveVersionFiles(sourceID string, files []domain.DownloadableFile, version string) ([]domain.DownloadableFile, error) {
	if !anyFileHasVersion(files) {
		return nil, fmt.Errorf("source %q: version resolution: %w", sourceID, source.ErrNotSupported)
	}
	var matches []domain.DownloadableFile
	for _, f := range files {
		if f.Version == version {
			matches = append(matches, f)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: version %q (available: %s)", ErrVersionNotFound, version, strings.Join(availableVersions(files), ", "))
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return installFileCategoryPriority(matches[i].Category) < installFileCategoryPriority(matches[j].Category)
	})
	return matches, nil
}

// availableVersions returns the distinct non-empty versions in files, in
// first-seen order - display material for ErrVersionNotFound.
func availableVersions(files []domain.DownloadableFile) []string {
	seen := make(map[string]bool, len(files))
	var out []string
	for _, f := range files {
		if f.Version == "" || seen[f.Version] {
			continue
		}
		seen[f.Version] = true
		out = append(out, f.Version)
	}
	return out
}

// anyFileHasVersion reports whether at least one file carries version info -
// the gate between version-aware and legacy (FileIDs-only) behavior.
func anyFileHasVersion(files []domain.DownloadableFile) bool {
	for _, f := range files {
		if f.Version != "" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/core/ -run 'TestResolveVersionFiles' -v` → PASS. Then `go vet ./internal/core/`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/resolve.go internal/core/resolve_test.go
git commit -m "feat(core): ResolveVersionFiles - exact version->file resolution (#96)"
```

---

### Task 2: `Versions` capability

**Files:**
- Modify: `internal/source/source.go:64-128` (struct + `CapabilitiesOf` fallback literal)
- Modify: `internal/source/nexusmods/nexusmods.go:86`, `internal/source/curseforge/curseforge.go:115`, `internal/source/custom/manifest.go:252`, `internal/source/custom/directory.go:65`, `internal/source/custom/api.go:91-98`
- Modify: `cmd/lmm/source.go:333-350` (`capabilitySummary`), `internal/tui/service_core.go:277-294` (`sourceCapabilitySummary`)
- Test: `internal/source/custom/api_test.go` (or the file holding existing `Capabilities` tests per source), `cmd/lmm/source_test.go`

**Interfaces:**
- Produces: `Capabilities.Versions bool` — true = the source's `GetModFiles` carries meaningful per-file `Version` strings that `ResolveVersionFiles` can match against. Summary token `"versions"`, appended after `"auth"` in both renderers.
- Declarations: NexusMods `true` (authoritative API field), CurseForge `true` (best-effort, regex-extracted — decision 4), manifest `true` (declared per-file; may be absent per mod, the dynamic error covers), directory `false` (single synthetic file, no history), api `a.endpoints.ModFiles != nil`.

- [ ] **Step 1: Write the failing tests.** Add to the existing per-source capability tests (locate with `grep -rn "Capabilities()" internal/source --include=*_test.go`); the api case is the non-trivial one:

```go
func TestAPICapabilities_VersionsFollowsModFilesEndpoint(t *testing.T) {
	withFiles := validAPIDef(t) // reuse the file's existing valid-definition helper; adapt name to what exists
	withFiles.API.Endpoints.ModFiles = &custom.EndpointConfig{Path: "/mods/{mod_id}/files", List: "files"}
	src, err := custom.NewAPI(withFiles)
	require.NoError(t, err)
	assert.True(t, src.Capabilities().Versions)

	without := validAPIDef(t)
	without.API.Endpoints.ModFiles = nil
	src2, err := custom.NewAPI(without)
	require.NoError(t, err)
	assert.False(t, src2.Capabilities().Versions)
}
```

And pin the summary token in `cmd/lmm/source_test.go` (extend the existing `capabilitySummary` test if present, else add):

```go
func TestCapabilitySummary_IncludesVersions(t *testing.T) {
	assert.Equal(t, "search,deps,updates,auth,versions", capabilitySummary(source.Capabilities{
		Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true,
	}))
	assert.Equal(t, "search", capabilitySummary(source.Capabilities{Search: true}))
}
```

- [ ] **Step 2: Run to verify failure** (`go test ./internal/source/... ./cmd/lmm/ -run 'Capabilit' -v`) — compile error: unknown field `Versions`.

- [ ] **Step 3: Implement.** Add `Versions bool` to the `Capabilities` struct with doc line `// Versions: GetModFiles carries per-file Version strings usable for exact version->file resolution (#96)`. Update `CapabilitiesOf`'s fallback literal to `Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true}`. Update all five `Capabilities()` implementations per the Interfaces block above (api: add `Versions: a.endpoints.ModFiles != nil,`). Add `add(c.Versions, "versions")` after the `add(c.Auth, "auth")` line in BOTH `capabilitySummary` and `sourceCapabilitySummary` (identical bodies — keep them byte-identical).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/source/... ./cmd/lmm/ ./internal/tui/ -v` (full packages — the summary string is pinned by existing source-list tests that will need their expected strings extended; update them in this task, they are legitimate contract changes). Then `go vet ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/source/source.go internal/source/nexusmods/nexusmods.go internal/source/curseforge/curseforge.go internal/source/custom/manifest.go internal/source/custom/directory.go internal/source/custom/api.go cmd/lmm/source.go internal/tui/service_core.go
git add internal/source/custom/api_test.go cmd/lmm/source_test.go
git commit -m "feat(source): Versions capability - advertises version->file resolvability (#96)"
```

(Also `git add` any other test files whose pinned summary strings changed — by name.)

---

### Task 3: `Service.ResolveModVersion`

**Files:**
- Modify: `internal/core/service.go` (near the other source-delegating methods, e.g. `GetModFiles`)
- Test: `internal/core/resolve_test.go` (append)

**Interfaces:**
- Produces: `func (s *Service) ResolveModVersion(ctx context.Context, sourceID string, mod *domain.Mod, version string) ([]domain.DownloadableFile, error)` — fetch + delegate. Consumed by T4 (CLI) and #97 (TUI picker).
- Consumes: `s.GetModFiles(ctx, sourceID, mod)` (existing service method), `ResolveVersionFiles` (T1).

- [ ] **Step 1: Write the failing test** (append to `resolve_test.go`). Use the embedding pattern from `internal/core/flows_install_test.go:557-586`:

```go
type multiVersionSource struct{ *mockSource } // mockSource: service_test.go:20

func (s *multiVersionSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "10", Name: "Main", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Name: "Old", FileName: mod.ID + "-old.zip", Version: "1.0", Category: "ARCHIVED"},
	}, nil
}

func TestServiceResolveModVersion(t *testing.T) {
	svc := newFlowsTestService(t) // flows_test.go:40
	mock := &multiVersionSource{newMockSource("src")}
	svc.RegisterSource(mock)
	mod := &domain.Mod{ID: "mod1", SourceID: "src", GameID: "testgame", Name: "Mod One", Version: "1.5"}

	files, err := svc.ResolveModVersion(context.Background(), "src", mod, "1.0")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "9", files[0].ID)

	_, err = svc.ResolveModVersion(context.Background(), "src", mod, "9.9")
	assert.ErrorIs(t, err, core.ErrVersionNotFound)
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/core/ -run 'TestServiceResolveModVersion' -v` → `undefined: svc.ResolveModVersion`.

- [ ] **Step 3: Implement** in `internal/core/service.go`:

```go
// ResolveModVersion fetches mod's raw file list from sourceID and resolves
// version against it via ResolveVersionFiles (#96). The list is deliberately
// unfiltered - archived files are exactly what a version pin usually names.
func (s *Service) ResolveModVersion(ctx context.Context, sourceID string, mod *domain.Mod, version string) ([]domain.DownloadableFile, error) {
	files, err := s.GetModFiles(ctx, sourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("listing files for version resolution: %w", err)
	}
	return ResolveVersionFiles(sourceID, files, version)
}
```

- [ ] **Step 4: Run to verify pass**, then `go vet ./internal/core/`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/service.go internal/core/resolve_test.go
git commit -m "feat(core): Service.ResolveModVersion (#96)"
```

---

### Task 4: Real `lmm install --version` (closes #93)

**Files:**
- Modify: `cmd/lmm/install.go` — delete `installVersionGuard` (:363-378) and its call (:382-384); flag help text (:310); wire resolution into `doInstall` (:497-518)
- Test: `cmd/lmm/install_test.go` — replace `TestInstallCmd_VersionFlag_RejectedBeforeGameResolution` (:136-158); add behavior tests near `TestDoInstall_ShowArchivedFlag_ThreadsThroughRefit` (:1001)

**Interfaces:**
- Consumes: `service.ResolveModVersion` (T3), `selectInstallFiles` (`install.go:80-136`, unchanged — it operates on whatever list it is given), `plan.Files` override contract (`flows.go:2113-2119`).
- Behavior contract: `--version X` resolves X against the raw file list (archived included — `--show-archived` is irrelevant to and not required by `--version`); the resolved matches become the selection pool for `--file`/`--yes`/interactive; batch dependencies still install at latest (decision 6). Errors: unknown version → the `ErrVersionNotFound` message listing available versions; version-less source → the `ErrNotSupported` message.

- [ ] **Step 1: Write the failing tests.** Replace the guard test with (same reparenting/reset conventions as the original, `install_test.go:136-158`):

```go
func TestInstallCmd_VersionFlag_NoLongerRejected(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""
	installModID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(installCmd); rootCmd.AddCommand(installCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "test mod", "--version", "1.2.3"})

	err := cmd.Execute()
	installVersion = ""

	// The flag now proceeds past its own validation into normal game
	// resolution (which fails here because no game is configured).
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not yet supported")
}
```

And the behavior tests using `setupDoInstallTest` + `fakeInstallSource` (`install_test.go:~400-500` — register a mod with files `{ID:"10",Version:"1.5",IsPrimary:true,Category:"MAIN"}` and `{ID:"9",Version:"1.0",Category:"ARCHIVED"}`, download content for file "9"):

```go
func TestDoInstall_VersionFlag_InstallsRequestedVersion(t *testing.T) {
	svc, game, fake := setupDoInstallTest(t)
	mod := &domain.Mod{ID: "mod1", SourceID: fake.id, GameID: game.ID, Name: "Mod One", Version: "1.5"}
	fake.AddMod(mod, []domain.DownloadableFile{
		{ID: "10", Name: "Main", FileName: "mod1.zip", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Name: "Old", FileName: "mod1-old.zip", Version: "1.0", Category: "ARCHIVED"},
	})
	fake.AddDownload("9", testZipBytes(t)) // reuse the file's existing zip-fixture helper name

	installModID = "mod1"
	installVersion = "1.0"
	installYes = true
	t.Cleanup(func() { installModID, installVersion = "", ""; installYes = false })

	require.NoError(t, doInstall(context.Background(), svc, game, nil))

	im, err := svc.GetInstalledMod(fake.id, "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", im.Version, "recorded version must be the requested one (#94 invariant)")
	assert.Equal(t, []string{"9"}, im.FileIDs)
}

func TestDoInstall_VersionFlag_UnknownVersionListsAvailable(t *testing.T) {
	svc, game, fake := setupDoInstallTest(t)
	mod := &domain.Mod{ID: "mod1", SourceID: fake.id, GameID: game.ID, Name: "Mod One", Version: "1.5"}
	fake.AddMod(mod, []domain.DownloadableFile{
		{ID: "10", Version: "1.5", IsPrimary: true, Category: "MAIN", FileName: "mod1.zip", Name: "Main"},
	})

	installModID = "mod1"
	installVersion = "2.0"
	installYes = true
	t.Cleanup(func() { installModID, installVersion = "", ""; installYes = false })

	err := doInstall(context.Background(), svc, game, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "2.0"`)
	assert.Contains(t, err.Error(), "available: 1.5")
}

func TestDoInstall_VersionFlag_FileOutsideVersionErrors(t *testing.T) {
	svc, game, fake := setupDoInstallTest(t)
	mod := &domain.Mod{ID: "mod1", SourceID: fake.id, GameID: game.ID, Name: "Mod One", Version: "1.5"}
	fake.AddMod(mod, []domain.DownloadableFile{
		{ID: "10", Version: "1.5", IsPrimary: true, Category: "MAIN", FileName: "mod1.zip", Name: "Main"},
		{ID: "9", Version: "1.0", Category: "ARCHIVED", FileName: "mod1-old.zip", Name: "Old"},
	})

	installModID = "mod1"
	installVersion = "1.0"
	installFileID = "10" // belongs to 1.5, not 1.0
	t.Cleanup(func() { installModID, installVersion, installFileID = "", "", "" })

	err := doInstall(context.Background(), svc, game, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file ID 10 not found") // selectInstallFiles' existing wording, now scoped to the version's files
}
```

(Adapt fixture-helper names — `testZipBytes`, `fake.id` — to what `setupDoInstallTest`'s file actually provides; the shapes above are the contract.)

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/lmm/ -run 'TestInstallCmd_VersionFlag|TestDoInstall_VersionFlag' -v`. Expected: the replaced test fails on "not yet supported"; the new tests fail the same way.

- [ ] **Step 3: Implement.**
  1. Delete `installVersionGuard` (install.go:363-378) and its call in `runInstall` (:382-384).
  2. Flag help (:310): `"specific version to install (default: latest; archived files are searched automatically)"`.
  3. In `doInstall`, replace the file-listing block (install.go:497-503) with a version-aware branch:

```go
	files, err := service.GetModFiles(ctx, installSource, mod)
	if err != nil {
		return fmt.Errorf("failed to get mod files: %w", err)
	}
	if installVersion != "" {
		// #96: resolve --version against the RAW list (archived included -
		// a version pin usually names an archived file). The matches become
		// the selection pool for --file / --yes / the interactive prompt.
		files, err = core.ResolveVersionFiles(installSource, files, installVersion)
		if err != nil {
			return err
		}
	} else {
		files = filterAndSortFiles(files, installShowArchived)
	}
	if len(files) == 0 {
		return fmt.Errorf("no downloadable files available for this mod")
	}
```

  `selectInstallFiles(files)` below needs no change — `--file` outside the version's matches now yields its existing `"file ID %s not found"` error, and the #94 stamp in `applyInstallPrimary` (`flows.go:2900`) records the resolved file's version because `plan.Files` is overridden with the version-matched selection (the existing lines at install.go:505-511).
  4. Batch/dependency path: no change (decision 6) — add one comment line above the `doInstallBatch` call (:489): `// --version applies to the named mod only; dependencies install at latest (#96 decision 6).`

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/lmm/ -v` (full package — the guard's other references are gone; any test still asserting "not yet supported" must be updated in this step). Then `go vet ./cmd/lmm/`.

- [ ] **Step 5: Commit**

```bash
git add cmd/lmm/install.go cmd/lmm/install_test.go
git commit -m "feat(cli): lmm install --version resolves and installs the named version (#96, closes #93)"
```

---

### Task 5: Version-aware deploy selection (core + cmd twin)

**Files:**
- Modify: `internal/core/flows.go` — new `selectVersionedDeployFiles` above `selectDeployFiles` (:1002); call-site swaps at :1329 (`redeployFromSource`), :1923 (`ApplyProfileSwitch`), :3824 (`ApplyImport`)
- Modify: `cmd/lmm/profile.go` — `selectFilesToDownload` (:1163) gains a `version string` parameter with identical semantics; call site in `doProfileApply` (:1055) passes `ref.Version`
- Test: `internal/core/flows_test.go` (switch), `internal/core/flows_import_test.go` (import), `cmd/lmm/profile_test.go` (twin)

**Interfaces:**
- Produces (core): `func selectVersionedDeployFiles(files []domain.DownloadableFile, version string, storedFileIDs []string, allowFallback bool) ([]*domain.DownloadableFile, bool, error)`. Consumed by T6's convergence entries too.
- Produces (cmd): `func selectFilesToDownload(files []domain.DownloadableFile, storedFileIDs []string, version string) ([]*domain.DownloadableFile, error)` — same precedence rules, same error wording.
- Precedence (decisions 2, 4, 5):
  1. `version == ""` or version-less list → exact current behavior (`selectDeployFiles`).
  2. Stored IDs found upstream AND `domain.EffectiveInstalledVersion(version, found) == version` → use them (fast path; vacuous equality when the found files carry no versions).
  3. Else exact-match resolution to `version`; among matches prefer files whose ID ∈ `storedFileIDs` (multi-file installs), else the primary-or-first match. This heals both stored-IDs-gone and hand-edited-version drift — never to latest.
  4. No match + stored IDs existed → extended #95 error: `stored file(s) no longer available upstream (file ID(s): %s; version %q not available) - reinstall the mod or run 'lmm update' to adopt the current version`.
  5. No match + no stored IDs → `%w: version %q is not available upstream (available: %s) - edit the profile's version or reinstall` wrapping `ErrVersionNotFound`.
- `ApplyUpdate`'s call site (:3230, `allowFallback=true`) is deliberately NOT converted — update semantics keep the documented fallback (#95 decision); the locked-mod refusal is #97.

- [ ] **Step 1: Write the failing core tests** in `internal/core/flows_test.go`, modeled on `TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion` (same fixtures: `mockSourceWithDownloads`, `registerDownloadableMod`, `seedProfileWithMod`):

```go
// A source serving two versions: 1.5 (current, ID 10) and 1.0 (archived, ID 9).
type twoVersionSource struct{ *mockSourceWithDownloads }

func (s *twoVersionSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "10", Name: "Main", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Name: "Old", FileName: mod.ID + "-old.zip", Version: "1.0", Category: "ARCHIVED"},
	}, nil
}

func TestApplyProfileSwitch_HonorsProfileVersion_Downgrade(t *testing.T) {
	// Profile "stable" pins mod1 at 1.0 (hand-edited: FileIDs stale/empty).
	// The switch loop must resolve 1.0 -> file 9 and record 1.0 - NOT latest.
}

func TestApplyProfileSwitch_StoredIDsGone_HealsToRecordedVersion(t *testing.T) {
	// ref: Version "1.0", FileIDs ["999"] (gone upstream). Must select file 9
	// (the 1.0 match) instead of the #95 hard fail, and record 1.0.
}

func TestApplyProfileSwitch_StoredIDsGone_VersionAlsoGone_HardFails(t *testing.T) {
	// ref: Version "0.5", FileIDs ["999"]. Neither resolvable: the mod fails
	// with the extended #95 wording naming both the IDs and "0.5";
	// other mods continue (assert result counts + failure detail).
}

func TestApplyProfileSwitch_VersionlessSource_KeepsLegacyBehavior(t *testing.T) {
	// Files carry no Version; ref.Version "1.0" (vacuous). Stored-IDs path
	// and primary-fallback behave exactly as before this task.
}
```

Fill the bodies following the existing switch-loop test in the same file (seed profile + refs via `seedProfileWithMod`/`ProfileManager.UpsertMod` with explicit `domain.ModReference{... Version: "1.0", FileIDs: ...}`, register downloads for BOTH file IDs, call `svc.PlanProfileSwitch` + `svc.ApplyProfileSwitch`, then assert `svc.GetInstalledMod(...).Version`). Mirror one healing + one vacuous case for `ApplyImport` in `flows_import_test.go` (same fixture pattern as `TestApplyImport_InstallLoop_RecordsFileVersion`). Also add one `DeployProfile` healing case (issue #96 names DeployProfile explicitly): seed an installed mod whose `FileIDs` are gone upstream but whose `Version` still resolves, delete its cache dir to force `redeployFromSource`, and assert the deploy succeeds with the recorded version's file rather than the #95 skip — reuse whichever existing `TestDeployProfile*` scaffolding in `flows_test.go` seeds cache + deployment.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/core/ -run 'HonorsProfileVersion|HealsToRecordedVersion|VersionAlsoGone|VersionlessSource' -v`. Expected: the downgrade/healing tests fail (latest installed / hard error), the vacuous test passes (it pins current behavior — keep it anyway as the regression guard).

- [ ] **Step 3: Implement the core helper** directly above `selectDeployFiles` (flows.go:1002):

```go
// selectVersionedDeployFiles is selectDeployFiles with the recorded version
// made authoritative (#96). version == "" (legacy refs) and version-less
// file lists (the #130 vacuous rule) fall through to selectDeployFiles
// unchanged. Otherwise: stored IDs win only while their effective version
// agrees with the record; drift and gone-IDs heal by exact-match resolution
// to the SAME version (never latest - #95's rule extended); unresolvable
// targets are hard per-mod errors naming the version.
func selectVersionedDeployFiles(files []domain.DownloadableFile, version string, storedFileIDs []string, allowFallback bool) ([]*domain.DownloadableFile, bool, error) {
	if version == "" || !anyFileHasVersion(files) {
		return selectDeployFiles(files, storedFileIDs, allowFallback)
	}
	if len(files) == 0 {
		return nil, false, errNoDeployFiles
	}
	if len(storedFileIDs) > 0 {
		if found, _, err := selectDeployFiles(files, storedFileIDs, false); err == nil {
			if domain.EffectiveInstalledVersion(version, found) == version {
				return found, false, nil
			}
		}
	}
	var matches []*domain.DownloadableFile
	for i := range files {
		if files[i].Version == version {
			matches = append(matches, &files[i])
		}
	}
	if len(matches) == 0 {
		if len(storedFileIDs) > 0 {
			return nil, false, fmt.Errorf("%w (file ID(s): %s; version %q not available) - reinstall the mod or run 'lmm update' to adopt the current version", errStoredFilesUnavailable, strings.Join(storedFileIDs, ", "), version)
		}
		return nil, false, fmt.Errorf("%w: version %q is not available upstream (available: %s) - edit the profile's version or reinstall", ErrVersionNotFound, version, strings.Join(availableVersions(files), ", "))
	}
	if len(storedFileIDs) > 0 {
		idSet := make(map[string]bool, len(storedFileIDs))
		for _, id := range storedFileIDs {
			idSet[id] = true
		}
		var stored []*domain.DownloadableFile
		for _, m := range matches {
			if idSet[m.ID] {
				stored = append(stored, m)
			}
		}
		if len(stored) > 0 {
			return stored, false, nil
		}
	}
	for _, m := range matches {
		if m.IsPrimary {
			return []*domain.DownloadableFile{m}, false, nil
		}
	}
	return []*domain.DownloadableFile{matches[0]}, false, nil
}
```

Swap the three call sites:
- flows.go:1923 (`ApplyProfileSwitch`): `selectVersionedDeployFiles(files, ref.Version, ref.FileIDs, false)`
- flows.go:3824 (`ApplyImport`): `selectVersionedDeployFiles(files, ref.Version, fileIDsToUse, false)`
- flows.go:1329 (`redeployFromSource`): `selectVersionedDeployFiles(files, mod.Version, mod.FileIDs, false)` — the DB row's version IS the record here; gone-IDs now heal to it.

- [ ] **Step 4: Run core tests to verify pass** — `go test ./internal/core/ -v`. The #95 pinning tests for the old error string still pass (the un-extended wording still fires when the list is version-less); if any pinned string test fails, reconcile the wording in THIS step, never approximately.

- [ ] **Step 5: Write the failing cmd-twin tests** in `cmd/lmm/profile_test.go` — direct unit tests (in-package, `selectFilesToDownload` is reachable):

```go
func TestSelectFilesToDownload_VersionAuthoritative(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "10", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Version: "1.0", Category: "ARCHIVED"},
	}

	// Drift: stored ID exists upstream but is the wrong version - version wins.
	got, err := selectFilesToDownload(files, []string{"10"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "9", got[0].ID)

	// Gone IDs heal to the recorded version.
	got, err = selectFilesToDownload(files, []string{"999"}, "1.0")
	require.NoError(t, err)
	assert.Equal(t, "9", got[0].ID)

	// Unresolvable: extended #95 wording.
	_, err = selectFilesToDownload(files, []string{"999"}, "0.5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "0.5" not available`)

	// Legacy: empty version behaves exactly as before.
	got, err = selectFilesToDownload(files, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "10", got[0].ID)
}
```

- [ ] **Step 6: Implement the cmd twin.** Port the identical logic into `selectFilesToDownload(files, storedFileIDs, version)` (profile.go:1163) — same precedence, byte-identical error wording (add local unexported `errVersionUnavailable = errors.New("version not found")` mirroring core's sentinel, and local `availableVersions`/`anyFileHasVersion` twins; the duplication note references `internal/tui/service_core.go:254-261`'s CANONICAL NOTE convention). Update the one call site (profile.go:1055): `selectFilesToDownload(files, fileIDsToUse, ref.Version)`. Update `TestSelectFilesToDownload*` existing tests' call shape (added parameter, `""` preserves old expectations).

- [ ] **Step 7: Run to verify pass** — `go test ./cmd/lmm/ -v`, then `go vet ./...`, `gofmt -l .` (expect empty).

- [ ] **Step 8: Commit**

```bash
git add internal/core/flows.go internal/core/flows_test.go internal/core/flows_import_test.go cmd/lmm/profile.go cmd/lmm/profile_test.go
git commit -m "feat: deploy-shaped flows honor the recorded version - heal-to-record, never latest (#96)"
```

---

### Task 6: Convergence — version drift schedules reinstall (the downgrade mechanism)

**Files:**
- Modify: `internal/core/flows.go` — `PlanProfileSwitch` classification switch (:1719-1743); `ApplyProfileSwitch` install loop cache-first (:1929-1965)
- Modify: `cmd/lmm/profile.go` — `doProfileApply` diff loop (:897-923) + install loop replace-when-deployed
- Test: `internal/core/flows_test.go`, `cmd/lmm/profile_test.go`

**Interfaces:**
- Consumes: T5's `selectVersionedDeployFiles` (already wired — convergence entries flow through it), `domain.EffectiveInstalledVersion`, `s.GetGameCache(game).Exists` (`internal/storage/cache/cache.go:38`), `service.GetInstaller(game)` (exported, used throughout flows.go).
- Behavior: a mod installed+enabled whose `im.Version != ref.Version` (ref.Version non-empty) is scheduled into `toInstall` carrying the ref as-is (its YAML FileIDs may name the target version's files; wrong-version IDs are handled by T5's precedence). Cache-first: the install loops skip the download step when `cache.Exists(gameID, sourceID, modID, mod.Version)` is already true after the version stamp. The deploy step replaces the existing deployment.

- [ ] **Step 1: Write the failing planner test** in `internal/core/flows_test.go`:

```go
func TestPlanProfileSwitch_VersionDrift_SchedulesReinstall(t *testing.T) {
	// seed: mod1 installed at 1.5, enabled, under profile "testing" (current
	// default). Target profile "stable" lists mod1 with Version "1.0".
	// Expect: plan.ToInstall contains mod1 with Version "1.0"; ToEnable and
	// ToDisable empty; NoChanges false.
}

func TestPlanProfileSwitch_MatchingVersion_RemainsNoop(t *testing.T) {
	// Same seed but target ref Version "1.5" (matches): mod classified as
	// today - no ToInstall entry (regression guard for the new case's guard
	// conditions, including ref.Version == "").
}
```

Fill bodies with the file's existing seeding helpers (`seedInstalledMod`, profile creation via `svc.NewProfileManager()`, `pm.UpsertMod(game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"})`, `pm.SetDefault` for the current profile). Then an end-to-end downgrade test:

```go
func TestApplyProfileSwitch_Downgrade_EndToEnd(t *testing.T) {
	// twoVersionSource (T5). mod1 installed at 1.5 (file 10), switch to
	// "stable" pinning 1.0. After PlanProfileSwitch + ApplyProfileSwitch:
	//   - GetInstalledMod(...).Version == "1.0", FileIDs == ["9"]
	//   - cache.Exists(gameID, "src", "mod1", "1.0") is true
	//   - result.Installed == 1
}
```

- [ ] **Step 2: Run to verify failure** — the drift test finds an empty `ToInstall`.

- [ ] **Step 3: Implement the planner case.** In `PlanProfileSwitch`'s classification switch (flows.go:1719), insert the drift case between `!installed` and the cache probe:

```go
		im, installed := allInstalled[key]
		switch {
		case !installed:
			toInstall = append(toInstall, ref)
		case ref.Version != "" && im.Version != ref.Version:
			// #96 convergence: the profile names a different version than
			// the installed row - reinstall at the profile's version
			// (downgrades included). ref is passed as-is: its own FileIDs
			// (if any) describe the TARGET version; the installed row's
			// describe the wrong one.
			toInstall = append(toInstall, ref)
		case !s.GetGameCache(game).Exists(game.ID, im.SourceID, im.ID, im.Version):
			...unchanged...
```

Add cache-first to `ApplyProfileSwitch`'s install loop: after the `mod.Version` stamp (flows.go:1929), compute `downloadedFileIDs` from `filesToDownload` up front and wrap the existing download loop:

```go
			downloadedFileIDs := make([]string, 0, len(filesToDownload))
			for _, f := range filesToDownload {
				downloadedFileIDs = append(downloadedFileIDs, f.ID)
			}
			if !s.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
				downloadFailed := false
				for _, file := range filesToDownload {
					...existing download loop body unchanged...
				}
				if downloadFailed {
					continue
				}
			}
```

(The loop's later install step already deploys from the cache; `SaveInstalledMod` upserts the new version — post-#134 the update policy survives.)

- [ ] **Step 4: Run to verify pass** — `go test ./internal/core/ -v` (the end-to-end downgrade test exercises planner + loop + T5 selection together).

- [ ] **Step 5: cmd twin — failing test** in `cmd/lmm/profile_test.go`, exercising `doProfileApply` end-to-end with the package's existing apply-test scaffolding (mirror whichever `TestDoProfileApply*` test seeds installed mods + profile YAML; if none exists at that level, test the diff decision through a focused extraction: move the classification body into `func classifyProfileMod(im *domain.InstalledMod, ref domain.ModReference, cached bool) applyAction` and table-test THAT — smallest testable seam, executor's choice, but the test must pin: drift → reinstall action; matching or empty ref.Version → today's classification).

- [ ] **Step 6: Implement the cmd twin.** In `doProfileApply`'s first diff loop (profile.go:900-923), the in-profile branch gains the drift case (both maps are already in scope — `profileKeys[key]` holds the full ref, `installedByKey` the row):

```go
		} else {
			ref := profileKeys[key]
			if ref.Version != "" && im.Version != ref.Version {
				// #96 convergence: reinstall at the profile's version.
				toInstall = append(toInstall, ref)
				needsRedownloadSet[key] = false // fresh target version: profile FileIDs, not DB's
				needsReplaceSet[key] = im.Deployed
				continue
			}
			if !im.Enabled {
				...existing enable/redownload logic unchanged...
			}
		}
```

with `needsReplaceSet := make(map[string]bool)` declared beside the existing maps, and in the install loop's deploy step: when `needsReplaceSet[key]`, call `service.GetInstaller(game).Replace(ctx, game, &prev.Mod, mod, profileName)` (where `prev := installedByKey[key]`) instead of the loop's `Install`, mirroring `ApplyUpdate`'s replace semantics (flows.go:3290-3300). Add the same cache-first `Exists` guard around the download loop as the core version (step 3), using the stamped `mod.Version`.

- [ ] **Step 7: Run to verify pass** — `go test ./cmd/lmm/ -v`, then the full suite: `go test ./...` (bare, never piped), `go vet ./...`.

- [ ] **Step 8: Commit**

```bash
git add internal/core/flows.go internal/core/flows_test.go cmd/lmm/profile.go cmd/lmm/profile_test.go
git commit -m "feat: profile apply/switch converge installed mods to the profile's version, downgrades included (#96)"
```

---

### Task 7: `ApplyUpdate` records the effective installed version

**Files:**
- Modify: `internal/core/flows.go:3208-3331` (`ApplyUpdate`)
- Test: `internal/core/flows_update_test.go`

**Interfaces:**
- Consumes: `domain.EffectiveInstalledVersion` (`internal/domain/mod.go:58-70`).
- Behavior: after `selectDeployFiles(files, effectiveFileIDs, true)` picks `filesToDownload` (:3230), the recorded version becomes `domain.EffectiveInstalledVersion(newVersion, filesToDownload)` — used for the `newMod.Version` stamp (cache key), `ApplyModUpdate`, the profile `modRef`, and `result.Applied`. This closes the #94 close-out note: update-apply was the only recording flow stamping the mod-level `NewVersion` verbatim, so a mod whose file version differs from its mod version failed `lmm verify`'s version-record check immediately after a clean update.

- [ ] **Step 1: Write the failing test** in `flows_update_test.go`, using the file's existing update-apply scaffolding (locate the existing `TestApplyUpdate*` seeding pattern and copy its fixture wiring; the new source serves a file whose Version differs from the mod-level new version):

```go
type fileVersionDivergesSource struct{ *mockSourceWithDownloads }

func (s *fileVersionDivergesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	// Mod-level says "2.0"; the actual file says "2.0b" (routine on NexusMods).
	return []domain.DownloadableFile{
		{ID: "20", Name: "Main", FileName: mod.ID + ".zip", Version: "2.0b", IsPrimary: true, Category: "MAIN"},
	}, nil
}

func TestApplyUpdate_RecordsEffectiveFileVersion(t *testing.T) {
	// seed mod1 installed at 1.0 (file 10); upd.NewVersion = "2.0".
	// After ApplyUpdate:
	//   - GetInstalledMod(...).Version == "2.0b" (the file's version - #94 invariant)
	//   - PreviousVersion == "1.0" (rollback intact)
	//   - cache.Exists(gameID, src, mod1, "2.0b") is true; "2.0" dir absent
	//   - the profile ref's Version == "2.0b"
}
```

- [ ] **Step 2: Run to verify failure** — recorded version is `"2.0"`.

- [ ] **Step 3: Implement.** In `ApplyUpdate`, after the `filesToDownload` selection (flows.go:3230), insert:

```go
	// #96/#94: record what is actually being installed, not the mod-level
	// NewVersion - update-apply was the last recording flow stamping the
	// mod-level string verbatim, which made verify's version-record check
	// flag freshly-updated mods whose file version differs from the mod
	// version. effectiveVersion keys the cache (via newMod.Version), the DB
	// row, and the profile ref below, matching every install flow.
	effectiveVersion := domain.EffectiveInstalledVersion(newVersion, filesToDownload)
	newMod.Version = effectiveVersion
```

and replace the three `newVersion` recording uses with `effectiveVersion`: `s.ApplyModUpdate(..., effectiveVersion, downloadedFileIDs)` (:3308), `modRef := domain.ModReference{... Version: effectiveVersion ...}` (:3324), `result.Applied` line (:3330). The earlier `if newMod.Version != newVersion { newMod.Version = newVersion }` (:3208) is subsumed — delete it (the stamp above is unconditional and later).

- [ ] **Step 4: Run to verify pass** — `go test ./internal/core/ -v` (existing update tests whose fixtures have matching file/mod versions are unaffected: `EffectiveInstalledVersion` returns `newVersion` when files carry it or no version at all).

- [ ] **Step 5: Commit**

```bash
git add internal/core/flows.go internal/core/flows_update_test.go
git commit -m "fix(core): ApplyUpdate records the installed file's version, closing the verify/update mismatch (#96, #94)"
```

---

### Task 8: Docs, design-note addendum, version 1.25.0

**Files:**
- Modify: `README.md`, `CHANGELOG.md`, `cmd/lmm/root.go` (version var), `docs/plans/2026-07-29-lock-vs-pinned-design.md`
- Regenerate: `docs/man/**` via `make man`
- Move: `docs/plans/2026-07-29-96-version-to-file-resolution.md` → `docs/plans/archive/`

- [ ] **Step 1: README.** Document `install --version` (exact-match, archived searched automatically, unknown-version error lists available versions); add a short "Version behavior in profiles" note under the profiles section: a profile's `version:` is the record of what that profile deploys — apply/switch/import converge to it, including downgrades; edit it by hand or share a profile to reproduce exact builds; sources without per-file version info keep file-ID behavior. Mention the new `versions` capability token in the custom-sources capability table.
- [ ] **Step 2: Design-note addendum.** Append to `docs/plans/2026-07-29-lock-vs-pinned-design.md`:

```markdown
## Addendum (2026-07-29, #96 planning)

Since #94, every install stamps the actual installed version into
`ModReference.Version` — so "version present = locked" cannot be the lock
signal. Refined model, superseding this note's "unlock removes the version"
line: **Version is the record** (always populated; deploy converges to it,
`lmm update` moves it); **#97 adds an explicit `locked:` marker** on the
profile ref (YAML-only — the epic's "no new DB column" decision holds) whose
presence is what `lmm update` refuses on and `unlock` clears. The version
field itself survives unlock, because it is the record, not the lock.
```

- [ ] **Step 3: CHANGELOG** under `## [1.25.0]`: Added — `install --version` installs the named version (closes #93); version→file resolution; `versions` capability; profile apply/switch/import converge to the profile's recorded version (downgrades included). Fixed — stored-files-gone deploys now heal to the recorded version when it is still available upstream (extends #95's hard-fail, which now fires only when the version is gone too); update-apply records the installed file's version (verify no longer flags freshly-updated mods). Add the comparison link at the bottom.
- [ ] **Step 4: Version + man.** Bump `version` in `cmd/lmm/root.go` to `1.25.0`; run `make man` (the install flag help changed in T4 — the genman drift test fails without this); run the full suite bare: `go test ./...`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Archive this plan** (`git mv docs/plans/2026-07-29-96-version-to-file-resolution.md docs/plans/archive/`).
- [ ] **Step 6: Commit**

```bash
git add README.md CHANGELOG.md cmd/lmm/root.go docs/man docs/plans/2026-07-29-lock-vs-pinned-design.md
git add docs/plans/archive/2026-07-29-96-version-to-file-resolution.md
git commit -m "chore: bump version to 1.25.0"
```

- [ ] **Step 7: Push, open the PR** (`Closes #96`, `Closes #93`, summary of decisions 1-8, attribution footer), await CI + Copilot, triage every round including suppressed findings.
