# TUI Phase 6a — Local Workflows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the six Phase 6a TUI workflows (purge view, files panel, policy editing, game switcher, profile create/delete, expanded help) on Phase 5's modal/action machinery — design in `2026-07-23-tui-phase6a-workflows-design.md`.

**Architecture:** Two new modal primitives (picker, text-input) plus one info overlay join the existing `pendingAction` confirm modal; four new `ActionProvider` methods and two new `DataProvider` methods (plus a `gameRebinder` optional interface mirroring `profileRebinder`) carry the features; no new screens, no CLI changes, no new core flows (everything consumes existing `core.Service`/`ProfileManager` methods, incl. #61's `PurgeProfile`).

**Tech Stack:** Go, Bubble Tea/bubbles (`textinput`), testify; existing tui test harness (prototype provider, `recordingActions`, real-Service provider fixtures).

## Global Constraints

- Branch `feat/tui-phase6a-workflows`; PRs to protected main; merge commits; Copilot triage incl. post-push rounds; TUI smoke-test before merge.
- TDD: every behavior lands with a failing test first. `gofmt` (tabs), `go vet ./...`, full `go test ./...` green per task; commit per task.
- Focused-input law: new printable keys (`X f P g c d`) must be swallowed by a focused search input (dispatch AFTER the focused-input branch, `app.go:489-504`).
- Exact-height invariant: modal/overlay views render via `actionModalView`-style panels; truncate, never wrap (use `truncate`, `availableWidth`, `panelWithHeight` helpers `app.go:1262-1323`).
- Single-flight: every new mutation path starts via `buildAction` (`actions.go:242`) or is guarded by `m.action.running || m.action.pending != nil`.
- CLI untouched this phase. Version bump lands ONLY in the final task (1.13.0).
- New TUI copy is lowercase-terse matching existing footer/help strings.

---

### Task 1: Picker modal primitive

**Files:**
- Create: `internal/tui/picker.go`, `internal/tui/picker_test.go`
- Modify: `internal/tui/app.go` (Model field, updateKey intercept, screenView render)

**Interfaces:**
- Consumes: `Model`, `KeyMap` (Up/Down/Select/CancelAction), panel helpers.
- Produces: `pendingPicker`, `pickerOption{Label, Note string}`, `Model.picker *pendingPicker`, `(m Model) promptPicker(p pendingPicker) Model` (nil-op when action busy), `(m Model) updatePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd)`, `pickerView() string`. Tasks 5 and 7 build on these exact names.

- [ ] **Step 1: Failing tests** — in `picker_test.go` (package `tui`, internal): `TestPickerNavigateAndChoose` (open a 3-option picker via `promptPicker`, send `j`/down twice + `enter`, assert `choose` called with index 2 and `m.picker == nil`), `TestPickerDigitQuickSelect` (send `"2"`, assert choose(1)), `TestPickerEscCancels` (esc → `picker == nil`, choose not called), `TestPickerBlockedWhileActionPending` (set `m.action.pending`, `promptPicker` must not set `m.picker`).
- [ ] **Step 2: Run** `go test ./internal/tui/ -run TestPicker -v` — expect compile failure (types undefined).
- [ ] **Step 3: Implement** in `picker.go`:

```go
type pickerOption struct{ Label, Note string }

type pendingPicker struct {
	title    string
	options  []pickerOption
	selected int
	choose   func(idx int) tea.Cmd
}
```

`promptPicker`: guard `m.action.running || m.action.pending != nil || m.picker != nil`; else set `m.picker = &p`. `updatePickerKey`: Up/Down move selected (clamped); digits `1`-`9` select `int(r-'1')` when in range and immediately choose; `Select` (enter) → clear picker, return `choose(selected)`; `CancelAction`/`Blur` (esc; also plain `n` must NOT cancel — pickers contain text options) → clear picker; Quit keys → `startQuit()`; default swallow. `pickerView`: bordered panel like `actionModalView` (`actions.go:543`), `>` marker on selected row, Note dimmed right of label, hint line `"↑/↓ move · enter choose · esc cancel"`. Wire in `app.go`: `picker *pendingPicker` field on Model; intercept in `updateKey` immediately after the `m.action.pending` intercept (`:470-472`); render in `screenView()` before the per-screen switch (after `actionModalView` branch).
- [ ] **Step 4: Run** the four tests — PASS; run full `go test ./internal/tui/`.
- [ ] **Step 5: Commit** `feat(tui): add picker modal primitive (#37)`

---

### Task 2: Text-input modal primitive

**Files:**
- Create: `internal/tui/input_modal.go`, `internal/tui/input_modal_test.go`
- Modify: `internal/tui/app.go` (field, intercept, render)

**Interfaces:**
- Consumes: `textinput` (construction pattern `search.go:91-104`), panel helpers.
- Produces: `pendingInput`, `Model.inputModal *pendingInput`, `promptInput(p pendingInput) Model`, `updateInputModalKey(msg) (tea.Model, tea.Cmd)`, `inputModalView() string`. Task 6 (profile create) consumes these.

- [ ] **Step 1: Failing tests**: `TestInputModalTypeAndSubmit` (open with validate=ok, type `"survival"`, enter → submit called with "survival", modal nil), `TestInputModalValidationErrorKeepsModalOpen` (validate returns `"name already exists"` → errMsg shown in `View()`, modal still open, submit not called), `TestInputModalEscCancels`, `TestInputModalBlockedWhileActionRunning`.
- [ ] **Step 2: Run** — compile failure.
- [ ] **Step 3: Implement**:

```go
type pendingInput struct {
	title    string
	input    textinput.Model
	errMsg   string
	validate func(value string) string // "" = ok, else error copy shown in-modal
	submit   func(value string) tea.Cmd
}
```

`promptInput`: same guards as picker (also `m.inputModal != nil`); focus the input (`p.input.Focus()`). `updateInputModalKey`: enter → trim value; empty → `errMsg = "name required"`; else `validate`; non-empty result → set errMsg, stay open; ok → clear modal, return `submit(value)`; esc → clear; ctrl+c → `startQuit()`; default → forward to `p.input.Update(msg)` (swallows everything printable — this is the focused-input law inside the modal). `inputModalView`: panel with title, `input.View()`, errMsg in `DangerText` when set, hint `"enter create · esc cancel"`. Intercept in `updateKey` after the picker intercept; render in `screenView` likewise.
- [ ] **Step 4: Run** tests — PASS; full package green.
- [ ] **Step 5: Commit** `feat(tui): add text-input modal primitive (#37)`

---

### Task 3: Info overlay primitive + deployed-files data method

**Files:**
- Create: `internal/tui/overlay.go`, `internal/tui/overlay_test.go`
- Modify: `internal/tui/service.go` (DataProvider + prototypeProvider), `internal/tui/service_core.go`, `internal/tui/app.go`, `internal/tui/app_test.go` (`recordingProvider` fake), `internal/tui/service_core_test.go`

**Interfaces:**
- Consumes: `svc.GetDeployedFilesForMod(gameID, profileName, sourceID, modID)` (`internal/core/service.go:701`), `currentProfile()`.
- Produces: `DataProvider.DeployedFiles(sourceID, modID string) ([]string, error)` (interface addition — implement on `coreProvider`, `prototypeProvider`, `recordingProvider`); `infoOverlay{title string; lines []string}`, `Model.overlay *infoOverlay`, `updateOverlayKey`, `overlayView`. Task 4 wires the `f` key.

- [ ] **Step 1: Failing tests**: overlay: `TestOverlayEscCloses`, `TestOverlayRendersTitleAndLines` (truncated at width), `TestOverlayCapsLines` (cap at `availableContentHeight`-chrome with `"+N more"` tail — mirror `actionModalMaxDetailLines` mechanics). Provider: `TestCoreProviderDeployedFiles` in `service_core_test.go` — extend `newCoreProviderFixture` usage: `svc.GetInstaller(game).Install(...)` one seeded mod, assert `DeployedFiles("nexusmods","mod-a")` returns the seeded relative paths sorted; `TestCoreProviderDeployedFilesEmpty` returns empty slice, nil error.
- [ ] **Step 2: Run** — compile failure (interface method missing on fakes → listed compile errors are the point).
- [ ] **Step 3: Implement**: interface method; `coreProvider.DeployedFiles` = `p.svc.GetDeployedFilesForMod(p.game.ID, p.currentProfile(), sourceID, modID)` (sorted); `prototypeProvider.DeployedFiles` returns canned `[]string{"skse64_loader.exe", "Data/SkyUI.esp"}`-style rows derived from the mod's name for any known mod ID; `recordingProvider` returns configurable `DeployedFilesResult []string` / `DeployedFilesErr error`. Overlay: struct + esc/`f`/`q`-handling (esc and `f` close; quit keys quit), render as titled panel of lines.
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): info overlay primitive + DeployedFiles provider method (#37)`

---

### Task 4: Files panel feature (`f` on Installed Mods)

**Files:**
- Modify: `internal/tui/keys.go` (+`Files` binding), `internal/tui/app.go` (updateKey case), `internal/tui/mutations.go` (`showDeployedFiles`), `internal/tui/mutations_test.go`

**Interfaces:**
- Consumes: Task 3's `DeployedFiles` + `infoOverlay`; selection via `m.selected[ScreenInstalledMods]`/`m.mods`.
- Produces: `Files key.Binding` (`f`), `(m Model) showDeployedFiles() (tea.Model, tea.Cmd)`.

- [ ] **Step 1: Failing tests**: `TestFilesKeyOpensOverlayWithDeployedFiles` (prototype model, select row 0, send `f`, assert `m.overlay != nil`, title contains mod name, lines non-empty), `TestFilesKeyEmptyStateMessage` (recordingProvider with empty result → overlay shows single line `"no files deployed"`), `TestFilesKeyIgnoredOnOtherScreens`, `TestFilesKeySwallowedByFocusedSearchInput` (focus search input, send `f`, assert input value contains "f" and no overlay), `TestFilesKeyErrorGoesToStatusLine` (provider error → `m.action.status` set, `statusIsError` true, no overlay).
- [ ] **Step 2: Run** — FAIL (binding/handler undefined).
- [ ] **Step 3: Implement**: binding `key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "files"))`; `showDeployedFiles` guards screen + valid selection + `provider != nil`; sync call to `DeployedFiles` (local DB read — documented exception to async loads); on error set status; else `m.overlay = &infoOverlay{title: fmt.Sprintf("Files — %s", item.Name), lines: files-or-empty-state}`. Wire `case key.Matches(msg, m.keys.Files)` in updateKey's outer switch.
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): per-mod deployed-files panel on f (#37)`

---

### Task 5: Update-policy editing (`P` on Installed Mods)

**Files:**
- Modify: `internal/tui/actions_provider.go` (interface + prototype impl), `internal/tui/service_core.go`, `internal/tui/keys.go` (+`Policy`), `internal/tui/actions.go` (+`actionSetPolicy` kind), `internal/tui/mutations.go` (`editSelectedModPolicy`), `internal/tui/app.go` (key case), tests: `mutations_test.go`, `actions_provider_test.go` (`recordingActions`), `service_core_test.go`

**Interfaces:**
- Consumes: Task 1's picker; `svc.SetModUpdatePolicy(sourceID, modID, gameID, profileName, policy domain.UpdatePolicy)` (`internal/core/service.go:665`).
- Produces: `ActionProvider.SetUpdatePolicy(ctx context.Context, item ModItem, policy string) (ActionOutcome, error)` with policy ∈ `"notify"|"auto"|"pin"`; `Policy key.Binding` (`P`); `editSelectedModPolicy()`.

- [ ] **Step 1: Failing tests**: `TestPolicyKeyOpensPickerWithCurrentMarked` (send `P` on Installed Mods; picker has exactly notify/auto/pin options; the item's current policy option Note is `"current"`), `TestPolicyPickerChoiceRunsActionAndRefreshes` (choose "pin" → `recordingActions.SetPolicyCalls == [{modID, "pin"}]`, done → status `"SkyUI update policy: pin"`, refresh cmd is `loadData`), `TestPolicyKeySwallowedByFocusedInput`, provider test `TestCoreProviderActions_SetUpdatePolicy` (real Service fixture: seed installed mod, call with "auto", read back `GetInstalledMod` → `domain.UpdateAuto`; invalid policy string → error).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: interface method; `coreProvider` maps `"notify"→domain.UpdateNotify, "auto"→domain.UpdateAuto, "pin"→domain.UpdatePinned` (unknown → `fmt.Errorf("unknown policy %q", policy)`), calls `SetModUpdatePolicy`, outcome Message `fmt.Sprintf("%s update policy: %s", item.Name, policy)`; prototype impl mutates its canned mod's `UpdatePolicy`. `editSelectedModPolicy`: guard screen/selection/actions; open picker titled `fmt.Sprintf("Update policy — %s", item.Name)`; `choose` closure runs the same `buildAction`-then-confirm-immediately pattern the input modal uses: `mm, pa := m2.buildAction(actionSetPolicy, ...)`; set `mm.action.running = true`; return `pa.confirm()` (no second confirm gate — the pick IS the confirmation). `actionDoneMsg` needs no kind-specific branch (default status+refresh path).
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): update-policy picker on P (#37)`

---

### Task 6: Profile create (`c`) and delete (`d`) on Profiles screen

**Files:**
- Modify: `internal/tui/actions_provider.go`, `internal/tui/service_core.go`, `internal/tui/keys.go` (+`CreateProfile`, `DeleteProfile`), `internal/tui/actions.go` (+`actionCreateProfile`, `actionDeleteProfile`), `internal/tui/mutations.go` (`createProfilePrompt`, `deleteSelectedProfile`), `internal/tui/app.go`, tests as Task 5

**Interfaces:**
- Consumes: Task 2's input modal; existing confirm modal; `ProfileManager.Create/Delete` via `svc.NewProfileManager()`.
- Produces: `ActionProvider.CreateProfile(ctx context.Context, name string) (ActionOutcome, error)`, `ActionProvider.DeleteProfile(ctx context.Context, name string) (ActionOutcome, error)`; bindings `c`/`d` (Profiles screen only).

- [ ] **Step 1: Failing tests**: `TestCreateProfileKeyOpensInputModal`; `TestCreateProfileDuplicateNameValidatesInModal` (existing name → in-modal error, no action dispatched); `TestCreateProfileSubmitRunsActionAndRefreshes` (submit "survival" → `CreateProfileCalls == ["survival"]`, status `Created profile: survival`); `TestDeleteProfileActiveRefusedOnStatusLine` (select active row, `d` → no modal, status error `"cannot delete the active profile"`); `TestDeleteProfileConfirmFlow` (non-active row → confirm modal titled `Delete profile "combat"?`, `y` → `DeleteProfileCalls == ["combat"]`, refresh); `TestProfileKeysIgnoredOffProfilesScreen`; provider tests against real Service: create then `pm.Get` succeeds; create duplicate → error; delete removes YAML (`pm.Get` → `ErrProfileNotFound`); delete of active profile at provider level returns error too (defense in depth: `if name == p.currentProfile() { return ActionOutcome{}, fmt.Errorf("cannot delete the active profile") }`).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: provider methods (`coreProvider` uses `p.svc.NewProfileManager()`; outcomes `Created profile: <name>` / `Deleted profile: <name>`); prototype impls mutate canned `Profiles` slice. `createProfilePrompt`: input modal, `validate` closure checks against `m.profiles` names (case-sensitive match = `"profile already exists"`); `submit` runs buildAction(actionCreateProfile)+immediate-confirm. `deleteSelectedProfile`: guard active row (status error, per test) then standard `promptAction` confirm; detail line `"mods keep their install records; only the profile list is removed"`.
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): profile create/delete on Profiles screen (#37)`

---

### Task 7: Purge behind confirmation (`X` on Dashboard/Installed Mods)

**Files:**
- Modify: `internal/tui/actions_provider.go`, `internal/tui/service_core.go`, `internal/tui/keys.go` (+`Purge`), `internal/tui/actions.go` (+`actionPurge`), `internal/tui/mutations.go` (`purgeProfilePrompt`), `internal/tui/app.go`, tests as Task 5

**Interfaces:**
- Consumes: `core.PurgeProfile` + `core.PurgeOptions`/`PurgeResult` and phases `DeployBeforeAllForced/DeployPurging/PurgeModPurged/PurgeModSkipped/PurgeWarning/PurgeNote/PurgeComplete` (`internal/core/flows.go:1423`); hook plumbing exactly as `coreProvider.DeployProfile` builds it (cached hooks/runner in `service_core.go`); `mergeDiagnostics`.
- Produces: `ActionProvider.PurgeProfile(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error)`; binding `X`.

- [ ] **Step 1: Failing tests**: `TestPurgeKeyPromptsWithModCountAndNames` (modal title `Purge 3 mod(s) from <Game>?`, detail lists mod names from `m.mods`, capped by existing detail cap); `TestPurgeKeyNoModsShortCircuitsToStatus` (empty mods → no modal, status `"no mods installed"`); `TestPurgeConfirmStreamsProgressAndReportsOutcome` (recordingActions replays two `ActionProgress` ticks then outcome `{Message:"Purged 2 mod(s)", Warnings:[...]}` → progress lines seen, final status includes warning count, refresh fires); `TestPurgeKeyWorksFromDashboardToo`; provider test `TestCoreProviderActions_PurgeProfile` (real Service: seed+install 2 mods, run, assert files gone from game dir, both DB rows `Deployed == false`, outcome message `Purged 2 mod(s)`, progress captured a `✓`-line per mod) and `..._SkipAndWarningsSurface` (before_each-failing hook script → Warnings contains `Skipped <name>: uninstall.before_each hook failed: ...`).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: `coreProvider.PurgeProfile`: fetch `mods := p.svc.GetInstalledMods(p.game.ID, p.currentProfile())` (re-fetch at apply — same documented plan-drift stance as `ApplyProfileSwitch`); empty → `ActionOutcome{Message: "no mods installed"}`; else call `p.svc.PurgeProfile` with the same hook block DeployProfile's provider method builds; progress mapping: `DeployPurging→{Line: fmt.Sprintf("purging %d mod(s)…", e.Total), Percent: -1}`, `PurgeModPurged→{Line: fmt.Sprintf("✓ %s (%d/%d)", e.ModName, e.Index, e.Total), Percent: -1}`, `PurgeModSkipped→{Line: "skipped " + e.ModName, Percent: -1}`, other phases not streamed. Outcome: `Message: fmt.Sprintf("Purged %d mod(s)", result.Purged)`; `Warnings: mergeDiagnostics(append(prefixed("Skipped ", result.Skipped), result.Warnings...), result.Notes)`. Errors return partial-result warnings alongside err (mirror `UninstallMod` provider convention). `purgeProfilePrompt` (works on Dashboard + Installed Mods): builds detail from `m.mods` names; standard `buildAction(actionPurge, ...)`+`promptAction`. Prototype impl: clears `Deployed`-ish canned state and returns `Purged N`.
- [ ] **Step 4: Run** — PASS; full suite.
- [ ] **Step 5: Commit** `feat(tui): purge behind confirmation view on X (#37)`

---

### Task 8: In-TUI game switcher (`g`)

**Files:**
- Modify: `internal/tui/service.go` (DataProvider + `GameInfo`), `internal/tui/service_core.go` (impl + `gameMu` + `SetGame`), `internal/tui/actions.go` (`gameRebinder`, `rebindGame`), `internal/tui/mutations.go` (`openGameSwitcher`, `switchGame`), `internal/tui/keys.go` (+`GameSwitch`), `internal/tui/app.go`, `internal/tui/prototype/data.go` + `internal/tui/service.go`/`actions_provider.go` prototype impls, tests: `mutations_test.go`, `service_core_test.go`, `app_test.go`

**Interfaces:**
- Consumes: Task 1 picker; `svc.ListGames()`/`svc.GetGame(id)` (verify exact names in `internal/core/service.go` at implementation — the CLI's `game list` path shows them), `resolveProfile` semantics (GetDefault → "default" fallback, reimplemented provider-side: `ProfileManager.GetDefault`).
- Produces: `GameInfo{ID, Name string; Active bool}`; `DataProvider.ListGames() ([]GameInfo, error)`; optional interface `gameRebinder interface{ SetGame(id string) error }` on BOTH coreProvider instances (mirror `profileRebinder`, `actions.go:289-308`); `Model.rebindGame(id string) error`; binding `g`.

- [ ] **Step 1: Failing tests**: `TestGameKeyOpensPickerActiveMarked`; `TestGameSwitchRebindsProvidersResetsAndReloads` (recordingProvider/actions with `SetGameCalls`; choose other game → both providers' `SetGameCalls == ["skyrim"]`, `m.state == stateLoading`, selections zeroed, `summary.Updates == -1` sentinel, sources re-seeded from `SourceInfos()`, in-flight search cancelled, returned cmd chain includes `loadData`); `TestGameSwitchBlockedWhileActionRunning` (running=true → `g` sets status error `"action in progress"`); `TestGameSwitchSameGameIsNoop`; provider tests: `TestCoreProviderListGames` (fixture: two games added → both listed, fixture game `Active`), `TestCoreProviderSetGame` (SetGame to second game; subsequent `Overview`/`Profiles` reflect it; unknown id → error, binding unchanged; hook cache invalidated — assert via the same seam `SetProfile` tests use).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: `coreProvider`: add `gameMu sync.RWMutex` + `currentGame() *domain.Game` accessor; mechanically replace every `p.game` read in `service_core.go` with `currentGame()` (the compiler + `grep -n "p\.game" internal/tui/service_core.go` must come back empty apart from accessor/SetGame). `SetGame(id)`: `svc.GetGame(id)` → error out on unknown; resolve profile via `GetDefault(id)` fallback `"default"`; under locks swap `game`+`profile`, nil `cachedHooks`. `ListGames`: enumerate configured games, `Active = id == currentGame().ID`, sorted by name. Model side: `openGameSwitcher` (any screen; guards busy + picker/input/overlay open) → `ListGames` sync → picker `"Switch game"` with active-marked Note; choose → `switchGame(id)`: no-op when already active; call `rebindGame` (type-assert both provider+actions); cancel in-flight search (reuse the switchedTo-path mechanics, `app.go:309-355` region); reset `summary = Summary{Updates: -1}`, `mods/profiles = nil`, zero all `selected` entries, `m.sources = m.provider.SourceInfos()`, `state = stateLoading`; return `m.loadData`. Prototype: restructure `prototype.Data` minimally — add `AltGame Game` + tiny `AltMods []Mod` canned set; `prototypeProvider.ListGames` returns both; `SetGame` swaps which set backs `Overview` (keep it small — the demo just needs the switcher to visibly work).
- [ ] **Step 4: Run** — PASS; full suite (race detector run too: `go test -race ./internal/tui/`).
- [ ] **Step 5: Commit** `feat(tui): in-TUI game switcher on g (#37)`

---

### Task 9: Help overlay expansion + footer copy

**Files:**
- Modify: `internal/tui/app.go` (`helpView` `:1202-1221`, `footerLine` `:631-634`), `internal/tui/app_test.go`, `internal/tui/snapshot_test.go` run

**Interfaces:** Consumes every binding added in Tasks 4-8. Produces no new API.

- [ ] **Step 1: Failing tests**: `TestHelpViewListsPerScreenGroups` (help output contains group headers `global`, `installed mods`, `profiles`, `search`, `dashboard` and the new hints `f files`, `P policy`, `X purge`, `g game`, `c new profile`, `d delete profile`), `TestFooterMentionsHelpKey` (footer contains `?`).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: restructure `helpView` into per-screen groups (global first, current screen's group highlighted/first after global), still a single truncated panel respecting the height invariant; keep footer terse (`?: help` stays the discovery point — don't stuff new keys into the footer beyond what fits).
- [ ] **Step 4: Run** — PASS; regenerate snapshots `UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui/ -run TestGenerateThemeSnapshots` and commit the refreshed `docs/assets/tui/*.ansi`.
- [ ] **Step 5: Commit** `feat(tui): per-screen help overlay groups (#37)`

---

### Task 10: Release chores + smoke gate

**Files:**
- Modify: `cmd/lmm/root.go` (version → `1.13.0`), `CHANGELOG.md`, comment on issue #37

- [ ] **Step 1:** Version `1.13.0`; CHANGELOG `[1.13.0]` section (Added: the six workflows, one bullet each, user-visible phrasing; note purge `--uninstall` stays CLI-only) + comparison links.
- [ ] **Step 2:** `gofmt -l cmd internal` empty; `go vet ./...`; `go test ./...` green; `go test -race ./internal/tui/`.
- [ ] **Step 3: TUI smoke test (merge gate):** `go build -o lmm ./cmd/lmm && ./lmm tui --prototype` — manually exercise: `X` purge modal+cancel, `f` files overlay, `P` policy pick, `g` game switch (data visibly changes), `c`/`d` on Profiles, `?` overlay on every screen, focused search input swallowing `Xf Pgcd`, 80×24 terminal size. Then against a real throwaway game config (the Task 7/8 provider fixtures' shape, or the smoke-env script pattern from the #60 session) run one real purge.
- [ ] **Step 4: Commit** `chore: bump version to 1.13.0`; push; PR titled `feat(tui): Phase 6a local workflows (#37)` with design-doc link; Copilot triage rounds; after merge: comment the 6a completion on #37, move design+impl docs to `docs/plans/archive/`.

---

## Self-review notes

- Spec coverage: purge→T7, files→T3+T4, policy→T5, switcher→T8, create/delete→T6, help→T9; primitives T1-T3; release→T10. All design-doc scope items covered; `--uninstall` purge correctly absent.
- Type consistency: `pendingPicker`/`promptPicker`/`pickerOption` (T1) used verbatim in T5/T8; `pendingInput`/`promptInput` (T2) in T6; `infoOverlay` (T3) in T4; `GameInfo`/`gameRebinder` defined once (T8).
- Known verify-at-implementation points (flagged, not placeholders): exact core method name for enumerating games (T8) and the hook-block builder name in `service_core.go` (T7) — both located by grep in the named files; the surrounding contracts are fully specified here.
