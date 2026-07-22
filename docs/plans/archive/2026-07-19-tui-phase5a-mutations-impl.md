# TUI Phase 5a — Local mutating actions (implementation plan)

**Date:** 2026-07-19
**Issues:** #37 (Phase 5/6 scope), #42 (lifecycle carry-forward), roadmap `docs/plans/2026-04-28-tui-implementation.md` §Phase 5
**Branch:** `feat/tui-phase5a-mutations`
**Version target:** v1.11.0 (MINOR)

## Decisions already made (do not relitigate)

1. **Orchestration is extracted into `internal/core`** (user-approved 2026-07-19).
   The CLI currently hand-orchestrates every mutation flow in `cmd/lmm`
   (package main — unimportable). Each flow moves into core as a
   behavior-preserving refactor; the CLI is refit to call it; the TUI writer
   calls the same code. CLI and TUI must not drift.
2. **Phase 5 ships in two releases.** This plan is **5a: local actions** —
   enable/disable, uninstall, deploy profile, profile switch — plus all shared
   machinery (writer interface, confirmation modal, async action pattern,
   refresh, status line, `ModItem.ID`). **5b** (install-from-search with
   download progress, check/apply updates) is a separate follow-up plan.
3. **Writer interface is separate from `DataProvider`** (agreed in Phase 4
   close-out): the read-only `DataProvider` stays provably read-only; mutations
   go through a new `ActionProvider` interface.
4. TUI merges are **gated on the user's interactive smoke test** — do not
   auto-merge.

## Scope boundaries

- IN: enable/disable toggle, uninstall, deploy profile, profile switch (with
  plan preview), shared confirmation modal, single-flight action guard,
  post-action data refresh, action status line, `ModItem.ID`, context cleanup
  on quit (#42 lifecycle item), CLI refit to extracted core flows.
- OUT (5b): install-from-search, check/apply updates, download progress UI.
- OUT (Phase 6): rollback, update-policy editing, purge, game switcher,
  per-mod files panel, profile create/delete.
- Capability gating (`source.CapabilitiesOf`): 5a actions are local-only and
  need no capability checks **except** deploy's re-download fallback, which
  surfaces source errors as action failures. Record in the 5b plan that
  install/update actions must consult capabilities + `ErrNotSupported` (§7
  rendering, deferred from custom-sources P4 via #50 triage).

## Fact base (verified 2026-07-19 scout; re-verify signatures before coding)

- `core.Service` has NO single install/uninstall/switch method. Mutation logic
  lives on `*Installer` (`GetInstaller(game)`), `*ProfileManager`
  (`NewProfileManager()`), `*Updater` (`NewUpdater()`), plus DB setters
  (`SetModEnabled`, `DeleteInstalledMod`, …) on Service.
- CLI flow ordering that MUST be preserved by extraction:
  - **enable** (`cmd/lmm/mod.go` `doModEnable` ~:203): GetInstalledMod →
    no-op if already enabled → verify cache present → `installer.Install` →
    `SetModEnabled(true)`. **Enable deploys files.**
  - **disable** (`doModDisable` ~:251): GetInstalledMod → no-op if already
    disabled → `installer.Uninstall` (keeps cache) → `SetModEnabled(false)`.
  - **uninstall** (`cmd/lmm/uninstall.go` `doUninstall` ~:51): find mod →
    `uninstall.before_*` hooks → `installer.Uninstall` → cache `Delete`
    (unless keep-cache) → `DeleteInstalledMod` → `pm.RemoveMod` →
    `after_*` hooks.
  - **deploy** (`cmd/lmm/deploy.go` `doDeploy` ~:69): optional purge →
    resolve link method (`NewInstallerWithLinker` for override) → gather mods
    in profile order → `install.before_all` hook → per mod: re-download if
    cache missing, `Uninstall`+`Install`, `SetModLinkMethod`,
    `SetModDeployed(true)`, `after_each` → `after_all` →
    `ApplyProfileOverrides`.
  - **profile switch** (`cmd/lmm/profile.go` `doProfileSwitch` ~:262): does
    NOT call `ProfileManager.Switch`. Computes toDisable/toEnable/toInstall
    diff vs current default, prints plan, confirms, executes per-mod, then
    `pm.SetDefault`. Early paths: already-on-profile; no-changes → just
    `SetDefault`.
- TUI seam: `DataProvider` in `internal/tui/service.go` (read-only);
  `coreProvider` (`service_core.go`) already holds `svc *core.Service`,
  `game *domain.Game`, `profile string`. Fakes that must also implement any
  new interface where required: `prototypeProvider`, plus test fakes
  `failingProvider` (app_test.go) and `noSourcesProvider` (search_test.go).
- `ModItem` (service.go:24) has NO mod ID field. All core mutations key on
  `(sourceID, modID)`.
- No reload trigger after `Init`; no modal precedent (`showHelp` is a footer
  swap, not a modal); no status line. Async precedent to mirror: search
  generation-tagging + `context.WithCancel` (`search.go` `startSearch`).
- Exact-height panel invariant (issues in #42): every screen renders to the
  exact height budget; long lines are truncated to
  `availableWidth − Panel.GetHorizontalFrameSize()` INSIDE panels. Any new
  modal/status line must respect this at 80x24.
- Hooks: `InstallBatch`/`UninstallBatch` embed hook execution in core, but
  single-mod CLI flows run hooks inline from cmd. Extraction must move the
  single-flow hook invocation into the extracted core flow (find the hook
  runner used by cmd — likely shared with core's batch path) so CLI and TUI
  both get hooks.
- Errors: `*domain.DeployError{Op, Primary, Rollback, Cleanup}` on deploy
  failures; `domain.ErrModNotFound/ErrProfileNotFound`; treat these as
  displayable failures, never panics.

## Design

### New core flow API (internal/core — `flows.go` + per-flow files as sensible)

Names are directional; implementers may refine, reviewers judge Go idiom.

```go
// EnableMod deploys an installed-but-disabled mod and flips DB state.
// Returns (false, nil) if the mod was already enabled (no-op).
func (s *Service) EnableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (changed bool, err error)
func (s *Service) DisableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (changed bool, err error)

type UninstallOptions struct{ KeepCache bool }
func (s *Service) UninstallMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts UninstallOptions) error

type DeployOptions struct {
    Purge      bool
    LinkMethod domain.LinkMethod // zero value = game's effective method
}
type DeployProgress struct{ Index, Total int; ModName string }
type DeployResult struct{ Deployed int; Skipped []string }
func (s *Service) DeployProfile(ctx context.Context, game *domain.Game, profileName string, opts DeployOptions, progress func(DeployProgress)) (*DeployResult, error)

// SwitchPlan is the displayable diff the CLI prints and the TUI modal shows.
type SwitchPlan struct {
    GameID, From, To string
    ToEnable, ToDisable []domain.InstalledMod
    ToInstall           []domain.ModReference // download required — see note
    NoChanges           bool
    AlreadyActive       bool
}
func (s *Service) PlanProfileSwitch(ctx context.Context, game *domain.Game, target string) (*SwitchPlan, error)
func (s *Service) ApplyProfileSwitch(ctx context.Context, game *domain.Game, plan *SwitchPlan, progress func(DeployProgress)) error
```

Notes:
- `ApplyProfileSwitch` `ToInstall` entries require downloads (network). The
  CLI supports this today, so the core flow keeps it. The **TUI in 5a
  refuses** a plan with `len(ToInstall) > 0` with a clear message
  ("profile needs downloads — use `lmm profile switch` until TUI install
  ships in 5b"); parity arrives in 5b. CLI behavior unchanged.
- Progress callbacks are synchronous and may be nil. TUI 5a does not render
  incremental progress (a single "working…" status suffices); the callback
  exists so the CLI keeps its current per-mod output and 5b can stream.
- Flows must wrap errors with `%w` and preserve the CLI's current
  user-visible semantics (skip lists, DeployError propagation).

### CLI refit

Each command body becomes: parse flags → resolve game/profile → (prompt where
it prompts today) → call the core flow → print results. Confirmation prompts
STAY in the CLI (core flows never prompt). `--force`, `--keep-cache`,
`--purge`, `--method` map onto options structs. Output text should remain
byte-compatible where tests assert on it; otherwise equivalent.

### TUI writer seam (internal/tui)

```go
// ActionProvider is the write-side seam. DataProvider stays read-only.
type ActionProvider interface {
    EnableMod(ctx context.Context, item ModItem) (bool, error)
    DisableMod(ctx context.Context, item ModItem) (bool, error)
    UninstallMod(ctx context.Context, item ModItem) error
    DeployProfile(ctx context.Context) error
    PlanProfileSwitch(ctx context.Context, profile string) (SwitchPlanView, error)
    ApplyProfileSwitch(ctx context.Context, profile string) error
}

// SwitchPlanView is the TUI-facing render model of core's SwitchPlan.
type SwitchPlanView struct {
    From, To            string
    Enable, Disable     []string // mod names
    NeedsDownloads      []string // names; non-empty ⇒ 5a refuses
    NoChanges, AlreadyActive bool
}
```

- `coreProvider` implements it via the new core flows (it already holds
  svc/game/profile).
- `prototypeProvider` implements it with in-memory simulation (flip Status
  strings, mutate its canned data) so `--prototype` demos the full flow with
  zero side effects.
- `Model` gets a separate `actions ActionProvider` field; prototype and core
  constructors wire their own. Test fakes implement it (a `recordingActions`
  fake for assertions; `failingProvider` extended or paired with a failing
  actions fake).

### ModItem addressing

Add `ID string` to `ModItem`; populate at both map sites
(`coreProvider.Overview`, `modsToItems`) and in prototype data. `Source` field
already exists — `(Source, ID)` fully addresses a mod.

### Confirmation modal + action machinery (internal/tui — new `actions.go`)

```go
type actionKind int // actionEnable, actionDisable, actionUninstall, actionDeploy, actionSwitch

type pendingAction struct {
    kind    actionKind
    title   string   // "Uninstall SkyUI?"
    detail  []string // affected game/profile/mods; switch shows plan lines
    confirm func() tea.Cmd // dispatched on 'y'
}

type actionModel struct {
    pending *pendingAction     // non-nil ⇒ modal shown, input intercepted
    running bool               // single-flight guard
    gen     int                // staleness tag, mirrors search
    cancel  context.CancelFunc // cancelled on quit (#42 lifecycle)
    status  string             // last result line ("" = hidden)
    statusIsError bool
}

type actionDoneMsg struct{ gen int; kind actionKind; summary string }
type actionFailedMsg struct{ gen int; kind actionKind; err error }
```

Behavior rules (these are the Phase 5 exit criteria, make them tests):

1. Mutation keys build a `pendingAction` and show the modal; nothing mutates
   before `y`.
2. While `pending != nil`, `updateKey` intercepts: `y`/`enter` confirms,
   `n`/`esc` cancels (state unchanged), everything else ignored except
   quit keys. Interception branch sits at the top of `updateKey`, modeled on
   the focused-search-input swallow branch.
3. While `running`, all mutation keys are ignored (single-flight); navigation
   still works.
4. On `actionDoneMsg` (fresh gen): set status line, clear running, dispatch
   `loadData` to refresh (`dataLoadedMsg` handling must clamp `selected`
   indices to the new list lengths).
5. On `actionFailedMsg`: status line shows the error (truncated to panel
   width), user stays on their current screen, state refreshed anyway
   (partial failures like DeployError leave real changes behind).
6. Modal renders as a bordered panel **replacing the content area** (not an
   overlay composite — simplest way to keep the exact-height invariant);
   chrome/footer stay. Long mod lists in `detail` truncate with "+N more".
7. Status line occupies one row above the footer only when non-empty; height
   budget for panels shrinks by exactly that row; any key press that isn't a
   modal response clears it.
8. On quit: cancel action context AND `m.search.cancel` if set (#42 item).

### Keybindings (5a)

- Installed Mods screen: `e` = toggle enable/disable (kind chosen from item
  Status), `x` = uninstall, `D` (shift-d) = deploy profile.
- Profiles screen: `enter` on a non-active profile = switch (modal shows the
  plan from `PlanProfileSwitch`; plan fetch is itself async with a loading
  status).
- Dashboard: `D` = deploy profile (same action as Installed).
- All new bindings registered in `KeyMap` with help text; help overlay and
  footer updated. No conflicts: e/x/D/enter are free on those screens today
  (verify `keys.go` at implementation time).
- Prototype mode: identical keys, simulated results.

## Tasks

Execute with subagent-driven development. Every task: RED (failing test
proven on unmodified code where it's a refactor guard) → GREEN → `go fmt`,
`go vet`, full `go test ./...`. Spec + quality review per task; whole-branch
final review with a live scratch-binary session before PR.

### Task 1 — Core: EnableMod/DisableMod flows + CLI refit
Extract `doModEnable`/`doModDisable` orchestration into
`Service.EnableMod/DisableMod` (behavior table above). Table-driven tests:
already-enabled no-op, missing-cache error, deploy+DB flip, disable keeps
cache, DB flip on disable, DeployError propagation. Refit `cmd/lmm/mod.go`;
CLI output preserved.

### Task 2 — Core: UninstallMod flow + CLI refit
Extract `doUninstall` (hooks → undeploy → cache delete unless KeepCache → DB
delete → profile RemoveMod → after-hooks) into `Service.UninstallMod`.
Tests include hook invocation order (reuse existing hook test fixtures) and
keep-cache behavior. Refit `cmd/lmm/uninstall.go`.

### Task 3 — Core: DeployProfile flow + CLI refit
Extract `doDeploy` into `Service.DeployProfile` with `DeployOptions` +
progress callback. Tests: profile-order iteration, link-method override,
purge path, skip accounting, `ApplyProfileOverrides` at end, hook ordering.
Refit `cmd/lmm/deploy.go` keeping per-mod console output via the callback.

### Task 4 — Core: PlanProfileSwitch/ApplyProfileSwitch + CLI refit
Extract the diff algorithm from `doProfileSwitch` into `PlanProfileSwitch`
(pure computation, heavily table-tested: already-active, no-changes,
enable/disable/install mixes) and execution into `ApplyProfileSwitch`
(tests: per-mod ordering, SetDefault last, rollback semantics preserved).
Refit `cmd/lmm/profile.go` to print the plan from the struct and keep its
prompt.

### Task 5 — TUI: ModItem.ID + ActionProvider seam
Add `ID` to `ModItem` (all populate sites + prototype data). Define
`ActionProvider` + `SwitchPlanView`. Implement on `coreProvider` (thin calls
into Tasks 1–4) and `prototypeProvider` (in-memory simulation). Extend test
fakes. Unit tests: coreProvider maps core results/errors correctly
(in-memory db + temp dirs per existing service_core_test.go pattern);
prototype simulation flips visible state.

### Task 6 — TUI: confirmation modal + action machinery
Implement `actionModel`, messages, interception, single-flight, status line,
refresh dispatch, quit-time context cancel (#42), per behavior rules 1–8.
Bubble Tea tests via key-event injection (existing `updateWithRunes` helpers):
confirm path, cancel path leaves state untouched, stale-gen result discarded,
duplicate-action key ignored while running, failure keeps screen + shows
status, refresh clamps selection. Snapshot the modal at 80x24.

### Task 7 — TUI: wire the four actions + keybindings
`e`/`x`/`D` on Installed Mods, `D` on Dashboard, `enter`-to-switch on
Profiles (async plan fetch; refuse `NeedsDownloads` with the 5b message).
KeyMap + help/footer updates. Tests per action: end-to-end model test from
keypress → modal → confirm → (fake) provider call recorded → refresh msg.

### Task 8 — Docs, changelog, version
README TUI section (new keys, confirmation behavior, prototype parity),
roadmap doc: mark Phase 5a delivered/5b remaining (single commit, tracked
file), CHANGELOG `[Unreleased]` → v1.11.0 with comparison link, bump
`version` in `cmd/lmm/root.go` (separate `chore:` commit).

### Task 9 — Final review + live verification
Whole-branch fable review. Live checks with a scratch binary + temp config
(never touch `~/.config` or the user's real game dirs): CLI enable/disable/
uninstall/deploy/switch behave identically pre/post refactor (diff outputs);
TUI actions in `--prototype` mode; core flows against temp game dirs.
Then PR → Copilot triage → **stop for the user's interactive smoke test**.

## Exit criteria (roadmap §Phase 5, scoped to 5a)

- Mutating actions require confirmation; cancelling leaves state unchanged.
- Successful actions refresh visible data; duplicate actions are blocked
  while one runs.
- Failures are visible (status line) without losing the current screen.
- CLI behavior for the four flows is unchanged (refactor-proof).
- `go test ./...`, `go vet`, `go fmt` clean; 80x24 render invariant holds.
- Version v1.11.0 + CHANGELOG; merge only after user smoke test.
