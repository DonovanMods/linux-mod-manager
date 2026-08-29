# v2 Core Refactor for Multi-Frontend (CLI + `lmm serve`) — Design

**Date:** 2026-08-27
**Status:** Approved design, pre-implementation (implementation plans are written per phase, Phase 0 first)
**Issues:** EPIC to be filed ("v2: core refactor for multi-frontend"); already filed: [#270](https://github.com/DonovanMods/linux-mod-manager/issues/270) (XDG paths), [#271](https://github.com/DonovanMods/linux-mod-manager/issues/271) (SQLite pragmas per connection)
**Supersedes:** the "CLI and TUI are equally first-class" parity directive in `CLAUDE.md`

## Summary

lmm keeps its CLI and gains a browser-based GUI (`lmm serve`, a later epic). To make
that cheap, `internal/core` becomes a complete, transport-agnostic application API
and both frontends become thin adapters over it. The TUI is removed first. The work
lands on a dedicated long-lived `v2` branch, unreleased; `develop`/`main` stay the
v1.30.x line until a single deliberate v2.0.0 cut.

Approach: **contracts first, then lift flows command-by-command** (not a rewrite, not
opportunistic). Four phases, each PR-sized and shippable to develop.

## Background / Evidence

A read-only seam audit (2026-08-27) established the facts this design rests on.
Non-test LOC: `cmd/lmm` 10,680 · `internal/core` 13,408 · `internal/tui` 13,704 ·
`source` 6,396 · `storage` 2,752 · `domain` 517 · `linker` 269.

**The TUI was the cleaner frontend; the CLI is where logic leaked.**

| | `internal/tui` | `cmd/lmm` |
|---|---|---|
| Intra-module imports beyond core/domain | `source`, `storage/config` | `storage/config` (10 files), `source/steam`, `source/custom`, `source/icarus`, `source/nexusmods`, `source/curseforge`, direct filesystem I/O |
| Presentation vs logic | mostly adapter + rendering | ~75–80% logic, ~20–25% flags/printing |
| Core methods only it calls | 1 (`AvailableModVersions`) | 27 low-level primitives (`NewImporter`, `SaveInstalledMod`, `GetLinker`, `DownloadMod`, `SetModEnabled`, …) |

Whole engines live in `main`:

- `cmd/lmm/profile.go:870` `doProfileApply` (~360 LOC) — converge/install engine; its own
  comment at `:1206` calls it "a bespoke disable/enable/install reimplementation".
- `cmd/lmm/import.go:435` `runImportScan` (~250 LOC) and `:715` `importExistingMod` +
  `:804` `copyFileStreaming` — scan, metadata backfill, write-into-cache, completion
  markers, DB save. No TUI equivalent ever existed.
- `cmd/lmm/hooks.go` + `install.go:1140` — the `before_all/before_each/after_each/after_all`
  lifecycle is sequenced in cmd; core only exposes `HookRunner.Run`
  (`internal/core/hooks.go:43`). The TUI re-derived it (`internal/tui/service_core.go:685-745`).
- `cmd/lmm/profile.go:583` `doProfileSync`, `:762` `doProfileReorder`,
  `cmd/lmm/update.go:585` `applySingleUpdate` (lock/pin gating duplicated ahead of core),
  `cmd/lmm/install.go:455` `doInstall` + `:1155` `batchInstallMods`,
  `cmd/lmm/game.go:207` `doGameDetect` + `:327` `gameFromDetected`,
  file-selection policy at `install.go:967/993` and `profile.go:1303/1383`.

**Core's strongest property:** it never writes to stdout/stderr and never reads stdin
(one `fmt.Fprintf` into a `hash.Hash`, `service.go:834`, is the only hit). Confirmations
are callback gates (`InstallOptions.ConfirmConflicts`, `ProfileImportOptions.ConfirmInstall`).

**Core's gaps:**

1. Four uncoordinated progress mechanisms: `ProgressFunc(DownloadProgress)`
   (`downloader.go:33`), `func(DeployProgress)` on 8 methods (`flows.go:1025`),
   `func(VerifyEvent)` (`verify.go:73`), and a callback smuggled through
   `context.Value` (`domain.UpdateProgressContextKey`, `domain/mod.go:12-17`, read in
   `nexusmods.go:248` and `curseforge.go:314`). `DeployProgress` is a ~20-field
   struct whose doc comments encode CLI print semantics.
2. 57 of 90 exported `Service` methods lack `ctx`; `storage/db` (33 methods) has zero
   `*Context` calls. Cancellation stops at the HTTP boundary.
3. `Service` is not goroutine-safe (no mutex; mutable `games` map). The TUI adapter
   added `gameMu`/`profileMu`/`hooksMu` itself after a real `SetGame`-vs-`Search` race.
   SQLite pragmas are applied to one pooled connection (#271); no `busy_timeout`.
4. `internal/domain` has zero `json` tags; all 124 live on ~20 CLI view structs.
   Mutating commands have no `--json`. Bootstrap (paths, dir hardening, source
   registration, ~140 LOC in `root.go`) lives in cmd; no `XDG_*` support (#270).
5. `flows.go` is 5,469 lines; `service.go` 1,466.
6. Two exported methods have no callers at all (`GetDefaultLinkMethod`, `Cache`);
   three more have no production caller but heavy test use (`AddGame` ~120 refs,
   `GetInstaller` ~70, `IsSourceAuthenticated` 5); two `…ForTest` exports serve
   `package core_test` files.

**Existing strengths to generalize:** the Plan/Apply pairs (`PlanInstall`/`ApplyInstall`,
`PlanImport`/`ApplyImport`, `PlanProfileSwitch`/`ApplyProfileSwitch`); conflicts returned
as values, not errors; 19 sentinel errors; the signal-aware root ctx (`root.go:289`);
hooks via `exec.CommandContext`; 21 CLI test files that drive cobra with captured
output plus 32 golden files.

## Settled decisions (do not re-litigate)

- **TUI removed first**, not frozen-and-carried. With `serve` as the GUI target, the
  Bubble Tea adapter is no longer a useful prototype, and carrying it would mean every
  seam change is made three times.
- **GUI = `lmm serve`** (local HTTP/JSON + SSE over core, web UI in a browser tab).
  Chosen over Wails/Electron/Tauri/Fyne. Keeps the single static no-CGO binary.
- **Approach C** (contracts first, then incremental lifting).
- **Branch model:** a long-lived `v2` integration branch, cut from `develop` at the
  start of Phase 0. Story branches fork from `v2` and PR back into `v2` with an
  explicit `--base v2`. `develop` and `main` remain the v1 line (v1 fixes and any v1
  features keep flowing develop → main as today). At v2.0.0, `v2` is PR'd into `main`
  as the release and `develop` is fast-forwarded to it; whether a `v1` maintenance
  branch is kept afterwards is decided at that point. `v2` is ruleset-protected
  against deletion and force-push only ("Protect v2", 21684423); **PRs are not
  enforced until the v2 public-release milestone** — the internal SDD review loop and
  the phase-end whole-branch live review are the gates. PRs remain available on demand
  for an independent (Copilot) review of a specific unit. At the public-release
  milestone the ruleset is brought up to `develop`'s (PR required, Copilot review).
- **Versioning:** v1.30.x is the public line. Everything here lands on `v2`
  unreleased. v2.0.0 is one deliberate cut, timed after Phase 3 or after `serve` —
  decided then. No interim pre-releases. `--json` shapes may change once, at v2.0.0.
- **No "generic library" extraction for its own sake.** `steam`, `storage/cache`,
  `httpclient`, `linker` are extractable but stay internal until a second consumer exists.
- **Single repo, single module, single binary.** `internal/` stays private.

## Section 1 — Target architecture, boundary, bootstrap

### Package layout at the end of the epic

```
cmd/lmm/            CLI only: cobra wiring, flags, rendering, prompts, range parsing.
                    May import ONLY internal/app, internal/core, internal/domain,
                    internal/source (the interface package; no subpackages).
internal/
  app/              Composition root. app.Open(app.Options) (*core.Service, error)
                    (gains ctx in Phase 1 alongside core.NewService):
                    path resolution (XDG + legacy fallback), directory creation and
                    0700 hardening, built-in source construction and API-key
                    resolution, custom-source loading. The ONLY package allowed to
                    import concrete sources (nexusmods, curseforge, icarus, steam, custom).
  core/             The application API. Flat *Service facade, split by flow:
                    service.go (struct, accessors), events.go, install.go, deploy.go,
                    update.go, rollback.go, uninstall.go, purge.go, switch.go,
                    profile_apply.go, profile_sync.go, importer.go, game_detect.go,
                    hooks.go (lifecycle, not just Run), selection.go (file-selection
                    policy), verify.go, merged_pak.go. flows.go ceases to exist.
  domain/           Unchanged role; gains json tags. No deps.
  source/ storage/ linker/   Unchanged roles.
  serve/            (future epic) HTTP/JSON + SSE handlers over core. Imports core+domain only.
```

`app` rather than bootstrap-in-core: core would otherwise import every concrete
source, dragging them into every core test build and making core less mock-friendly.
`app` has no logic of its own; it is the one place that knows what an lmm installation
looks like on disk. `cmd/lmm` and later `lmm serve` both call `app.Open`.

### The boundary is a test

`cmd/lmm/boundary_test.go` runs `go list -f '{{.Imports}}' ./cmd/lmm` and asserts every
intra-module import is in the allowed set **or** in an explicit allow-list of current
violations, each entry carrying a reason and the Phase 2 step that removes it. Two
failure modes, both loud: a new disallowed import fails; an allow-listed import that has
disappeared also fails ("ratchet: remove the entry"). When the list is empty (end of
Phase 3) the allow-list mechanism is deleted; the test stays. No new dependency —
`go list` is available wherever tests run.

### Facade shape

`*core.Service` stays flat (grep-able; no sub-facade ceremony). Its surface changes in
two directions:

- The 27 CLI-only primitives lose their external caller as each flow lifts and are
  **unexported in the same PR**. That is how "lifted" is verified.
- The two never-called exports go in Phase 0, and the two `…ForTest` exports move to
  `export_test.go` there. `AddGame`, `GetInstaller`, `IsSourceAuthenticated` are
  production-dead but test-used; they are unexported or removed in Phase 2 as the
  tests that use them migrate with their flows.

Net: ~90 → ~55–60 exported methods, all flows or queries a frontend legitimately needs,
each with a doc comment.

### What stays in cmd

Rendering, prompting, and parsing of user input (range selections like `1,3-5`). The
*policy* those inputs feed — candidate files and their order, bare-mod-ID
disambiguation across sources, "append unmentioned mods" in a reorder — is core.
The test: **would `lmm serve` need to reimplement it?** If yes, it is core.

## Section 2 — Events and observability

### One sink, passed explicitly

Every long-running or multi-step `Service` method takes a trailing
`sink core.EventSink` (`type EventSink func(Event)`; `nil` = discard). Per-operation
(`serve` attaches one per job), explicit (no context smuggling). It replaces all four
existing mechanisms; migration from `progress func(DeployProgress)` is mechanical
because the shape is the same.

Trailing parameter, not a field on the `Options` structs: options describe *what* to do
and are serializable plan inputs; the sink is a runtime attachment and must never be
serialized.

### Typed events behind a one-method interface

```go
type Event interface{ EventType() string }   // "step" | "download" | "mod" | "hook" | "warning" | "verify" | "update_check"

type Scope struct {                              // embedded in every event
    Op      Op                    `json:"op"`      // install | deploy | update | rollback | uninstall | purge | switch | import | verify | ...
    Mod     *domain.ModReference  `json:"mod,omitempty"`
    ModName string                `json:"mod_name,omitempty"`
    Index   int                   `json:"index,omitempty"`   // 1-based within Total
    Total   int                   `json:"total,omitempty"`
}

type StepEvent        struct{ Scope; Phase Phase; Detail string; Percent float64 }
type DownloadEvent    struct{ Scope; File *domain.DownloadableFile; Downloaded, TotalBytes int64 }
type ModEvent         struct{ Scope; State ModState; Version string; Class DeployModClass; FilesExtracted, RawFallbacks int; Err string }
type HookEvent        struct{ Scope; Stage HookStage; Script string; Result *HookResult }
type WarningEvent     struct{ Scope; Message string }
type VerifyEvent      struct{ Scope; Kind VerifyEventKind; Finding VerifyFinding /* + today's payload minus presentation hints */ }
type UpdateCheckEvent struct{ Scope }
```

`ModState` is `started | done | skipped | failed`; `Err` carries a per-item failure inside
a continue-on-error batch (the overall error is still the return value).

Serialization is a fixed envelope, `{"type": "download", "data": {...}}`, via
`core.MarshalEvent`, covered by one table-driven golden test over every event type. The
envelope is the future SSE wire format; `serve` adds nothing to it.

### Rules

- **Events are for live display; results are the record.** Every `Apply*` returns its
  `*XResult`; `Result.Warnings` stays authoritative (the "must reach the user
  unconditionally" rationale at `flows.go:60` survives). A `WarningEvent` is *also*
  emitted at discovery so `serve` can show it live; the CLI keeps printing warnings from
  the result at the end, byte-identical.
- **No presentation in events.** `VerifyEvent.Green` and `Variant: "fixed_green"` are
  removed; the renderer derives colour from `Kind`/`Variant` semantics. The
  `DeployProgress` doc comments describing `fmt.Println` behaviour move to
  `cmd/lmm/render.go`, a single `switch ev := e.(type)`.
- **Verbosity is a renderer concern.** Core always emits everything (cheap,
  synchronous). The 79 `verbose` reads in cmd become the renderer choosing what to print
  per level. `noHooks` (4 reads) becomes `SkipHooks` on the relevant `Options`.
- **Synchronous, ordered, cheap.** Sink runs on the operation's goroutine; core never
  spawns goroutines for delivery; buffering is the job wrapper's concern in `serve`.
  After `ctx` cancellation and return, no more events.
- **Diagnostics go to `log/slog`, not events.** `ServiceConfig.Logger *slog.Logger`
  (nil → `slog.DiscardHandler`); the CLI wires a stderr handler at debug level under
  `--verbose`. If a frontend would render it to the user it is an Event; if only an
  operator debugging lmm cares (HTTP retries, cache hits, SQL timings) it is slog.

### The one interface change

`ModSource.CheckUpdates(ctx, installed)` gains an explicit
`report source.ProgressFunc` parameter (`func(n, total int, name string)`, nil-safe).
Core wraps it into `UpdateCheckEvent`; `domain.UpdateProgressContextKey` and
`domain.UpdateProgressFunc` are deleted. Touches every source implementation and the
mocks — mechanical.

### Retirement order

One PR each, byte-identical CLI proven by capture tests: ctx-key → `UpdateCheckEvent`
(smallest, warms up the pattern) → `DownloadProgress` → `VerifyEvent` cleanup →
`DeployProgress` across all 8 methods in one PR (largest single contract PR; done at
once so two mechanisms never coexist). Flow tests get a `core.RecordEvents()` helper
returning a sink and the recorded slice.

## Section 3 — Cancellation and concurrency

### Cancellation contract

Every exported `Service` method that touches I/O (DB, filesystem, network, subprocess)
takes `ctx context.Context` first. Pure accessors (`ConfigDir()`, `ListGames()`,
`GetSource(id)`) do not. ~45 signature changes in one compile-driven PR. All 33
`storage/db` methods gain `ctx` and use `QueryContext`/`ExecContext`/`QueryRowContext`/
`BeginTx`. #271 (per-connection pragmas via DSN, `busy_timeout`, `:memory:` pool fix)
rides the same PR.

### What cancellation means mid-mutation (stated invariant)

- Honoured at **mod boundaries** within a batch and **inside** naturally cancellable
  work (downloads, extraction, conversion — ctx-aware I/O). Not honoured mid-link-set:
  once a mod's files start being linked/unlinked, that mod finishes.
- Therefore after a cancelled operation, **DB and disk agree for every mod** — each mod
  is either fully applied and recorded, or untouched. The profile may be partially
  applied; that is an already-supported state (`verify --fix` / converge repair it).
- A cancelled `Apply*` returns `ctx.Err()` (wrapped) **and** its partial `*XResult`
  describing what completed.
- Downloads never leave a half-written cache entry (completion markers already exist;
  this makes the rule explicit and tested).
- Tests: for each lifted flow, a table-driven test cancels at each phase boundary and
  asserts the per-mod DB==disk invariant.

### Concurrency model: concurrent queries, serialized mutations

- `Service` is documented as safe for concurrent use with exactly this guarantee: query
  methods (`Get*`, `List*`, `Plan*`, `Search*`, `CheckGameUpdates`, `Verify` without
  fix) may run concurrently with each other and with at most one in-flight mutation;
  mutating operations (`Apply*`, `DeployProfile`, `Purge*`, `Uninstall`,
  `Enable/Disable`, `Set*`, `Reorder*`, `Save*`) are **serialized service-wide**.
- Mechanism: a 1-slot semaphore acquired with `ctx`
  (`select { case s.opSem <- struct{}{}: … case <-ctx.Done(): return ctx.Err() }`), so
  a waiter is itself cancellable. `beginOp` checks `ctx.Err()` before the `select` so an
  already-done ctx loses deterministically instead of racing the semaphore 50/50. Held
  for the whole mutation including hooks. Reads during a mutation see WAL snapshot
  state, per-mod consistent by the invariant above.
- Not finer-grained: a mod manager should never run two deploys against one game
  concurrently; per-game locking is YAGNI until `serve` proves a need (the semaphore is
  one field, easy to key by game later). `serve`'s job runner executes sequentially
  anyway.
- The `games` map goes behind an `RWMutex`; a new `SaveGame` updates YAML and map
  together under the write lock. `source.Registry` (already `RWMutex`), the CurseForge
  cache mutex, and the package-level `config.gamesMu` stay as-is (the last is accepted
  debt, not moved).
- `make check` grows `test-race`; a dedicated test hammers queries while an `Apply` runs
  and asserts (a) no race, (b) a second mutation blocks, (c) cancelling the blocked one
  returns `ctx.Err()` without acquiring.

A job registry (start → ID → stream → cancel) is deliberately **not** core; it belongs
to `serve`. Core's contract — pass a ctx, pass a sink, get a result — is exactly enough
for that registry to be thin.

## Section 4 — Serialization contract and Plan/Apply

### Core types are the JSON contract

`internal/domain` and every core `*Plan`/`*Result`/`*Options` type gain `json` tags
(snake_case, matching the existing 124 tags in cmd). The CLI's `--json` emits these
types directly; cmd's ~20 view structs are deleted. `serve` later returns the same
types as response bodies. Each type is pinned by a golden file
(`internal/core/testdata/json/*.golden`) so any shape change is a visible diff.
v2.0.0 is the one moment shapes may change; afterwards the "JSON additions are MINOR"
precedent resumes.

Rules:

- JSON carries data, never formatted text: sizes in bytes, times RFC 3339, durations in
  seconds. Where a view struct has a derived field, the underlying datum moves into the
  core type.
- Int enums (`LinkMethod`, `DeployMode`, `UpdatePolicy`, `DeployPhase`,
  `DeployModClass`, `VerifyTier`, `VerifyEventKind`) implement
  `MarshalText`/`UnmarshalText` and serialize as string names.
- List fields are always `[]`, never `null`. Optional scalars use `omitempty`; optional
  structs are pointers.
- `--json` stays one document on stdout; `{"error":"…"}` on failure (both existing
  conventions). Mutating commands (`install`, `deploy`, `profile apply/switch/import`,
  `uninstall`, `purge`, `update` apply) gain `--json`, emitting their `*XResult`. Under
  `--json`, events are suppressed; streaming is `serve`'s job. (NDJSON-to-stderr is a
  cheap possible follow-up, not scope.)
- **`encoding/json/v2`** (stable in Go 1.27) is attractive because its defaults enforce
  the rules above. Phase 1 includes a half-day spike against the release notes deciding
  v1-with-discipline vs v2, recorded in the decisions log. `go.mod` → `go 1.27` is a
  Phase 0 item either way (it currently pins 1.25.6).

### Plan/Apply is the shape of every mutation

`PlanX(ctx, …) (*XPlan, error)` is side-effect-free (reads DB/sources; computes
conflicts, downloads needed, dependency additions, hooks that would run, files that
would be linked/removed). `ApplyX(ctx, game, plan, opts, sink) (*XResult, error)`
executes. Plans are serializable, so `serve` shows a preview and the CLI gets a uniform
`--dry-run` (replacing `ConvergeDeployedFiles(dryRun bool)`).

Target coverage: Install (exists; extended to batches), Import (exists; profile
import), Switch (exists), Deploy, Uninstall, Update (absorbing the lock/pin gating
`applySingleUpdate` duplicates), Rollback, Purge, **ProfileApply** (plan = DB-vs-profile
diff), ProfileSync, Adopt (local-file import), GameDetect (plan = detected games,
apply = save). Reorder and the `Set*` setters stay simple calls.

### No callbacks into the frontend from Apply

`InstallOptions.ConfirmConflicts` and `ProfileImportOptions.ConfirmInstall` (29
references) are removed. Conflicts are plan data: the frontend inspects
`plan.Conflicts`, decides, and passes `opts.AcceptConflicts bool` to Apply — an
all-or-nothing accept, which is what both existing callbacks already are. A
per-conflict resolution list is a possible later extension for `serve`, not scope.
Apply with unaccepted conflicts returns `domain.ErrFileConflict`. Anything
discoverable only mid-apply is an error or a `WarningEvent`, never a prompt.

### Stale plans

`Apply` re-derives the plan's precondition snapshot (the installed-mod set it was
computed from) and returns `core.ErrStalePlan` if it differs; the frontend re-plans.
One helper used by every Apply. It is the only new typed error this epic needs beyond
the existing `ErrModLocked`, `domain.ErrAuthRequired`, `ErrFileConflict` (which `serve`
will later map to HTTP statuses).

## Section 5 — Phases

### Phase 0 — v2 opener (small; the first PRs into the new `v2` branch)

1. Remove `lmm tui`: delete `cmd/lmm/tui.go` + `internal/tui/**`; drop `bubbletea`,
   `bubbles`, `charmbracelet/x/ansi` (`go mod tidy` clears indirects; `lipgloss`,
   `termenv`, `x/term` stay for CLI colour/terminal detection in `root.go`/`auth.go`);
   scrub the 33 stale "TUI" comment references in core; README (8 refs); `make man`;
   CHANGELOG `[Unreleased]` → *Removed*. Archive
   `docs/plans/2026-04-28-tui-implementation.md`. Close #74, #225, #226, #232, #257 with
   a comment pointing at the `serve` epic; drop `area:tui` from #87. Replace the
   `CLAUDE.md` "CLI and TUI are equally first-class" paragraph with the boundary rule
   and the new layout.
2. `go.mod` → `go 1.27`.
3. Delete `GetDefaultLinkMethod` and `Cache`; move `EnabledMergeSourcesForTest` and
   `ReconcilePakManifestsForTest` into `export_test.go`.
4. `internal/app` composition root + #270 XDG (legacy-path fallback when the XDG dir is
   absent and `~/.config/lmm` / `~/.local/share/lmm` exist; `--config`/`--data` still
   override; `cache_path` in config.yaml still wins). Side effect: sandboxes can isolate
   via `XDG_*` instead of faking `HOME`.
5. Boundary ratchet test with its initial allow-list.

### Phase 1 — contracts (one PR each, in order)

1. ctx everywhere + db `*Context` + #271.
2. Concurrency: semaphore, `games` lock, `SaveGame`, race test, `test-race` in `make check`.
3. Events, four PRs: ctx-key → `UpdateCheckEvent` (ModSource change); `DownloadProgress`;
   `VerifyEvent` cleanup; `DeployProgress` (8 methods) + `cmd/lmm/render.go`.
4. slog wiring + `SkipHooks`.
5. JSON: tags on domain + core types, enum `MarshalText`, goldens, json v1/v2 spike decision.

**Feature work pauses until Phase 1 lands** so new features are written against the
contracts from day one.

### Phase 2 — lift flows (one PR per family)

Each PR: characterization tests first; byte-identical CLI; consumed primitives
unexported; the flow's chunk of `flows.go` moved to its own file; allow-list entry
removed.

| # | Lift | From | Notes |
|---|---|---|---|
| 1 | Hook lifecycle sequencing | `cmd/lmm/hooks.go`, `install.go:1140` | Everything below runs hooks. Deletes `cmd/lmm/hooks.go` |
| 2 | File-selection policy | `install.go:967/993`, `profile.go:1303/1383` | CLI keeps prompts + range parsing |
| 3 | Batch/dependency install | `doInstall`, `batchInstallMods` | Extends `PlanInstall`/`ApplyInstall` to batches |
| 4 | Update apply | `applySingleUpdate` | Gating moves into `PlanUpdate` |
| 5 | Profile apply / sync / reorder | `doProfileApply`, `doProfileSync`, `doProfileReorder` | `doProfileApply` is bespoke by its own admission — lift its semantics faithfully first; unifying with `ApplyInstall` is a separate, named decision |
| 6 | Local import scan + adopt | `runImportScan`, `importExistingMod`, `copyFileStreaming` | Naming: core calls these `ScanLocal` / `PlanAdopt` / `ApplyAdopt` ("adopt" = bring an untracked local mod under management); `Import` stays reserved for profile import. The CLI command name `lmm import` is unchanged |
| 7 | Game detect + `SaveGame`/`SaveProfile` | `doGameDetect`, `gameFromDetected` | Removes `source/steam` and most `storage/config` from cmd |
| 8 | Remaining `storage/config` reads | 10 cmd files | Become core queries |
| 9 | Plan/Apply for Deploy/Uninstall/Purge/Rollback + uniform `--dry-run` | core | Retires `ConvergeDeployedFiles(dryRun)` |

### Phase 3 — contract completion

`--json` on mutating commands and the switch to core types (view structs deleted);
`ConfirmConflicts`/`ConfirmInstall` removed in favour of plan data + `ErrStalePlan`;
allow-list empty and its mechanism deleted; `flows.go` gone; doc comments on every
exported core identifier; README architecture + `CLAUDE.md` updated. Then the v2.0.0
timing decision.

## Testing and verification

The pre-change CLI is the spec.

- Before lifting a flow, every output path it has gets a capture test if one is missing
  (characterization first). Byte-identical output is enforced; a deviation is never
  silent — it is named in the PR or fixed.
- Core: `RecordEvents` sequence assertions; cancel-at-each-phase invariant tests; the
  race test; JSON goldens per type and per event envelope.
- Each phase ends with a whole-branch live review in twin sandboxes (branch binary vs
  merge-base binary, byte-diffed parity matrices). Per-PR reviews structurally miss
  cross-cutting regressions; a refactor is nothing but cross-cutting.
- Copilot triage on every PR, including post-push rounds.

## Tracker and process

- One EPIC issue "v2: core refactor for multi-frontend (CLI + serve)" with per-phase
  checklists; one child issue per PR-sized unit (~3 + 6 + 9 + 3); a `v2` label to
  filter (no milestones, per policy).
- A second, mostly-empty EPIC "v2: `lmm serve` local web UI" holds the intents of the
  closed TUI issues as a checklist. Not designed now.
- Process is the develop-branch workflow with `v2` substituted for `develop`, minus
  mandatory PRs: Orca `orchestration` by name; worktrees via
  `orca-ide worktree create --base-branch v2 --issue <n>`; SDD review policy
  (implementer → reviewer → fix waves) per unit; each unit is still its own story
  branch, **merged into `v2` locally with `--no-ff`** so it stays one revertable merge
  commit; issues close at that merge with a comment naming the merge commit; no
  version bumps; CHANGELOG under `[Unreleased]` (the whole block moves at v2.0.0).
  Open a PR `--base v2` only when an independent Copilot review is wanted (recommended
  for the `DeployProgress` contract change and each phase-end integration).
- **v1 work continues on `develop`/`main` untouched.** Anything that lands there and is
  also wanted in v2 is cherry-picked onto `v2` with manual resolution (expected once the
  TUI is gone); never merge `develop` or `main` wholesale into `v2`. Keep v1 traffic to
  fixes so the cherry-pick load stays small.
- This document lives in `docs/plans/` (gitignored in-flight) and is force-added on the
  unprotected `docs/v2-core-refactor` branch (never merged); it archives to
  `docs/plans/archive/` on the Phase 3 PR.

## Out of scope

`serve` itself (HTTP, SSE, job registry, web UI, localhost auth); per-game locking;
NDJSON event stream in the CLI; extracting `steam`/`storage/cache`/`httpclient` as
external libraries; #79 token encryption; feature issues (#269 Steam Workshop, #267
BepInEx) — they resume after Phase 1 on the new contracts.

## Risks

- **Untested CLI behaviours** — mitigated by characterization-first per flow.
- **`doProfileApply` semantics** may diverge from `ApplyInstall`; lifted faithfully,
  unified only by a named decision.
- **Merge friction** with concurrent feature work — mitigated by the Phase 1 pause.
- **json v2 semantics** — spiked against release notes, not assumed.
- **v1 fixes needed on v2** after TUI removal — cherry-pick, expect manual resolution;
  the longer `v2` lives, the more `develop` drifts, so v1 traffic should stay fix-only.

## Decisions log

| Date | Decision | Why |
|---|---|---|
| 2026-08-27 | Drop the TUI; GUI = `lmm serve` | Parity tax; static binary; browser tab acceptable |
| 2026-08-27 | TUI removed first, not carried | Adapter is Bubble-Tea-shaped, not HTTP-shaped; avoids 3× seam changes |
| 2026-08-27 | Approach C (contracts first) | Deliberate API without rewrite risk; develop stays shippable |
| 2026-08-27 | v1.30.x stays on develop/main; v2 on a dedicated `v2` branch until one v2.0.0 cut | Sparse versioning; v1 fixes keep flowing; JSON shapes may change once |
| 2026-08-27 | No mandatory PRs on `v2` until public release; deletion + force-push still blocked; `--no-ff` local merges per unit | Solo-dev velocity; SDD loop + phase-end live review are the real gates; history stays revertable per unit |
| 2026-08-27 | `internal/app` composition root | Keeps concrete sources out of core; one bootstrap for both frontends |
| 2026-08-27 | Boundary enforced by ratchet test | The parity directive was unenforced and half-worked |
| 2026-08-27 | Trailing `EventSink` param; typed events; fixed envelope | Explicit, per-operation, serializable; kills ctx smuggling |
| 2026-08-27 | `Result.Warnings` authoritative; `WarningEvent` additive | Byte-identical CLI; live display for serve |
| 2026-08-27 | Mod-boundary cancellation; DB==disk per mod | Rollback-per-mod is over-engineering; verify/converge already repair partial profiles |
| 2026-08-27 | Service-wide mutation serialization via ctx-aware semaphore | Two deploys on one game is never wanted; per-game is YAGNI |
| 2026-08-27 | Events suppressed under `--json` | One-document-on-stdout invariant; streaming is serve's job |
| 2026-08-27 | Plan/Apply for all mutations except setters/reorder; no frontend callbacks from Apply | Serializable previews; works over HTTP |
| pending | v2.0.0 timing (after Phase 3 vs after serve) | User decides at Phase 3 close |
| 2026-08-27 | Update-check progress via an optional `source.UpdateProgressReporter` interface, not a `ModSource.CheckUpdates` signature change | Same explicitness, no churn across 6 implementations + 18 test mocks; only nexusmods/curseforge report per-mod |
| 2026-08-27 | slog is wired to a new `--log-level` flag (default off), not to `--verbose` | `-v` prints 21 user-facing stdout lines that must stay byte-identical; diagnostics are a separate channel |
| 2026-08-27 | Phase 1 threads ctx through `Service`, `storage/db`, `Extractor.Extract`, `Cache.CloneMod`, `Installer` I/O methods — not `ProfileManager`, `storage/config`, or `linker` | Those are single-file YAML/link ops with nothing to cancel; they are reshaped in Phase 2 anyway |
| 2026-08-27 | `VerifyEvent.Green` → semantic `Fixed bool`; `Variant` → `ChecksumPopulated bool`; `"fixed_green"` dropped (dead) | No presentation hints in events; the renderer derives colour from meaning |
| 2026-08-27 | Every flow event carries `Phase DeployPhase`; the CLI renders via a cmd-side projection (`lineOf`) so the 8 closures stay byte-identical | The 63-phase vocabulary IS the CLI print contract; typed payloads are for serve |
| 2026-08-27 | `encoding/json/v2` for the core contract goldens (Deterministic + indent); CLI `--json` unchanged until Phase 3 | v2 defaults (nil slice → `[]`, strict names) enforce the contract rules; Go 1.27 ships it stable |
| 2026-08-28 | `yaml.v3` honours `TextMarshaler`: `storage/config` DTOs keep plain-string enum fields; never yaml-marshal domain types directly | Task 18's `MarshalText` on `LinkMethod`/`DeployMode`/`UpdatePolicy` would silently switch `yaml.v3` output from int to name for any struct that yaml-marshals the domain type itself |
| 2026-08-28 | error-as-text wire convention: `Err error json:"-"` + `ErrorMessage string json:"error,omitempty"` (`SourceWarning`, `ScanResult`; `BatchResult` when tagged — #285) (superseded 2026-08-28: BatchResult deleted, see below) | One convention for "error rendered as text" instead of two divergent Go/wire names for the same idea |
| 2026-08-28 | `beginOp` checks `ctx.Err()` before the semaphore `select`, not just inside it | Deterministic loss on a done ctx instead of a 50/50 race between the two `select` arms |
| 2026-08-28 | `app.Open` takes ctx; bootstrap token reads are cancellable; the sanctioned `context.Background()` sites remain three | `internal/app/sources.go`'s `ResolveAPIKey` call was the fourth, unadjudicated site; threading ctx through `Open` closes it rather than adding a fourth exception |
| 2026-08-28 | `lmm game detect` opens SQLite/config via `app.Open` since `710f13f` — intentional user-visible delta (a corrupt `lmm.db` now fails `detect`) | Consistency with every other command, and `SaveGame` needs a `Service`; accepted, not a regression |
| 2026-08-28 | `verifyRun` storing the per-call ctx in a struct field is accepted, not a "ctx in a struct" violation | Request-scoped struct created and discarded inside one `Verify` call; the no-ctx-in-structs rule targets long-lived objects, not this shape |
| 2026-08-28 | `cmd/lmm/hooks.go` deleted at the end of Unit K, not Unit F | Unit F only moves hook resolution into core; the two remaining cmd-side hook sequencers (`batchInstallMods`, `doImport`'s archive tail) die with their engines in Units H and K - the spec's "#1 deletes hooks.go" is satisfied by the end of #6 |
| 2026-08-28 | `Installer.InstallBatch`/`UninstallBatch`, `BatchOptions`, `BatchResult`, `SkippedMod`, `InstalledModResult`, `UninstalledModResult` deleted | Zero production callers (`applyInstallBatchMod` is the live batch engine); retires #284 item 2 and #285 item 1 without tagging dead types |
| 2026-08-28 | `ScanResult.Err`/`ErrorMessage` removed | Never set by `ScanModPath` (YAGNI); a future per-entry error need adds them back with the error-as-text convention in Unit K |
| 2026-08-28 | `domain.DeployError` stays untagged | It's an error type, not a wire type - the existing exclusion comment is the record |
| 2026-08-28 | `Plan` structs carry an unexported `snapshot` (`json:"-"`) of the installed-mod set they were computed from; every `Apply` re-derives it and returns `ErrStalePlan` on mismatch | One helper (`installedSnapshot` + `checkPlanFresh`) used by every new Apply; existing pairs (Install, Import, Switch) adopt it in the unit that touches them (H, K, J) |
| 2026-08-28 | Prompts stay callbacks in Phase 2 | `ConfirmConflicts`/`ConfirmInstall` are removed in Phase 3 per spec; new Plan/Apply pairs expose the decision as plan data + an `Options` bool (`AcceptConflicts`, `Confirmed`) from day one so nothing new needs Phase 3 surgery |
| 2026-08-28 | `--dry-run` added only where the spec says (deploy, uninstall, purge; update already has it) | `ConvergeDeployedFiles(dryRun)` is unexported in Unit M and its `dryRun` bool survives internally until `PlanDeploy` subsumes it; retiring the bool entirely is Phase 3 |
| 2026-08-28 | `DetectedGame` becomes `domain.DetectedGame`; Steam scanning is exposed by `internal/app` (`app.DetectGames`), not `core` | Core must not import concrete sources |
| 2026-08-28 | The swallowed lock refusal in `doProfileApply`/`doProfileSync` (an `UpsertMod` error is a `--verbose`-only warning) preserved byte-for-byte in Phase 2 | Filed as a Phase 3 behaviour fix (Task 0 files it) |
| 2026-08-28 | `#283` (per-source update-check counters) stays deferred to Phase 3 | A global counter changes `-v` output |
| 2026-08-28 | `--log-level` validates via a `pflag.Value` (`logLevelFlag`), not a bare `PersistentPreRunE` check, with pflag's `*InvalidValueError` wrapper stripped for that flag only in `rootCmd.SetFlagErrorFunc` | Cobra's `Command.execute()` checks `--help`/`--version` and returns before ever walking the parent chain for `PersistentPreRunE`, so only Set-time validation rejects a bad level on those paths; the uniform flag-shaped error text this produces replaces the former `initializing service:` attribution on every path - a deliberate user-visible delta (#284) |
| 2026-08-28 | Plan-time reads precede Apply-time hooks: `install.before_all`/`install.before_each` run after the plan's metadata fetches (multi-select path since #288; dependency path since `PlanInstall`) | Inherent to Plan/Apply; hook authors must not rely on `before_all` preceding source reads |
| 2026-08-28 | scan import: the `Scanning` line precedes the installed-set read; a DB read failure now surfaces after it (#291) | `ScanLocal` reads the installed mods before it scans, where the pre-lift engine read them before printing `Scanning`; only a DB read failure exposes the one extra leading line, and restoring parity would cost a redundant read in cmd plus engine sequencing back in the frontend |
| 2026-08-28 | adopt backfill fetches run inside `ApplyAdoptBackfill`, not the plan, so the `-v` line interleaving stays byte-identical; the backfill is its own Apply because the pre-lift engine saved it before the confirm prompt | The three per-row `-v` lines (fetch failed / save failed / metadata updated) are emitted per row in installed order, which one loop reproduces exactly; a plan-time split would have to carry a per-candidate `FetchError` and re-interleave it. Network reads inside an Apply are permitted HERE for that reason - not as general precedent |
| 2026-08-28 | adopt source matching runs at plan time (`PlanAdopt`), so match lines no longer stream during the scan | Symmetric with the backfill row above: `matchUntracked` resolves every untracked entry's source hit before `runImportScan` renders anything, where the pre-lift `tryMatchSources` printed each match as it was found mid-scan; nothing downstream depends on that interleaving, so the plan-time shift costs nothing to accept |
