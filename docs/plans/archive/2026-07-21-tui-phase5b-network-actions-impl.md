# TUI Phase 5b — Network actions: install-from-search + updates (implementation plan)

**Date:** 2026-07-21
**Issues:** #37 (Phase 5 scope), #50-era §7 capability-gap UX requirement, 5a carry-forwards (SDD ledger)
**Branch:** `feat/tui-phase5b-network-actions` (off main @ 9b9d5a0, v1.11.0)
**Version target:** v1.12.0 (MINOR)

## Decisions already made (do not relitigate)

1. Phase 5 ships in two releases; 5a (local actions, v1.11.0) is merged. 5b
   is the network half: **install-from-search with download progress, and
   check/apply updates**, plus lifting the profile-switch NeedsDownloads
   refusal. User-approved 2026-07-20/21.
2. Orchestration lives in `internal/core` flows; CLI is refit
   behavior-preserving; TUI consumes the same flows. Same conventions as 5a
   (all binding): Warnings/Notes split, partial-result-on-error, dual-channel
   diagnostics (progress events at point of occurrence + result slices),
   core never prints, hooks configured identically to the CLI.
3. TUI merges are **gated on the user's interactive smoke test** — do not
   auto-merge.
4. Width design target ~160 cols normal, truncate-degrade below; exact-height
   invariant at 80x24 and width floor 40 stays binding.
5. Capability gating is a 5b acceptance criterion (recorded on #37): actions
   that a source cannot perform must be gated via `source.CapabilitiesOf`
   where a capability exists, and runtime `source.ErrNotSupported` must
   render as a clean one-line §7 notice (CLI already has
   `capabilityGapNotice` for search; TUI actions need the equivalent).

## Scope boundaries

- IN: core install flow (plan/apply split), core update-apply flow, TUI
  install from search results (with per-file/percent progress), TUI check +
  apply updates, switch NeedsDownloads lifted (switch may download), 5a
  hardening carry-forwards (listed in Task 6), docs + v1.12.0.
- OUT (Phase 6, #37): rollback, update-policy editing, purge-behind-confirm
  (needs #61), game switcher, per-mod files panel, profile create/delete.
- OUT: fixing #60 (switch enable-loop orphaning — pre-existing core bug,
  its own issue), #58 items (aggregate pagination), #61 (purge extraction)
  — unless a task naturally lands on one, in which case flag first.

## Fact base (from 5a scouting + implementation; re-verify against code)

- **CLI install** (`cmd/lmm/install.go` `doInstall` ~:440), the largest
  orchestration: resolveSource → profileOrDefault → `GetMod` (or
  interactive `searchAndSelectMods`) → dependency resolution with confirm
  prompt ("Install N mod(s)? [Y/n]") → `GetModFiles` → `selectInstallFiles`
  → `install.before_all`/`before_each` hooks → download
  (`downloadSelectedFiles`, `Service.DownloadMod` with `ProgressFunc`) →
  `confirmInstallConflicts` ("N file(s) will be overwritten. Continue?
  [y/N]", skipped with `--force`; uses `Installer.GetConflicts`) →
  `installer.Install`/`Replace` → `SaveInstalledMod` (Enabled:true,
  Deployed:true, UpdatePolicy:notify, GameID normalized to lmm id) →
  `SaveFileChecksum` → `pm.UpsertMod` (creates profile if missing) →
  `after_each`/`after_all` hooks.
- **CLI update** (`cmd/lmm/update.go`): `Updater.CheckUpdates(ctx, game,
  installed)` exists in core already (returns `[]domain.Update`, partial
  results + joined errors, respects pinned/local); `applyUpdate` (~:329) is
  CLI-side orchestration: GetMod → GetModFiles → resolve
  `FileIDReplacements` → DownloadMod → hooks → `installer.Replace` →
  `ApplyModUpdate` → `SetModLinkMethod` → `UpsertMod`.
- **Core flows file**: `internal/core/flows.go` (~1200 lines after 5a) —
  follow its conventions exactly; `selectDeployFiles` (file-selection
  fallback logic) already lives there; `redeployFromSource` shows the
  in-flow download pattern; `DeployProgress` + phase constants are the
  event vocabulary to extend.
- **Switch**: `ApplyProfileSwitch` already handles ToInstall downloads
  (CLI parity); only the TUI-side `coreProvider.ApplyProfileSwitch`
  refuses NeedsDownloads (`errProfileNeedsDownloads`,
  internal/tui/actions_provider.go).
- **TUI machinery** (5a): `pendingAction`/`promptAction`/`buildAction`
  (single-flight, gen), status line, refresh-always. Progress today: none
  streamed (coreProvider passes nil). Bubble Tea streaming needs a
  msg-pump: a `progress func` callback fires on the flow goroutine — bridge
  to the Update loop via a channel + `tea.Cmd` listener or
  `program.Send`-style injection; design in Task 4 (there is no Program
  handle in Model — channel listener cmd is the established Bubble Tea
  pattern: cmd reads one msg from channel, Update re-issues the listener).
- **Capabilities**: `source.CapabilitiesOf(src) Capabilities{Search,
  Dependencies, Updates, Auth bool}` — no Download capability exists;
  download inability surfaces at runtime as `source.ErrNotSupported`. CLI
  §7 model: `capabilityGapNotice` (cmd/lmm/search.go:86).
- **Carry-forward details** (5a final review + ledger): DisableMod swallows
  undeploy error (`_ =`, flows.go:61); `coreProvider.profile` written by
  `SetProfile` (Update goroutine) vs read by in-flight `Search` goroutine
  (`installedModKeys`) — real data race, benign today; `resolvedHooks`/
  `hookRunner` re-parse YAML per action; quit is cancel-then-exit (no
  drain); switch modal shows fetch-time plan, apply re-plans (drift).

## Design

### Core: install flow (plan/apply split — the TUI modal needs a pure plan)

```go
type InstallPlan struct {
    SourceID, GameID, Profile string
    Mod       domain.Mod
    Files     []domain.DownloadableFile // selected per selectDeployFiles fallback logic
    Dependencies []domain.Mod           // resolved, in install order (empty if source lacks the capability)
    Conflicts []Conflict                // from Installer.GetConflicts, vs current profile state
    Replaces  *domain.InstalledMod      // non-nil ⇒ reinstall/upgrade path uses Replace
    TotalDownloadBytes int64            // when sources report sizes; -1 unknown
}
func (s *Service) PlanInstall(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*InstallPlan, error)

type InstallResult struct {
    Installed []string // mod names, deps included, install order
    Warnings, Notes []string
}
// ApplyInstall executes a plan: hooks → download (progress) → deploy →
// DB save (GameID normalized!) → checksum → profile upsert → after hooks.
// Dependencies install before the primary, matching the CLI.
func (s *Service) ApplyInstall(ctx context.Context, game *domain.Game, plan *InstallPlan, progress func(DeployProgress)) (*InstallResult, error)
```

- Plan is PURE (no download, no DB write, no hook). Conflict computation
  reads only. Dependency resolution may hit the network (source API) but
  must not mutate; document that Plan can be slow → TUI shows "Planning…".
- CLI refit: `doInstall` becomes prompt-shell around Plan/Apply. Its two
  prompts (deps confirm, conflict confirm) stay CLI-side, fed from the
  plan's Dependencies/Conflicts. `--force` maps to skipping the conflict
  prompt, not to a flow flag, unless tracing shows flow-level force
  semantics — implementer documents. Interactive `searchAndSelectMods`
  stays CLI-side entirely.
- Download progress rides `DeployProgress` events (extend phases:
  `InstallDownloadProgress` with Percent, per-file Detail, ModName/ModID)
  so the CLI keeps its current percent display and the TUI streams the
  same events.

### Core: update-apply flow

```go
type UpdateApplyResult struct {
    Applied []string // "name old→new"
    Warnings, Notes []string
}
// ApplyUpdate executes one domain.Update (CLI applyUpdate ordering:
// GetMod → files → FileIDReplacements → download w/ progress → hooks →
// Replace → ApplyModUpdate → SetModLinkMethod → UpsertMod).
func (s *Service) ApplyUpdate(ctx context.Context, game *domain.Game, profileName string, upd domain.Update, progress func(DeployProgress)) (*UpdateApplyResult, error)
```

CLI refit: `applyUpdate`/`applySingleUpdate` call the flow; policy
filtering, `--all`, `--dry-run`, table printing stay CLI-side.
`CheckUpdates` stays as-is (already core).

### TUI: ActionProvider additions

```go
// added to ActionProvider (all implementations + fakes):
PlanInstall(ctx context.Context, item ModItem) (InstallPlanView, error)
ApplyInstall(ctx context.Context, item ModItem) (ActionOutcome, error)   // re-plans internally, like ApplyProfileSwitch
CheckUpdates(ctx context.Context) (UpdatesView, error)
ApplyUpdate(ctx context.Context, u UpdateItem) (ActionOutcome, error)

type InstallPlanView struct {
    Name, Version, Source string
    Files, Dependencies []string
    Conflicts []string // "path (owned by X)"
    Reinstall bool
    SizeLabel string   // "12.3 MiB" or "size unknown"
}
type UpdateItem struct{ Source, ID, Name, FromVersion, ToVersion string }
type UpdatesView struct{ Updates []UpdateItem; Warnings []string }
```

- Capability/§7 gating in the provider: map `source.ErrNotSupported` (and
  ErrAuthRequired) to clean one-line messages mirroring the CLI's
  `capabilityGapNotice`/auth hint wording. Where `Capabilities` can
  pre-gate (Updates cap for CheckUpdates per source; Dependencies cap just
  degrades to empty deps), do so silently like `SearchAllSources`.
- Prototype: simulated install (search result → appears in installed data,
  fake progress ticks), canned update set (one auto, one notify), simulated
  apply. Zero side effects outside its data.
- Switch refusal LIFTED: `coreProvider.ApplyProfileSwitch` stops refusing
  NeedsDownloads (core already downloads); the switch modal detail keeps
  listing downloads (now as work to be done, not a refusal);
  `errProfileNeedsDownloads` and its prototype scenario become the
  progress-bearing path (prototype `requiem-overhaul` now demoes a
  downloading switch instead of a refusal). Update README copy.

### TUI: streaming progress

- Bridge pattern (Task 4): buffered channel + listener `tea.Cmd`
  (`waitForProgress(ch)` returning `actionProgressMsg`; Update handles it,
  re-issues the listener; flow goroutine's progress callback does a
  non-blocking send — drop-oldest/coalesce, never block the flow).
- `actionProgressMsg{gen int; line string; percent float64}` — stale-gen
  discarded like done/failed msgs.
- Render: while `running`, the status line shows the latest progress line
  (e.g. `Installing SkyUI: skyui_5_1.7z 42%`); modal is already closed.
  No new panel in 5b; a proper progress pane is Phase 6 polish.
- Cancel-then-drain carry-forward (Task 6): quit during a running action
  waits (with a "finishing…" beat) for the flow goroutine to observe
  cancellation before exiting, bounded by a short timeout; flows get
  ctx checks between per-mod steps where 5a left them missing.

### Keybindings (5b)

- Search screen, results focused (input blurred): `i` = install selected
  result ("Planning…" → install modal with InstallPlanView; if installed
  already, modal says Reinstall). `enter` on a result stays
  detail/selection as today (verify; do not steal an existing binding).
- Dashboard: `u` = check updates → status line count → Updates modal
  (list + "apply all eligible"?) — 5b keeps it simple: modal lists
  updates (truncated) and confirming applies ALL notify+auto updates
  sequentially with progress; per-mod selection is Phase 6.
- Installed Mods: `u` = same check/apply flow (same action, same modal).
- KeyMap/help/footer updated; collision check (`i`/`u` free today —
  verify; `u` must not collide with anything on those screens).

## Tasks

Same SDD process as 5a: fresh implementer per task, spec+quality review
per task, fix waves, fable final review with live verification, PR,
Copilot triage, STOP for user smoke test. RED before GREEN throughout;
`go fmt`/`go vet`/full suite per commit; `-race` on TUI work.

### Task 1 — Core: PlanInstall (pure) + tests
Extract doInstall's read-only computation (mod fetch, dep resolution,
file selection via existing selectDeployFiles, conflict detection,
reinstall detection) into `PlanInstall`. Purity test (zero mutations),
table tests per bucket, capability-degradation tests (no-deps source →
empty Dependencies, not an error). NO CLI change yet.

### Task 2 — Core: ApplyInstall + CLI refit
Execution flow per the CLI ordering (hooks, download with progress
events, conflict-aware Install/Replace, DB save with GameID
normalization regression test, checksum, UpsertMod, after-hooks).
Refit `doInstall` onto Plan+Apply keeping both prompts and all output
byte-identical (capture tests). The dual-channel/partial-result/Notes
conventions apply. Biggest task — implementer may split RED/GREEN per
sub-flow but lands one coherent refit.

### Task 3 — Core: ApplyUpdate + CLI refit
Extract `applyUpdate` per the ordering above; refit update.go
(`applySingleUpdate` + the auto/all loops) keeping output byte-identical.
FileIDReplacements and rollback-cache preservation traced and tested.

### Task 4 — TUI: progress bridge + provider additions
Channel/listener progress pump into the 5a action machinery
(actionProgressMsg, stale-gen discard, status-line rendering, coalescing
non-blocking sends). ActionProvider gains the four methods (+ fakes,
prototype sims incl. fake progress ticks and canned updates). §7/auth
error mapping tests. Switch NeedsDownloads refusal lifted (core +
prototype + README copy + the requiem-overhaul scenario becomes a
downloading demo).

### Task 5 — TUI: wire install + updates keys
`i` on search results (blurred), `u` on Dashboard/Installed Mods, modals
per design, end-to-end tests (keypress → plan modal with files/deps/
conflicts → confirm → provider called with (Source,ID) → progress msgs →
done → refresh; cancel paths; stale plan discard; capability-gap and
auth-required render as clean status lines). Prototype end-to-end demo
test. Help/footer updated (160-col wording rule).

### Task 6 — Hardening carry-forwards
(a) EnableMod/DisableMod → result-struct convergence (DisableMod's
swallowed undeploy error becomes a Note; CLI refit prints it verbose —
restores the Task-1-era dropped diagnostic); (b) `coreProvider.profile`
race: mutex-guard the field AND cancel+gen-bump in-flight search on
switch-done (both cheap, both correct); (c) hook config cached per
provider instance with reload on... trace what invalidates it (profile
switch) — rebind resets cache; (d) cancel-then-drain per the design
note, with ctx checks added between flow loop iterations where missing;
(e) switch modal copy notes plans re-compute at apply time (drift doc).
Each item RED-first where behavioral.

### Task 7 — Docs, changelog, version v1.12.0
README (install/update keys, progress line, switch-with-downloads),
roadmap doc (Phase 5 COMPLETE — 5b delivered; Phase 6 next), CHANGELOG
1.12.0 + links, version bump (separate chore commit). Move this plan doc
to docs/plans/archive/ in the docs commit (per repo convention) — also
commit the 5a plan doc archived earlier (already moved locally).

### Task 8 — Final review + live verification + PR
Fable whole-branch review: twin-sandbox CLI parity (install/update
command matrix vs v1.11.0 build incl. prompts, --force, dep and conflict
paths, update policies), live TUI install/update against a directory
source + a manifest source with real downloads (temp dirs only), switch-
with-downloads, capability-gap rendering against a no-search/no-update
source, `-race -count=2`. Then PR → Copilot triage → **STOP for user
smoke test** (needs real NexusMods interaction from the user's side).

## Exit criteria

- Search result → confirmed install with visible progress; deps and
  conflicts shown before confirm; refusal paths render clean one-liners.
- Updates checkable and appliable from TUI with progress; policies
  respected (pinned never offered; auto+notify applied on confirm).
- Profile switches needing downloads work from the TUI.
- CLI install/update output byte-identical (the flows are extractions).
- Carry-forward hardening landed; `-race` clean; height/width invariants
  hold; v1.12.0 + CHANGELOG; merge only after user smoke test.
