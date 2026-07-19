# Code Smell Remediation Plan

**Date:** 2026-04-25
**Scope:** Structural and correctness-level code smells in the lmm Go codebase (~25.8k LOC, 109 files).
**Out of scope:** Naming, doc gaps, dead-code sweep, TUI work, CurseForge batch endpoint, performance.

The plan is staged so each phase ends with a green build and updated/added tests. Phases are
ordered low-risk → high-risk so churn compounds in our favour.

---

## Audit findings (high priority only)

1. **Leaky `Service.DB()` abstraction.** `internal/core/service.go:392` exposes `*db.DB`. CLI
   commands (e.g. `cmd/lmm/profile.go:451`, `cmd/lmm/uninstall.go:157`) reach into the storage
   layer directly with `service.DB().X()`, bypassing the orchestration boundary.

2. **God functions in `cmd/lmm/`.**
   - `runInstall` — 609 lines (`install.go:141`)
   - `runProfileSwitch` — 293 lines (`profile.go:289`)
   - `runProfileApply` — 288 lines (`profile.go:645`)
   - `runProfileImport` — 255 lines (`profile.go:535`)

3. **`context.Background()` invented mid-stack.** ~18 production sites in `cmd/lmm/*.go`
   (e.g. `install.go:173`, `profile.go:441,737,1298`). Cobra already exposes `cmd.Context()`,
   but nothing is threaded from the entry point, so SIGINT and timeouts cannot propagate.

4. **Duplicated CLI boilerplate.** `requireGame()` + `initService()` + `defer service.Close()`
   appears verbatim in ~10 command files. The `errors.Is(err, domain.ErrAuthRequired)` block
   is copied verbatim 5×.

5. **Inconsistent compound-error wrapping.** `internal/core/installer.go:61,78,138,150,163,165,168,178,189`
   mix `%w` for the primary error with `%v` for rollback / cleanup errors in the same string,
   collapsing the chain on the secondary errors.

6. **Duplicate HTTP client patterns.** `internal/source/nexusmods/client.go` and
   `internal/source/curseforge/client.go` (`doRequest` at L52–125) implement the same
   auth / error-mapping shape twice with no shared base.

7. **Global mutable CLI flag vars.** `cmd/lmm/root.go:21–31` plus per-command file-scope
   duplicates make tests order-dependent and complicate parallelisation.

8. **Stale `TODO`s with no issue refs** (e.g. `cmd/lmm/import.go:366`,
   `internal/source/curseforge/client.go:265`).

---

## Phase 1 — Plumbing (mechanical, low risk)

Goal: stop inventing context mid-stack and remove the most common boilerplate.

- **1a. Thread `cmd.Context()` from entry to leaf.**
  - In `Execute()`, build a signal-aware context via `signal.NotifyContext` (SIGINT, SIGTERM)
    and call `rootCmd.ExecuteContext(ctx)` instead of `rootCmd.Execute()`.
  - Replace each `ctx := context.Background()` in `cmd/lmm/*.go` (production paths only) with
    `ctx := cmd.Context()`.
  - Add a regression test that cancelling the parent context aborts a long-running command.
  - Leave `internal/core/extractor.go:218` alone for now — it derives a timeout from a
    constant; a follow-up phase can promote it to a derived `WithTimeout(ctx, …)`.

- **1b. `withService` middleware.**
  - Add `withService(cmd *cobra.Command, fn func(ctx context.Context, svc *core.Service) error) error`
    in `cmd/lmm/helpers.go`. It handles `requireGame`, `initService`, `defer service.Close()`
    with the existing stderr warning, and forwards `cmd.Context()`.
  - Convert `uninstall.go` first as the reference implementation, then sweep the remaining
    commands one PR-sized batch at a time.

- **1c. `handleAuthError` helper.**
  - Add `handleAuthError(sourceID string, cause error) error` in `cmd/lmm/helpers.go`.
  - Replace the 5 duplicated `errors.Is(err, domain.ErrAuthRequired)` blocks in `search.go`,
    `install.go`, `update.go`.

**Exit criteria:** `go test ./...` green; no `context.Background()` in `cmd/lmm/*.go` outside tests;
auth-error formatting routed through one function.

---

## Phase 2 — Error wrapping consistency

Goal: every error chain stays inspectable.

- **2a.** Introduce a typed composite error in `internal/domain/errors.go`, e.g.:
  ```go
  type DeployError struct {
      Op       string // "deploy", "remove", ...
      File     string
      Primary  error
      Rollback error // optional
      Cleanup  error // optional
  }
  func (e *DeployError) Error() string
  func (e *DeployError) Unwrap() []error
  ```
- **2b.** Replace the 9 `%w; … %v` strings in `internal/core/installer.go` with the new type.
  Verify existing tests still match via `errors.Is/As`; update any that string-match.

**Exit criteria:** no `%w; … %v` patterns remain in `internal/core/`. `errors.Is`/`As` works
through composite errors.

---

## Phase 3 — Tighten the `Service` boundary

Goal: nothing outside `internal/core/` may reach into `*db.DB`.

- **3a.** Audit every external caller of `service.DB()`. For each, add a focused method on
  `*core.Service` (e.g. `SetModEnabled(ctx, ref, profile, enabled)`,
  `DeleteInstalledMod(ctx, ref, profile)`) and migrate the callers.
- **3b.** Lower-case `DB()` to `db()` (or remove). Same audit for `Registry()` if it leaks
  similarly.
- **3c.** Add tests that exercise the new service methods directly so the behaviour is
  pinned at the service boundary, not the storage one.

**Exit criteria:** `grep -r "service.DB()" cmd/ internal/` returns zero hits outside
`internal/core/`. Tests still green.

---

## Phase 4 — Decompose the god functions

Goal: no `RunE` over ~150 lines; each step is independently testable.

- **4a. `runInstall` (609 → ~120).** Extract:
  - `selectMod(ctx, svc, source, query) (*Mod, error)`
  - `resolveAndConfirmDeps(ctx, svc, mod) (*installPlan, error)`
  - `downloadAndStage(ctx, svc, plan) ([]Staged, error)`
  - `deployAndRecord(ctx, svc, staged, profile) error`

- **4b. Profile commands.** Move orchestration into `internal/core/profile.go` as
  `SwitchProfile(ctx, …)`, `ApplyProfile(ctx, …)`, `ImportProfile(ctx, …)`. The CLI files become
  presenters: arg parsing → call core → format output.

**Exit criteria:** all `runX` functions ≤150 lines. New core functions covered by table-driven
tests.

---

## Phase 5 — Source client base

Goal: shared HTTP plumbing for `nexusmods` and `curseforge`.

- **5a.** Extract `internal/source/httpclient` with auth-header injection, retry,
  JSON decode, and error mapping (incl. `domain.ErrAuthRequired` translation).
- **5b.** Refactor `nexusmods` and `curseforge` clients to use it. Recorded-response tests
  must still pass without modification (proves no behavioural change).

**Exit criteria:** `doRequest`-style duplication gone. Adding a third source costs only
endpoint + types.

---

## Phase 6 — Cleanup

- **6a.** Convert remaining `TODO` comments to GitHub issues (or delete if obsolete).
- **6b.** Move per-command flag globals into command-scoped structs bound via Cobra; only
  `--config`, `--data`, `-g`, `-v`, `--no-hooks`, `--json`, `--no-color` stay persistent on root.

**Exit criteria:** no top-level mutable flag vars in command files; root.go retains only
the persistent flag struct.

---

## Versioning

Each phase is a MINOR-or-PATCH bump per `CLAUDE.md`:

- Phase 1 — PATCH (refactor; no user-visible change apart from Ctrl+C now cancelling).
- Phase 2 — PATCH (errors still match `Is/As`).
- Phase 3 — PATCH.
- Phase 4 — PATCH.
- Phase 5 — PATCH.
- Phase 6 — MINOR if any flag rebind alters CLI surface; otherwise PATCH.

Bump and update `CHANGELOG.md` in a separate commit at the end of each phase.
