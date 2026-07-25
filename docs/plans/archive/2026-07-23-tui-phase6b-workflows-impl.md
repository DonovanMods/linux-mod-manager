# TUI Phase 6b — Conflicts, Reorder, Rollback, Export/Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the four remaining Phase 6 workstreams — conflict view, load-order reorder, update rollback + changelogs, profile export/import — on deterministic deploy ordering, per `2026-07-23-tui-phase6b-workflows-design.md`.

**Architecture:** One foundation change (multi-mod deploy paths iterate in profile load order) plus three behavior-preserving core extractions (`GetProfileConflicts`, `ApplyRollback`, `PlanImport`/`ApplyImport` — Plan\*/Apply\* + `DeployProgress` events, CLI rewritten on top byte-identically), then TUI features on the existing modal/action machinery: one new screen (Conflicts), five new `ActionProvider` methods, one new `DataProvider` method.

**Tech Stack:** Go, Bubble Tea/bubbles, testify; existing tui test harness (prototype provider, `recordingActions`/`recordingProvider`, real-Service fixtures); directory-type custom sources for zero-network sandbox fixtures.

## Global Constraints

- **Parity directive (user, 2026-07-23):** no separate CLI-vs-TUI functions for the same task — shared behavior in `internal/core`, both interfaces consume it; new capability data surfaces in both. Interface-specific concerns (arg parsing, prompting, rendering) stay interface-side.
- **Extraction fidelity:** pre-change CLI code is the spec. Capture tests pin output BEFORE each extraction; byte-identical after, except changes this plan explicitly declares (conflicts `Winner:` lines, deterministic ordering of previously map-ordered output) — those update the capture baseline in the same task, never silently.
- Branch `feat/tui-phase6b-workflows`; PRs to protected main; merge commits; TUI smoke-test gates merge.
- TDD: failing test first, every task. `gofmt` (tabs), `go vet ./...`, full `go test ./...` green per task; commit per task.
- Focused-input law: new printable keys (`6 J K < v E I`) must be swallowed by a focused search input (dispatch AFTER the focused-input branch, app.go) and by open input modals.
- Exact-height invariant: new screen/modal/overlay content renders via existing panel helpers (`truncate`, `availableWidth`, `panelWithHeight`); truncate, never wrap; 80x24 floor.
- Single-flight: every mutation path goes through `buildAction` (actions.go) or is guarded by `m.action.running || m.action.pending != nil`; modal closures return `tea.Cmd` only, dispatch via deferred message resolved on the live model (6a's `policyChosenMsg` pattern).
- Both `coreProvider` AND `prototypeProvider` implement every new provider method; prototype stays side-effect-free outside its in-memory data.
- New TUI copy is lowercase-terse matching existing footer/help strings. Version bump lands ONLY in the final task (1.14.0).

---

### Task 1: Deterministic deploy ordering (core + CLI)

**Files:**
- Modify: `internal/core/flows.go` (`DeployProfile` ~:946, `PlanProfileSwitch` ~:1473), `cmd/lmm/profile.go` (`doProfileApply` :920-1010)
- Test: `internal/core/flows_test.go` (or the file holding existing DeployProfile/PlanProfileSwitch tests — follow their fixture pattern), cmd-level capture test for `profile apply`

**Interfaces:**
- Produces: `core.OrderByProfile(profile *domain.Profile, mods []domain.InstalledMod) []domain.InstalledMod` — stable order: mods absent from `profile.Mods` first (sorted by `SourceID:ID` key), then in `profile.Mods` order (first = lowest priority; later deploys overwrite earlier). Tasks 2 and 4 consume this exact function.

- [ ] **Step 1: Failing tests** — `TestOrderByProfile` (table-driven: listed mods follow profile order; unlisted sort first by key; empty profile = all sorted; duplicate-free). `TestPlanProfileSwitchDeterministicOrder` (two runs over a 3-mod fixture return identical `ToEnable`/`ToInstall` slices, in target-profile `Mods` order; `ToDisable` in From-profile order). `TestDeployOrderWinsConflicts` (two mods sharing a relative path, deploy profile: file owner = later mod in load order; reorder profile + redeploy: owner flips — use the installer/temp-dir fixture pattern from existing deploy tests).
- [ ] **Step 2: Run** `go test ./internal/core/ -run 'TestOrderByProfile|TestPlanProfileSwitchDeterministic|TestDeployOrderWins' -v` — FAIL (function undefined / order assertions flake-fail).
- [ ] **Step 3: Implement** — `OrderByProfile` in flows.go. `DeployProfile`: order `mods` with it right after `GetInstalledMods`. `PlanProfileSwitch`: build `ToDisable` by iterating `OrderByProfile(fromProfile, currentMods)` filtered to the map's members (not `for key, im := range currentEnabled`); build `ToEnable`/`ToInstall` by iterating `targetProfile.Mods` slice order (not `range targetKeys`), keeping the existing per-key logic verbatim. `doProfileApply`: iterate `OrderByProfile(profile, installedMods)` instead of `range installedByKey`, and `profile.Mods` instead of `range profileKeys` — print/announce logic unchanged. Ordering of previously map-random output is now deterministic — a declared change; pin it in the capture test.
- [ ] **Step 4: Run** targeted tests then full `go test ./...` — PASS.
- [ ] **Step 5: Commit** `feat(core): deterministic load-order iteration for multi-mod deploys (#37)`

---

### Task 2: `GetProfileConflicts` core query + CLI conflicts rewrite

**Files:**
- Create: `internal/core/conflicts.go`, `internal/core/conflicts_test.go`
- Modify: `cmd/lmm/conflicts.go` (`doConflicts` :58-204, `conflictJSON` :23-27)
- Test: cmd-level capture test for `lmm conflicts` (text + `--json`)

**Interfaces:**
- Consumes: `svc.GetInstalledMods`, `svc.GetDeployedFilesForMod` (service.go:701), `svc.GetFileOwner` (:707), Task 1's `OrderByProfile`.
- Produces (Task 3 consumes these exact types):

```go
type ConflictModRef struct{ Key, Name string } // Key = "sourceID:modID"; Name falls back to Key when unknown
type ProfileConflict struct {
    Path            string
    Owner           ConflictModRef   // current owner per DB
    AlsoIn          []ConflictModRef // other providers, load order
    LoadOrderWinner ConflictModRef   // last provider in profile load order (unlisted providers sort first, mirroring OrderByProfile)
    Stale           bool             // Owner.Key != LoadOrderWinner.Key
}
func (s *Service) GetProfileConflicts(ctx context.Context, game *domain.Game, profileName string) ([]ProfileConflict, error)
```

- [ ] **Step 1: Failing tests** — capture test FIRST: seed a twin-mod conflict fixture (`:memory:` DB + temp dirs, two mods deploying one shared path), pin current `doConflicts` text and `--json` bytes. Core tests: `TestGetProfileConflictsWinnerAndStale` (owner=later mod → `Stale:false`; flip profile order without redeploy → `Stale:true`, winner = new last), `TestGetProfileConflictsSortedByPath` (results sorted by `Path` — fixes today's map-random order, a declared change), `TestGetProfileConflictsUnlistedProvider` (provider absent from profile.Mods loses to any listed one), `TestGetProfileConflictsNone` (empty slice, nil error).
- [ ] **Step 2: Run** — core tests FAIL undefined; capture test PASSES (pins baseline).
- [ ] **Step 3: Implement** `GetProfileConflicts`: port doConflicts' aggregation shape, but source each mod's provided files from its **cache manifest** (the install-time `GetConflicts`/`cache.ListFiles` source) instead of `GetDeployedFilesForMod` — the DB is single-owner-per-path, so the old union could never collide and `lmm conflicts` never reported anything (user-approved fix, 2026-07-24; consider enabled mods only — the set participating in deployment — and document that rule). Owner still from `GetFileOwner` (:113-138), name map (:84-88), winner from load-order position, sort by Path. Rewrite `doConflicts` on top: identical text/JSON plus additive `    Winner: <name>` line after `Also in:` (suffix ` (stale — redeploy to apply)` when `Stale`) and additive JSON fields `winner string`, `stale bool`. Update capture baseline deliberately in this step.
- [ ] **Step 4: Run** full suite — PASS; eyeball the capture diff: ONLY sorted order + Winner additions.
- [ ] **Step 5: Commit** `feat(core): extract GetProfileConflicts with load-order winner; conflicts CLI surfaces it (#37)`

---

### Task 3: Conflicts TUI screen

**Files:**
- Create: `internal/tui/conflicts_view_test.go`
- Modify: `internal/tui/navigation.go` (+`ScreenConflicts`), `internal/tui/keys.go` (+`ConflictsScreen` binding `6`), `internal/tui/service.go` (DataProvider +`Conflicts`, `Summary` +`Conflicts int`, prototypeProvider), `internal/tui/service_core.go` (coreProvider), `internal/tui/app.go` (screen render, dashboard count, help group), `internal/tui/app_test.go` (recordingProvider), `internal/tui/navigation_test.go`, `internal/tui/help_test.go`

**Interfaces:**
- Consumes: Task 2's `GetProfileConflicts`; panel helpers; per-screen help machinery (`helpGroups` app.go:1321).
- Produces: `DataProvider.Conflicts(ctx context.Context) ([]ConflictItem, error)`; `ConflictItem{Path, Owner, Winner string; AlsoIn []string; Stale bool}`; `Summary.Conflicts int` (dashboard count).

- [ ] **Step 1: Failing tests** — `TestConflictsScreenNavigation` (`6` and tab-rotation reach ScreenConflicts; String() = "Conflicts"), `TestConflictsScreenRendersRows` (recordingProvider with one stale + one in-sync item: FILE/OWNER/WINNER columns, stale marker, detail pane shows AlsoIn + hint copy — stale: `load order says <winner> should win — deploy (D) to apply`; in-sync: `reorder mods (J/K on installed) to change the winner`), `TestConflictsScreenEmptyState` ("No conflicts detected."), `TestDashboardConflictCountWired` (Summary.Conflicts renders where `?` was), `TestCoreProviderConflicts` (service_core_test fixture: maps names/stale through), `TestConflictsKeySwallowedByFocusedSearchInput`, help-group test for the new screen.
- [ ] **Step 2: Run** — FAIL/compile-error (interface additions break fakes — expected).
- [ ] **Step 3: Implement** — screen enum + screens slice + label; `6` binding dispatched after focused-input branch; `Conflicts` fetched alongside the existing refresh cycle (same loadGen-staleness guard the mods list uses); coreProvider maps `core.ProfileConflict`→`ConflictItem` (Name fields, fall back to Key); prototype cans two conflicts (one stale) + `Summary.Conflicts: 2`; render list + selected-row detail via panel helpers, exact-height safe; dashboard count replaces the `?` placeholder.
- [ ] **Step 4: Run** package + full suite — PASS. Manual: `./lmm tui --prototype` shows screen 6.
- [ ] **Step 5: Commit** `feat(tui): conflicts screen with load-order winners (#37)`

---

### Task 4: Load-order reorder on Installed Mods

**Files:**
- Modify: `internal/tui/keys.go` (+`MoveDown` `J`/`ctrl+down`, +`MoveUp` `K`/`ctrl+up`), `internal/tui/app.go` (dispatch), `internal/tui/mutations.go` (+`moveSelectedMod(delta int)`), `internal/tui/actions_provider.go` (interface + prototype), `internal/tui/service_core.go` (coreProvider + Overview ordering), `internal/tui/mutations_test.go`, `internal/tui/service_core_test.go`

**Interfaces:**
- Consumes: `ProfileManager.ReorderMods(gameID, profileName, mods []domain.ModReference)` (internal/core/profile.go:207), Task 1's `OrderByProfile`.
- Produces: `ActionProvider.ReorderMods(ctx context.Context, orderedKeys []string) (ActionOutcome, error)` — orderedKeys is the FULL desired order (every installed mod's `source:id` exactly once). coreProvider builds `[]domain.ModReference` preserving each existing profile ref's Version/FileIDs and synthesizing refs from the DB record for mods not yet listed in profile.Mods, then calls `ReorderMods`. Outcome message: `load order updated`.

- [ ] **Step 1: Failing tests** — `TestCoreProviderOverviewFollowsLoadOrder` (Overview's ModItems ordered by `OrderByProfile`, so the list the user reorders IS deploy order), `TestMoveSelectedModDownPersistsOrder` (prototype model: `J` on row 0 swaps rows 0/1, selection follows the mod, provider called with full new key order), `TestMoveAtListEdgeNoop`, `TestReorderInertWhileFiltered` (search-filtered list: `J` leaves order untouched, status line explains `reorder unavailable while filtered`), `TestReorderInertWhileActionRunning`, `TestReorderErrorRefreshesFromDisk` (recordingActions error → status error + refresh dispatched), `TestReorderSetsDeployHint` (status shows `order changed — deploy (D) to apply`; cleared after a deploy/switch completes), `TestCoreProviderReorderModsPreservesRefs` (Version/FileIDs survive; unlisted mod gains a ref), `TestMoveKeysSwallowedByFocusedSearchInput`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** — bindings; `moveSelectedMod` guards (screen, selection, running/pending, filter/alt-list inert), swaps in-memory list, calls provider synchronously (local YAML write — same documented sync exception as `DeployedFiles`), sets `m.orderChanged`; hint rendered in status line until a deploy/switch action resolves successfully (clear in those resolvers); game-switch reset clears it too. Prototype reorders its slice through `activeMods()`/`setActiveMods`.
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): inline load-order reorder with J/K (#37)`

---

### Task 5: `ApplyRollback` core extraction + CLI rewrite

**Files:**
- Modify: `internal/core/flows.go` (new flow + doc-comment updates on reused Update\* phases), `cmd/lmm/update.go` (`doUpdateRollback` :375-507)
- Test: `internal/core/flows_test.go` (rollback tests), cmd-level capture test for `update rollback`

**Interfaces:**
- Consumes: `installer.Replace` (installer.go), `Service.RollbackModVersion` (service.go:660), `SetModLinkMethod`, `ProfileManager.UpsertMod`, hook runner accessors (the flows.go equivalents of cmd's getHookRunner/getResolvedHooks — follow ApplyUpdate's own hook plumbing at flows.go:2971).
- Produces:

```go
type RollbackOptions struct{ Force bool } // Force-gates the two before_each hooks, mirroring --force
type RollbackResult struct {
    ModName, FromVersion, ToVersion string
    Warnings, Notes                 []string // after_each hook failures; link-method note (same display contracts as UpdateApplyResult)
}
func (s *Service) ApplyRollback(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts RollbackOptions, progress func(DeployProgress)) (*RollbackResult, error)
```

- [ ] **Step 1: Failing tests** — capture test FIRST pinning `doUpdateRollback` output on a sandbox fixture (mod with PreviousVersion in cache — seed via an update or direct DB/cache setup, following Phase 5b's update-extraction capture-test pattern). Core: `TestApplyRollbackSwapsVersions` (DB versions swapped, profile ref upserted with previous version), `TestApplyRollbackNoPreviousVersion` (error `no previous version available for rollback`), `TestApplyRollbackMissingCache` (error `previous version %s not found in cache`), `TestApplyRollbackHookForceGate` (before_each failure: fatal without Force, `UpdateBeforeEachForced` event + proceed with), `TestApplyRollbackAfterEachWarnings` (both after_each failures land in Warnings, order: uninstall then install), `TestApplyRollbackCompensatesOnDBFailure` (RollbackModVersion failure → reverse Replace attempted, error returned).
- [ ] **Step 2: Run** — core FAIL undefined; capture PASSES.
- [ ] **Step 3: Implement** — port :375-507 verbatim into the flow: guards; hook gauntlet (reuse `UpdateBeforeEachForced`/`UpdateWarning`/`UpdateNote` phases — extend their doc comments to mention rollback, matching the "extend, don't fork" precedent); `Replace(current→previous)`; `RollbackModVersion` with compensating reverse-Replace; `SetModLinkMethod` failure → Notes; `UpsertMod` failure → compensate both and error. CLI rewrite: keep `GetInstalledMod` + `Rolling back %s %s → %s...` header CLI-side, drive prints from events/result — byte-identical (`✓ Rolled back:` line from result fields).
- [ ] **Step 4: Run** — capture byte-identical; full suite PASS.
- [ ] **Step 5: Commit** `refactor(core): extract ApplyRollback flow from update rollback CLI (#37)`

---

### Task 6: Rollback TUI (`<` on Installed Mods)

**Files:**
- Modify: `internal/tui/keys.go` (+`Rollback` `<`), `internal/tui/service.go` (`ModItem` +`PreviousVersion string`), `internal/tui/service_core.go`, `internal/tui/actions_provider.go` (interface + prototype), `internal/tui/app.go` (dispatch), `internal/tui/mutations.go` (+`rollbackSelectedMod`), tests alongside

**Interfaces:**
- Consumes: Task 5's `ApplyRollback`; `buildAction`/`promptAction`.
- Produces: `ActionProvider.Rollback(ctx context.Context, item ModItem, progress func(ActionProgress)) (ActionOutcome, error)`; `ModItem.PreviousVersion` populated by both providers.

- [ ] **Step 1: Failing tests** — `TestRollbackKeyOpensConfirmModal` (prototype mod canned with PreviousVersion: `<` → pending action titled `Roll back "<name>" v<cur> → v<prev>?`), `TestRollbackKeyNoPreviousVersion` (status line `no previous version to roll back to`, no modal), `TestRollbackConfirmAppliesAndRefreshes` (recordingActions: Rollback called with item; refresh follows success AND failure), `TestPrototypeRollbackSwapsVersions` (Version/PreviousVersion swap, visible on repeated Overview), `TestCoreProviderRollback` (maps ApplyRollback with Force=false; outcome `Rolled back "<name>" to <ver>`; warnings merged via `mergeDiagnostics`), `TestRollbackKeySwallowedByFocusedSearchInput`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** — ModItem field (coreProvider from `InstalledMod.PreviousVersion`; prototype cans one mod with it); binding + dispatch; handler guards then `buildAction(actionRollback, title, detail-lines, ...)` calling `actions.Rollback`.
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): update rollback behind confirmation (#37)`

---

### Task 7: Changelogs in the update flow

**Files:**
- Create: `internal/core/changelog.go`, `internal/core/changelog_test.go`
- Modify: `cmd/lmm/update.go` (:225, :306, delete `stripHTMLForTerminal` :520), `internal/tui/actions_provider.go` (`UpdateItem` +`Changelog string`, prototype), `internal/tui/service_core.go`, `internal/tui/mutations.go` (update-modal `v` handling), `internal/tui/app.go` (retain pending updates view), tests alongside

**Interfaces:**
- Consumes: `pendingPicker`, `infoOverlay`, the pending-action modal key handler (`updatePendingActionKey`).
- Produces: `core.CleanChangelog(html string) string` (the moved `stripHTMLForTerminal`, verbatim); `UpdateItem.Changelog` (full cleaned text — no 800-char truncation; the overlay scrolls).

- [ ] **Step 1: Failing tests** — `TestCleanChangelogMatchesLegacyStrip` (port the existing CLI behavior cases; CLI output capture for `lmm update` changelog block stays byte-identical), `TestUpdateModalVOpensChangelogOverlaySingle` (one update with changelog: `v` while the apply-updates modal is pending → infoOverlay titled `<name> <from> → <to>`, changelog lines; modal still pending after overlay closes), `TestUpdateModalVOpensPickerMultiple` (two updates → pendingPicker labeled per update; choosing one opens its overlay), `TestUpdateModalVEmptyChangelog` (overlay line `no changelog available`), `TestVIgnoredOutsideUpdateModal`, `TestCoreProviderCheckUpdatesPopulatesChangelog`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** — move the helper to core; CLI calls `core.CleanChangelog` at :225/:306. coreProvider.CheckUpdates maps `CleanChangelog(u.Changelog)`; prototype cans one changelog + one empty. Retain the `UpdatesView` on the model when opening the apply modal; handle `v` inside `updatePendingActionKey` ONLY when the pending action is the updates batch; overlay/picker intercepts already sit above the pending-action intercept — verify order, picker/overlay closures return `tea.Cmd` dispatched via deferred message (6a pattern). Modal footer hint gains `v changelog`.
- [ ] **Step 4: Run** — PASS; full suite; CLI update capture unchanged.
- [ ] **Step 5: Commit** `feat(tui): changelog viewer in update flow; share CleanChangelog with CLI (#37)`

---

### Task 8: `PlanImport`/`ApplyImport` core extraction + CLI rewrite

**Files:**
- Modify: `internal/core/flows.go` (new flow + Import\* `DeployPhase` constants), `cmd/lmm/profile.go` (`doProfileImport` :407-636)
- Test: `internal/core/flows_test.go`, cmd-level capture tests for `profile import`

**Interfaces:**
- Consumes: `ProfileManager.ParseProfile`/`ImportWithOptions` (internal/core/profile.go:246-273), `GetMod`/`GetModFiles`/`DownloadMod`/`installer.Install`/`SaveInstalledMod`/`UpsertMod`, the file-selection logic behind cmd's `selectFilesToDownload` (move/share it in core if it still lives CLI-side — the install/switch flows' existing core equivalent is the reference), `ConfirmConflicts`-style callback precedent (flows.go:2177).
- Produces:

```go
type ImportPlan struct {
    Profile                        *domain.Profile
    Installed, NeedsRedownload, Missing []domain.ModReference
    Exists                         bool                     // a profile with this name already exists
    // internal: needsRedownload set + stored-FileIDs lookup, preserved for ApplyImport's selection rules
}
type ImportOptions struct {
    Force, NoInstall bool
    // ConfirmInstall, when non-nil and downloads are pending, is called AFTER
    // the profile is saved (mirroring the CLI's prompt position); returning
    // false skips the install loop. nil = proceed.
    ConfirmInstall func(toDownload []domain.ModReference) bool
}
type ImportResult struct {
    ProfileName                 string
    Installed, Failed, Skipped  int
    Warnings, Notes             []string
}
func (s *Service) PlanImport(ctx context.Context, game *domain.Game, data []byte) (*ImportPlan, error)
func (s *Service) ApplyImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ImportOptions, progress func(DeployProgress)) (*ImportResult, error)
```

- [ ] **Step 1: Failing tests** — capture tests FIRST on directory-source sandbox fixtures pinning `doProfileImport` for: all-installed, needs-redownload, missing-with-install (prompt `y`), prompt-declined (`n`), `--no-install`, `--force` overwrite, existing-profile-without-force error. Core: `TestPlanImportCategorizes` (DB+cache categorization incl. cross-profile scan :428-438), `TestApplyImportSavesAndInstalls` (missing mod downloaded via directory source, installed, SaveInstalledMod + UpsertMod with downloaded FileIDs), `TestApplyImportConfirmInstallDeclined` (saved, Skipped set, no installs), `TestApplyImportNoInstall`, `TestApplyImportForceOverwrite` + duplicate-name error without Force, `TestApplyImportPartialFailure` (one bad ref → Failed count, loop continues), `TestApplyImportRedownloadUsesStoredFileIDs` (:544-552 rule preserved), `TestApplyImportCtxCancelled` (loop breaks between mods, quit-drain compatible).
- [ ] **Step 2: Run** — core FAIL; captures PASS.
- [ ] **Step 3: Implement** — port :407-636: PlanImport = parse + categorize (pure); ApplyImport = `ImportWithOptions` save → early-out on no-downloads/NoInstall (Skipped) → ConfirmInstall gate → install loop with new Import\* phases, one per CLI print site (`ImportModInstalling`, `ImportDownloading` (\r %.1f%%), `ImportModFailed` (per-error Detail verbatim), `ImportModInstalled`, `ImportNote` for the verbose-gated UpsertMod warning, plus an `ImportSaved` event at the save point) — same doc-comment discipline as the Update\* block (flows.go:657-706). Share the per-ref download/install steps with the ApplyProfileSwitch/ApplyInstall machinery where output fidelity permits (the `purgeMods` shared-loop precedent); where fidelity forbids, keep the ported loop and say so in the task report. CLI rewrite: summary prints from plan (:462-478 verbatim), prompt via ConfirmInstall closure wrapping `readPromptLine`, event-driven loop prints, summary from result (:629-633).
- [ ] **Step 4: Run** — captures byte-identical across all seven pinned paths; full suite PASS.
- [ ] **Step 5: Commit** `refactor(core): extract PlanImport/ApplyImport from profile import CLI (#37)`

---

### Task 9: Import TUI (`I` on Profiles)

**Files:**
- Modify: `internal/tui/keys.go` (+`ImportProfile` `I`), `internal/tui/actions_provider.go` (interface + prototype), `internal/tui/service_core.go`, `internal/tui/app.go` (dispatch), `internal/tui/mutations.go` (+`importProfilePrompt` chain), tests alongside

**Interfaces:**
- Consumes: Task 8's flow; `pendingInput`, `buildAction`, deferred-message dispatch; existing `switchSelectedProfile`-style plan/apply chain for the follow-up.
- Produces: `ActionProvider.PlanImport(ctx context.Context, data []byte) (ImportPlanView, error)` and `ActionProvider.ApplyImport(ctx context.Context, data []byte, progress func(ActionProgress)) (ActionOutcome, error)` (re-plans at apply, Force=true, ConfirmInstall=nil — modal confirm is the consent); `ImportPlanView{Name, GameID string; Installed, NeedsDownload, Missing []string; Exists bool}`.

- [ ] **Step 1: Failing tests** — `TestImportKeyOpensPathInput` (Profiles screen, `I` → input modal titled `import profile — path to yaml`), `TestImportUnreadablePathErrorsInModal` (validate/submit surfaces read error, modal stays open), `TestImportPreviewModalFromPlan` (recordingActions plan: detail lines carry counts + names, `overwrites existing profile` warning when Exists, `different game: <id>` warning when GameID ≠ active game), `TestImportConfirmAppliesWithProgress` (ApplyImport called, refresh after), `TestImportOfferSwitchAfterApply` (successful apply for the ACTIVE game → follow-up confirm `switch to "<name>" now?`; yes routes into the existing profile-switch plan flow; declined → no action; non-active-game import → no offer), `TestPrototypeImportAddsProfile`, `TestImportKeySwallowedByFocusedSearchInput`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** — `I` on Profiles → `pendingInput` (path); submit reads the file TUI-side (`os.ReadFile`, mirroring the CLI's own read-then-delegate at :396-405) and dispatches a deferred plan msg; preview modal via `buildAction` with ApplyImport as the confirm body; on success dispatch a deferred switch-offer msg resolved on the live model (running/pending guarded). coreProvider maps plan→view (names as `sourceID:modID v<ver>` matching CLI list lines) and ApplyImport with Force=true/nil-Confirm. Prototype: canned plan (`Name:"imported"`, one missing mod), ApplyImport appends the profile — visible on repeated Profiles read.
- [ ] **Step 4: Run** — PASS; full suite; prototype demo: `I` end-to-end.
- [ ] **Step 5: Commit** `feat(tui): profile import with preview and switch offer (#37)`

---

### Task 10: Export TUI (`E` on Profiles)

**Files:**
- Modify: `internal/tui/keys.go` (+`ExportProfile` `E`), `internal/tui/actions_provider.go` (interface + prototype), `internal/tui/service_core.go`, `internal/tui/app.go`, `internal/tui/mutations.go` (+`exportProfilePrompt`), tests alongside

**Interfaces:**
- Consumes: `ProfileManager.Export` (internal/core/profile.go:217), `pendingInput`.
- Produces: `ActionProvider.ExportProfile(ctx context.Context, name, path string) (ActionOutcome, error)` — coreProvider writes with `os.OpenFile(path, O_WRONLY|O_CREATE|O_EXCL, 0644)`; an existing file refuses with error `file exists: <path>`. Outcome: `exported "<name>" to <path>`.

- [ ] **Step 1: Failing tests** — `TestExportKeyOpensPathInputPrefilled` (`E` on selected profile → input prefilled `<gameID>-<name>.yaml`), `TestExportWritesFile` (coreProvider + t.TempDir(): file exists, contents == `pm.Export` bytes), `TestExportRefusesOverwrite` (pre-create file → error surfaces in status line, file untouched), `TestExportSuccessStatusLine`, `TestPrototypeExportSucceedsWithoutWriting` (no filesystem side effects), `TestExportKeySwallowedByFocusedSearchInput`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** — binding + handler (selected profile, guards); sync provider call (local write — same sync exception as reorder); status line result. CLI unchanged (`profile export` still prints to stdout — presentation-side difference over the same `pm.Export`, per the parity directive's interface-side carve-out).
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): profile export to file (#37)`

---

### Task 11: Help, docs, and staleness sweep

**Files:**
- Modify: `internal/tui/app.go` (helpGroups), `internal/tui/help_test.go`, `cmd/lmm/tui.go` (`Long`), `README.md` (TUI section + keybindings), `docs/plans/2026-04-28-tui-implementation.md` (status header, Phase 6 section, CLI-parity tables), `CHANGELOG.md` (`[Unreleased]`)

**Interfaces:** none new — consumes every prior task's keys/screens.

- [ ] **Step 1: Failing test** — extend the help-group tests: Installed Mods lists `J/K` + `<`; Profiles lists `E`/`I`; global lists `6`; Conflicts screen has its own group; update-modal footer mentions `v`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement + sweep** — help entries; then grep `cmd/lmm/ README.md docs/` for `not yet`, `read-only`, `aren't available`, `use 'lmm`, `until Phase 6`, `Phase 6` residue and fix every hit that this phase obsoletes (tui.go Long bit BOTH prior phases); roadmap: mark Phase 6 COMPLETE in the status header, move the issue-#37 parity-table rows to "covered"; CHANGELOG `[Unreleased]`: Added (conflict screen + CLI winner lines, reorder, rollback TUI, changelogs, export/import TUI) and Changed (deterministic deploy ordering — called out as behavior change; conflicts output sorted+Winner lines).
- [ ] **Step 4: Run** full `go test ./...`, `go vet ./...`; re-grep returns no stale hits.
- [ ] **Step 5: Commit** `docs: phase 6b help, README, roadmap, changelog (#37)`

---

### Task 12: Release chore — v1.14.0

**Files:**
- Modify: `cmd/lmm/root.go` (`version` variable), `CHANGELOG.md` (cut `[Unreleased]` → `[1.14.0] - <today>`, comparison links)

- [ ] **Step 1:** Bump `version` to `1.14.0`; move `[Unreleased]` items to `[1.14.0]` dated today; add the compare link.
- [ ] **Step 2:** `go build -o lmm ./cmd/lmm && ./lmm --version` → `1.14.0`; full `go test ./...` green.
- [ ] **Step 3: Commit** `chore: bump version to 1.14.0`

---

## Post-plan process (repo conventions — not plan tasks)

Final whole-branch live review with the most capable model: twin sandboxes (branch vs merge-base binaries; HOME + `--config`/`--data` isolation — lmm ignores XDG vars), byte-diffed CLI parity matrices for `conflicts` / `update rollback` / `profile import` / `profile apply` / `deploy`, tmux-driven TUI walkthrough of all four workstreams. Then: PR → Copilot triage (incl. post-push rounds) → build `./lmm` in repo root for the USER'S smoke test (checklist must include real-NexusMods items: live changelog rendering, rollback of a genuinely updated mod) → merge-commit → tag `v1.14.0` → close #37 with completion comment → archive this plan + the design doc to `docs/plans/archive/` (rides the PR) → update memory + SDD ledger.
