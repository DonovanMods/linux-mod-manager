# TUI Phase 6a — Local Workflows (design)

> Status: **approved design** (2026-07-23). Implementation plan to follow in
> `2026-07-23-tui-phase6a-workflows-impl.md`. Targets **v1.13.0**.
>
> Phase 6 (roadmap `2026-04-28-tui-implementation.md` §Phase 6 + issue #37's
> additions) is split in two:
>
> - **6a (this doc)** — the workflows that ride on Phase 5's existing modal/
>   action machinery with near-zero core changes.
> - **6b (later)** — the workflows needing new core extractions: conflict view,
>   load-order reorder, update rollback + changelogs, profile export/import
>   entry points.

## Scope

Six items, all local operations (no network, no source capabilities needed):

1. **Purge behind a confirmation view** (`X` on Dashboard/Installed Mods) —
   consumes `core.PurgeProfile` (#61) exactly as its extraction planned.
2. **Per-mod deployed-files panel** (`f` on Installed Mods) — read-only
   overlay over `GetDeployedFilesForMod`.
3. **Update-policy editing** (`P` on Installed Mods) — notify/auto/pin picker
   over `SetModUpdatePolicy`. (Decided: lives on Installed Mods now; the 6b
   update view may add a second entry point later.)
4. **In-TUI game switcher** (`g`, global) — modal picker; rebinds the session
   to the chosen game. (Decided: modal on a global key, not a screen or a
   Dashboard-only entry.)
5. **Profile create / delete** (`c` / `d` on Profiles) — text-input modal and
   confirm modal over `ProfileManager.Create`/`Delete`.
6. **Workflow help overlay** (`?`, global) — expands the existing help binding
   into a full per-screen key reference.

**Out of scope (deliberate):**

- Everything 6b (conflicts, reorder, rollback, changelogs, export/import).
- Purge's `--uninstall` variant — the TUI offers only the record-preserving
  purge; deleting records stays CLI-only.
- Game add/detect/set-default (CLI-only per the roadmap's parity table);
  the switcher only switches between already-configured games.

## Keys and UX

| Key | Where | Action |
| --- | ----- | ------ |
| `X` | Dashboard, Installed Mods | Purge: confirm modal shows mod count + names (the fetched `GetInstalledMods` list IS the plan — no separate dry-run API), then streaming progress mapped from `DeployBeforeAllForced`/`DeployPurging`/`PurgeModPurged`/`PurgeModSkipped`/`PurgeWarning`/`PurgeNote`/`PurgeComplete`; result on the status line ("Purged N mod(s)", warning count when non-zero) |
| `f` | Installed Mods | Read-only overlay listing the selected mod's deployed files; `esc` closes; "no files deployed" empty state |
| `P` | Installed Mods | Three-option picker (notify/auto/pin, current policy marked) → `SetModUpdatePolicy`; `p` remains search paging |
| `g` | Global (any screen, input blurred) | Game-switcher modal listing configured games, active marked; select rebinds the session; `esc` cancels |
| `c` | Profiles | Create-profile text-input modal (reuses the search input component); validates non-empty and unique before enabling confirm |
| `d` | Profiles | Delete-profile confirm modal; refuses the active profile with a status-line error before any modal confirms |
| `?` | Global | Full help overlay, key groups per screen; `?`/`esc` closes |

Keys `X`, `f`, `P`, `g`, `c`, `d` are currently unbound (verified against
`keys.go`); `?` is bound but underpowered. Focused-input rule (user law from
Phase 4/5): a focused search input swallows printable keys, so all new
printable bindings only fire with the input blurred — same dispatch position
as `i`/`e`/`x` in `updateKey`.

## Architecture

No new top-level screens — everything is a modal or overlay on existing
screens, using Phase 5a's machinery unchanged: confirmation modal component,
single-flight action runner, generation staleness, status line,
refresh-always-after-mutation, progress pump (single-slot coalescing channel)
for the purge stream, cancel-then-drain on quit (purge honors ctx between
mods, per #61).

**New provider surface** (small additions, same DataProvider/ActionProvider
split as today):

- Data: `DeployedFiles(sourceID, modID) ([]string, error)`,
  `ListGames() ([]GameInfo, error)` (id, name, active flag).
- Action: `PurgeProfile(ctx, progress) (*core.PurgeResult, error)`,
  `SetUpdatePolicy(sourceID, modID, policy) error`,
  `CreateProfile(name) error`, `DeleteProfile(name) error`.

**Game switcher** is the only structurally new element: on selection it
constructs a fresh DataProvider/ActionProvider pair bound to the chosen game
(same constructor path `lmm tui` uses at launch), re-resolves the active
profile via `ProfileManager.GetDefault`, resets per-screen state (selections,
search results, update counts back to the `?` sentinel), and triggers a full
reload. Guard: blocked with a status-line message while an action is in
flight (single-flight owns this check). The prototype mode gets a canned
second game so `--prototype` demos the switcher.

**Purge** empty state: zero installed mods short-circuits to a status-line
message ("No mods installed") without opening a modal, mirroring the CLI's
early-out.

## Error handling

Existing conventions only: fatal action errors land on the status line named
by action; partial results surface a warning count (purge's
`len(result.Skipped)` + `len(result.Warnings)`) consistent with
deploy/uninstall today; guard paths (active-profile delete, duplicate/empty
profile name, purge-with-no-mods, switch-while-busy) resolve to status-line
errors *before* any mutation starts. Errors never leave a modal open on a
completed action (modal closes, status line reports).

## Testing

Same harness as 5a/5b, TDD throughout:

- **Model tests** (`internal/tui`): key events against the mock provider —
  modal open/confirm/cancel per feature, guard paths, generation staleness
  (e.g. a purge completing after a game switch must not paint stale state),
  focused-input swallowing of the new printable keys.
- **Provider tests** (`service_core_test.go` pattern): new provider methods
  against a real temp-dir `core.Service` (in-memory SQLite, `t.TempDir()`).
- **Snapshot pass**: help overlay and each new modal at 80×24 (exact-height
  invariant); truncation behavior at the 40-col floor.
- CLI is untouched this phase — no output-fidelity tests needed.

## Process & release

- Branch `feat/tui-phase6a-workflows`, PRs to protected main (merge commits,
  Copilot triage rounds incl. post-push, TUI smoke-test merge gate applies).
- Version **1.13.0** (MINOR — new features), changelog under a new section.
- On completion: comment on #37 marking the 6a items done; 6b scope stays
  open there; archive this doc + the impl plan per the plan-doc lifecycle.
