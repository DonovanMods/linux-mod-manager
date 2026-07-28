# Source Registry PR 1 (Mechanism) Implementation Plan — #76

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Execute task-by-task with per-task review, per the repo's established SDD pattern.

**Goal:** Built-ins register, declare capabilities, and expose metadata through the same uniform path custom sources use — no call site branches on source identity.

**Architecture:** Five new optional metadata interfaces in `internal/source` (duck-typed like `CapabilityReporter`); NexusMods/CurseForge implement them plus explicit `Capabilities()`; `cmd/lmm/root.go` gains a single registration pipeline over source factories; `auth.go`'s five switches and the CLI/TUI TYPE-column type-switches are replaced by interface queries. Spec: [2026-07-28-source-registry-design.md](2026-07-28-source-registry-design.md) — sections 1–3 and the PR-1 half of 7–8. Behavior deltas allowed in this PR: the auth picker gains auth-capable custom sources; everything else is behavior-preserving.

**Tech stack:** Go 1.25, cobra, testify; existing httptest/mock patterns per package.

## Global Constraints

- Branch `feat/source-registry-mechanism` off main. PR references #76 (does NOT close it — PR 2 remains). Version: MINOR → 1.21.0 at finalize.
- TDD RED-first with recorded evidence for every behavior change; gofmt (tabs); per-commit: build, `gofmt -l` empty, `go vet ./...`, full suite `GOCACHE=$(pwd)/.go-mod/cache go test ./...`; `-race` on touched packages before each commit.
- Add files BY NAME (never `git add -A`; untracked IDEAS.md must never be staged).
- Help-text changes regenerate man pages (`make man` — the drift test enforces).
- The three behavioral normalizations (deploy default, import matching, game add menu) and game-scoped listing are PR 2 — do NOT touch them here. `game_add.go` keeps its current flow this PR even though `GameCatalog` ships now.
- Cobra singleton hygiene in tests: restore anything you reparent or set (`t.Cleanup`), per the patterns in recent `cmd/lmm/*_test.go`.

---

### Task 1: Metadata interfaces + built-in/custom conformance

**Files:**
- Modify: `internal/source/source.go` (new interfaces + `GameEntry`)
- Modify: `internal/source/nexusmods/nexusmods.go`, `internal/source/curseforge/curseforge.go` (implement)
- Modify: `internal/source/custom/{directory,manifest,api}.go` (add `TypeLabel()` only)
- Tests: `internal/source/source_test.go`, `internal/source/nexusmods/nexusmods_test.go`, `internal/source/curseforge/curseforge_test.go`, `internal/source/custom/*_test.go`

**Interfaces (produces — exact contracts later tasks consume):**

```go
// In internal/source/source.go, alongside CapabilityReporter:
type EnvKeyProvider interface{ EnvKey() string }
type KeyValidator interface{ ValidateKey(ctx context.Context, key string) error }
type AuthInstructionsProvider interface{ AuthInstructions() string }
type GameEntry struct{ ID, Name, Slug string }
type GameCatalog interface{ ListGames(ctx context.Context) ([]GameEntry, error) }
type TypeLabeler interface{ TypeLabel() string }
```

Conformance matrix to implement (each cell RED-first):

| Type | EnvKey | ValidateKey | AuthInstructions | GameCatalog | TypeLabel | Capabilities (explicit) |
|---|---|---|---|---|---|---|
| NexusMods | `"NEXUSMODS_API_KEY"` | wraps `NewClient(nil,"").ValidateAPIKey(ctx,key)` | current `printAuthInstructions` text moved verbatim | — | `"built-in"` | all true |
| CurseForge | `"CURSEFORGE_API_KEY"` | wraps `NewClient(nil,key).GetGames(ctx)` probe (discard result, return wrapped error) | current text moved verbatim | wraps `client.GetGames`; `GameEntry{ID: strconv.Itoa(g.ID), Name: g.Name, Slug: g.Slug}` | `"built-in"` | all true |
| custom.Directory | — | — | — | — | `"directory"` | (already explicit) |
| custom.Manifest | — | — | — | — | `"manifest"` | (already explicit) |
| custom.API | — | — | — | — | `"api"` | (already explicit) |

Steps:
1. RED: compile-time conformance pins (e.g. `var _ source.EnvKeyProvider = (*nexusmods.NexusMods)(nil)` style in each package's test file) plus behavior tests: `TestNexusMods_EnvKey`, `TestNexusMods_ValidateKey_{OK,Unauthorized}` (httptest), `TestCurseForge_ListGames` (httptest, asserts GameEntry mapping incl. int→string ID), `TestTypeLabels` (table over all five types), `TestBuiltinCapabilitiesExplicit` (both implement `CapabilityReporter`; all-true). Run; capture failures.
2. Implement. `CapabilitiesOf` doc comment updated: default exists for test doubles; production sources declare explicitly. No signature changes to anything existing.
3. Full suite + `-race ./internal/source/...`. Commit: `feat(source): self-describing source metadata interfaces + built-in conformance (#76)`.

### Task 2: Unified registration pipeline

**Files:**
- Modify: `cmd/lmm/root.go` (`registerSources`, `registerCustomSources`, `getSourceAPIKey` call sites)
- Modify: `cmd/lmm/auth.go` (`getEnvKeyForSource` switch → `envKeyFor(src)` helper)
- Tests: `cmd/lmm/root_test.go` (+ keep `TestInitService_RegistersSources` green unchanged)

**Interfaces:**
- Consumes: Task 1's `EnvKeyProvider`.
- Produces: `envKeyFor(src source.ModSource) string` — returns `EnvKey()` if implemented, else `envKeyForSourceID(src.ID())`. Task 3 consumes it.

Steps:
1. RED: `TestRegisterSources_KeyResolutionPrecedence` (env var beats DB token for a built-in, using `t.Setenv("NEXUSMODS_API_KEY", ...)` + a stored token; assert via `IsAuthenticated()`); `TestRegisterSources_DerivedEnvKeyForCustom` (existing behavior pinned through the new path); `TestRegisterSources_FirstWinsCollision` (a custom def with id `nexusmods` still skips with the warning — pin the warning text via `customSourceWarnOut`).
2. Implement: built-ins constructed keyless (`nexusmods.New(nil, "")`), ordered factory slice (built-ins first, then customs via unchanged `LoadSourceDefinitions`), one loop: construct → collision check (existing `GetSource` guard, warning preserved) → `getSourceAPIKey(svc, id, envKeyFor(src))` → `SetAPIKey` when implemented → `RegisterSource`. Delete `getEnvKeyForSource`'s switch; keep `envKeyForSourceID` as fallback.
3. Full suite (the two registration-pin tests in `helpers_test.go`/`root_test.go` must pass unchanged) + `-race ./cmd/lmm/`. Commit: `refactor(cli): one registration pipeline for built-in and custom sources (#76)`.

### Task 3: Auth flows de-switched

**Files:**
- Modify: `cmd/lmm/auth.go` (delete `supportedSources`, `getSourceDisplayName`, `printAuthInstructions` switch, `validateAPIKey` switch, `isSupportedSource`; rework `promptForSource`, `selectAuthSource`, `resolveLogoutSource`, `printLoginResult`, `printAuthLoginSuccess`, `doAuthStatus`)
- Tests: `cmd/lmm/auth_test.go`, `cmd/lmm/auth_status_test.go`

**Interfaces:**
- Consumes: Task 1's `KeyValidator`/`AuthInstructionsProvider`; Task 2's `envKeyFor`.
- Produces: picker/status behavior other code observes; no new exported symbols.

Behavior contract (the ONE intended delta this PR): interactive picker lists every registered source with `CapabilitiesOf(src).Auth`, sorted by ID; built-ins appear because they're always registered. Display names via `Name()`. Login: `KeyValidator` present → validate live, "Successfully authenticated" path; absent → store + "validated on first use" path (existing messages preserved verbatim). Instructions: `AuthInstructionsProvider` or generic fallback naming `envKeyFor(src)`. `auth status`: iterate registered auth-capable sources sorted by ID (built-ins no longer a separate hardcoded tier — output content for a stock setup must remain materially identical: both built-ins listed with their states), then the existing orphaned-token pass unchanged.

Steps:
1. RED: `TestPromptForSource_ListsAuthCapableRegistered` (register a mock auth-capable custom source; picker offers three entries sorted); `TestAuthLogin_ValidatorPath` vs `TestAuthLogin_StoredPath` (mock sources with/without `KeyValidator`; assert message split); `TestAuthStatus_UniformIteration` (stock: both built-ins present; plus one custom auth source listed once, not double-listed). Update existing literal-prompt assertions.
2. Implement; delete the five switch constructs.
3. Full suite + `-race ./cmd/lmm/` + `-shuffle=on` twice. `make man` (auth Long text references may change — if help text changed, regen; if not, verify no drift). Commit: `refactor(cli): auth flows driven by source metadata, not identity switches (#76)`.

### Task 4: TYPE column via TypeLabeler (CLI + TUI)

**Files:**
- Modify: `cmd/lmm/source.go` (`isCustomSource`/type strings → `TypeLabel()` query; keep error-row rendering)
- Modify: `internal/tui/service_core.go` (`customSourceType` → `TypeLabel()` query; delete the "kept in sync by hand" duplicate)
- Tests: `cmd/lmm/source_test.go`, `internal/tui/sources_view_test.go` (+ goldens if any snapshot covers the Sources screen)

**Interfaces:** Consumes Task 1's `TypeLabeler`. Fallback for a source implementing neither `TypeLabeler` nor being a known type: `"unknown"` (pin with a bare-mock test; today's negative-switch would have said "built-in" — this is an internal-only label change for test doubles, not reachable by real sources).

Steps:
1. RED: CLI `source list` table test with one of each real type + a bare mock (expects directory/manifest/api/built-in/built-in/unknown); TUI SourceInfos equivalent.
2. Implement both consumers; goldens regen ONLY if a snapshot actually renders type labels from a bare mock (predict: no golden changes — verify).
3. Full suite + `-race` both packages. Commit: `refactor: TYPE labels come from sources — kills the hand-synced CLI/TUI switches (#76)`.

### Task 5: Finalize

1. Verification sweep: build, gofmt, vet, full suite, `go test -race ./...`, `make man` idempotency, `-shuffle=on` on `cmd/lmm` twice.
2. CHANGELOG `[1.21.0]`: Changed — built-ins now register/declare through the same path as custom sources (internal); auth picker now offers auth-capable custom sources interactively (user-visible). Internal — TYPE labels self-reported; registration pipeline unified. Bump `cmd/lmm/root.go` → 1.21.0; `make man` in the bump commit (version header).
3. Archive THIS plan (`git mv` to `docs/plans/archive/`) in the PR; the design doc STAYS in `docs/plans/` until PR 2 completes.
4. PR: references #76 ("mechanism half; behavior normalizations + #75 follow in PR 2"), notes the one intended behavior delta. Copilot triage rounds via `gh-await-review`. USER GATE: light smoke (auth picker with a custom source configured; `lmm source list` unchanged) + merge authorization.

## Self-review (spec coverage)

Spec §1 → Task 1; §2 → Task 2; §3 → Task 3; TYPE-column half of §1 → Task 4; §6 back-compat pins → Tasks 2–3 tests (env precedence, collision, registration pins unchanged); §7 PR-1 test list → distributed above; §8 PR-1 scope + delta → Global Constraints + Task 3 contract. Deferred by design: §4, §5, §9 (PR 2 / follow-up issues). Type consistency: `GameEntry`/`envKeyFor` signatures stated once and referenced; no other cross-task symbols introduced.
