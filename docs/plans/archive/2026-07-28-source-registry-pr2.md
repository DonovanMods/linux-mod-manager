# Source Registry PR 2 (Behavior + #75) Implementation Plan — closes #76 and #75

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Execute task-by-task with per-task review, per the repo's established SDD pattern.

**Goal:** The three behavioral normalizations (deploy source resolution, import matching, registry-driven game add) plus game-scoped source listings — completing the source-registry design and closing #76 and #75.

**Architecture:** Spec is [2026-07-28-source-registry-design.md](2026-07-28-source-registry-design.md) §§4–5 plus the PR-2 halves of §§6–8, built on PR 1's landed mechanism (v1.21.0: metadata interfaces, unified pipeline, `TypeLabelOf`, `GameCatalog` on CurseForge). Core gains `SourcesForGame`; the CLI's last identity-keyed flows normalize onto the registry; listings scope to the active game in both interfaces.

**Tech stack:** Go 1.25, cobra, bubbletea, testify.

## Global Constraints

- Branch `feat/source-registry-behavior` off main (9fc02cd). PR **closes #76 AND #75**. Version: MINOR → 1.22.0 at finalize.
- TDD RED-first with recorded evidence; gofmt (tabs); per-commit: build, `gofmt -l` empty, `go vet ./...`, full suite `GOCACHE=$(pwd)/.go-mod/cache go test ./...` **run bare or with explicit exit-code capture — NEVER piped in a `&&` chain** (recorded lesson), `-race` on touched packages, `-shuffle=on ./cmd/lmm/` where cmd tests change.
- Add files BY NAME (never `git add -A`; untracked IDEAS.md must never be staged). Help-text changes regenerate man pages (`make man`; drift test enforces). Cobra singleton hygiene in tests (t.Cleanup restoration; no bare `Execute()` without the context-reset precedent).
- CLI/TUI parity is a standing directive: the game-scoped listing semantics land in both interfaces in the same task.
- This PR contains TUI behavior changes → USER SMOKE TEST gates the merge.

## Tasks

### Task 1: `SourcesForGame` core helper + `SearchAllSources` refactor

**Files:** `internal/core/service.go` (or the file holding `SearchAllSources` — locate it), new tests in the matching `_test.go`.

**Interfaces (produces):** `func (s *Service) SourcesForGame(gameID string) ([]source.ModSource, error)` — resolves the game, intersects `game.SourceIDs` keys with the registry (unregistered keys silently skipped, matching `SearchAllSources`'s current tolerance), returns sorted by `ID()`. Error only for unknown game. Tasks 2–4 consume it.

Steps: RED (known game incl. sort order; unknown game error; `SourceIDs` key not registered → skipped; empty `SourceIDs` → empty slice) → implement → refactor `SearchAllSources`'s hand-rolled intersection (service.go:206-210 area) onto it, asserting existing aggregate-search tests stay green unchanged → full suite → commit `feat(core): SourcesForGame — one game-to-registered-sources intersection (#76)`.

### Task 2: deploy + import normalizations

**Files:** `cmd/lmm/deploy.go`, `cmd/lmm/import.go`, their tests.

1. **deploy** (design §4.1): `-s` default `"nexusmods"` → `""`; the mod-id form resolves via `resolveSource(game, deploySource, false)` — sole configured source auto, several → interactive prompt (deploy has no `-y`; do not add one). RED: sole-source auto path; multi-source prompt path (drive stdin per existing prompt-test patterns); explicit `-s` unchanged. Flag help + Long text updated (man regen).
2. **import scan-matching** (design §4.2): `tryMatchCurseForge` generalizes — iterate `SourcesForGame(game.ID)` filtered to `CapabilitiesOf(src).Search`, sorted (already sorted), first source whose result satisfies the **existing, unchanged acceptance rules** wins; rename to `tryMatchSources` (or similar). `--skip-match` skips the whole loop. RED: single-source game matches via a non-CurseForge mock source (proves generalization); multi-source order (curseforge before nexusmods alphabetically — pin); no-searchable-sources → no match, no error.
3. **import `--id` default** (design §4.2): the CurseForge-preferred block (import.go:99-113 area) → `resolveSource(game, importSource, false)`. RED both paths.
4. Help text updates for both commands; `make man` in the commit.

Commit: `feat(cli): deploy and import resolve sources dynamically — no built-in preference (#76)`.

### Task 3: registry-driven `game add` + game-prompt normalization

**Files:** `cmd/lmm/game_add.go`, `cmd/lmm/helpers.go`, `cmd/lmm/auth.go` (delete `getSourceDisplayName`), tests.

1. **game add** (design §4.3): menu built from the registry (`svc.ListSources()`, sorted by ID) instead of the literal two-item list. Per selection: source implements `source.GameCatalog` → the existing interactive catalog-search flow (today's CurseForge path, now generic — fetch via `ListGames`, filter/prompt, save `SourceIDs{id: entry.ID}` using `GameEntry.ID` or `.Slug` per what the source's own lookups expect — CurseForge saves the numeric ID string exactly as today, VERIFY); no `GameCatalog` → the manual-identifier flow (today's NexusMods slug path, prompt text generalized to name the source). Single-source-per-add stays (no multi-select — YAGNI). RED: menu lists a registered mock custom source; catalog path drives a mock `GameCatalog`; manual path for a catalog-less source; saved `games.yaml` shape identical for the two built-ins vs today (pin — zero migration).
2. **promptForGameSource** (`helpers.go`): display names from the registry — change its signature to accept the service (or a `func(id) string` resolver from callers that have one), render `Name (id)` per the established format, fall back to the bare ID for an unregistered source. Then **delete `getSourceDisplayName`** from auth.go (its last caller is gone — the PR-1 review flagged this exact cleanup). Update every `promptForGameSource` caller (search/install/update/mod paths — find them all) and their tests.
3. `make man` for changed help text.

Commit: `feat(cli): game add builds from the registry; game prompt renders registry names (#76)`.

### Task 4: game-scoped source listings — closes #75

**Files:** `cmd/lmm/source.go`, `internal/tui/service_core.go`, `internal/tui/` (Sources screen: keys, view, help), `internal/tui/service.go` (provider interface + prototype), tests.

Contract (design §5):
- **CLI**: `lmm source list` scopes to the active game when resolvable (`-g` or default game — resolve WITHOUT erroring when absent); new `--all` flag → full registry plus an `IN USE` column (marker for the active game's sources; column omitted when no game context). No game resolvable → full list, today's shape unchanged. Broken-definition error rows visible in ALL views. `--json`: scoped by default likewise; rows gain `"in_use"` only in `--all`-with-game (additive, MINOR per repo precedent). Update the command's Long text (man regen).
- **TUI**: Sources screen defaults to the game-scoped list; `a` toggles the full registry with the same in-use marker; footer/help shows the toggle (check idle-hint priority rules — see keys.go conventions); screen title or header indicates scope ("Sources — <game>" vs "Sources — all"). Provider: `SourceInfos()` gains scoping — extend the DataProvider seam (e.g. `SourceInfos(all bool)` or a parallel scoped method — pick what ripples least through fakes; stubProvider absorbs most) and `SourceInfo` gains `InUse bool`. Prototype provider: canned data serves both views plausibly.
- RED-first on both sides: CLI scoped/all/no-game/json shapes; TUI toggle behavior, marker rendering, help line. Height-budget rules apply to any new header line (clamp lessons).

Commit: `feat: source listings scope to the active game — CLI --all flag, TUI toggle (#75)`.

### Task 5: carry-ins + finalize

1. Carry-ins (all small, from PR-1 review rounds):
   - `doAuthStatus` orphan-token loop: distinguish truly-unregistered (`service.GetSource` fails) from registered-but-auth-removed; label the latter accurately (e.g. "stored token for source without auth declared"). Pin with a test.
   - `probeSource` (`cmd/lmm/source.go` ~171): use `envKeyFor(src)` once the source is constructed, not `envKeyForSourceID(def.ID)` directly (divergence-proofing).
   - `TestRegisterSources_KeyResolutionPrecedence`: strengthen so an env-vs-token precedence regression actually fails (e.g. distinct env/token values + a seam observing the applied key, or validate via a mock KeyValidator recording the key — choose the least invasive).
2. Verification sweep: build, gofmt, vet, full suite (explicit exit codes), `-race ./...`, `-shuffle=on ./cmd/lmm/ ./internal/tui/` twice, `make man` idempotency.
3. CHANGELOG `[1.22.0]`: Changed — deploy/import dynamic source resolution (no more `nexusmods`/CurseForge defaults), registry-driven `game add` (custom sources usable at game creation), game-scoped source listings with `--all`/TUI toggle; note the `in_use` JSON addition. Bump root.go → 1.22.0 + `make man` in the bump commit.
4. Archive THIS plan **and** the design doc (`git mv docs/plans/2026-07-28-source-registry-design.md docs/plans/archive/`) — the design completes with this PR.
5. File the design §9 follow-up issues (category/tags placeholders; declarative validate endpoint; declarative dependency endpoint; POST/GraphQL transport — the last marked "design against a concrete source") and reference them from a closing comment draft for #76.
6. PR: **closes #76 and #75**; Copilot triage; **USER SMOKE TEST** (TUI Sources toggle + deploy/import/game-add flows) + merge authorization.

## Self-review (spec coverage)

Design §4.1/4.2/4.3 → Tasks 2–3; §5 → Tasks 1+4; §6 zero-migration pins → Task 3's games.yaml-shape test + existing suites; §7 PR-2 test list → distributed; §8 phasing → this PR; §9 → Task 5.5. Carry-ins traced to PR-1 review records. Cross-task contract: `SourcesForGame` signature stated in Task 1, consumed in Tasks 2 (import) and 4; `GameEntry` from PR 1 consumed in Task 3.
