# TUI Verify/Health Surface — Design (#224)

**Date:** 2026-08-06
**Issue:** #224 (from the 2026-08-06 CLI↔TUI parity audit)
**Status:** Approved design, pending implementation plan
**Follows:** #221 (pak conversion — verify is now the lazy-migration/repair story), #217 (convergence always runs)
**Feeds:** #86 (TUI mod details view) — the screen-7 host built here is its landing zone

## Problem

`lmm verify` / `verify --fix` is the entire health-and-repair story — missing
files, checksums, version records, merged-pak freshness, stale-deployment
convergence, and #221's `conversion_failed` / `needs_reingest` lazy
migration — yet it has no TUI surface at all. A user living in the TUI never
learns a pak failed conversion or that their deployment has drifted, and has
no repair action. This is the largest genuine CLI↔TUI parity gap found by the
2026-08-06 audit.

Structural constraint: `doVerify` is ~700 lines in `cmd/lmm/verify.go`
(package `main`, not importable) that orchestrates ~19 core seams but owns
all classification, repair sequencing, and output itself. The TUI cannot
reuse it without an extraction.

## Decisions (user-ratified)

1. **Cost model — local auto + full on demand.** Plain verify is
   network-bound (the per-mod version pre-pass calls `GetModFiles` upstream
   even without `--fix`), so the TUI must not run a full verify implicitly.
   Verify splits into a **Local** tier (disk/DB only) and a **Full** tier
   (adds the network version pass). The dashboard auto-runs Local; Full runs
   only on explicit user action.
2. **Surface — screen 7 as a generic context-view host** with Verify/Health
   as its _home content_ (revised from a bespoke verify screen at user
   request, to pre-pay #86's landing zone). See TUI section.
3. **Fix flow — batch fix behind the standard confirmation modal**, exactly
   mirroring CLI `--fix` semantics (same repairs, same ordering, same
   locked-mod refusals). Per-finding fix is out of scope (same posture as
   #74's deferral).
4. **Architecture — Approach A: full extraction** of the verify engine into
   `internal/core`, findings + progress events; the CLI becomes a renderer
   with byte-identical output enforced by capture tests (the 5a/5b recipe;
   the pre-refactor CLI output is the spec). Parity directive satisfied with
   no CLI-vs-TUI twins.

## Architecture

### Core verify engine (`internal/core/verify.go`)

```go
type VerifyTier int          // VerifyLocal | VerifyFull
type VerifyOptions struct {
    Tier      VerifyTier
    Fix       bool           // apply repairs (CLI --fix semantics, identical ordering)
    ModFilter string         // CLI's optional [mod-id] argument
}
type VerifyFinding struct {  // same vocabulary as today's verify JSON rows
    ModID   string
    ModName string
    FileID  string           // file ID, or game-dir path for stale_deployment rows (shipped v1.29.0 contract)
    Status  string           // ok|missing|no_checksum|file_count_mismatch|skipped|version_mismatch|
                             // version_unverifiable|stale_compile|stale_deployment|fixed_stale_deployment|
                             // conversion_failed|needs_reingest|fixed_needs_reingest (+ fix variants per today's contract)
    Note    string
}
type VerifyResult struct {
    Findings         []VerifyFinding
    Issues, Warnings int      // counting rules live HERE — single source of truth
}
func (s *Service) Verify(ctx context.Context, game *domain.Game, profile string,
    opts VerifyOptions, progress func(VerifyEvent)) (*VerifyResult, error)
```

- **Local tier:** checksum/missing-file walk, file-count check, merged-pak
  staleness + conversion outcomes (`MergedPakOutcomes`), pak re-ingest
  detection (`PakNeedsReingest`), stale-deployment convergence
  (`ConvergeDeployedFiles`, dry-run unless Fix). No network — guaranteed by
  test (a source mock that fails the test if called).
- **Full tier:** Local + the per-mod upstream version pre-pass (the only
  network consumer; `skipped` finding on source-unreachable, never fatal).
- **Fix mode:** the same engine applying today's repairs in today's order —
  re-download → checksum fill → version re-key (incl. cross-profile sibling
  repair) → merged-pak resync → pak re-ingest → convergence removal — with
  identical locked-mod refusal semantics.
- **Events:** the engine emits typed `VerifyEvent`s as work happens
  (finding-produced, phase progress e.g. "checking versions i/N",
  stderr-bound sync warnings). Output ORDER is part of the CLI contract, so
  findings are emitted in exactly today's production order. Follows the
  existing flows-events pattern (`internal/core/flows.go`).
- `reportConvergencePass` (#217) and the rest of `doVerify`'s inline logic
  fold INTO the engine; the JSON contract is the completeness proof (text
  and JSON already render from the same data points — findings formalize
  those points).

### CLI (`cmd/lmm/verify.go`) — pure renderer

`doVerify` keeps flag/profile resolution, then calls the engine with a
progress callback that prints exactly today's lines (colors, remedy hints,
plain-vs-fix variants) from event fields; `--json` collects findings into
the unchanged `verifyJSONOutput` shape. **Golden capture transcripts of the
pre-refactor binary are recorded FIRST** (plain / `--fix` / `--json` across
representative scenarios) and pinned by capture tests; the existing dense
verify suite stays untouched and green.

### TUI

**Screen-7 context-view host** (`internal/tui`): the 7th nav slot (digit
`7`, tab-cycle after Conflicts) is a host component rendering pluggable
content that satisfies a small interface — title, body render, keymap,
help-panel group. The host owns chrome, scrolling, and single-flight wiring
once; content views stay small.

- **Verify/Health is the home content:** digit 7, tab-cycle, and the
  dashboard jump land there when nothing contextual is pushed.
- **One-deep view stack now:** contextual content (e.g. #86's mod details,
  entered from a selected mod) will be _pushed_ onto the host; `esc` pops
  back to the originating screen; popping empty shows home (verify).
  **YAGNI boundary:** no content registry, no multi-level stack — just the
  interface, the home view, and esc-back semantics #86 will exercise.
- Nav label: **"Health"** (theme layouts may restyle, but the semantic name
  is Health); help panel gains the group; nav digit handling extends to 7.

**Verify home content** (all Section-3 semantics as approved):

- Two panes like Conflicts: findings list (`STATUS / MOD / FILE`,
  status-tinted rows) + detail pane (full note and the CLI's remedy
  guidance). Header shows last scan tier + relative age. Empty state:
  "no findings (local) — run a full check (c)".
- `c` — run the Full (network) check with live status-line progress;
  single-flight; cancellable by quit-drain.
- `F` — batch fix: standard confirmation modal summarizing repairs (counts
  by category, detail lines capped "+N more"), then engine fix mode with
  progress; completion shows per-item results in the info overlay (update-
  batch pattern), then auto re-runs the Local scan so screen + dashboard
  refresh.

**Dashboard:** health line in the summary block — `Health: N issue(s), M
warning(s) (local)` / `Health: OK (local)` — computed by an async Local scan
on load and on game/profile rebind (conflicts-count pattern; `?` until it
lands). Command menu gains a "Verify Integrity" entry jumping to screen 7.

**Help-text staleness (recurring trap):** `cmd/lmm/tui.go` `Long` help,
README TUI section, and the help panel all gain the verify capability in the
same change.

## Error handling

Same posture as the CLI: per-mod source-unreachable → `skipped` findings,
never aborts; locked-mod repair refusals are findings (`version_mismatch` +
`locked` note); per-item fix failures surface as findings and the run
continues (joined-error semantics). Only environmental failures (DB/config
unreadable) abort — TUI status line, CLI error. Context cancellation is
honored between per-mod steps (quit-drain works mid-network-pass). A failed
dashboard auto-scan is non-fatal: health shows `?`, error on the status
line.

## Testing

| Layer       | Approach                                                                                                                                                                          |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Core engine | Table-driven per status (in-memory SQLite, `t.TempDir()`); fix-mode fault-injection (existing patterns); Local-tier never-touches-network proof via failing mock source           |
| CLI         | Golden capture tests recorded from the PRE-refactor binary — byte-identical stdout for plain/`--fix`/`--json` representative scenarios; existing verify suite untouched and green |
| TUI         | Model tests in existing `internal/tui` style: host push/pop + esc-back, verify rendering, key gating, fix confirm flow, single-flight refusals; dashboard-scan-is-Local seam test |
| Process     | SDD per-task reviews, final whole-branch live review, README/CHANGELOG, **user interactive smoke test gates the merge** (TUI work)                                                |

## Out of scope

- Per-finding fix (deferred like #74's per-item selection).
- #86/#87 content views themselves (this story only builds their host).
- Any change to CLI verify behavior or its JSON contract (byte-identical is
  a hard requirement).
- Verify row-reachability for fully-uninstalled mods (#217's noted second
  gap — unfiled by decision).

## Success criteria

1. CLI verify output (text and JSON, plain and `--fix`) is byte-identical to
   pre-refactor across the capture scenarios.
2. TUI dashboard shows the local health signal automatically; screen 7 home
   shows findings; `c` runs the full pass; `F` repairs with confirmation —
   all against the same core engine, no duplicated classification logic.
3. #221's `conversion_failed`/`needs_reingest` states are visible and
   repairable from the TUI.
4. The host interface supports pushing a second content view (proven by
   test, not by building #86).
5. Full suite green; user smoke test passes.
