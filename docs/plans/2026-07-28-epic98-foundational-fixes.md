# EPIC #98 Foundational Fixes (#94, #95) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the two live correctness bugs blocking EPIC #98 Phase 2: installed mods record the mod-level latest version instead of the installed file's version (#94, including a `lmm verify --fix` repair for pre-existing bad rows), and deploy/switch/import silently substitute the primary file when stored file IDs are gone upstream (#95).

**Architecture:** Two sequential PRs. **PR A (#94)** adds a pure domain helper `EffectiveInstalledVersion` and stamps the resolved file version onto the in-flight `mod` copy after file selection and before the first download in each install-recording flow — one stamp per flow fixes the DB record, the profile `ModReference.Version`, and the cache directory key together, because all three read `mod.Version` downstream. It then adds a `version-record` check to `lmm verify` (network-backed, `--fix`able) that repairs pre-existing bad rows. **PR B (#95)** makes `selectDeployFiles`' primary-file fallback a policy parameter: deploy, profile switch, and import hard-fail that mod with a clear error (other mods continue); the update-apply path keeps the fallback because "use the new version's primary file" is correct update semantics when old file IDs are pruned upstream.

**Tech Stack:** Go, cobra, modernc.org/sqlite (`:memory:` in tests), testify, `t.TempDir()` for cache dirs.

**User decisions (2026-07-28):** #95 = hard-fail the affected mod (no opt-in fallback flag). #94 = forward-only recording fix is NOT sufficient alone (re-deploy doesn't heal old rows; update only heals when upstream moves) — a repair path is required, hence the verify check.

## Global Constraints

- TDD strictly: every behavioral task starts with a failing test, run to observe the failure, then implement, then re-run to green. (`~/.claude/CLAUDE.md`)
- NEVER pipe `go test` output inside a `&&` chain (`go test ... | tail` masks failures). Run bare or capture `$?`. (project memory)
- `git add` files BY NAME — never `git add -A` (untracked `IDEAS.md` must stay untracked). (project memory)
- gofmt formatting (tabs); `go vet ./...` clean before each commit.
- Error wrapping with `%w`; sentinel errors follow the existing pattern (`internal/core/flows.go:938` `errNoDeployFiles` — unexported, `doc comment`, declared above its function).
- Byte-fidelity caution: `internal/core/flows.go` pins many user-facing strings via tests; when changing a message, update the pinning test in the same task, never "approximately".
- CLI/TUI parity: behavior changes land in core so both interfaces inherit them; renderer changes touch both `cmd/lmm/` and `internal/tui/` in the same task.
- Both PRs bump version (MINOR each) + CHANGELOG as their final task; tag on the MERGE COMMIT after merge. (repo conventions)
- Work references GitHub issues #94 and #95 (EPIC #98). PR bodies end with the standard Claude Code attribution.

## File Structure (both PRs)

| File | Role in this plan |
|---|---|
| `internal/domain/mod.go` | New pure helper `EffectiveInstalledVersion` (PR A) |
| `internal/domain/mod_test.go` | Table-driven tests for the helper (PR A) |
| `internal/core/flows.go` | Version stamps in 4 flows (PR A); `selectDeployFiles` policy + call sites (PR B) |
| `internal/core/flows_install_test.go`, `flows_test.go`, `flows_import_test.go`, `flows_update_test.go` | Flow-level tests (both PRs) |
| `cmd/lmm/install.go` | Stamp in `batchInstallMods` (PR A) |
| `cmd/lmm/profile.go` | Stamp in `doProfileApply` (PR A); `selectFilesToDownload` hard-fail (PR B) |
| `cmd/lmm/verify.go` | `version-record` check + `--fix` repair (PR A) |
| `cmd/lmm/verify_test.go` | First behavioral verify tests (PR A) |
| `cmd/lmm/deploy.go` | Remove `DeployFallbackUsed` rendering (PR B) |
| `internal/tui/service_core.go` | Remove fallback progress lines (PR B) |
| `internal/tui/service_core_internal_test.go` | Update pinned renderings (PR B) |
| `CHANGELOG.md`, `cmd/lmm/root.go`, `README.md` | Docs/version (both PRs) |

---

# PR A — #94: record the installed file's version (branch `fix/94-installed-file-version`)

### Task A1: `domain.EffectiveInstalledVersion` helper

**Files:**
- Modify: `internal/domain/mod.go` (near `DownloadableFile`, `mod.go:39-50`)
- Test: `internal/domain/mod_test.go`

**Interfaces:**
- Produces: `func EffectiveInstalledVersion(modVersion string, selected []*DownloadableFile) string` — used by Tasks A2–A7. Rule: first selected file with `IsPrimary && Version != ""`; else first selected file with `Version != ""`; else `modVersion`. Nil entries skipped.

- [ ] **Step 1: Write the failing table-driven test** in `internal/domain/mod_test.go`:

```go
func TestEffectiveInstalledVersion(t *testing.T) {
	f := func(id, version string, primary bool) *domain.DownloadableFile {
		return &domain.DownloadableFile{ID: id, Version: version, IsPrimary: primary}
	}
	tests := []struct {
		name     string
		modVer   string
		selected []*domain.DownloadableFile
		want     string
	}{
		{"no files falls back to mod version", "1.5", nil, "1.5"},
		{"single file with version wins", "1.5", []*domain.DownloadableFile{f("10", "1.0", false)}, "1.0"},
		{"file without version falls back", "1.5", []*domain.DownloadableFile{f("10", "", true)}, "1.5"},
		{"primary file version preferred over earlier non-primary", "1.5",
			[]*domain.DownloadableFile{f("10", "0.9-patch", false), f("11", "1.0", true)}, "1.0"},
		{"first non-empty when no primary has a version", "1.5",
			[]*domain.DownloadableFile{f("10", "", true), f("11", "1.0", false)}, "1.0"},
		{"nil entries skipped", "1.5", []*domain.DownloadableFile{nil, f("10", "1.0", false)}, "1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, domain.EffectiveInstalledVersion(tt.modVer, tt.selected))
		})
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/domain/... -run TestEffectiveInstalledVersion -v` — expect FAIL (undefined function).
- [ ] **Step 3: Implement** in `internal/domain/mod.go`, directly below the `DownloadableFile` struct:

```go
// EffectiveInstalledVersion resolves the version string that describes what
// the selected files actually are: the primary selected file's Version when
// it carries one, else the first selected file with a non-empty Version,
// else modVersion (the mod-level version). Install-recording flows stamp
// this onto the mod before downloading so the DB row, the profile
// ModReference, and the cache directory key all describe the bytes on disk
// (issue #94) instead of the mod-level latest.
func EffectiveInstalledVersion(modVersion string, selected []*DownloadableFile) string {
	for _, f := range selected {
		if f != nil && f.IsPrimary && f.Version != "" {
			return f.Version
		}
	}
	for _, f := range selected {
		if f != nil && f.Version != "" {
			return f.Version
		}
	}
	return modVersion
}
```

- [ ] **Step 4: Run the test again** — expect PASS. Also run `go test ./internal/domain/...` (whole package) and `go vet ./internal/domain/...`.
- [ ] **Step 5: Commit** `git add internal/domain/mod.go internal/domain/mod_test.go && git commit -m "feat(domain): EffectiveInstalledVersion resolves file-level install version (#94)"`

### Task A2: stamp in `applyInstallPrimary` (single/primary install)

**Files:**
- Modify: `internal/core/flows.go:2889-2910` (`applyInstallPrimary`)
- Test: `internal/core/flows_install_test.go`

**Interfaces:**
- Consumes: `domain.EffectiveInstalledVersion` (Task A1).
- Context: `mod := plan.Mod` local copy at `flows.go:2890`; downloads via `s.DownloadModToCache(..., &mod, file, ...)` key the cache by `mod.Version` (`service.go:572`); the DB save (`flows.go:3028` area, `InstalledMod{Mod: mod}`) and reinstall-transaction check (`flows.go:2910`, `plan.Replaces.Version == mod.Version`) also read it. The stamp goes AFTER `mod := plan.Mod` and BEFORE the reinstall-transaction check, so a same-file reinstall (whose recorded version now equals the file version) still takes the transaction path.

- [ ] **Step 1: Write the failing test** in `internal/core/flows_install_test.go`. Use the existing helpers (`newFlowsTestService`, `mockSourceWithDownloads`, `createTestZip`) and the embed-and-override mock pattern (e.g. `sizedFileSource` at `flows_install_test.go:65`):

```go
// oldFileSource serves two files: the primary/latest (v1.5) and an archived
// older file (v1.0). mockSource's default GetModFiles is overridden.
type oldFileSource struct {
	*mockSourceWithDownloads
}

func (s *oldFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "1", Name: "Main File", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true},
		{ID: "2", Name: "Old File", FileName: mod.ID + "-old.zip", Version: "1.0"},
	}, nil
}

func TestApplyInstall_ExplicitOldFile_RecordsFileVersionAndCacheKey(t *testing.T) {
	// Mirror the CLI --file path: PlanInstall, then overwrite plan.Files with
	// the non-primary old file (cmd/lmm/install.go:497-513 does exactly this).
	// Assert the DB row records "1.0" (not the mod-level "1.5"), the cache
	// path is keyed "1.0", and CheckUpdates subsequently OFFERS an update -
	// the user-visible symptom of #94 (an older install must not suppress
	// its own update notifications).
}
```

Test body outline (write it fully in the task):
1. Build service with an `oldFileSource` whose mod has `Version: "1.5"`; register download content for file ID `"2"` via `AddDownload`.
2. `plan, err := svc.PlanInstall(...)`; `require.NoError`; set `plan.Files = []domain.DownloadableFile{{ID: "2", ..., Version: "1.0"}}` (copy the old file entry from `GetModFiles`).
3. `_, err = svc.ApplyInstall(ctx, game, plan, opts)`; `require.NoError`.
4. Assert DB: `svc.GetInstalledMod(...)` row `.Version == "1.0"`.
5. Assert cache: `svc.GetGameCache(game).Exists(game.ID, sourceID, modID, "1.0")` is true and `.Exists(..., "1.5")` is false.
6. Assert update visibility: run `core.NewUpdater(registry).CheckUpdates(...)` against the same source and require exactly one update with `NewVersion == "1.5"` (mockSource-based sources use `domain.IsNewerVersion(inst.Version, remoteMod.Version)`; confirm the mock's `CheckUpdates` supports this — if `mockSource` has no `CheckUpdates`, assert instead via `domain.IsNewerVersion("1.0", "1.5")` == true AND add the CheckUpdates assertion at the source-mock level that does support it, e.g. `updateMockSource` in `updater_test.go:17`).

- [ ] **Step 2: Run** `go test ./internal/core/... -run TestApplyInstall_ExplicitOldFile -v` — expect FAIL (row records "1.5", cache keyed "1.5").
- [ ] **Step 3: Implement.** In `applyInstallPrimary` (`flows.go:2890`), immediately after `mod := plan.Mod` and before the reinstall-transaction check at `:2910`:

```go
	// #94: record what is actually being installed. plan.Files is the final
	// selection (the CLI --file/picker path overwrites it after PlanInstall),
	// and mod.Version keys the cache, the DB row, and the profile ref below.
	selectedFiles := make([]*domain.DownloadableFile, len(plan.Files))
	for i := range plan.Files {
		selectedFiles[i] = &plan.Files[i]
	}
	mod.Version = domain.EffectiveInstalledVersion(mod.Version, selectedFiles)
```

- [ ] **Step 4: Run the test — PASS.** Then run the whole core package bare: `go test ./internal/core/...` (some existing tests may pin "1.5"-style recorded versions via the default `mockSource` single-file fixture whose file has no `Version` field set — those keep passing because empty file versions fall back to `mod.Version`; investigate any failure individually, do not blindly update goldens).
- [ ] **Step 5: Commit** `git add internal/core/flows.go internal/core/flows_install_test.go && git commit -m "fix(core): install records the selected file's version, cache keyed to match (#94)"`

### Task A3: stamp in `applyInstallBatchMod` (dependency/batch installs)

**Files:**
- Modify: `internal/core/flows.go:2757-2762` (`applyInstallBatchMod`)
- Test: `internal/core/flows_install_test.go`

**Interfaces:** Consumes `domain.EffectiveInstalledVersion`. Context: this path ignores `plan.Files` and re-resolves per mod — `selected, _, err := selectDeployFiles(files, nil)` at `flows.go:2757`, then `file := selected[0]` at `:2762`; downloads at `:2775` and the DB save at `:2809` read `mod.Version` through the `mod *domain.Mod` pointer (safe to stamp: `plan.Dependencies` entries are not re-read after apply).

- [ ] **Step 1: Failing test:** a dependency mod whose source reports mod-level `Version: "2.0"` but whose primary file carries `Version: "2.0.1"`; install the parent; assert the dependency's DB row and cache dir carry `"2.0.1"`.
- [ ] **Step 2: Run it** — FAIL.
- [ ] **Step 3: Implement.** After `file := selected[0]` (`flows.go:2762`), before the `fileEvt` emit:

```go
	mod.Version = domain.EffectiveInstalledVersion(mod.Version, selected) // #94
```

- [ ] **Step 4: Re-run — PASS**; run `go test ./internal/core/...` bare.
- [ ] **Step 5: Commit** `git add internal/core/flows.go internal/core/flows_install_test.go && git commit -m "fix(core): batch/dependency installs record file-level version (#94)"`

### Task A4: stamp in `ApplyProfileSwitch` and `ApplyImport`

**Files:**
- Modify: `internal/core/flows.go:1929-1933` (switch) and `:3822-3826` (import)
- Test: `internal/core/flows_test.go`, `internal/core/flows_import_test.go`

**Interfaces:** Consumes `domain.EffectiveInstalledVersion`. Context: switch — `mod` fetched at `:1903`, `filesToDownload` from `selectDeployFiles(files, ref.FileIDs)` at `:1920`, downloads at `:1941`, save at `:1967`, profile ref at `:1980`. Import — `mod` at `:3785`, selection at `:3813`, downloads at `:3834`, save at `:3856`, ref at `:3869`. Stamp between the `usedFallback` emit block and the download loop in each.

- [ ] **Step 1: Failing tests** (one per flow, mirroring `TestService_ApplyProfileSwitch_InstallLoop_FallbackUsedWhenStoredFileIDsNotFound` at `flows_test.go:2673` for setup style, and `TestApplyImportRedownloadUsesStoredFileIDs` at `flows_import_test.go:328`): source lists file `"1"` `Version: "1.0"` (matching the stored `FileIDs`) while the mod-level version is `"1.5"`; run the flow; assert the saved row's `.Version == "1.0"` and cache `Exists(..., "1.0")`.
- [ ] **Step 2: Run both** — FAIL.
- [ ] **Step 3: Implement**, identically in both flows:

```go
			mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94
```

(Placement: switch — after the `usedFallback` block ending `:1930`, before `var downloadedFileIDs`; import — after the block ending `:3822`, before the download loop at `:3826`.)

- [ ] **Step 4: Re-run — PASS**; `go test ./internal/core/...` bare.
- [ ] **Step 5: Commit** `git add internal/core/flows.go internal/core/flows_test.go internal/core/flows_import_test.go && git commit -m "fix(core): profile switch and import record file-level version (#94)"`

### Task A5: stamp the two live CLI-side sites

**Files:**
- Modify: `cmd/lmm/install.go:1040-1079` (`batchInstallMods`), `cmd/lmm/profile.go:1055-1097` (`doProfileApply`)
- Test: existing cmd-level test files (`cmd/lmm/install_test.go`, `cmd/lmm/profile_test.go`) — follow their established fake-service/temp-dir setup.

**Interfaces:** Consumes `domain.EffectiveInstalledVersion`. Context: `batchInstallMods` selects via `selectPrimaryFile(files)` (returns `*domain.DownloadableFile`, `profile.go:1133`) then saves `InstalledMod{Mod: *mod}` at `install.go:1079` and the profile ref at `:1103`. `doProfileApply` selects via `selectFilesToDownload` at `profile.go:1055`, downloads at `:1074` (cache keyed by `mod.Version`), saves at `:1097`, ref at `:1114`. (The two `cmd/lmm/import.go` sites are LOCAL imports — no `DownloadableFile` exists, version comes from filename parsing — out of #94's scope.)

- [ ] **Step 1: Failing tests**: cmd-level, seeding a source whose primary file version differs from the mod version; run the command path; assert the DB row's version equals the file version. If cmd-level harnessing is disproportionate, test the same behavior through the core flows these paths mirror AND add a minimal cmd assertion on the saved row.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.** `batchInstallMods`, after the `selectPrimaryFile` call:

```go
	mod.Version = domain.EffectiveInstalledVersion(mod.Version, []*domain.DownloadableFile{file}) // #94
```

`doProfileApply`, after the `usedFallback` warning block (`profile.go:1061-1063`), before the download loop:

```go
			mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94
```

- [ ] **Step 4: Re-run — PASS**; `go test ./cmd/lmm/...` bare.
- [ ] **Step 5: Commit** `git add cmd/lmm/install.go cmd/lmm/profile.go cmd/lmm/install_test.go cmd/lmm/profile_test.go && git commit -m "fix(cli): batch install and profile apply record file-level version (#94)"`

### Task A6: `lmm verify` version-record check (report only)

**Files:**
- Modify: `cmd/lmm/verify.go` (`doVerify` at `:84`, `verifyFileJSON` at `:21-33`, `Long` doc at `:38-67`)
- Test: `cmd/lmm/verify_test.go`

**Interfaces:**
- Consumes: `svc.GetInstalledMods(game.ID, profile)` (`service.go:649`), `svc.GetModFiles` (`service.go:371`), `domain.EffectiveInstalledVersion`.
- Produces: new `verifyFileJSON.Status` literals `"version_mismatch"` (bucket `issues`, fixable in Task A7) and `"version_unverifiable"` (bucket `warnings`, not fixable — stored file IDs no longer exist upstream). Task A7 extends this check with the `--fix` branch.

Check semantics (a pre-pass loop alongside the file-count pre-pass at `verify.go:129`, one entry per installed mod, `FileID` left empty):
- Skip silently: `SourceID == domain.SourceLocal`, `ManualDownload`, or `len(FileIDs) == 0`.
- `GetModFiles` error → `warnings++`, text "could not check version (source unreachable)", JSON status `skipped`.
- Stored `FileIDs` matched against the returned list → `effective := domain.EffectiveInstalledVersion(mod.Version, matched)`. If no stored ID matches → `"version_unverifiable"` + reinstall hint. If `effective != mod.Version` → `"version_mismatch"` (report shows both values). Else `ok` (counted, not printed per-line — matching the existing quiet-ok convention).
- The `Long` help text gains a paragraph: the version-record check contacts each mod's source; statuses documented incl. which are fixable.

- [ ] **Step 1: Write failing behavioral tests** (the file currently has only cobra-wiring smoke tests — these are the first): seed an installed row with `Version: "1.5"`, `FileIDs: ["2"]` against a fake source whose file `"2"` reports `Version: "1.0"` → expect `version_mismatch` in text and JSON with `issues == 1`; a second case where the source no longer lists `"2"` → `version_unverifiable`, `warnings == 1`. Follow the cmd-package test conventions (TestMain temp-dir flooring from `main_test.go`; build a `core.ServiceConfig` like `install_gameid` tests do).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** the pre-pass loop + status plumbing + `Long` text.
- [ ] **Step 4: Re-run — PASS**; `go test ./cmd/lmm/...` bare.
- [ ] **Step 5: Commit** `git add cmd/lmm/verify.go cmd/lmm/verify_test.go && git commit -m "feat(cli): verify detects version-record mismatches (#94)"`

### Task A7: `verify --fix` repairs version records

**Files:**
- Modify: `cmd/lmm/verify.go`
- Test: `cmd/lmm/verify_test.go`

**Interfaces:**
- Consumes: Task A6's `version_mismatch` detection; `svc.GetGameCache(game).ModPath(...)` (`cache.go:29`) with `os.Rename` (precedent: `cmd/lmm/import.go:160-169`); `svc.GetInstalledMod` + `svc.SaveInstalledMod` (`service.go:884`, `:820` — full-row load-mutate-save, deliberately NOT `UpdateModVersion`, which would shift the wrong value into `previous_version` and poison rollback); `getProfileManager(service)` (as used at `install.go:971`) + `pm.UpsertMod(gameID, profile, domain.ModReference{SourceID, ModID, Version: effective, FileIDs: mod.FileIDs})`; `svc.GetInstaller(game).Install(...)` for the re-link.

Fix sequence per `version_mismatch` row (guarded on `verifyFix && mod.SourceID != domain.SourceLocal`, matching `redownloadModFile`'s gate at `verify.go:296`):
1. Cache re-key: if `oldPath := ModPath(..., mod.Version)` exists and `newPath := ModPath(..., effective)` does not → `os.Rename(oldPath, newPath)`. If the new path already exists, leave the cache alone and continue (report notes it). Rename failure → the row stays `version_mismatch`, error printed, no DB write (never let the DB and cache disagree in a NEW way).
2. DB: load the full row, set `Version = effective`, `SaveInstalledMod`.
3. Profile ref: `pm.UpsertMod` with the corrected version.
4. Re-link: if the mod is `Deployed` and `LinkMethod == domain.LinkSymlink`, the game-dir symlinks point INTO the renamed cache dir and are now dangling — re-run `installer.Install(ctx, game, &mod.Mod, profile)` to refresh them. Hardlink/copy deployments are untouched by the rename.
5. On full success: mutate the row's JSON entry to `ok` and decrement `issues` (the existing in-place pattern at `verify.go:206-210`); text prints "Repaired: <old> → <new>".

- [ ] **Step 1: Write failing tests**: (a) mismatch row, not deployed → after `--fix`: DB row shows the file version, cache `Exists` under the new key and not the old, profile YAML ref updated, JSON status `ok`, `issues == 0`; (b) mismatch row, `Deployed` + symlink → additionally assert the deployed symlink resolves post-fix (use `t.TempDir()` game dir); (c) rename-blocked case (pre-create the new-version cache dir) → DB IS still fixed, cache left as-is, note emitted.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** the repair helper (model: `redownloadModFile`, `verify.go:296`).
- [ ] **Step 4: Re-run — PASS**; `go test ./cmd/lmm/...` bare; `go vet ./...`.
- [ ] **Step 5: Commit** `git add cmd/lmm/verify.go cmd/lmm/verify_test.go && git commit -m "feat(cli): verify --fix repairs version records, re-keys cache, re-links (#94)"`

### Task A8: PR A docs, CHANGELOG, version bump, PR

- [ ] **Step 1:** README verify section (if present — `grep -n "verify" README.md`) gains the version-record check + the note that verify now contacts sources; regenerate man pages if help text changed (`make man`, drift test will fail otherwise).
- [ ] **Step 2:** CHANGELOG: `### Fixed` — #94 record/cache fix; `### Added` — verify version-record check + `--fix` repair. Move `[Unreleased]` → `## [1.23.0] - 2026-07-28` (adjust if the current version differs from v1.22.0; MINOR because of the new verify capability + JSON statuses), update comparison links.
- [ ] **Step 3:** Bump `version` in `cmd/lmm/root.go` to `1.23.0`. Commit separately: `chore: bump version to 1.23.0`.
- [ ] **Step 4:** Full gate, each command run bare: `go fmt ./...`, `go vet ./...`, `go test ./...`, `trunk check`. Then push branch, open PR "fix(core): record the installed file's version, not the mod's latest (#94)" referencing EPIC #98; run Copilot triage rounds per repo convention (`gh-await-review` via background Bash).

---

# PR B — #95: no silent primary-file fallback (branch `fix/95-no-silent-fallback`; branch from main AFTER PR A merges — it edits the lines adjacent to A4's stamps)

### Task B1: `selectDeployFiles` fallback becomes a policy

**Files:**
- Modify: `internal/core/flows.go:993-1026` (+ sentinel above it), call sites `:1321` (deploy), `:1920` (switch), `:2200` (PlanInstall), `:2757` (batch), `:3219` (update), `:3813` (import)
- Test: `internal/core/flows_test.go`, `flows_import_test.go`, `flows_update_test.go`

**Interfaces:**
- Produces: `func selectDeployFiles(files []domain.DownloadableFile, storedFileIDs []string, allowFallback bool) ([]*domain.DownloadableFile, bool, error)` and sentinel `errStoredFilesUnavailable`. With `allowFallback == false`, a would-be fallback returns `nil, false, fmt.Errorf("%w (file ID(s): %s) - reinstall the mod or run 'lmm update' to adopt the current version", errStoredFilesUnavailable, strings.Join(storedFileIDs, ", "))`. With `true`, behavior is unchanged (update-apply semantics: falling back to the NEW version's primary file is correct when a source prunes old files — CurseForge routinely does; hard-failing there would break updates. The update path's own silent-substitution subtleties belong to #96).
- Call-site policy: deploy `:1321` → `false`, existing `skip(err.Error())` at `:1323` already routes the error to `result.Skipped` + `DeploySkipped`; switch `:1920` → `false`, `fail(err.Error())` at `:1922` routes to `SwitchInstallError`; import `:3813` → `false`, `fail(err.Error())` at `:3815` routes to `ImportModFailed` (+`Failed++`); update `:3219` → `true`; nil-ID sites `:2200`/`:2757` → `false` (unreachable branch — no stored IDs means no fallback).
- Dead phases removed: `DeployFallbackUsed` (`flows.go:306`), `SwitchFallbackUsed` (`:452`), `ImportFallbackUsed` (`:787`) and their emit blocks (`:1325-1329`, `:1925-1930`, `:3818-3822`).

- [ ] **Step 1: Write the failing tests first:**
  - Deploy (currently UNTESTED fallback — green-field): `TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError` mirroring `TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent` (`flows_test.go:1093`): stored `FileIDs: ["stale-id"]` vs mock's only file `"1"`; assert `require.NoError` overall (per-mod failure never fails the deploy), exactly one `result.Skipped` entry containing "no longer available upstream" and "stale-id", `DeploySkipped` fired, `DeployFallbackUsed` NOT fired, and the mod NOT deployed.
  - Switch: convert `TestService_ApplyProfileSwitch_InstallLoop_FallbackUsedWhenStoredFileIDsNotFound` (`flows_test.go:2673`) into `..._StoredFileIDsGone_FailsMod`: expect `SwitchInstallError` with the message, mod not installed, and (add a second mod) the loop continuing.
  - Import: mirror `TestApplyImportPartialFailure` (`flows_import_test.go:276`): `ImportModFailed` + `Failed == 1`, no substitution.
  - Update keeps fallback: `TestApplyUpdate_StoredFileIDsGoneUpstream_FallsBackToPrimary` in `flows_update_test.go`: stored IDs absent from the new version's file list and no `FileIDReplacements` → update succeeds using the primary file.
- [ ] **Step 2: Run all four** — FAIL (first three see substitution; fourth passes only after signature change compiles — expected compile failure counts as the observed failure here).
- [ ] **Step 3: Implement:** sentinel + signature + branch; update all six call sites; delete the three phases, their emit blocks, and their `usedFallback` locals.
- [ ] **Step 4:** `go test ./internal/core/...` bare — PASS, including pre-existing pins (the negative-assertion idiom from `flows_test.go:1093`'s family guards double-printing).
- [ ] **Step 5: Commit** `git add internal/core/flows.go internal/core/flows_test.go internal/core/flows_import_test.go internal/core/flows_update_test.go && git commit -m "fix(core): stored-file-gone is a per-mod failure, not a silent fallback (#95)"`

### Task B2: CLI + TUI renderer cleanup

**Files:**
- Modify: `cmd/lmm/deploy.go:171-172` (remove `DeployFallbackUsed` case), `cmd/lmm/profile.go:364-366`, `:503-505` (remove `SwitchFallbackUsed`/`ImportFallbackUsed` cases), `internal/tui/service_core.go:1107-1108` (switch line), `:1643-1644` (import line)
- Test: `internal/tui/service_core_internal_test.go` (`:44-48` pins the fallback rendering — update), plus any cmd test pinning the warning strings (`grep -rn "using primary" cmd internal --include="*_test.go"`)

- [ ] **Step 1:** Update the pinned-rendering tests to expect the fallback lines GONE (compile errors from the removed phase constants guide the sweep — that is the failing state).
- [ ] **Step 2:** Remove the render cases in CLI and TUI.
- [ ] **Step 3:** `go test ./cmd/lmm/... ./internal/tui/...` bare — PASS. `go vet ./...`.
- [ ] **Step 4: Commit** `git add cmd/lmm/deploy.go cmd/lmm/profile.go internal/tui/service_core.go internal/tui/service_core_internal_test.go && git commit -m "fix(tui,cli): drop dead fallback-warning renderers (#95)"`

### Task B3: CLI `selectFilesToDownload` (doProfileApply) same policy

**Files:**
- Modify: `cmd/lmm/profile.go:1151` (`selectFilesToDownload`) and its only call site `:1055-1063`
- Test: `cmd/lmm/profile_test.go`

**Interfaces:** Mirror B1 exactly on the CLI copy (it exists for documented duplication reasons — `flows.go:2234`): signature `(files []domain.DownloadableFile, storedFileIDs []string) ([]*domain.DownloadableFile, error)` — single caller is deploy-class, so no `allowFallback` parameter needed; would-be fallback returns the same wrapped error text as core's. Call site replaces the `usedFallback` warning with `fmt.Printf("    Error: %v\n", err)` + `continue` (the pattern already present at `:1056-1058`).

- [ ] **Step 1:** Failing test: profile apply with stale stored FileIDs → mod skipped with the upstream-gone error, no substitution, other mods proceed.
- [ ] **Step 2:** Run — FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test ./cmd/lmm/...` bare — PASS.
- [ ] **Step 5: Commit** `git add cmd/lmm/profile.go cmd/lmm/profile_test.go && git commit -m "fix(cli): profile apply fails mods whose stored files are gone upstream (#95)"`

### Task B4: PR B docs, CHANGELOG, version bump, PR

- [ ] **Step 1:** `grep -rn "using primary" docs README.md` — update any documentation of the old warning to describe the new failure + remediation. Regenerate man pages if help text changed.
- [ ] **Step 2:** CHANGELOG `### Changed`: deploy/switch/import now fail a mod (with reinstall/update hint) instead of silently substituting the primary file; update-apply intentionally retains the fallback. `## [1.24.0]` + links.
- [ ] **Step 3:** Bump `cmd/lmm/root.go` to `1.24.0`; separate `chore: bump version to 1.24.0` commit.
- [ ] **Step 4:** Full gate (bare): `go fmt ./...`, `go vet ./...`, `go test ./...`, `trunk check`. **This PR archives this plan doc** (move to `docs/plans/archive/`) per repo convention. Push, open PR "fix(core): fail deploys when stored files are gone upstream (#95)" referencing EPIC #98, Copilot triage rounds.

---

## Post-merge (both PRs)

- Tag each release on its MERGE COMMIT (repo convention; lightweight tags).
- Comment on #94 and #95; close them (check them off in EPIC #98's body — they are the "Foundational fixes" tier).
- #93 remains open (interim guard shipped v1.18.2; real implementation belongs to #96).
- Note for #96's design: the update-apply path still records `Update.NewVersion` rather than the downloaded file's version — deliberate here (it breaks the sloppy-upstream-metadata update loop: a mod whose author bumps the mod version without new files gets ONE spurious-looking update offer that resolves on apply, instead of looping forever), but #96's version→file resolver should revisit it.

## Self-review notes

- Spec coverage: #94 record fix (A2-A5 cover all six live recording sites; the two `cmd/lmm/import.go` sites are local imports with no file version — out of scope by construction), cache keying (same stamp — verified against the blast-radius list: every cache read keys off the recorded `mod.Version`, so record==key holds by construction), repair (A6-A7 per user decision), update-offer symptom (A2 step 1.6). #95 hard-fail (B1) in all three flows + CLI copy (B3), TUI parity (B2), update exemption tested (B1).
- Deliberate exclusions: update-apply fallback retained (rationale in B1); local-import version semantics unchanged; `verify` stays CLI-only (no TUI verify surface exists — not a new parity gap).
