# TUI Phase 6b — Conflicts, Reorder, Rollback, Export/Import (Design)

**Date:** 2026-07-23
**Target version:** v1.14.0 (MINOR), one branch, one PR
**Refs:** issue #37 (Phase 6 scope additions), roadmap `docs/plans/2026-04-28-tui-implementation.md` (Phase 6 section + CLI-parity tables), issue #42 (deferred height/width polish — out of scope here)
**Prior art:** Phase 6a design/impl (archive, 2026-07-23), Phase 5b impl (archive, 2026-07-21)

Phase 6b completes the Phase 6 roadmap: the four remaining workstreams are the
conflict view, load-order reordering, update rollback + changelogs, and profile
export/import entry points. It also fixes deploy ordering, which the conflict
view's "load-order winner" column depends on.

**Guiding constraint (user directive, 2026-07-23):** CLI/TUI parity extends
to implementation — no separate CLI-vs-TUI functions accomplishing the same
task. Shared behavior lives in `internal/core` (Plan*/Apply* + events),
consumed by both interfaces; new capability data surfaces in both. Interface-
specific concerns (arg parsing, prompting, rendering) stay interface-side.

Out of scope: everything in the roadmap's "Deliberately CLI-only for now" table
(auth, local import, verify, mod edit, profile sync/apply, game add/detect/
set-default, settings, configurable keybindings), issue #42's height/width
overflow class, and issue #68's 6a polish.

---

## 0. Deterministic deploy ordering (foundation, behavior change)

**Problem.** File-conflict winners are decided by deploy order (last writer
wins, recorded via `db.SaveDeployedFile`), but every place that deploys a *set*
of mods iterates a Go map (`installedByKey` in `doProfileApply`; equivalent
iteration in `ApplyProfileSwitch`), so winners are nondeterministic and
`profile reorder` has no reliable effect.

**Change.** All multi-mod deploy paths iterate in **profile load order**
(`domain.Profile.Mods`; first = lowest priority; later deploys overwrite
earlier):

- `Service.ApplyProfileSwitch` (internal/core/flows.go) — the enable/deploy
  loop follows the target profile's `Mods` order.
- CLI `doProfileApply` and any other cmd/ loop that deploys multiple mods
  (audit during implementation; `deploy` command included).
- Installed mods **not** listed in the profile's `Mods` slice deploy first, in
  stable (sorted-key) order, so that ordered mods always win over unlisted
  stragglers and output stays deterministic.

**Consequences.** Deploys become reproducible; reorder + redeploy actually
flips conflict winners. This is a user-visible behavior change → its own
CHANGELOG entry. CLI output *format* must not change (existing capture/parity
expectations hold); only line *ordering* within multi-mod operations may
change, and where existing tests pinned an order they are updated
deliberately, not silently.

**Tests.** Two mods sharing a file: deploy profile → later-ordered mod owns
the file; reorder + redeploy → ownership flips. Unlisted-mod case: listed mod
beats unlisted provider.

---

## 1. Conflict view

### Core extraction

**Detection bug fix (user-approved 2026-07-24):** the pre-6b `lmm conflicts`
could never report a conflict — `deployed_files` keeps a single owner per
path, and the CLI unioned each mod's *currently-owned* files, which cannot
collide. Detection now sources each mod's provided files from its cache
manifest (the same source install-time `GetConflicts` uses), with ownership
still from the DB. This is a Fixed entry in the CHANGELOG, and the capture
baseline pins the newly-functional output (formats unchanged).

The aggregation logic in `cmd/lmm/conflicts.go:doConflicts` (owner via
`GetFileOwner`, display-name mapping) moves to core as a pure read-only
query:

```go
// internal/core (service or a conflicts.go)
type ProfileConflict struct {
    Path            string
    Owner           ModRef   // current owner per DB (key + display name)
    AlsoIn          []ModRef // other providers, profile load order
    LoadOrderWinner ModRef   // last provider in profile load order
    Stale           bool     // Owner != LoadOrderWinner (reordered since deploy)
}

func (s *Service) GetProfileConflicts(ctx, game, profileName) ([]ProfileConflict, error)
```

`LoadOrderWinner` = the provider appearing **last** in `Profile.Mods`;
providers not in the profile's mod list sort before listed ones (mirroring
section 0). `Stale` flags rows where the DB owner disagrees — i.e. the user
reordered (or ordering was historically nondeterministic) and a redeploy would
change the winner.

CLI `lmm conflicts` (text + `--json`) rewrites on top of
`GetProfileConflicts`. Per the CLI/TUI-parity directive, the CLI surfaces the
new data too, **additively**: existing lines/fields keep their exact format
(capture tests pin them before extraction), and each conflict gains a
`Winner:` line — with a stale marker and redeploy hint when the DB owner
disagrees with load order — while `--json` gains additive `winner` and
`stale` fields. The capture tests are then deliberately updated to pin the
new output.

### TUI screen

New top-level `ScreenConflicts`:

- Nav: key `6`, appended to the tab/number rotation after Sources
  (`navigation.go` screens slice, `keys.go`, screen labels, per-screen help).
- Layout: file list (columns `FILE / OWNER / WINNER`, stale rows visually
  marked) over a detail pane for the selected file: all providing mods in
  load order, owner and winner called out, plus a resolution hint —
  - stale: "load order says X should win — deploy (D) to apply"
  - in sync: "reorder mods (shift+j/k on Installed Mods) to change the winner"
- Empty state: "No conflicts detected."
- Data flows through read-only `DataProvider` (new `Conflicts()` method), with
  prototype fixtures including at least one stale and one in-sync conflict.
- Dashboard: the conflict count placeholder (`?`) wires to the real count of
  conflicting files; refreshes with the existing refresh cycle.
- 80x24: same truncate-degrade rules as other screens; exact-height invariant
  holds.

---

## 2. Load-order reorder (Installed Mods)

- Keys: `shift+j` / `shift+k` (aliases `ctrl+down` / `ctrl+up`) move the
  selected mod down/up one slot. Selection follows the moved mod.
- Each successful move persists immediately: new
  `ActionProvider.ReorderMods(orderedKeys []string)` → `coreProvider` builds
  `[]domain.ModReference` and calls `ProfileManager.ReorderMods`. Errors
  surface in the status line and the list refreshes to disk truth.
- After any successful move, the status line hints
  "order changed — deploy (D) to apply" until the next deploy or profile
  switch clears it (model-local flag; no persistence).
- **Guards:** reorder keys are inert (with a status-line explanation) while
  the list is search-filtered or an alternate list is active (`activeMods()`
  is not the full profile order — a partial view cannot express a total
  order), and while an action is running/pending (standard single-flight
  guard).
- Prototype provider implements reorder on its fixture slice through the same
  seam.
- CLI `profile reorder` is unchanged.

---

## 3. Update rollback + changelogs

### Rollback core extraction

`cmd/lmm/update.go:doUpdateRollback` (~130 lines of orchestration) moves to
core, following the `ApplyUpdate` pattern:

```go
func (s *Service) ApplyRollback(ctx, game, profileName, modKey, progress ProgressFunc) (RollbackResult, error)
```

Preserved behavior, in order: guard `PreviousVersion != ""`; previous version
present in cache; uninstall/install hook gauntlet (before_each Force-gated,
after_each non-fatal → warnings); `installer.Replace(current → previous)`;
`RollbackModVersion` DB swap; `SetModLinkMethod`; `pm.UpsertMod`; compensating
reverse-`Replace` if the post-replace bookkeeping fails. Progress events reuse
the `DeployProgress` event vocabulary (new rollback event kinds as needed).
CLI rewrites on top; **byte-identical output**, capture-tested.

### TUI rollback

- Key `<` on Installed Mods, enabled only when the selected mod has
  `PreviousVersion` (help entry always listed; status-line explanation when
  unavailable).
- Confirmation modal: "Roll back <name> v<current> → v<previous>?" plus the
  standard partial-mutation warning; then the normal progress pump,
  refresh-always, and outcome reporting via new `ActionProvider.Rollback`.
- Prototype provider simulates a rollback (version strings swap).

### Changelogs

- `UpdateItem` (actions_provider.go) gains `Changelog string`, populated from
  `domain.Update.Changelog` with the same HTML-strip cleaning the CLI applies
  (cleaning helper shared, not duplicated; full text, no 800-char truncation —
  the overlay scrolls).
- In the apply-updates confirmation modal, `v` views changelogs: exactly one
  update → open scrollable `infoOverlay` directly; multiple → `pendingPicker`
  of updates first. Empty changelog (e.g. CurseForge) renders
  "no changelog available."
- Modal footer/help mentions `v`. Modal-over-modal follows the 6a
  deferred-message pattern (picker/overlay closures return `tea.Cmd` only;
  dispatch resolves on the live model with running/pending guards).

---

## 4. Profile export / import (Profiles screen)

### Export — key `E`

`pendingInput` prefilled `"<game-id>-<profile>.yaml"` (relative paths resolve
against CWD) → `ProfileManager.Export` → write file `0644`. Refuses to
overwrite an existing file (status-line error; re-invoke to try another path).
Success → status line "exported <profile> to <path>". No new core code beyond
reusing `Export`.

### Import — key `I`, full core extraction

Per the CLI/TUI-parity directive, the import flow is extracted whole from
`cmd/lmm/profile.go:doProfileImport` into core, following the Plan/Apply
pattern:

```go
// pure: parse + categorize, no side effects
func (s *Service) PlanImport(ctx, game, data []byte) (ImportPlan, error)
// save profile + download/install the missing/needs-download mods, emitting
// progress events; honors opts (force overwrite, skip installs)
func (s *Service) ApplyImport(ctx, game, plan ImportPlan, opts ImportOptions, progress ProgressFunc) (ImportResult, error)
```

`ImportPlan` carries the parsed profile plus mods categorized as installed /
needs-download (in DB or cache but not this profile) / missing (must fetch
from source). `ApplyImport`'s download+install loop must not be a twin of the
machinery `ApplyProfileSwitch`/`ApplyInstall` already use — share the
internal helpers where behavior fidelity permits (the `purgeMods` shared-loop
precedent), with byte-identical CLI output as the referee.

- **CLI:** `lmm profile import` rewrites on top of `PlanImport` (summary
  printing) + its existing `"Download and install mods? [Y/n]"` prompt
  (prompting stays interface-side, between Plan and Apply) + `ApplyImport`
  (event-driven printing). Output byte-identical, capture-tested, including
  `--force` and `--no-install`.
- **TUI:**
  1. `pendingInput`: path to a profile YAML.
  2. `PlanImport`; parse errors land in the status line.
  3. Preview modal from the plan: profile name, game, counts per category
     (with mod names, truncated to fit), and an explicit overwrite warning if
     a profile with that name exists. Confirm → `ApplyImport` (force
     overwrite consented by the modal) with the standard progress pump.
  4. Follow-up modal: "Switch to '<profile>' now?" — yes runs the existing
     `PlanProfileSwitch`/`ApplyProfileSwitch` flow; no leaves the profile
     saved but inactive. (This is an entry point to existing functionality,
     not a duplicate flow; the CLI equivalent remains `profile switch`.)

---

## 5. Cross-cutting

- **Help:** per-screen help groups gain: shift+j/k + `<` (Installed Mods),
  `E`/`I` (Profiles), `6` (global nav), `v` (update modal), and a Conflicts
  screen group.
- **Staleness sweep:** `cmd/lmm/tui.go` `Long` text, README (TUI section +
  keybindings), roadmap phase tables — grep for "not yet", "read-only",
  "aren't available", "use 'lmm", "Phase 6", "`?` until" residue. This trap
  bit every prior phase.
- **ActionProvider/DataProvider deltas:** `Conflicts()` (data);
  `ReorderMods`, `Rollback`, `ExportProfile`, `PlanImport`/`ApplyImport`
  (actions); `UpdateItem.Changelog`. Both
  `coreProvider` and `prototypeProvider` implement everything; prototype
  parity is a merge gate.
- **Docs/versioning:** CHANGELOG under `[Unreleased]` → v1.14.0 at release;
  deploy-ordering behavior change called out separately; version bump in
  `cmd/lmm/root.go` as its own chore commit.

## Testing

- TDD per task: failing test first, including capture tests pinning current
  CLI output for `conflicts`, `update rollback`, `profile import` (incl.
  `--force`/`--no-install` and the Y/n prompt paths), and any touched
  multi-mod deploy path *before* extraction. Where the spec deliberately
  adds output (conflicts winner lines), the capture baseline is updated in
  the same task, never silently.
- Ordering determinism tests per section 0; `GetProfileConflicts` unit tests
  (winner/stale, unlisted providers, no conflicts); `ApplyRollback` tests
  mirror the old CLI paths (no-previous-version error, missing cache, hook
  failure + Force, compensating rollback); `PlanImport`/`ApplyImport` tests
  cover categorization, overwrite/force, no-install, and download-failure
  partial results.
- TUI: Bubble Tea model tests by injected messages (reorder moves + guards,
  conflicts screen render/empty state, rollback modal enable/disable,
  changelog overlay routing, import preview/confirm/switch chain, export
  overwrite refusal). `:memory:` SQLite, `t.TempDir()`.
- Final whole-branch review live-verifies: twin sandboxes (branch vs
  merge-base binaries), byte-diffed CLI parity matrices for conflicts/
  rollback/profile commands, tmux-driven TUI walkthrough.

## Process

Repo conventions apply: branch off protected `main`; SDD per-task loop (brief
→ implementer → review package → reviewer → fix waves with same-reviewer
re-review); final live review; PR with Copilot triage including post-push
rounds; **user smoke test gates the merge** (real-NexusMods items — e.g.
changelog rendering from live data, rollback of a genuinely updated mod — go
on the smoke checklist explicitly); merge-commit; tag `v1.14.0`; close #37
with a completion comment (all its Phase 5/6 items are then shipped or
documented CLI-only); move this design + the impl plan to
`docs/plans/archive/` (rides the PR); update the roadmap status header;
update memory + SDD ledger.
