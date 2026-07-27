# Release-Hygiene Tail Implementation Plan (EPIC #104 — final item)

**Goal:** Close EPIC #104's Release-hygiene section: fill the worst *meaningful* coverage gaps (not chase numbers), put `-race` into the test run and CI, leave fmt/vet/test/trunk clean, final CHANGELOG/bump.

**Measured baseline (2026-07-27, main = 13bb4f1 / v1.20.0):**
- Coverage vs targets: Domain 78.1 (target 90), Storage cache 63.3 / config 65.6 / db 68.8 (target 85), Core 81.7 ✓ (80), Source: custom 90.3 ✓ / httpclient 78.6 ✓ / curseforge 62.8 / nexusmods 67.6 / steam 40.8 (target 75), linker 39.6 (untargeted, repo-lowest), cmd/lmm 47.4 (key-paths audit).
- `-race ./...`: CLEAN, all 17 packages.
- CI: `.github/workflows/` contains ONLY `release.yml` — there is no test workflow.
- trunk: no launcher in this sandbox; user runs it locally / CI.

**Function-level gap analysis (what's actually untested):** linker Undeploy/IsDeployed (copy+hardlink), symlink IsDeployed, CleanupEmptyDirs — all 0%; db UpdateModPolicy/SetModEnabled/SetModLinkMethod/SetModFileIDs+replaceModFileIDs — 0%; config Save/DeleteGame 0%, ListProfiles 12.5%, DeleteProfile 25%; domain ParseLinkMethod/ParseDeployMode + two String methods 0%; cache Size 0%; steam DetectGames/GetLibraryPaths/vdf getLibraryPaths/FindSteamRoots 0%; curseforge client GetGame/GetMods/GetModFile/GetCategories + trivial getters 0%; nexusmods client GetLatestUpdated/GetTrending + trivial getters 0%; cmd/lmm runConflicts 0%. NOT worth chasing: interactive stdin prompts (promptForSource, game_add flows, readAPIKey), the ExchangeToken error stub.

## Global Constraints

- Branch: `chore/phase7-release-tail` off main (13bb4f1). PR references #104 (does NOT close it — the epic closes after this merges and boxes are checked, in a separate step).
- Version: **PATCH → 1.20.1** (tests + CI infra, no user-facing behavior change).
- These are tests FOR EXISTING BEHAVIOR: they must pass against current code. **If writing a test exposes a real bug, STOP on that item and report it to the coordinator** — do not silently change production code. Zero production-code changes are expected in Tasks 1–3 (a trivial test seam is allowed only if unavoidable, reported, and behavior-preserving).
- House testing style: table-driven, testify require/assert, `:memory:` SQLite, `t.TempDir()`, `t.Parallel()` where safe. gofmt tabs; per-commit: build, gofmt -l empty, vet, full suite. Add files BY NAME; IDEAS.md stays untracked. The man drift test guards docs/man — no help-text edits in this batch.
- Don't chase percentages: cover the listed functions' real behavior (success + representative failure paths), then stop. No tests for trivial getters that a linter would flag as tautological — EXCEPT where one table covers many cheaply.

## Tasks

### Task 1: domain + storage tests

- `internal/domain`: table tests for ParseLinkMethod and ParseDeployMode (valid values, case handling as implemented, invalid → error/default per actual code) and the two String methods (game.go:12, game.go:59) — one table each covering all enum values.
- `internal/storage/config`: Save (round-trip: Save then Load, field fidelity incl. omitted/default fields; write-error path via unwritable dir), DeleteGame (existing, missing, persistence after reload), ListProfiles (empty dir, populated, non-YAML files ignored — verify actual behavior first), DeleteProfile (existing, missing).
- `internal/storage/db`: UpdateModPolicy, SetModEnabled, SetModLinkMethod, SetModFileIDs/replaceModFileIDs — in-memory DB, install a mod via existing helpers, mutate, read back; missing-mod error paths. Check what db.go New's uncovered 62% is (38.1%) — likely error branches; cover the cheap ones (bad path).
- `internal/storage/cache`: Size (populated cache vs empty vs missing dir).

### Task 2: linker deploy-lifecycle tests

For EACH of symlink/hardlink/copy strategies (shared table or per-strategy subtests over `t.TempDir()` source/target trees):
- Deploy → IsDeployed true → Undeploy → IsDeployed false, files actually gone from target, source untouched.
- IsDeployed on never-deployed target → false; Undeploy on never-deployed → verify actual semantics (error vs no-op) and pin it.
- Copy strategy specifics: Undeploy must remove copies; hardlink: Undeploy removes links, source data intact.
- CleanupEmptyDirs: nested empty dirs removed, non-empty preserved, missing root handled per actual behavior.

### Task 3: source + CLI key-path tests

- `internal/source/steam`: vdf getLibraryPaths with fixture libraryfolders.vdf content (both current map-format and any legacy format the parser handles); GetLibraryPaths/getLibraryPathsFromMap via a temp steam root; DetectGames with a fabricated library tree (appmanifest files) — check how FindSteamRoots discovers roots first: if it's hardwired to $HOME paths, test via HOME override (t.Setenv) rather than a code seam.
- `internal/source/curseforge`: httptest-mocked client tests for GetGame, GetMods, GetModFile, GetCategories (success + non-200 + malformed JSON); trivial getters (SetAPIKey/IsAuthenticated/AuthURL) folded into one small test; resolveGameID's uncovered branches.
- `internal/source/nexusmods`: GetLatestUpdated/GetTrending mocked; one compact test covering ID/Name/AuthURL/SetAPIKey/IsAuthenticated; ValidateAPIKey mocked success/failure. Skip ExchangeToken beyond asserting its documented error (one line, it's the OAuth-refusal stub).
- `cmd/lmm`: runConflicts — drive via the real command path with a prepared temp config/db (existing test helpers show how install/list tests bootstrap state): no-conflicts case, conflict case (two mods shipping the same path — check how conflicts are computed; reuse core helpers if setup is heavy), and `--json` shape. This is the "CLI key paths" item — runConflicts only; do not chase cmd/lmm's percentage.

### Task 4: CI + finalize

1. `.github/workflows/test.yml` (new): on push to main + pull_request — checkout, setup-go (version from go.mod), `gofmt -l` must be empty, `go vet ./...`, `go test -race ./...`. Minimal, no lint step (trunk stays a local/user concern), no coverage upload.
2. `Makefile`: `## test-race: Run tests with the race detector` target (GOCACHE_LOCAL pattern); .PHONY.
3. Verification sweep: build, gofmt, vet, full suite, `go test -race ./...`, `make man` idempotency (should be untouched), coverage re-run — record the new per-package numbers in the PR body (before/after table).
4. CHANGELOG `[1.20.1]`: Added — CI test workflow (gofmt/vet/race), `make test-race`; Fixed/Internal — coverage backfill summary (linker lifecycle, storage mutations, steam/curseforge/nexusmods clients, domain parsers, conflicts CLI). Bump root.go → 1.20.1 (man regen — version header — include in bump commit). Archive this plan doc. PR references #104; Copilot triage; user gate is review-only (test-only change, no smoke needed beyond CI green).

## Execution notes

Tasks 1–3 are test-only and independent but run SEQUENTIALLY on the one branch (established pattern; avoids tree conflicts). Sonnet implementers, per-task sonnet review, final whole-branch review most-capable. Any bug a test exposes: STOP, report, coordinator decides (fix-in-batch vs file an issue).
