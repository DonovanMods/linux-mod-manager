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
| 2026-08-29 | `DeployProfile`/`UninstallMod`/`PurgeProfile` stay exported as Plan+Apply conveniences despite having zero non-test callers | Kept for core tests and for any frontend that wants one call instead of Plan-then-Apply; resolves Task 25 review Important #1 without unexporting or deleting them |
| 2026-08-29 | `uninstall`/`purge --dry-run` on a `DeployCompile` game always states the merged-artifact resync/removal line, even in the case (e.g. nothing merged yet) where nothing would actually change | Always in the safe direction (over-states removal/resync, never under-states); modelling it precisely as a plan field is Phase 3, once Unit M's merge work lands (Task 25 review Minor #1) (superseded 2026-08-29: see the Ruling 8/#296 implementation row below — the dry-run line is now conditional, not unconditional) |
| 2026-08-29 | Exported `*core.Service` methods stand at 103 at Phase 2 close vs the plan's ≤60 target | Plan/Apply pairs outpaced unexports; Phase 3 route: delete cmd view structs (`--json` emits core types), unexport fixture-only survivors (`SaveFileChecksum`, `DownloadMod`, `SaveInstalledMod`, `GetInstaller`, …) via test seams, decide the Plan+Apply conveniences, delete the boundary allow-list mechanism |
| 2026-08-29 | `update rollback` maps only `domain.ErrModNotFound` to "mod not found"; a DB-level `GetInstalledMod` failure now surfaces its own error instead of being relabelled | The pre-lift blanket mapping hid real failures; the new text is strictly more informative and no golden or capture pins the old one |
| 2026-08-29 | Ruling 1: Plans stay side-effect-free re: managed state (a cache fill isn't a mutation); conflicts for a not-yet-cached mod surface in Apply as `*core.ConflictError` before any deploy/DB write; `ConfirmConflicts`/`ConfirmInstall` callbacks deleted, replaced by `AcceptConflicts`/`ProfileImportOptions.Install` | The frontend re-runs Apply with the decision instead of core calling back into it |
| 2026-08-29 | Ruling 2: non-interactive rule - under `--json` the CLI never reads stdin; every prompt gets a deciding flag (`-y/--yes` on `profile switch`/`sync`, `--all`/`--select` on `game detect`); without it a `--json` run fails before mutating with `ErrConfirmationRequired`; `game add`/`auth login` stay interactive-only and reject `--json` with `ErrInteractiveOnly` | Serializable, scriptable JSON mode requires no interactive fallback |
| 2026-08-29 | Ruling 3: JSON shapes change once - every `--json` document becomes a core/domain/query type via `encoding/json/v2` + `Deterministic(true)` + 2-space indent; the error envelope becomes `{"error":...,"details":...}`; `source list` becomes indented like everything else | One deliberate break in the v2.0.0 window instead of accreting ad hoc view structs |
| 2026-08-29 | Ruling 4 (#298, #299): `ResolveReorder` ambiguity candidates sorted by source ID; `PlanProfileSync` buckets ordered by `OrderByProfile` (profile order, then installed order for additions); `ListGames`/`lmm status` ordered by game ID; affected plain-text captures re-pinned once | Map iteration order previously leaked into user-visible output |
| 2026-08-29 | Ruling 5 (#294): the CLASS of swallowed `UpsertMod` failures becomes a non-verbose `Warning:` line - a refused/failed profile write, today a lock refusal but also any profile load/save failure (e.g. `ErrProfileNotFound`), widened from lock-only in the Task 13 review's round 1 fix wave - and not just the two originally-named sites: `profile apply`/`sync` at ruling time, `profile switch` joined in Task 13b; the four hand-worded refusals in `update.go`/`mod_edit.go` print `plan.Refusal`/`core.LockedRefRefusalError` text. **Refined by the unit Q review (I1, 2026-08-29): one wording per refusal KIND, not one string.** `core.LockedRefUnlockOnlyRefusalError` is a second variant of the canonical refusal, built by the same sentence builder (`lockedRefSentence`) and differing only in the remedy clause: the gates that refuse on `ref.Locked` ALONE, regardless of version - `ApplyUpdate`, `ApplyRollback`, `ApplyRelinkMod`'s re-link branch - say "unlock with 'lmm mod unlock -s ... -p ... <id>' first" and never name `lmm mod lock`, because moving the lock leaves all three still refusing; the gates that compare the ref's locked version against a target - `lockedInstallRefusal`, `applyInstallBatchMod`, `ApplyRelinkMod`'s metadata-only `--version` guard, and `ProfileManager.UpsertMod` (the profile apply/sync/switch warning, wording out of scope per #311) - keep the two-remedy wording, verified: moving the lock to the target version does unblock `UpsertMod`. **Also (M1):** all three `Plan.Refusal` fields - `UpdatePlan`, `RollbackPlan`, `RelinkPlan` - now carry the SENTENCE half only, never the `ErrModLocked` sentinel prefix, since `cmd/lmm` prints them verbatim after its own context line and the wrapped form read "mod is locked: `<mod>` is locked at v..."; the errors the matching `Apply*` return keep the sentinel, so `errors.Is` is unaffected. This moves the `refusal` key's value in `update_plan`/`rollback_plan` (`relink_plan` already carried the sentence). **Also (M3):** on the FATAL path of `profile apply`/`sync`/`switch` there is no result document to carry `Result.Warnings`, and Ruling 15 forbids the stderr line under `--json`, so the accumulated warnings were reaching neither stream; `cmd/lmm`'s `profileWarningsError` (an `Unwrap`+`Details() any` wrapper on the `core.ConflictError`/`gameDetectPartialError` pattern) now carries them into the error envelope as `details.warnings`, constructed only when there is at least one, so a warning-free fatal run still emits the bare `{"error": ...}` envelope. Plain text is unchanged. | A silently dropped refusal read as success. The refinement: Ruling 5's rationale is that the call sites of one refusal can never drift apart in wording, which one string per KIND satisfies; a single string across kinds instead made `mod edit`'s previously-correct "unlock ... first" guidance name a remedy the gate provably does not honour (unit Q review I1, reproduced end-to-end). Goldens re-recorded once for it: `relink_plan`, `update_plan`, `rollback_plan`, plus the four byte-exact cmd captures |
| 2026-08-29 | Ruling 6 (#283): `UpdateCheckEvent` gains `GlobalIndex`/`GlobalTotal`; `-v` prints the global pair | Per-source counters restarting at 1 misrepresented overall progress |
| 2026-08-29 | Ruling 7: `profile import`'s install prompt moves before Apply (the profile is no longer saved before the question); install/import conflict prompts stay where they were, but a re-run Apply may print its download line as cached (the second clause superseded 2026-08-29 by the Apply-order row below) | Ruling 1's Plan/Apply split needs a fixed, recorded prompt order |
| 2026-08-29 | Ruling 8: `UninstallPlan`/`PurgePlan` gain `MergedArtifact *MergedArtifactEffect` computed from the profile; the unconditional dry-run resync/removal line becomes conditional; `ImportArchiveResult.MergedPakSynced` reflects what actually ran | The prior unconditional line over-stated merged-pak effects |
| 2026-08-29 | Ruling 9: `game detect`'s non-interactive flags (`--all`, `--select`) add output only when passed | Keeps the default interactive path byte-identical |
| 2026-08-29 | Ruling 10: exported-`*Service`-method count is an output, not a target; the ≤60 goal is retired in favor of "every export is a flow, a legitimate query, or a documented test-seed API" | Plan/Apply pairs outpaced the original budget without adding real bloat |
| 2026-08-29 | Ruling 11: `ProfileManager` gains ctx (the Phase 1 deferral that never happened); sanctioned `context.Background()` sites drop to two (db.New migrate, root.go signal root) | Closes the last unadjudicated context.Background() site |
| 2026-08-29 | Ruling 12 (#300): `core` must not import any concrete source package - the `*custom.Directory` assertion becomes a `source.LocalFileServer` capability interface implemented by `custom.Directory`; core's boundary test covers every `internal/source/*` package except `internal/source` and `httpclient` | The last concrete-source import broke the architecture's own boundary rule |
| 2026-08-29 | Ruling 13: `flows.go` dissolves into `mod_toggle.go`, `phases.go`, `switch.go`, `profile_import.go`, `hooks.go`, `selection.go`; `OrderByProfile` lands in `deploy.go` | A single 2,048-line grab-bag file outlived its purpose once the flows it held stabilized |
| 2026-08-29 | Ruling 14: v2.0.0 mechanics (timing, Go module path) are the owner's call; Unit S prepares a release checklist but does not execute it | The plan implements the contract, not the release decision |
| 2026-08-29 | Ruling 15: `--dry-run --json` emits the Plan document; `--json` on an applying command emits the Result document; a decline/`ErrConfirmationRequired` emits the error envelope; events stay suppressed under `--json` | Keeps `--json` to exactly one document on stdout regardless of path |
| 2026-08-29 | #294: lock refusals are one wording (`LockedRefRefusalError`) everywhere; `UpsertMod` refusals inside profile apply/sync are warnings, not `-v` notes | Ruling 5 implemented. `relinkLockRefusalMessage` deleted (`RelinkPlan.Refusal` now carries `lockedRefRefusalMessage`'s sentence — no `ErrModLocked` prefix, since `doModEdit` re-wraps it, so the printed error equals `LockedRefRefusalError(...).Error()` byte-for-byte); `update.go`'s three locked branches print `UpdatePlan.Refusal`/`RollbackPlan.Refusal` (documented as unread until now) after a context line, dropping their own "Move the lock:" duplicate because the canonical text already names both `-s`/`-p` remedies. `ApplyProfileApply`/`ApplyProfileSync` emit new `SwitchInstallWarning`/`SyncUpdateWarning` events and append to `Result.Warnings`, which the CLI already prints unconditionally to stderr (`!jsonOutput`); `ApplyProfileSwitch` kept `SwitchInstallNote` at this point — Ruling 5 named apply/sync only, and changing switch would have been an unruled plain-text delta (flagged as a follow-up candidate). Goldens re-recorded once: `relink_plan`, `profile_apply_result`, `profile_sync_result`. **Task 13b (2026-08-29) closed that gap**: Ruling 5 was clarified to cover the whole class of swallowed lock refusals, not just the two named sites (see the Ruling 5 row above), so `ApplyProfileSwitch`'s identical `UpsertMod` refusal now also emits `SwitchInstallWarning` and appends to `SwitchResult.Warnings`; `SwitchInstallNote` is retired (its constant and wire name kept only so a previously recorded `"switch_install_note"` still round-trips). No goldens changed — `switch_result.golden`'s literal fixture never exercised this refusal — but two existing `ApplyProfileSwitch` Go tests that happened to trigger the same `UpsertMod` failure via a different cause (`ErrProfileNotFound`, target profile not yet created) moved their assertions from `Result.Notes` to `Result.Warnings` accordingly. **Task 13 review round 1 (2026-08-29)** fixed a regression the fix itself introduced and widened the ruling once more: (1) the CLI was printing `Result.Warnings` only on the success path, so a warning followed by a later fatal error (apply's/sync's/switch's own loops each have one reachable fatal path - switch's final `SetDefault` call, apply's/sync's own ctx cancellation between loop iterations) was silently dropped instead of reaching stderr; all three now print `Result.Warnings` before returning the fatal error too. (2) The ruling's cause is widened from lock-only to any swallowed `UpsertMod` failure - `ProfileManager.UpsertMod` also fails on `config.LoadProfile`/`SaveProfile` errors (e.g. a missing profile), which the two `ErrProfileNotFound` switch tests above already exercised without a lock in sight, so "lock refusal" was never the true boundary. Also recorded: the warning's position in the output moved from interleaved (printed at its point of occurrence, `--verbose`-only, pre-#294) to the end-of-run batch just before the summary (post-#294, unconditional) - a multi-mod apply can therefore show an earlier mod's refusal printed after a later mod's own install line; harmless (the CLI's own tests capture stdout/stderr separately) but worth recording for a future live-streaming frontend |
| 2026-08-29 | Apply order: cache fill and conflict check precede hooks (install/import archive) so a refused conflict costs no hook run and an accepted re-run does not re-download cached files — consistent with "plan-time reads precede Apply-time hooks" | Ruling 1 turned the decline into a whole second Apply; with the gate behind the hooks that charged the user a duplicate `install.before_all`/`before_each` (non-idempotent hooks run twice) and a duplicate full download. The cache fill is not a mutation of managed state, so it can precede the hooks; the accept re-run then reads warm through the existing `HasFileIDs` cache-first guard (#96/#138) — the cache decides, not `AcceptConflicts`. Two carve-outs re-download/re-ingest by design and stay uncached on purpose: a same-version reinstall (the reinstall-cache transaction always stages into a fresh EMPTY cache, so it can never read warm) and a local directory source (excluded outright — its cache entry mirrors a directory that can change between runs, and the re-ingest is what drops members the source removed, #166; it also costs no network, so skipping it would save nothing) — both pinned by characterization tests (task-8 review, Important 1). A warm fill also computes and saves a checksum from the cached content when the download itself is skipped, so a cache-hit install never regresses to NO CHECKSUM (task-8 review, Important 2). Ruled plain-text delta: download/extract lines now precede hook lines when both print, and the accept re-run prints no second download block except for those two carve-outs |
| 2026-08-29 | #283: `UpdateCheckEvent` gains `GlobalIndex`/`GlobalTotal` (`json:"global_index"/"global_total"`); `Updater.CheckUpdates` reserves each source's batch a slot in a running `globalOffset` (advanced by `len(mods)` before that source's own registry lookup/error is known, so the total stays the sum of every batch regardless of a mid-loop source failure) against a fixed `globalTotal := len(checkable)`; `cmd/lmm/update.go`'s `-v` sink prints `GlobalIndex`/`GlobalTotal` instead of the per-source `Index`/`Total` | Ruling 6 implemented. Per-source `Index`/`Total` are untouched (still restart at 1 per source — `TestUpdater_CheckUpdates_EmitsUpdateCheckEventsPerReportingSource`'s M2 case stays green); only the CLI's printed numbers change. `bySource` is still a map, so which source's batch claims the low end of the global range is nondeterministic per run — `TestUpdater_CheckUpdates_GlobalCounterSpansSources`/`TestDoUpdate_Verbose_GlobalCounterAcrossSources` assert the sequence property (`1..N` with a per-source-constant offset) rather than pinning one order, verified at `-count=20`/`-count=10`. `--json` is a no-op change: `quietSink` keeps `sink` nil under `--json` regardless of `--verbose`, so `UpdateProgressReporter.CheckUpdatesWithProgress` is never invoked and no event (old or new field) reaches the process; `UpdateCheckReport` needed no new field, confirmed by `TestDoUpdate_JSON_TwoSources_NoGlobalCounterLeak`. One events-golden change (the only one of the phase, as scoped): `testdata/events/update_check.golden` gains `"global_index":7,"global_total":12` (the golden table row's sample values, chosen distinct from `Index`/`Total`'s existing 2/5 to make the two pairs visually unmistakable) |
| 2026-08-29 | Ruling 8 implemented (#304), plus #296: (i) `mergedArtifactEffectForUninstall`/`mergedArtifactEffectForPurge` are judged from `enabledMergeSources`+`isDeployedNow` rather than the stored `MergedPakOutcomes`, because `syncMergedPak` diffs CURRENT merge inputs against a STORED fingerprint and the input side is what decides what will happen; the uninstall helper's "mod contributes nothing" branch additionally reads the stored fingerprint (`readMergedFingerprint`) and compares it against `currentMergedFingerprint` via `mergedFingerprintsEqual` before answering nil, so a fingerprint gone stale for a reason unrelated to the uninstalled mod (a base-game patch since the last sync, #196) is still reported as a resync rather than silently missed (task-15 review Important #1, closed same task); the helper's own error paths answer in the same safe direction (unit Q review M6): only `mergeCompilerForGame` failing returns nil, because that is the one failure that leaves no artifact NAME to print - a failed `enabledMergeSources` or `currentMergedFingerprint` leaves the outcome undecidable with the name in hand, and answers resync rather than the silence nil renders as; (ii) `MergedArtifactEffect.Path` is game-dir-relative, matching `UninstallPlan.Files`/`DeployResult.MergedArtifact`'s own convention; (iii) uninstalling the LAST merge source now prints "...would be removed afterwards" where the base always said "...resynced afterwards" — required by Ruling 8's own `resync\|remove` vocabulary, a ruled plain-text delta beyond brief item (b)'s literal "only ... the removed line"; (iv) a `--uninstall` purge whose merged artifact is cached but not deployed now prints no artifact line — the effect models the game directory, not the cache entry, so a cache-only clear is not reported; (v) #296's `domain.ExportedProfile` carries no yaml tags at all (the type is JSON-only) — the exported file needs the profile file's own `*string`-pointer encoding (unset vs. explicitly-empty), which no tag on a `GameHooks`/`GameHooksExplicit` pair can express, so `internal/storage/config` owns that encoding via its own `exportedProfileYAML` DTO rather than yaml-marshalling the domain type (corrected 2026-08-29, unit Q review M4: the row said `yaml:"-"`, which the task-15 fix wave `d842299` had already superseded by removing every yaml tag from the type) | Supersedes the `:531` row above, which is now false: the dry-run line is conditional, not unconditional. (i)'s fingerprint comparison closes the gap between the plan's judgment and `syncMergedPak`'s own fast-path condition that the task-15 review found and Important #1 fixed within the same task |
| 2026-08-29 | Ruling 11 implemented (Task 18, #305): every `ProfileManager` I/O method (`Get`, `GetDefault`, `List`, `ListNames`, `Create`, `CreateOrResetDefault`, `Delete`, `SetDefault`, `AddMod`, `UpsertMod`, `SetModLock`, `ClearModLock`, `RemoveMod`, `ReorderMods`, `Export`, `Import`, `ImportWithOptions`) takes `ctx context.Context` first, with a `ctx.Err()` pre-check before any disk access, matching `storage/db`'s convention; `ParseProfile` stays ctx-less (pure in-memory parse, no I/O, same rule that keeps `storage/config` ctx-less). Sanctioned `context.Background()`/`context.TODO()` sites in non-test code: two (`db.New` migrate, `root.go` signal root) | `UpsertMod`'s new guard exposed a real gap: `profile_apply.go`'s `ToInstall` loop and `profile_sync.go`'s `ToAdd`/`ToRemove`/`ToUpdate` loops treat any mutator failure — including, now, `context.Canceled` — as the #294 non-fatal warning, so a cancellation landing inside `UpsertMod`/`AddMod`/`RemoveMod` could be silently absorbed as an ordinary business refusal instead of propagating fatally (caught by two existing counting-based cancellation tests, `TestDoProfileApply_/TestDoProfileSync_LockedRefWarningSurvivesFatalContextCancellation`). Fixed at those two files only: a swallowed mutator failure now re-checks `ctx.Err()` and returns it fatally before falling into the warning path. The identical shape exists at ~13 other call sites (`switch.go`, `update.go`, `rollback.go`, `mod_edit.go`, `verify_repair.go`, `install.go`, `adopt.go`, `import_archive.go`, `profile_import.go`) with no test currently pinning their cancellation timing — left as a follow-up rather than fixed unasked in a ctx-threading task |
| 2026-08-30 | Ruling 10 implemented (Task 19, #305): `downloadMod`, `getInstaller`, and `purgeMergedPak` unexported - each had zero production callers outside a package-internal wrapper, only test fixtures. `cmd/lmm` fixtures (package main, no export_test.go visibility) reseed through the real flow: `verify_test.go`'s directory-source fixture uses `PlanInstall`/`ApplyInstall` with `InstallOptions.SkipVerify` (which already skips `SaveFileChecksum`, reproducing the exact cache-but-no-checksum state `DownloadMod` gave it); 16 sites across 10 files that hand-drove an `*Installer` now share a `deployInstalledMod` helper (`PlanDeploy`/`ApplyDeploy`), which also records `Deployed`/`LinkMethod` state the raw `Installer.Install` never wrote (one fixture, `seedPurgeableMod`, explicitly reverts `Deployed` afterward to preserve `TestJSONGolden_Purge/dry_run_plan`, pinned against the old fixture's `Deployed:false`-with-files-on-disk state). `internal/core`'s own fixtures (`core_test`, invisible to `cmd/lmm`) use new `export_test.go` shims: `DownloadModForTest` (26 sites / 7 files) and `GetInstallerForTest` (47 sites / 13 files); `TestPurgeMergedPak_AbsentCacheEntry` now drives `purgeMergedPak` through `PurgeProfile` with an empty mods list (the real path a `DeployCompile` purge takes), checking `PurgeResult.Warnings` instead of a direct error return, since `purgeMergedPak`'s failure is non-fatal inside that flow. `SaveInstalledMod`/`GetGameCache`/`SyncMergedPak` (production-caller-free since Unit P's `mod edit`/`mod files` core-flow lift) and `SaveFileChecksum`/`AvailableModVersions`/`IsSourceAuthenticated`/`ScanLocal`/`Logger`/`DeployProfile`/`UninstallMod`/`PurgeProfile` stay exported by ruling, each with a doc comment stating why and (where a fixture surface exists) a site/file count as of this task. Two more, `SetModLinkMethod`/`SetModDeployed`, join the kept-by-ruling set discovered mid-task: 5 of 7 `cmd/lmm/verify_test.go` sites (plus the pattern's own core_test twin) deliberately seed a DB-vs-disk DIVERGENCE (DB says deployed+symlink, nothing on disk) that `ApplyDeploy` cannot produce - that divergence is exactly what `verify --fix` exists to repair - so only the 2 sites that pair with a real `*Installer` deploy were reseeded via `ApplyDeploy` (which drops the manual setter calls, since it makes the identical targeted writes itself). `DeleteInstalledMod` also stays exported: its one `cmd/lmm` caller seeds a merged-pak fingerprint that outlives the mod it names, and both `ApplyUninstall` and `ApplyPurge` additionally resync/purge the merged pak as part of removing a mod, which would erase the exact stale-fingerprint state the test needs. Exported-`*Service`-method count is not tracked as a target or reported here (Ruling 10's own point) | Closes the last unresolved row of the Phase-2-close audit (`:532`): fixture-only survivors are now either unexported behind a real-flow reseed or test seams, or explicitly kept with a reason, per method - not a blanket "keep everything" or "unexport everything" pass. The `Deployed`/`LinkMethod` divergence finding (SetModLinkMethod/SetModDeployed) was not anticipated by the brief, which listed them for unexport; discovered and ruled on mid-task rather than forced through with a weakened assertion |
| 2026-08-30 | Ruling 16 (#305, task-18 review fix wave): the DB→profile pair is a two-step commit and cancellation is adjudicated by sub-class. **(A) Completing writes** — a profile write that completes an already-applied DB mutation (`uninstall.go:287`, `purge.go:138`, `install.go:1823`/`:2279`, `import_archive.go:367`, `adopt.go:617`, `profile_import.go:433`, `mod_edit.go:332`+`:338` (the relink's two profile legs) and `:374` (the version-only edit), `switch.go:474`) runs through the shared `core.completeProfileWrite` helper, which executes it under `context.WithoutCancel(ctx)` so it always finishes, then re-checks `ctx.Err()` and lets the cancellation outrank the write's own error; the mirrored `core.completeDBWrite` (`ops.go:93`) does the identical thing in the OTHER direction — a DB write completing an already-applied profile mutation — for the relink's third leg, `mod_edit.go:343` (`saveInstalledMod` the new DB row under the new identity). A relink is therefore a three-step completion chain, not a pair: `mod_edit.go:319`'s DB delete of the OLD row, then `:332` `RemoveMod` (drop the old ref) → `:338` `UpsertMod` (write the new ref) → `:343` `saveInstalledMod` (the new row), with the cancellation re-checked once, after all three, at `:348`; the version-only edit's OWN plain `saveInstalledMod` call (`mod_edit.go:363`) is now guarded `if !plan.Relink`, so a relink never redundantly re-saves the row its own chain's `:343` leg already wrote (corrected 2026-08-30, re-review round 2 NEW-2: this row previously described the relink as a two-step, profile-only completion — accurate when the row was written at `3bf6afa`, but fix wave round 1b, `57dfcfa`, landed the same day, added the DB-save leg plus `completeDBWrite`, and introduced the `!plan.Relink` guard; neither doc was revisited). Each caller returns its partial result with `context.Canceled` immediately after, so the run stops, no further item is processed, and `result.Installed`-style counters do not credit a run that ended — true for every (A) site including `adopt.go`, which re-review round 2's NEW-1 fix closed: `ApplyAdopt`'s loop had rendered the completing profile write's own cancellation as an ordinary per-mod `AdoptFailed` and kept going, so a Ctrl-C on the last match exited 0 with the fully-committed mod counted as failed rather than adopted. A non-cancellation failure keeps every site's existing warning/note text byte-for-byte. `profile_apply.go:554` and `profile_sync.go:263/:280/:295` deliberately keep Task 18's plain re-check instead: their #294 warning is the user-visible artifact of the refusal and pinning it is what Important 1's new test does. **(B) Pre-write reads/creates** — `ensureProfileExists` returns any error that is not `domain.ErrProfileNotFound` (`errors.Is`, never `==`) instead of mapping "I could not tell" onto "the profile is fine" (task 18 re-review round 2 NEW-10: this reaches an unreadable-but-present profile YAML too, not just a cancellation - a parse error or EACCES on an install now fails loud instead of silently completing, so a batch install into such a profile aborts up front with a fatal "could not create profile: …" instead of running to completion and reporting success with per-mod warnings, and the single-mod/import call sites gain a "Warning: could not create profile: …" line ahead of the pre-existing "could not update profile" warning; accepted as the correct behavior - nothing should be recorded into a profile that will not load); `import_archive.go:350` now calls that same helper rather than restating it with `==`; `verify_repair.go`'s sibling sweep stops and returns `ctx.Err()` (a third return value, non-nil only under cancellation) instead of emitting N identical bogus per-sibling failures. **(C) Tolerant gates** — `install.go:432`/`:913`/`:1675`, `update.go:808`, `rollback.go:300`, `updater.go:237`/`:282`, `hooks_resolve.go:42`, `queries.go:234`/`:241` return `ctx.Err()` when the profile read failed AND the ctx is cancelled; every other read failure keeps today's tolerant "no lock"/"no profiles" answer. Two signatures moved to make (C) expressible: `Service.Status` gains `(*StatusReport, error)` (it was documented "deliberately errorless", which left no way to say "cancelled" while it reported "no profiles" for every game), and `verifyRun.repairSiblingProfiles` gains its third return. Tests are one per SHAPE, not per site: `TestService_PurgeProfile_CancellationBetweenRecordDeleteAndProfileRefRemoval` (A), `TestEnsureProfileExists_CancelledRead_IsReportedNotTreatedAsExisting` (B), `TestLockedInstallRefusal_CancelledRead_DoesNotDegradeTheGateOpen` (C), plus `TestDoProfileApply_CancellationInsideUpsertMod_IsFatalNotWarned` (review Important 1, pinning the apply guard the same commit had left dead) and `TestProfileManager_Mutators_HonourCancellation` (Minor 4). | Supersedes the Ruling 11 row's "left as a follow-up": threading ctx into `ProfileManager` turned "profile write failed" into "possibly a cancellation" at 24 sites, seven of which then returned **exit 0** with a mod in the database, deployed, and absent from its profile YAML — a regression that commit created, invisible to every capture and golden in the repo (which is why deferring it would have meant it never got budgeted). The (A) shape follows Phase 1's Ctrl-C-mid-reinstall lesson: completion and recovery never inherit cancellation, but the cancellation is reported, not swallowed. Also closed here: review Minor 6 (`runListProfiles` takes ctx instead of reading a possibly-nil `cmd.Context()`, and the five test fixtures drop the `SetContext` workaround), Minor 7 (`errors.Is` at `import_archive.go:351`), Minor 5 and Minor 8 (`ProfileManager`'s type doc states the ctx contract, names `ParseProfile` as the exception, and stops pointing at the dissolved `flows.go`) |
