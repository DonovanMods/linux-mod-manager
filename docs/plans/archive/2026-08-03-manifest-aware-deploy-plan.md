# Manifest-Aware Deploy (#210) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy-direction operations link only cache files vouched for by a recorded completion-marker manifest; removal-direction operations keep the full union, so a deploy cycle self-heals entries polluted by stale pre-#197 per-mod paks.

**Architecture:** One new unexported resolver `deployableFiles` in `internal/core` encapsulates the recorded-manifests-else-union rule. Deploy-direction call sites (`Install`, new side of `replaceWithCaches`, `IsInstalled`, `GetDeployedFiles`, `GetConflicts`, profile conflict scanner) switch to it; removal-direction sites (`Uninstall`, old side of replace) stay on `ListFiles`. A `cache.PruneUnclaimed` helper removes unclaimed files at download-commit time when provenance is fully recorded.

**Tech Stack:** Go, testify, `t.TempDir()` filesystem tests (repo testing conventions: `internal/core` tests in `core_test` package; internal tests in `*_internal_test.go` package `core`).

**Spec:** `docs/plans/2026-08-03-manifest-aware-deploy-and-exmodz-default-design.md` (Part 1). Issue: #210.

## Amendment (user ruling 2026-08-03, Task 3 breaker)

`deployableFiles` narrows ONLY when all markers are recorded AND the entry
holds at least one retained source (`.lmm-source-*` — detected via a new
`cache.HasRetainedSource(versionDir)` helper); otherwise it returns the full
`ListFiles` union. This protects #144's unattributed-content shapes
(`TestInstaller_ReplaceForUpdate_SameCacheDir_FallsBackToUnionWithoutProvenance`,
`TestApplyUpdate_SameVersionFileOnlyUpdate_LegacyCacheFallsBackToUnion`),
which the original all-recorded rule broke. Consequences for the tasks below:
Task 1's narrowing-case test fixtures gain a retained-source file and a new
union test covers all-recorded-without-retained-source; Task 2/3/6 fixtures
that model the #210 mixed shape gain a retained-source file (matching the
real entries, which always have one); Task 5's `PruneUnclaimed` carries the
same retained-source gate. Error-wrap amendment (Task 2 review): the
resolver returns cache errors unwrapped; call sites wrap once with
"resolving deployable files: %w" (replace path: "resolving deployable
new-side files: %w").

## Global Constraints

- Branch `fix/210-manifest-aware-deploy` off `develop`; PR `--base develop` (main is the default branch — a forgotten flag targets protected main).
- No version bump; CHANGELOG entry under `[Unreleased]`.
- TDD: every behavior change lands with its failing test written and run first.
- `gofmt` (tabs), error wrapping with `%w`, table-driven tests where repetition warrants.
- Never pipe `go test` into another command in a `&&` chain (repo lesson — the pipe eats the exit code).
- Legacy behavior is sacred: any cache entry with a bare (`Recorded=false`) marker, or no markers, must deploy the full `ListFiles` union byte-for-byte as today.

---

### Task 1: `deployableFiles` resolver

**Files:**

- Create: `internal/core/deployable.go`
- Test: `internal/core/deployable_internal_test.go` (package `core`, precedent: `merged_pak_internal_test.go`)

**Interfaces:**

- Consumes: `cache.Cache.ListFiles/FileManifests/ModPath` (existing).
- Produces: `func deployableFiles(gameCache *cache.Cache, gameID, sourceID, modID, version string) ([]string, error)` — used by Tasks 2–4. Returns files in `ListFiles` order. Errors are `ListFiles`/`FileManifests` errors wrapped with `%w`.

- [ ] **Step 1: Write the failing tests**

```go
package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// seedEntry stores the given files and returns the cache and version dir.
func seedEntry(t *testing.T, files map[string][]byte) (*cache.Cache, string) {
	t.Helper()
	c := cache.New(t.TempDir())
	for name, content := range files {
		require.NoError(t, c.Store("g", "src", "mod", "1.0", name, content))
	}
	return c, c.ModPath("g", "src", "mod", "1.0")
}

func TestDeployableFiles_AllRecorded_ExcludesUnclaimed(t *testing.T) {
	c, dir := seedEntry(t, map[string][]byte{
		"claimed.pak": []byte("a"),
		"stale.pak":   []byte("b"), // claimed by no manifest
	})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", []string{"claimed.pak"}))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"claimed.pak"}, files)
}

func TestDeployableFiles_RecordedZeroMembers_DeploysNothing(t *testing.T) {
	// The live #210 shape: retain-only exmodz marker (recorded, zero members)
	// plus a stale pre-#197 compiled pak.
	c, dir := seedEntry(t, map[string][]byte{"LargerResourceStacks_P.pak": []byte("pak")})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", nil))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestDeployableFiles_BareMarker_FallsBackToUnion(t *testing.T) {
	c, dir := seedEntry(t, map[string][]byte{
		"a.pak": []byte("a"),
		"b.pak": []byte("b"),
	})
	// Legacy bare marker: completion vouched, provenance unknown.
	require.NoError(t, cache.MarkFileComplete(dir, "pak"))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.pak", "b.pak"}, files)
}

func TestDeployableFiles_MixedRecordedAndBare_FallsBackToUnion(t *testing.T) {
	c, dir := seedEntry(t, map[string][]byte{
		"a.pak": []byte("a"),
		"b.pak": []byte("b"),
	})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"a.pak"}))
	require.NoError(t, cache.MarkFileComplete(dir, "f2")) // bare

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.pak", "b.pak"}, files)
}

func TestDeployableFiles_NoMarkers_FallsBackToUnion(t *testing.T) {
	c, _ := seedEntry(t, map[string][]byte{"a.pak": []byte("a")})

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"a.pak"}, files)
}

func TestDeployableFiles_ClaimedButMissingOnDisk_Dropped(t *testing.T) {
	// A manifest may claim a member that was manually deleted; the resolver
	// returns what is actually deployable (verify owns missing-file repair).
	c, dir := seedEntry(t, map[string][]byte{"present.pak": []byte("a")})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"present.pak", "gone.pak"}))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"present.pak"}, files)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run TestDeployableFiles -v`
Expected: FAIL — `undefined: deployableFiles` (compile error). If `cache.MarkFileComplete` has a different exported name for the bare-marker writer, check `internal/storage/cache/cache.go` (~line 80) and use the actual name in both test and doc comment.

- [ ] **Step 3: Implement the resolver**

```go
// Package-level doc: deployable.go holds the deploy-direction file resolver
// (#210). Removal-direction operations (Uninstall, the old side of a
// replace) deliberately do NOT use it - cleanup must remove anything that
// might ever have been linked, so they keep the full ListFiles union. That
// asymmetry is what lets one deploy cycle self-heal an entry polluted by a
// stale, unclaimed file: the union side unlinks it, this resolver never
// re-links it.
package core

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// deployableFiles returns the version-dir-relative files that deploy-direction
// operations may link for this cache entry, in ListFiles order.
//
// When EVERY completion marker in the entry carries a recorded member
// manifest, the result is the union of recorded members intersected with the
// files actually on disk - content claimed by no manifest (e.g. a stale
// pre-#197 compiled per-mod pak carried forward by staging seeding, #210) is
// excluded. A claimed-but-missing member is silently dropped here; verify
// owns missing-file detection and repair.
//
// When ANY marker is bare (Recorded=false - "provenance unknown, never
// none"), or the entry has no markers at all, the full ListFiles union is
// returned unchanged: pre-manifest entries, import-populated entries, and
// pure pre-#197 entries keep their historical deploy behavior exactly.
func deployableFiles(gameCache *cache.Cache, gameID, sourceID, modID, version string) ([]string, error) {
	files, err := gameCache.ListFiles(gameID, sourceID, modID, version)
	if err != nil {
		return nil, fmt.Errorf("listing cached files: %w", err)
	}
	manifests, err := gameCache.FileManifests(gameID, sourceID, modID, version)
	if err != nil {
		return nil, fmt.Errorf("reading cache manifests: %w", err)
	}
	if len(manifests) == 0 {
		return files, nil
	}
	claimed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			return files, nil
		}
		for _, member := range m.Members {
			claimed[member] = true
		}
	}
	deployable := make([]string, 0, len(files))
	for _, f := range files {
		if claimed[f] {
			deployable = append(deployable, f)
		}
	}
	return deployable, nil
}
```

Note: `FileManifests` members are stored slash-separated and `ListFiles` returns OS-separator paths — on Linux they agree; mirror how `resolveSharedDirUpdate` (installer.go:314-326) compares them (it uses the manifest strings directly against listing entries). If tests on the nested-path case disagree, normalize with `filepath.FromSlash` on the claimed side.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -run TestDeployableFiles -v`
Expected: PASS (all six).

- [ ] **Step 5: Commit**

```bash
git add internal/core/deployable.go internal/core/deployable_internal_test.go
git commit -m "feat: add manifest-aware deployableFiles resolver (#210)"
```

---

### Task 2: Install, IsInstalled, GetDeployedFiles, GetConflicts use the resolver

**Files:**

- Modify: `internal/core/installer.go:43` (Install), `:452` (IsInstalled), `:495` (GetConflicts), `:527` (GetDeployedFiles)
- Test: `internal/core/installer_test.go` (package `core_test`)

**Interfaces:**

- Consumes: `deployableFiles` (Task 1).
- Produces: no signature changes — behavior only. `Uninstall` (installer.go:410) is intentionally untouched.

- [ ] **Step 1: Write the failing test**

```go
// In internal/core/installer_test.go (package core_test).

// TestInstaller_Install_SkipsUnclaimedFiles reproduces the #210 live shape:
// a retain-only marker (recorded, zero members) plus a stale unclaimed pak.
// Install must deploy nothing; a legacy bare-marker entry must keep the
// historical deploy-everything behavior.
func TestInstaller_Install_SkipsUnclaimedFiles(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	modCache := cache.New(cacheDir)

	require.NoError(t, modCache.Store("icarus", "icarus", "m1", "1.4", "Stale_P.pak", []byte("stale")))
	dir := modCache.ModPath("icarus", "icarus", "m1", "1.4")
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", nil))

	game := &domain.Game{ID: "icarus", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "m1", SourceID: "icarus", Version: "1.4", GameID: "icarus"}
	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	require.NoError(t, inst.Install(context.Background(), game, mod, "default"))
	_, err := os.Lstat(filepath.Join(gameDir, "Stale_P.pak"))
	assert.True(t, os.IsNotExist(err), "unclaimed stale pak must not deploy")

	// Deployed-file views agree with the deploy decision.
	deployed, err := inst.GetDeployedFiles(game, mod)
	require.NoError(t, err)
	assert.Empty(t, deployed)
}

func TestInstaller_Install_LegacyBareMarker_DeploysUnion(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	modCache := cache.New(cacheDir)

	require.NoError(t, modCache.Store("icarus", "icarus", "m2", "2.2", "MorePoints_P.pak", []byte("pak")))
	dir := modCache.ModPath("icarus", "icarus", "m2", "2.2")
	require.NoError(t, cache.MarkFileComplete(dir, "exmodz")) // pre-manifest bare marker

	game := &domain.Game{ID: "icarus", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "m2", SourceID: "icarus", Version: "2.2", GameID: "icarus"}
	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	require.NoError(t, inst.Install(context.Background(), game, mod, "default"))
	_, err := os.Lstat(filepath.Join(gameDir, "MorePoints_P.pak"))
	assert.NoError(t, err, "legacy entry must keep deploying its pak")

	installed, err := inst.IsInstalled(game, mod)
	require.NoError(t, err)
	assert.True(t, installed)
}

// TestInstaller_IsInstalled_RetainOnlyEntry guards the deploy-loop
// interaction: a retain-only entry (deployable set empty) must not report
// "not installed" in a way that spins redeploy loops - with nothing
// deployable and nothing deployed there is nothing missing.
func TestInstaller_IsInstalled_RetainOnlyEntry(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	modCache := cache.New(cacheDir)

	require.NoError(t, modCache.Store("icarus", "icarus", "m1", "1.4", "Stale_P.pak", []byte("stale")))
	dir := modCache.ModPath("icarus", "icarus", "m1", "1.4")
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", nil))

	game := &domain.Game{ID: "icarus", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mod := &domain.Mod{ID: "m1", SourceID: "icarus", Version: "1.4", GameID: "icarus"}
	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	installed, err := inst.IsInstalled(game, mod)
	require.NoError(t, err)
	assert.False(t, installed, "empty deployable set keeps IsInstalled's len==0 false answer")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run 'TestInstaller_Install_SkipsUnclaimedFiles|TestInstaller_Install_LegacyBareMarker|TestInstaller_IsInstalled_RetainOnlyEntry' -v`
Expected: `TestInstaller_Install_SkipsUnclaimedFiles` FAILS (stale pak IS deployed today); the legacy test PASSES already (it pins existing behavior — keep it as the regression guard).

- [ ] **Step 3: Switch the four call sites**

In `Install` (installer.go:43), `IsInstalled` (:452), `GetConflicts` (:495), `GetDeployedFiles` (:527), replace

```go
files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
```

with

```go
files, err := deployableFiles(i.cache, game.ID, mod.SourceID, mod.ID, mod.Version)
```

**Amended after Task 2 review (user ruling 2026-08-03):** `deployableFiles`
returns cache-layer errors UNWRAPPED (the cache layer already contextualizes
them), and each switched call site wraps with
`fmt.Errorf("resolving deployable files: %w", err)` — including
`GetDeployedFiles`, which previously returned the error bare. This replaces
the original "keep each site's existing error-wrapping line unchanged"
instruction, which produced doubled/misleading prefixes. Do NOT touch `Uninstall` (:410) — add a comment there:

```go
// Deliberately the full ListFiles union, not deployableFiles (#210):
// removal must cover anything that might ever have been linked, including
// stale unclaimed files a pre-fix deploy linked. Narrowing this would
// strand those links forever.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: the three new tests PASS; the full package stays green (existing installer tests exercise no-marker entries → union fallback keeps them passing).

- [ ] **Step 5: Commit**

```bash
git add internal/core/installer.go internal/core/installer_test.go
git commit -m "fix: deploy-direction installer paths use manifest-aware file set (#210)"
```

---

### Task 3: replaceWithCaches deploys the manifest-aware new side

**Files:**

- Modify: `internal/core/installer.go:129-136` (old/new listing block), `:242-280` (resolveSharedDirUpdate doc comment)
- Test: `internal/core/installer_test.go`

**Interfaces:**

- Consumes: `deployableFiles` (Task 1).
- Produces: no signature changes. `oldFiles` stays the `ListFiles` union (removal direction); `newFiles` becomes the resolver output and is also what `resolveSharedDirUpdate` receives as `unionFiles`.

- [ ] **Step 1: Write the failing test**

```go
// TestInstaller_Replace_NewSideSkipsUnclaimed: replacing onto a version whose
// cache entry mixes a recorded retain-only marker with a stale unclaimed pak
// must (a) undeploy the old version's files, (b) NOT deploy the stale pak.
func TestInstaller_Replace_NewSideSkipsUnclaimed(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	modCache := cache.New(cacheDir)

	// Old version: ordinary claimed pak, deployed.
	require.NoError(t, modCache.Store("icarus", "icarus", "m1", "1.0", "Old_P.pak", []byte("old")))
	oldDir := modCache.ModPath("icarus", "icarus", "m1", "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(oldDir, "pak", []string{"Old_P.pak"}))

	// New version: the #210 mixed shape.
	require.NoError(t, modCache.Store("icarus", "icarus", "m1", "1.4", "Stale_P.pak", []byte("stale")))
	newDir := modCache.ModPath("icarus", "icarus", "m1", "1.4")
	require.NoError(t, cache.MarkFileCompleteWithMembers(newDir, "exmodz", nil))

	game := &domain.Game{ID: "icarus", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	oldMod := &domain.Mod{ID: "m1", SourceID: "icarus", Version: "1.0", GameID: "icarus"}
	newMod := &domain.Mod{ID: "m1", SourceID: "icarus", Version: "1.4", GameID: "icarus"}
	inst := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	require.NoError(t, inst.Install(context.Background(), game, oldMod, "default"))
	require.NoError(t, inst.Replace(context.Background(), game, oldMod, newMod, "default"))

	_, err := os.Lstat(filepath.Join(gameDir, "Old_P.pak"))
	assert.True(t, os.IsNotExist(err), "old file must be undeployed")
	_, err = os.Lstat(filepath.Join(gameDir, "Stale_P.pak"))
	assert.True(t, os.IsNotExist(err), "stale unclaimed pak must not deploy")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestInstaller_Replace_NewSideSkipsUnclaimed -v`
Expected: FAIL — `Stale_P.pak` is deployed today.

- [ ] **Step 3: Implement**

In `replaceWithCaches` (installer.go:133), replace the new-side listing:

```go
newFiles, err := deployableFiles(newCache, game.ID, newMod.SourceID, newMod.ID, newMod.Version)
if err != nil {
	return fmt.Errorf("listing new cached files: %w", err)
}
```

Leave `oldFiles` on `ListFiles` (removal direction). Update `resolveSharedDirUpdate`'s doc comment where it describes `unionFiles` ("every file in the shared directory's listing is attributed"): it now receives the deploy-direction set — when the resolver narrowed, every entry is attributed by construction (resolver requires all-recorded); when the resolver fell back to the union, the attribution check behaves exactly as before. Add one sentence there stating this, citing #210.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -v`
Expected: new test PASSES; all existing replace/update tests (including the #144 same-version suite and #150 rollback suite) stay green — they build fully-recorded or fully-legacy entries, both of which the resolver passes through faithfully. If any #144 test fails, STOP and re-read its fixture: it means the fixture mixes recorded and unrecorded markers and the interaction needs a design conversation, not a patch.

- [ ] **Step 5: Commit**

```bash
git add internal/core/installer.go internal/core/installer_test.go
git commit -m "fix: replace deploys manifest-aware new side, union removal (#210)"
```

---

### Task 4: Profile conflict scanner ignores unclaimed files

**Files:**

- Modify: `internal/core/conflicts.go:99` (the `gameCache.ListFiles` call in the enabled-mods provider loop)
- Test: `internal/core/conflicts_test.go` (add to the existing scanner suite; follow its established fixture pattern)

**Interfaces:**

- Consumes: `deployableFiles` (Task 1).
- Produces: no signature changes.

- [ ] **Step 1: Write the failing test**

Follow the existing test fixtures in `internal/core/conflicts_test.go` for constructing the service/DB/profile (reuse its helper if one exists — read the file first). The new case, shaped on the existing two-mods-one-path tests:

```go
// TestProfileConflicts_IgnoresUnclaimedCacheFiles: a stale unclaimed file in
// mod A's cache entry that collides with a path mod B legitimately provides
// must NOT be reported - deploy will never link it (#210).
//
// Fixture: mod A's entry = claimed "shared.pak" is NOT present; instead A has
// marker "exmodz" with zero members plus unclaimed file "shared.pak" on disk.
// Mod B's entry = "shared.pak" claimed by its marker. Expect: no conflict on
// "shared.pak" (A provides nothing).
```

Write it as real code against the suite's actual helpers (they exist in the file — mirror the neighboring test's setup verbatim, changing only the cache-entry construction to use `cache.MarkFileCompleteWithMembers`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestProfileConflicts_IgnoresUnclaimedCacheFiles -v`
Expected: FAIL — conflict on `shared.pak` is reported today.

- [ ] **Step 3: Implement**

In `internal/core/conflicts.go:99`, replace `gameCache.ListFiles(game.ID, m.SourceID, m.ID, m.Version)` with `deployableFiles(gameCache, game.ID, m.SourceID, m.ID, m.Version)`. Keep the surrounding `fs.ErrNotExist` tolerance exactly as is (`deployableFiles` wraps `ListFiles`' error with `%w`, so `errors.Is(err, fs.ErrNotExist)` still matches).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -run TestProfileConflicts -v`
Expected: PASS, including all pre-existing scanner tests.

- [ ] **Step 5: Commit**

```bash
git add internal/core/conflicts.go internal/core/conflicts_test.go
git commit -m "fix: conflict scanner uses manifest-aware provider set (#210)"
```

---

### Task 5: Prune unclaimed files at download commit

**Files:**

- Modify: `internal/storage/cache/cache.go` (extract dir-based manifest reader; add `PruneUnclaimed`)
- Modify: `internal/core/service.go` (`commitStagedCacheWithMarker`, ~line 855)
- Test: `internal/storage/cache/cache_test.go`, `internal/core/service_test.go`

**Interfaces:**

- Consumes: the existing marker/manifest parsing inside `Cache.FileManifests`.
- Produces: `func PruneUnclaimed(versionDir string) error` (package-level in `cache`, like `MarkFileCompleteWithMembers`). Refactor `Cache.FileManifests` to delegate to a new unexported `fileManifestsAt(versionDir string) (map[string]FileManifest, error)` so both share one parser.

- [ ] **Step 1: Write the failing tests**

```go
// In internal/storage/cache/cache_test.go:

func TestPruneUnclaimed_RemovesUnclaimedWhenAllRecorded(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "claimed.pak", []byte("a")))
	require.NoError(t, c.Store("g", "s", "m", "1.0", "stale.pak", []byte("b")))
	require.NoError(t, c.Store("g", "s", "m", "1.0", "sub/stale2.pak", []byte("c")))
	dir := c.ModPath("g", "s", "m", "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"claimed.pak"}))

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"claimed.pak"}, files)
	// Emptied subdirectory is removed too.
	_, err = os.Stat(filepath.Join(dir, "sub"))
	require.True(t, os.IsNotExist(err))
}

func TestPruneUnclaimed_NoOpWithBareMarker(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "a.pak", []byte("a")))
	dir := c.ModPath("g", "s", "m", "1.0")
	require.NoError(t, cache.MarkFileComplete(dir, "f1")) // bare: provenance unknown

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"a.pak"}, files)
}

func TestPruneUnclaimed_NoOpWithoutMarkers(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "a.pak", []byte("a")))
	dir := c.ModPath("g", "s", "m", "1.0")

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"a.pak"}, files)
}

func TestPruneUnclaimed_NeverTouchesReservedEntries(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "claimed.pak", []byte("a")))
	dir := c.ModPath("g", "s", "m", "1.0")
	// A retained source is reserved bookkeeping, claimed by nothing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", nil))
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"claimed.pak"}))

	require.NoError(t, cache.PruneUnclaimed(dir))

	_, err := os.Stat(filepath.Join(dir, cache.RetainedSourceName("exmodz")))
	require.NoError(t, err, "reserved entries are never pruned")
}
```

(Adjust `cache.RetainedSourceName`'s exact call shape to its real signature in `cache.go:273` — it takes the fileID.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/storage/cache/ -run TestPruneUnclaimed -v`
Expected: FAIL — `undefined: cache.PruneUnclaimed`.

- [ ] **Step 3: Implement**

In `cache.go`: extract the body of `FileManifests` into `fileManifestsAt(versionDir string)`; `FileManifests` becomes `return fileManifestsAt(c.ModPath(gameID, sourceID, modID, version))`. Then:

```go
// PruneUnclaimed deletes non-reserved regular files in versionDir that no
// recorded manifest claims, then removes directories the deletions emptied
// (#210). It is a no-op unless EVERY marker in the directory carries a
// recorded manifest - a bare marker means unknown provenance, and pruning
// on guesswork could delete a legacy file's live content. Reserved
// (ReservedPrefix) entries are never candidates. Callers invoke it on a
// STAGING directory at commit time, so a prune can never race a deploy.
func PruneUnclaimed(versionDir string) error {
	manifests, err := fileManifestsAt(versionDir)
	if err != nil {
		return fmt.Errorf("reading manifests for prune: %w", err)
	}
	if len(manifests) == 0 {
		return nil
	}
	claimed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			return nil
		}
		for _, member := range m.Members {
			claimed[filepath.FromSlash(member)] = true
		}
	}
	files, err := walkEntries(versionDir, false) // same walker ListFiles uses
	if err != nil {
		return fmt.Errorf("walking entry for prune: %w", err)
	}
	for _, f := range files {
		if claimed[f] {
			continue
		}
		if err := os.Remove(filepath.Join(versionDir, f)); err != nil {
			return fmt.Errorf("pruning unclaimed %s: %w", f, err)
		}
	}
	removeEmptyDirs(versionDir)
	return nil
}
```

`removeEmptyDirs`: if the cache package has no such helper, add a small unexported one (post-order walk, `os.Remove` on empty dirs, never removing versionDir itself; ignore ENOTEMPTY). Check `internal/linker.CleanupEmptyDirs` first — if its semantics fit (it exists per installer.go:438), prefer reusing it over writing a twin; importing linker from cache is NOT acceptable (dependency direction), so a local helper is right if reuse would invert layers.

In `internal/core/service.go`, wire into the single commit point:

```go
func commitStagedCacheWithMarker(cachePath, stagePath, fileID string, members []string) error {
	if err := cache.MarkFileCompleteWithMembers(stagePath, fileID, members); err != nil {
		return err
	}
	// #210: with full provenance recorded, drop anything no manifest claims
	// (pre-#197 compiled paks carried forward by prepareStaging's seeding).
	// A legacy bare marker anywhere makes this a silent no-op.
	if err := cache.PruneUnclaimed(stagePath); err != nil {
		return err
	}
	return commitStagedCache(cachePath, stagePath)
}
```

Add a service-level test in `internal/core/service_test.go` following the file's existing download/ingest test fixtures (read neighbors first): stage a seeded entry containing a stale unclaimed pak plus a recorded marker, run the commit path (whichever existing test helper drives `DownloadModToCache`'s commit — mirror it), assert the stale pak is gone from the committed entry and the retained source survives.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/storage/cache/ ./internal/core/ -v`
Expected: PASS. Watch specifically for `service_test.go`/`extractor_test.go` fixtures that committed entries with deliberately-unclaimed extra files — if one fails, the fixture was depending on pre-#210 debris surviving; update the fixture, not the prune rule.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/cache/cache.go internal/storage/cache/cache_test.go internal/core/service.go internal/core/service_test.go
git commit -m "fix: prune unclaimed cache files at download commit (#210)"
```

---

### Task 6: DeployProfile self-heal integration test + CHANGELOG

**Files:**

- Test: `internal/core/flows_deploy_selfheal_test.go` (new; package `core_test`)
- Modify: `CHANGELOG.md` (`[Unreleased]` section)

**Interfaces:**

- Consumes: everything above through `Service.DeployProfile` — the seam both `lmm deploy` (cmd/lmm/deploy.go:65 → doDeploy → DeployProfile) and the TUI call, satisfying the #197 entry-point-test lesson for this mutation.
- Produces: nothing new — acceptance proof.

- [ ] **Step 1: Write the failing-before/green-after integration test**

Build the fixture with the same service-construction helpers the existing `flows.go` deploy tests use (find a `DeployProfile` test in `internal/core/` and mirror its setup — service with temp config/db, one installed+enabled mod row). The scenario:

```go
// TestDeployProfile_SelfHealsStaleUnclaimedPak is #210's acceptance test:
// a pre-fix deploy linked a stale unclaimed pak into the game dir; one
// deploy cycle must remove that link (Uninstall's union direction) and not
// re-create it (Install's manifest-aware direction).
//
// Fixture: installed mod m1@1.4, cache entry = stale "Stale_P.pak" on disk
// + marker "exmodz" recorded with zero members. Game dir already contains
// a symlink Stale_P.pak -> the cached stale pak (simulating the pre-fix
// deployment).
//
// After DeployProfile: game dir does NOT contain Stale_P.pak; result has
// Deployed == 1 (the mod "deploys" successfully with zero files of its
// own); no Skipped entries.
```

Write it as real code against the discovered helpers; the assertions are the three lines in the comment (Lstat IsNotExist, `result.Deployed == 1`, `len(result.Skipped) == 0`).

- [ ] **Step 2: Run the full suite**

Run: `go test ./... -v` (capture to a file, no pipes in `&&` chains) and `go vet ./...`
Expected: everything PASSES.

- [ ] **Step 3: CHANGELOG**

Under `## [Unreleased]` add (create a `### Fixed` heading if absent):

```markdown
### Fixed

- Deploy no longer links cache files that no download manifest claims: stale
  per-mod paks left behind by pre-v1.28 exmodz installs were being deployed
  alongside the merged pak, double-applying their table edits. One
  `lmm deploy` cycle now removes such links, download commits prune the
  stale files, and the conflict scanner ignores them. Legacy cache entries
  without recorded manifests keep their exact previous behavior. (#210)
```

- [ ] **Step 4: Commit**

```bash
git add internal/core/flows_deploy_selfheal_test.go CHANGELOG.md
git commit -m "test: DeployProfile self-heal acceptance for #210 + changelog"
```

---

### Task 7: PR

- [ ] **Step 1: Final verification**

Run: `go build -o lmm ./cmd/lmm && go vet ./...` then `go test ./...` (separately, never piped).
Run: `trunk check` if available in the environment.

- [ ] **Step 2: Push and open PR**

```bash
git push -u origin fix/210-manifest-aware-deploy
gh pr create --base develop \
  --title "fix: manifest-aware deploy — stop shipping unclaimed cache files (#210)" \
  --body "Closes #210 (close manually after merge — develop merges don't auto-close). <summary per repo template>. 🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

- [ ] **Step 3: Copilot triage** — wait for the automatic review (`gh-await-review` via background Bash), triage every comment (fix or reply with rationale), and re-check for NEW comments after any fix push before merging.

---

## Verification checklist (post-merge, live machine)

The user's real Icarus install is the origin of this bug. After merge, build `./lmm` and have the user (or a sandboxed HOME per repo sandboxing rules) run `lmm -g icarus deploy`: `LargerResourceStacks_P.pak` and `MegaPoints_P.pak` symlinks must disappear from `.../Icarus/Content/Paks/mods/`, while `laanp-Combined_QOL_v1_w243_P.pak` (pak-only mod, claimed) and `zzz_LMM_Merged_P.pak` remain.
