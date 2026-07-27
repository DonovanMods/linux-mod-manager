# Source/Search Hygiene Batch Implementation Plan (#52, #58 — EPIC #104)

**Goal:** Close #52 (custom-sources deferred findings, 14 items — all verified still valid on v1.18.0) and #58's polish slice (5 valid items; 2 already resolved; the `--limit` fetch loop splits into its own issue).

**Architecture:** No new subsystems. Small fixes + tests across internal/source/custom, internal/storage/config, internal/core, internal/tui (search), cmd/lmm. One new plan-surface field (`DependencyWarnings` — the plan drafted it as `DependencyErrors`; the implementation settled on the warnings name) and one new aggregate-search honesty field are the only contract changes; both additive.

**Tech Stack:** Go, testify require, in-memory SQLite / t.TempDir per repo test conventions.

## Global Constraints

- Branch: `fix/source-search-hygiene` off `main`; one PR closing #52 and #58.
- TDD for every behavior change (RED evidence recorded); test-only items just land with the suite green.
- gofmt tabs, vet clean, full `go test ./...` per commit; dense why-comments; add files BY NAME (never `git add -A`; IDEAS.md stays untracked).
- Version: PATCH → 1.18.1 (fixes, tests, additive internal fields; no new user-facing features).
- CLI JSON contracts: `search --json` stdout must remain exactly one document — any new honesty notice goes to stderr in JSON mode; `source list --json` empty case becomes `[]` (additive-safe).
- The `--limit N` per-source fetch loop is OUT (feature-ish per epic decision): split into a new issue during finalize; do NOT implement.
- Exploration verdicts (2026-07-27, agents verified per-item against 06e9817) are the factual base for all line references below; lines may drift ±10 during the batch — locate by name.

---

### Task 1: Directory source — symlinks, v/V over-trim, dead field, PageSize test (#52 items 1-4)

**Files:** `internal/source/custom/directory.go` (+`directory_test.go`), `internal/core/importer.go` (+ its tests).

1. **Symlinked subdirs** (item 1, TDD): `scan()` (directory.go:86-94) uses `entry.IsDir()` — false for symlinked dirs, silently dropped. Fix: classify via `os.Stat(entryPath)` (follow). Also `copyDir` (importer.go:192-210) uses `filepath.Walk` (never follows): a nested dir-symlink falls into `copyFileStreaming` → EISDIR. Fix copyDir to handle symlinked dirs (Stat-follow) — or if that balloons, restrict scope to top-level scan following + a clear error for nested dir symlinks; document the choice. RED tests: symlinked mod dir appears in scan/Search; symlinked subdir inside a mod ingests without error (or errors clearly, per chosen scope).
2. **v/V over-trim** (item 2, TDD): `nameAndVersionFrom` (directory.go:154-163) `TrimRight(base[:idx], "-_ vV")` eats a real trailing V: `"ModV-1.0"` → name "Mod" (reproduced). Fix: trim separators `-_ ` unconditionally; trim v/V ONLY when the version match consumed it (extend `domain.ExtractVersionFromName` to report the full match, or re-derive locally). RED table: {"ModV-1.0"→"ModV"}, {"Modv-2.3"→"Modv"}, keep {"MyMod-v1.0"→"MyMod"}, {"ServerV2-1.0"→"ServerV2"}.
3. **Dead `isArchive`** (item 3): delete field (directory.go:73,115) — write-only, verified.
4. **PageSize default test** (item 4): assert `res.PageSize == 20` in the existing zero-PageSize subtest (search.go:39-41 is the code).

Commit: `fix(source): directory source follows symlinks; name trim keeps real V suffixes (#52)`.

### Task 2: Metadata + loader — case-insensitive ModInfo, tests, Unwrap (#52 items 5-9)

**Files:** `internal/source/custom/metadata/modinfo.go`, `metadata/archive.go` (+tests), `internal/storage/config/sources.go` (+`sources_test.go`), `cmd/lmm/root_test.go`.

1. **Case-insensitive ModInfo.xml** (item 5, TDD): `Detect` (modinfo.go:15-21) os.Stats the exact name; archive.go:9-11's own comment tracks this gap. Fix: dir path — list entries, `strings.EqualFold` match; archive path — `findModInfoEntry` (archive.go:64-83) EqualFold on basename. RED: lowercase `modinfo.xml` dir fixture; `MODINFO.XML` zip entry.
2. **V1-missing-Name fixture test** (item 6, test-only): `<ModInfo><Version .../></ModInfo>` without `<Name>` → parse error → fallback path (behavior already correct, pin it).
3. **registerCustomSources direct test** (item 7, test-only): call it (root.go:206-232) with fixture dirs hitting all three skip branches (load error, id collision, construction failure); assert via `svc.ListSources()` (stderr not redirectable — don't assert it).
4. **Unreadable sources dir test** (item 8, test-only): chmod 0000 the sources dir → `LoadSourceDefinitions` hard error (sources.go:32-38). Guard: skip if running as root (`os.Geteuid() == 0`) since chmod won't bite.
5. **SourceLoadError.Unwrap** (item 9, TDD-lite): add `func (e SourceLoadError) Unwrap() error { return e.Err }` (sources.go:17-24) + `errors.As` round-trip test.

Commit: `fix(source): case-insensitive ModInfo.xml; loader error unwrapping + coverage (#52)`.

### Task 3: Core — dependency-error surfacing, staging helper, declared filename (#52 items 10-12)

**Files:** `internal/core/flows.go`, `internal/core/service.go` (+ core tests), `cmd/lmm/install.go` (warning print), possibly `internal/tui/service_core.go` (surface via existing plan warnings seam if one exists — investigate; do not build new UI).

1. **resolveInstallDependencies swallows errors** (item 10, TDD): flows.go:2242-2248 degrades ANY GetDependencies error to "no deps"; existing test codifies it (flows_install_test.go:265-282 — update its expectations). Fix per issue prescription: `errors.Is(err, source.ErrNotSupported)` → silent skip (unchanged); any OTHER error → record into a new additive `InstallPlan.DependencyWarnings []string` (name per existing plan-struct style) and continue (plan still succeeds). Surface: CLI install prints each as a warning line (stderr, matching existing warning style); TUI: if the install-confirm modal already renders plan detail lines, append there — otherwise leave TUI surfacing out and note it (no new UI). RED: mock source whose GetDependencies fails with a non-ErrNotSupported error → plan succeeds AND carries the warning; ErrNotSupported case stays silent.
2. **Staging helper** (item 11, refactor): extract `prepareStaging(...)` from the verbatim-duplicated blocks (service.go:364-374 vs 422-432); drop the two redundant `os.MkdirAll(stagePath, ...)` calls (copyFileStreaming mkdirs internally, importer.go:427). Behavior-preserving — existing tests stay green unchanged.
3. **ingestLocalToCache `file` param** (item 12, TDD): param unused; copy-mode filename uses `filepath.Base(localPath)` (service.go:443) instead of the declared `file.FileName` — latent mismatch bug. Fix: use `file.FileName` when non-empty (fallback to base(localPath)). RED: DownloadableFile{FileName:"declared.zip"} + differently-named temp file → cached name is declared.zip.

Commit: `fix(core): surface dependency-resolution failures; staging helper; honor declared filenames (#52)`.

### Task 4: CLI cosmetics — JSON [], single error report (#52 items 13-14)

**Files:** `cmd/lmm/source.go`, `cmd/lmm/root.go` (+tests).

1. **`source list --json` null** (item 13): `var rows []sourceInfo` → `rows := make([]sourceInfo, 0, …)` (source.go:70); unit-test the empty-encode contract directly (unreachable via CLI today — say so in the test comment).
2. **Double error reporting** (item 14, TDD): `registerCustomSources` warns to os.Stderr during init; `source list` re-derives the same errors as table rows → same message twice per invocation. Fix shape (pick the least invasive that's testable): route `registerCustomSources` warnings through an injectable writer defaulting to os.Stderr, and have the `source list` path suppress the init-time warnings (quiet registration) since it renders them properly as rows. RED: pipe/capture os.Stderr around a source-list run with a broken definition fixture → message appears exactly once across stderr+stdout.

Commit: `fix(cli): source list reports each load error once; empty --json is [] (#52)`.

### Task 5: Aggregate search — pagination overshoot, honesty notice, warning parity (#58 items 1,3,4,5,6)

**Files:** `internal/core/service.go` (+aggregate tests), `internal/tui/service_core.go`, `internal/tui/search.go`, `internal/tui/app.go` (search views), `internal/tui/service.go` (prototype), `cmd/lmm/search.go` (+tests), `internal/tui/sources_view_test.go`.

1. **Pagination overshoot** (item 1, TDD): merged TotalCount is summed but PageSize is per-source (service_core.go:289-310, service.go:14 SearchPageSize=10), so `hasNextPage` (search.go:128-133) and the footer's totalPages (app.go:1512-1514) offer reachable EMPTY pages (3×10-mod sources: page 1 shows all 30, "2/3" reachable and empty). Fix: mark the aggregate page as final when every contributing source returned fewer than its own page size (`AggregateSearchResult` gains an additive `Exhausted bool` or per-source remaining bookkeeping — SearchAllSources already tracks per-source results, service.go:175-227); `hasNextPage`/footer respect it. RED: stub multi-source test constructing the overshoot; assert no next-page offer once all sources exhausted; single-source behavior unchanged (TestPaginationKeysRequeryWithinBounds stays green).
2. **No-searchable-sources honesty notice** (item 3, TDD): attempted==0 currently indistinguishable from genuine zero results (service.go:185-227 skips silently; CLI search.go:180-191 prints "No mods found."; TUI app.go:1303-1313 same). Fix: surface `AttemptedCount` (additive) on AggregateSearchResult; CLI prints a distinct one-liner ("none of <game>'s sources support searching; install by ID instead") — to STDERR in --json mode (one-document invariant); TUI zero-results branch renders the honest variant. RED: all-capabilities-false stub game in core, CLI, and TUI tests.
3. **Prototype warning coverage** (item 4): `prototypeProvider.Search` never sets Warnings (service.go:349-365) — add one canned all-sources-mode warning so demo mode exercises the warning line; test asserts it renders.
4. **Wording parity** (item 5, cosmetic): TUI's no-sources error (search.go:58) drifts from CLI's (cmd/lmm/search.go:76-81 — includes game name, "add sources"); align the TUI string (thread the game name into newSearchModel); update both wording tests to assert parity.
5. **Auth fallback assertion** (item 6, one-liner): add `require.Equal(t, "yes", infos[0].Auth, …)` at sources_view_test.go:~152.

Commit: `fix(search): aggregate pagination stops at exhaustion; honest notice when no source supports search (#58)`.

### Task 6: Finalize

1. Full verification: build, gofmt (scoped), vet, `go test ./...`; TUI goldens should NOT change (no rendering-geometry changes — verify with a regen + `git status docs/assets/tui` clean; restore).
2. Split issue: file "CLI: `--limit N` should page per-source until satisfied or exhausted" with the exploration's current-behavior evidence (cmd/lmm/search.go:112-117 single fetch, page=0) + fix/test sketch; label enhancement; "Part of EPIC #104? NO — deferred out (feature)"; note on #58 that the item moved.
3. CHANGELOG `[1.18.1]` (### Fixed: symlinked mod dirs; v/V trim; case-insensitive ModInfo.xml; dependency-failure warnings; declared filenames; single load-error report; empty `--json` arrays; aggregate pagination overshoot; no-searchable-source notice; wording parity. Keep house voice). Bump root.go → 1.18.1 (separate `chore:` commit).
4. Archive plan doc in-PR. PR closes #52 and #58; body notes: all 14 #52 items verified-then-fixed; #58's resolved items (windowing via #42; the #37 gate per recorded decision — INCLUDE the caveat that capability gating is reactive (post-attempt mapped errors) not proactive (pre-attempt disabled controls), flagged for the owner to confirm intent when reading); smoke gate note (TUI touched in Task 5).
5. Issue closing comments per repo convention.

## Execution notes

- Tasks 1-4 are package-disjoint from each other BUT sequential anyway (skill rule: no parallel implementers in one worktree). Task 5 is the largest. Order 1→2→3→4→5→6.
- Models: Task 4 cheap-tier; 1,2 mid-tier; 3,5 mid-tier with careful review; final whole-branch review most-capable.
- Watch item-10 scope: DependencyWarnings surfacing must not grow into new TUI UI — plan-field + CLI warning is the deliverable.
