# Deploy Convergence (#168 + #212) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `verify --fix` converges the game directory with current reality — removing lmm-attributed stale deployed files (#168) and dangling cache-pointing symlinks (#212) — with plain `verify` reporting the same findings as warnings.

**Architecture:** One remove-only core method `Service.ConvergeDeployedFiles` with two provenance-gated passes: (1) row-driven — `deployed_files` rows whose paths no current mod provides (judged against the union of ALL installed mods' `deployableFiles` sets, so ownership churn can't remove a path another mod legitimately deploys) are undeployed and their rows deleted; (2) a symlink-only sweep of the game dir removing links that point at nonexistent targets under an lmm cache root. CLI `verify` wires dry-run (warnings) and `--fix` (removal) modes.

**Tech Stack:** Go, testify, `t.TempDir()` symlink fixtures; in-memory SQLite for rows; existing verify CLI test patterns.

**Spec:** `docs/plans/2026-08-04-deploy-convergence-and-batch-hook-order-design.md` Part 2. Issues: #168, #212 (both closed by this).

## Global Constraints

- Branch `feat/168-212-deploy-convergence` off `develop` (after #214 merges); PR `--base develop`.
- No version bump; CHANGELOG under `[Unreleased]` (`### Added` — widens `--fix`'s contract, MINOR-class per #168's framing).
- TDD; gofmt (tabs); `%w` wrapping; never pipe `go test` into another command in a `&&` chain.
- **Remove-only invariant:** a convergence never deploys, never modifies content, never touches a non-symlink file except via a DB row, never touches a symlink pointing outside the lmm cache roots, never removes a path any installed mod's current deployable set still contains.
- Cache roots for the sweep's prefix check: the service's global `CacheDir` and the game's `CachePath` (when set) — resolved via `filepath.Clean` + separator-suffix prefix comparison (no naive `strings.HasPrefix` that would match `/cache-evil`).
- Healthy (target-exists) symlinks are never sweep candidates — this structurally protects the merged pak's link; a dangling merged-pak link IS removable (next deploy/recompile recreates it).

---

### Task 1: DB — per-path deployed-file delete

**Files:**

- Modify: `internal/storage/db/files.go`
- Test: `internal/storage/db/files_test.go` (or wherever `SaveDeployedFile`'s tests live — grep and colocate)

**Interfaces:**

- Consumes: existing `deployed_files` schema (`game_id, profile_name, relative_path, source_id, mod_id`).
- Produces: `func (d *DB) DeleteDeployedFile(gameID, profileName, relativePath string) error` — deletes one row; deleting a nonexistent row is a silent no-op (idempotent). Task 2 consumes it.

- [ ] **Step 1: Write the failing test**

```go
func TestDeleteDeployedFile(t *testing.T) {
	d, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = d.Close() }()

	require.NoError(t, d.SaveDeployedFile("g", "default", "mods/a.pak", "src", "m1"))
	require.NoError(t, d.SaveDeployedFile("g", "default", "mods/b.pak", "src", "m1"))

	require.NoError(t, d.DeleteDeployedFile("g", "default", "mods/a.pak"))
	files, err := d.GetDeployedFilesForMod("g", "default", "src", "m1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"mods/b.pak"}, files)

	// Idempotent: deleting again is a no-op, not an error.
	require.NoError(t, d.DeleteDeployedFile("g", "default", "mods/a.pak"))
}
```

- [ ] **Step 2: RED** — `go test ./internal/storage/db/ -run TestDeleteDeployedFile -v` (undefined method).

- [ ] **Step 3: Implement**

```go
// DeleteDeployedFile removes one deployed-file ownership row. Deleting a
// row that does not exist is a silent no-op - convergence (#168/#212) calls
// this for paths it just undeployed, and a row may already be gone when the
// path was attributed only by a dangling link.
func (d *DB) DeleteDeployedFile(gameID, profileName, relativePath string) error {
	_, err := d.Exec(`
		DELETE FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND relative_path = ?`,
		gameID, profileName, relativePath)
	if err != nil {
		return fmt.Errorf("deleting deployed file record: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: GREEN** — focused run, then `go test ./internal/storage/db/`.
- [ ] **Step 5: Commit** — `git add internal/storage/db/files.go <test file>` ; `git commit -m "feat: per-path deployed-file delete for convergence (#168)"`

---

### Task 2: Core — ConvergeDeployedFiles

**Files:**

- Create: `internal/core/converge.go`
- Test: `internal/core/converge_test.go` (package `core_test`, mirroring the flows-test service builders; internal test file only if the helpers demand it)

**Interfaces:**

- Consumes: `deployableFiles` (internal/core/deployable.go, #210), `s.GetGameCache`/`GetGameCachePath`, `s.GetInstalledMods`, `s.db.GetDeployedFilesForMod` (via existing service wrapper `GetDeployedFilesForMod`), Task 1's `DeleteDeployedFile` (add a thin service wrapper if the service pattern requires one — mirror `SetModDeployed`'s shape), the profile-effective linker (`s.GetInstallerForProfile` or the raw `linker` — use whatever undeploys elsewhere in core; `linker.Linker.Undeploy` tolerates absent paths).
- Produces:

```go
type ConvergedFile struct {
	Path     string // game-dir-relative
	Reason   string // "no longer provided by <source>/<mod>" | "dangling link into lmm cache"
	SourceID string // owning mod when known ("" for sweep finds with no row)
	ModID    string
}

type ConvergeResult struct {
	Removed []ConvergedFile // dryRun=true: candidates; dryRun=false: actually removed
}

func (s *Service) ConvergeDeployedFiles(ctx context.Context, game *domain.Game, profileName string, dryRun bool) (*ConvergeResult, error)
```

Behavior:

1. Build `provided` = union over ALL installed mods (this game+profile, enabled AND disabled — a disabled mod's files are undeployed by disable, but its rows may linger; treat rows below) of `deployableFiles(...)`, tolerating per-mod `fs.ErrNotExist` cache misses (mod provides nothing).
2. Row pass: for each installed mod, each row path from `GetDeployedFilesForMod`; if path ∉ `provided` → candidate `{Path, "no longer provided by ...", SourceID, ModID}`; unless dryRun: `Undeploy(gameDir/path)` (error → collect as warning-style error in Reason? NO — keep result simple: an Undeploy failure aborts nothing; record the error by returning it wrapped in a joined error at the end while continuing; mirror verify's per-item tolerance) then `DeleteDeployedFile`.
3. Sweep pass: `filepath.WalkDir(game.ModPath)`; for each symlink (`d.Type()&fs.ModeSymlink != 0`): `os.Readlink`; if target (made absolute relative to the link's dir when relative) is under a cache root AND `os.Stat(linkPath)` fails with `fs.ErrNotExist` (dangling) → candidate `{Path: rel, Reason: "dangling link into lmm cache"}`; unless dryRun: `os.Remove(linkPath)` + best-effort `DeleteDeployedFile`. Deduplicate against paths already handled by the row pass.
4. ctx checked between mods and periodically in the walk (mirror the conflict scanner's ctx pattern).

- [ ] **Step 1: Write the failing tests** — table/scenario tests, real filesystem:

```go
// Scenarios (each its own test, shared builder):
// 1. RowDrivenStaleRemoved: mod m1 rows {a.esp, gone.esp}; cache provides
//    only a.esp (recorded manifest). Converge(dryRun=false): gone.esp's
//    game-dir symlink removed, row deleted, a.esp untouched; Result lists
//    exactly gone.esp with m1 attribution.
// 2. SharedPathProtectedByUnion: m1 row for shared.esp but m2's deployable
//    set still contains shared.esp -> NOT removed, not in Result.
// 3. DanglingCacheLinkSwept: symlink stray.pak -> <cacheRoot>/...missing
//    target, no DB row. Swept with reason "dangling link into lmm cache".
// 4. ForeignSymlinkUntouched: symlink user.pak -> /somewhere/else (dangling
//    or not) stays; not in Result.
// 5. HealthySymlinkUntouched: link with existing cache target (the merged-
//    pak shape) stays even though no row exists for it.
// 6. DryRunTouchesNothing: scenario 1+3 with dryRun=true -> Result lists
//    both, filesystem and DB unchanged.
// 7. RegularFileNeedsRow: a REGULAR file (copy-mode deploy) with a stale
//    row IS removed via the row pass; a regular file with NO row is never
//    touched by the sweep.
```

Write them fully against the existing service/DB builders (the flows tests construct Service with temp config + `:memory:` DB — mirror; NEEDS_CONTEXT if no reusable builder exists).

- [ ] **Step 2: RED** — `go test ./internal/core/ -run TestConverge -v` (undefined types/method).
- [ ] **Step 3: Implement** `internal/core/converge.go` per the Produces contract, doc comments carrying the remove-only invariant and the union guard rationale (#168/#212, the #210 resolver as the provenance source).
- [ ] **Step 4: GREEN** — focused, then full `go test ./internal/core/` and `gofmt -l internal/core/`.
- [ ] **Step 5: Commit** — `git commit -m "feat: ConvergeDeployedFiles core pass (#168, #212)"` (add converge.go, converge_test.go, service wrapper file if touched).

---

### Task 3: CLI — verify wiring

**Files:**

- Modify: `cmd/lmm/verify.go`
- Test: colocate with existing verify CLI tests (grep `verify` tests in cmd/lmm; mirror their service/fixture pattern)

**Interfaces:**

- Consumes: `Service.ConvergeDeployedFiles` (Task 2).
- Produces: user-visible behavior only. Plain `verify`: after existing checks, run dryRun and print each candidate as a warning row (match the file's existing warning format, e.g. `! <path> - STALE DEPLOYMENT (<reason>)`) and count them in the existing warnings tally. `--fix`: run with dryRun=false AFTER the existing cache repairs, print each removal (`Fixed: removed <path> (<reason>)` matching the file's existing --fix output style — read the file and copy its conventions exactly), count into the fixed tally. JSON mode (`--json`): emit candidates/removals as entries in the existing `verifyFileJSON` array with `Status: "stale_deployment"` / `"fixed_stale_deployment"` — follow the file's existing status-string style; JSON additions are MINOR-class (established repo precedent), consistent with this branch's class.

- [ ] **Step 1: Write the failing CLI-seam test** — per the #197 lesson, at least one test drives the real verify command path (however the existing verify tests invoke it) against a fixture with one stale row + one dangling link: plain verify reports 2 warnings, `--fix` removes both and reports them fixed, second `--fix` run reports clean. Write against discovered patterns; NEEDS_CONTEXT if verify has no test harness at all (then the test lives at the core seam already covered in Task 2 and this task documents manual verification output in its report — but grep first, verify.go behaviors have tests from #164/#166 era).
- [ ] **Step 2: RED** — new test fails (no such output today).
- [ ] **Step 3: Implement** the wiring per Produces.
- [ ] **Step 4: GREEN** — focused, then `go test ./cmd/lmm/`; `gofmt -l cmd/lmm/`.
- [ ] **Step 5: Commit** — `git commit -m "feat: verify reports and --fix removes stale deployments (#168, #212)"`

---

### Task 4: Polish bundle + CHANGELOG

**Files:**

- Modify: `internal/core/deployable.go` (ModPath reuse), `cmd/lmm/install_test.go` (EOF + uniform-validate tests), `internal/core/service_test.go` or the prune tests' home (organic prune integration test), `docs/configuration.md` (compile bullet rewrite), `CHANGELOG.md`.

**Contracts:**

1. `deployable.go`: compute `versionDir := gameCache.ModPath(gameID, sourceID, modID, version)` once and pass it to `HasRetainedSource` (behavior unchanged; existing tests stay green).
2. `cmd/lmm/install_test.go`: (a) `TestSelectInstallFiles_EOFAfterRejectedSelection` — input `"1,2\n"` (no second line) with the mixed-reject validate → returns an error (not a hang); (b) `TestSelectInstallFiles_ValidateRunsOnFastPaths` — single-file list with an always-error validate → error returned (fast path); two-file list + `installYes=true` (save/restore global) with always-error validate → error returned.
3. Organic prune test: drive a real DeployCompile download twice (mock source serving an exmodz; first ingest under pre-#197-shaped entry seeded with a compiled pak + recorded marker claiming it, then re-download) asserting the stale pak is pruned at the second commit — mirror `TestService_DownloadMod_PrunesUnclaimedStaleFiles`'s fixtures, replacing the hand-seeded debris with a first-generation commit whose marker claims the pak (the honest pre-#197 shape). If the mock-source plumbing can't express two generations, report the limitation and keep the existing test as-is (say so in the report; do not force it).
4. `docs/configuration.md` compile bullet: replace the stale sentence "The downloaded file is compiled into a new artifact before caching (currently Icarus only: an `.exmodz` diff is applied to the game's base data tables to produce a deployable `_P.pak`)" with: "The downloaded `.exmodz` is validated and retained (currently Icarus only); at deploy time, every enabled compile-mode mod's changes are merged — in profile load order — into one profile-level `zzz_LMM_Merged_P.pak` built against the installed game's own base tables. Only sources that implement compiling support this mode." (Keep the rest of the bullet and the Merge precedence paragraph unchanged.)
5. CHANGELOG under `### Added`:

```markdown
- `lmm verify` now detects stale deployments — game-directory files lmm
  deployed that no installed mod still provides, and dangling symlinks
  left pointing into the mod cache — and `verify --fix` removes them
  (provenance-gated: only lmm-attributed files are ever touched). (#168, #212)
```

- [ ] **Steps:** TDD where behavior changes (the two new CLI tests + organic prune test are the RED/GREEN items; ModPath reuse and docs are mechanical), full `go test ./...` + `go vet ./...` + `gofmt -l` at the end, then:
      `git commit -m "test/docs: polish bundle — prune provenance test, chooser edge tests, docs refresh (#168)"`

---

### Task 5: PR

- [ ] `go build -o lmm ./cmd/lmm`; `go vet ./...`; `go test ./...`; `trunk check` if available.
- [ ] Push `feat/168-212-deploy-convergence`; `gh pr create --base develop --title "feat: verify --fix converges stale deployments (#168, #212)"` with body ending `🤖 Generated with [Claude Code](https://claude.com/claude-code)`; note "Fixes #168, #212 — close manually after merge".
- [ ] Copilot triage via `gh-await-review` (background), including post-push rounds.
