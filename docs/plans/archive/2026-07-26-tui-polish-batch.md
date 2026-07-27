# TUI Polish Batch Implementation Plan (#68, #77, #91, #106 — EPIC #104)

**Goal:** Close four small TUI polish issues in one branch: `--no-color` plumbing (#91), recently-updated marker (#77), dashboard placeholders (#106), and the Phase 6a deferred-polish ledger (#68).

**Architecture:** No new subsystems. #91 pins a lipgloss color profile behind a new `Options.NoColor`; #77 extends `modRow`'s fixed-width flags column; #106 adds a `GetLastDeployTime` DB query surfaced through `Summary.LastDeploy` into all four dashboard layouts plus `lmm status`; #68 is a curated sweep of DRY/UX/test items already mapped to exact sites.

**Tech Stack:** Go, Bubble Tea, lipgloss/termenv, SQLite (modernc), testify require.

## Global Constraints

- Branch: `feat/tui-polish-batch` off `main`; one PR closing all four issues.
- TDD for every behavior change: failing test first, RED evidence recorded, then GREEN.
- gofmt (tabs), `go vet` clean, full `go test ./...` before each commit; dense why-comments in the package style.
- Styled-bytes assertions are vacuous in the no-color test env — use the Transform-marker technique (see `TestSearchDetailPaneStylesInstalledStatus`, search_test.go) or a pinned in-process renderer profile for any styling assertion. Never assert `theme.X.Render(...)` bytes against a whole view.
- Invariants that must not regress: exact-height at 120x36 (4 themes), the fits-height-budget sweep, `TestDashboardLayoutsFitHeightBudgetWithLongDynamicValues`, existing modRow alignment tests.
- CLI/TUI parity directive: new capabilities (LastDeploy) surface in both interfaces.
- Version: MINOR bump to 1.18.0 at the end (new marker + new status field/JSON addition; JSON-contract-additions-are-MINOR precedent).
- Scope corrections established by exploration (do NOT re-implement what already works):
  - #106(b): the dashboard already shows a real conflicts count after first load — `loadData` overwrites `Overview`'s `-1` (`app.go:365`). Fix is the stale comment at `service_core.go:162-163` (rewrite to explain `loadData` owns the live value), NOT new conflict wiring.
  - #106(c): `Summary.Updates` already refreshes on check completion (`mutations.go:996`). Add a regression test only if none exists; cross-session persistence is DEFERRED (open design decision — note on the issue, do not implement).
  - #68 items resolved-by-documentation (close on the issue, no code): triple update-policy set (doc comments at `actions_provider.go:713-718` / `service_core.go:544-551` argue deliberate separation — prototype has no domain enum; TUI wire string "pin" must not collide with CLI "pinned"); `rebindGame` split-brain (`actions.go:362-372`, unreachable in practice, structural prevention out of scope).

---

### Task 1: #91 — `--no-color` reaches the TUI (+ nav-bar color-only fix)

**Files:** Modify `cmd/lmm/tui.go`, `cmd/lmm/root.go` (read `noColor`), `internal/tui/app.go` (Options + nav), `internal/tui/theme/theme.go` (only if a renderer seam is needed). Test: `internal/tui/nocolor_test.go` (new), nav test in `app_test.go`.

**Facts:** root `--no-color` flag: `root.go:43,68`, consumed only by CLI color helpers (`colorEnabled()` root.go:74-81). `tui.Options` (`app.go:40-51`) has no color field; `runTUI` (`cmd/lmm/tui.go:47-70`) never reads `noColor`. No `SetColorProfile`/renderer call exists anywhere in `internal/tui`; `NO_COLOR` env works only transitively via termenv's default-renderer detection. Nav bar (`app.go:948-959`) marks the current screen by `theme.Selected` color ALONE — invisible without color.

**Steps:**
1. RED test (nocolor_test.go): differential renderer test. Baseline: `lipgloss.SetColorProfile(termenv.ANSI)` (t.Cleanup restore via `lipgloss.SetColorProfile(termenv.Ascii)` — note profile is process-global, so this test must NOT be t.Parallel(); document why), render a sized prototype model's `View()`, assert it CONTAINS `"\x1b["`. Then build a model with `Options{NoColor: true}` and assert its `View()` contains NO `"\x1b["` despite the forced ANSI profile. This fails: `Options.NoColor` doesn't exist (compile error).
2. Implement: add `NoColor bool` to `Options` with a doc comment (root `--no-color` flag and NO_COLOR env parity; pins the Ascii profile so styles emit no escapes). In `NewModel`/`NewPrototypeModel` (or a shared helper they both call), when `opts.NoColor`, call `lipgloss.SetColorProfile(termenv.Ascii)` BEFORE theme construction (`theme.New(...)` — check where styles are instantiated; if theme styles are package-level at init, move/force re-resolution — verify empirically with the test). In `runTUI`, set `NoColor: noColor || os.Getenv("NO_COLOR") != ""` (reuse `colorEnabled()` if exported-able within package main — it is, same package).
3. RED test for nav: with a Transform-marked `Selected` style (the search_test.go technique), assert the current screen's nav label carries a NON-color marker distinguishable from other labels — currently fails because only color distinguishes it.
4. Implement nav fix: current screen's label gets a plain-text distinguisher in addition to styling — render as `[1] Dashboard` → e.g. `«[1] Dashboard»`? No — match app copy style: wrap current in `theme.Selected` AND swap the number-bracket glyphs, e.g. `▸1▸`? Keep it minimal and terminal-safe: prefix the current screen's label with `•` (bullet + space) inside the styled span, adjusting nav width accounting if any test pins the nav line. Choose the glyph that existing tests/snapshots tolerate; regenerate snapshots ONLY if the nav change is intended to show there (it is — regenerate in Task 7's finalize if goldens change, noting it in the commit).
5. Full suite + vet. Commit: `fix(tui): honor --no-color and NO_COLOR via pinned Ascii profile; mark nav selection without color (#91)`.

### Task 2: #77 — transient "updated this session" marker

**Files:** Modify `internal/tui/app.go` (`modRow`, `modFlags`), test `app_test.go` or new `updated_marker_test.go`.

**Facts:** `Model.lastUpdates *UpdatesView` (`app.go:144`) is set on EVERY check (`mutations.go:1006,1024`) — including checks the user never applied — and cleared only on game switch (`mutations.go:1523`). Flags column: `const flagsWidth = 3 // "pin"` (`app.go:1934`), fixed-width by design (alignment Finding 2, doc `app.go:1914-1927`). `modFlags` (`app.go:1955-1960`) renders `"pin"` or `""`.

**Design (decided):** Mark a mod only when it was actually UPDATED: match `lastUpdates.Updates` by Source+ID (the `viewSelectedModChangelog` key, `mutations.go:276`) AND `mod.Version == u.NewVersion` — a checked-but-unapplied update fails the version comparison, so no false "updated" cue. Marker: `*`. Widen `flagsWidth` 3→5 to fit `pin *`, `pin  `, `    *`, `     ` (pin left-aligned, marker in the last column) — fixed width preserved, alignment invariant intact; name column loses 2 cols (acceptable at 80; the long-name tests guard truncation). Lifetime: session (rides `lastUpdates`'s existing lifetime; cleared on game switch). Prototype parity: the prototype provider's canned check/apply flow sets `lastUpdates` through the same resolver, so the marker works in demo mode without provider changes — verify with a prototype-flow test, not new fake data.

**Steps:**
1. RED test: drive a model through the apply-updates flow (or set `m.lastUpdates` + `m.mods` directly per existing test patterns): a mod matching Source+ID+NewVersion shows `*` in its row; a mod matching Source+ID whose Version ≠ NewVersion (checked, not applied) shows NO marker; a pinned+updated mod shows both `pin *`; rows stay aligned (reuse the existing alignment assertion pattern from modRow's tests).
2. Implement `modFlags` extension + `flagsWidth = 5` + doc comment (why version comparison: checked≠applied; why 5: pin+marker coexistence).
3. Guard tests: existing modRow alignment + 80-col long-name tests still pass; height-budget sweep unaffected.
4. Full suite + vet. Commit: `feat(tui): mark mods updated this session in the installed list (#77)`.

### Task 3: #106(a) — real "Last deploy" everywhere

**Files:** Modify `internal/storage/db/files.go` (+`files_test.go`), `internal/tui/service.go` (Summary), `internal/tui/service_core.go` (Overview), `internal/tui/service.go` prototype branch, `internal/tui/app.go` (4 dashboard layouts), `internal/tui/prototype/data.go` (canned value). 

**Facts:** `deployed_files.deployed_at` persisted on every upsert (`migrations.go:139-154`, `files.go:25-38`). No MAX query exists (full method list verified). `"Last deploy: ?"` is hardcoded (`app.go:1062`, party only); terminal/commander/crt have NO deploy row — parity means ADDING one to each. Summary flows `Overview` → `loadData` → `dataLoadedMsg` → `m.summary`.

**Steps:**
1. RED DB test (in-memory SQLite per repo pattern): `GetLastDeployTime(gameID, profileName)` returns nil before any deploy (never-deployed ≠ error), the max `deployed_at` after several `SaveDeployedFile` calls, and scopes by game+profile. Implement: `SELECT MAX(deployed_at) FROM deployed_files WHERE game_id = ? AND profile_name = ?` returning `*time.Time` (NULL → nil).
2. Summary: add `LastDeploy *time.Time` (nil = never/unknown; doc the distinction: coreProvider nil = never deployed; prototype sets a canned recent time). Populate in `coreProvider.Overview` via the new method (find the db handle the provider reaches — `p.svc` exposes storage; add a thin `core.Service` passthrough if the provider has no direct db access, keeping layering: TUI → core → storage).
3. Rendering: shared helper `lastDeployLabel(t *time.Time) string` — nil → `"never"`, else relative ("3h ago" / "2d ago"; ≥7d → date). RED test the helper table-driven (fake clock: pass `now` as a parameter — no time.Now() inside the pure helper). Wire into all four layouts: party replaces the hardcoded line; terminal adds a `> DEPLOY` row; commander adds a left-panel `Deploy` line; crt adds a `▓ DEPLOY` row — matching each layout's existing copy style, inside the existing clamp/truncate passes. RED first: a test asserting each layout's view contains the label for a summary with a known timestamp (Transform/plain-text assertion, sized model per theme).
4. Prototype: canned `LastDeploy` in the prototype Overview (e.g. a fixed reference time; remember `Date.now`-style nondeterminism — derive display from injected `now` at render, with the model using `time.Now()` only at the View boundary... simplest deterministic approach: compute the label at data-load time is wrong (stale); compute in View with `time.Now()` and keep tests deterministic by passing summaries with nil or with times relative to a captured now, asserting only the coarse unit ("h ago" suffix) or injecting a now-func on Model — pick the smallest seam: `m.now func() time.Time` defaulting to `time.Now`, overridden in tests).
5. Full suite + vet. Commit: `feat(tui): real last-deploy timestamp on every dashboard layout (#106)`.

### Task 4: #106(d) — CLI parity + (b)/(c) closeout

**Files:** Modify `cmd/lmm/status.go` (+ its tests), `internal/tui/service_core.go` (comment), maybe `internal/tui/mutations_test.go` (regression test).

**Steps:**
1. `lmm status <game>`: RED test (text + `--json`) for a `Last Deploy:` line in `showGameStatus` (after Active Profile block, `status.go:324-338`) and `last_deploy,omitempty` on `statusGameDetailJSON` (`status.go:242-253`), nil omitted / "never" in text. Reuse `GetLastDeployTime` via the same core passthrough. All-games table: leave unchanged (schema churn not worth it — note in PR).
2. Rewrite the stale comment at `service_core.go:162-163`: Conflicts `-1` is intentional in Overview because `loadData` (`app.go:353-365`) computes the live count on every refresh; Updates `-1` = unknown-until-checked, refreshed by `resolveCheckUpdatesResult` (`mutations.go:996`).
3. Verify a regression test pins the updates-refresh behavior (`m.summary.Updates` set after check completes); add one if absent (search `mutations_test.go`/`policy_test.go` first).
4. Full suite + vet. Commit: `feat(cli): last-deploy in game status; correct stale overview comments (#106)`.

### Task 5: #68 — DRY/consistency code sweep (behavior-preserving)

**Files:** `internal/tui/app.go`, `internal/tui/mutations.go`, `internal/tui/actions_provider.go`, `internal/tui/service_core.go`, `internal/tui/picker.go`.

All sites verified on current main; each bullet is one small mechanical change (behavior-preserving — goldens must not change; prove like PR #105's cleanup commit: regenerate snapshots, `git status docs/assets/tui` clean):
1. `helpRow(key, desc string) string` helper; collapse the 3 raw `"%-16s %s"` sites (`app.go:1722,1735,1791`); `helpEntry` delegates to it.
2. Promote-to-front double-append (`app.go:1829-1836`): single-pass build.
3. `"Update policy — %s"` built twice (`mutations.go:440,478`): build once/share.
4. `resolveProfileCreate` never-shown title (`mutations.go:1235-1240`): delete the dead title.
5. Closure-capture harmonization: the 5 `actions := m.actions` sites (`mutations.go:479,561,765,946,1234`) → direct `m.actions` capture like the other ~12 (verify no aliasing reason exists per site before changing — if one is load-bearing, document instead).
6. Purge progress-tick: prototype `"✓ %s"` (`actions_provider.go:766`) → match core's `"✓ %s (%d/%d)"` (`service_core.go:768`).
7. Picker quit-key: `picker.go:52` → `m.isQuitKey(msg)` to match overlay (`overlay.go:70`); keep the equivalence doc comment but shrink it.
Commit: `refactor(tui): Phase 6a DRY sweep — helpRow, dead title, capture style, tick format (#68)`.

### Task 6: #68 — UX polish (TDD each)

1. Silent picker-drop hint (`mutations.go:472-475`): on drop, set a muted status hint ("busy — choice ignored" in app copy voice) WITHOUT stomping a running action's status — reuse/extend `hasVisibleStatus` semantics; RED test: drop path leaves a visible hint; running-action status not overwritten.
2. Input modal: clear `errMsg` on any edit keystroke (`input_modal.go:100-104` default branch); truncate the hint line like siblings (`input_modal.go:154-158`); RED tests for both (stale-error-clears-on-typing; overlong hint stays within width).
3. Status-stomp guards: `purgeProfilePrompt` (`mutations.go:1305-1313`) and `deleteSelectedProfile` (`mutations.go:1254-1266`) must not overwrite the status line while `m.action.running` — same pattern the drop-hint uses; RED tests.
Commit: `fix(tui): picker-drop hint, input-modal error lifecycle, status-stomp guards (#68)`.

### Task 7: #68 — test-coverage batch + fake consolidation

1. `pickerWindow` direct unit table (n/selected/budget → start/windowSize), including edges the indirect tests skip.
2. Picker overflow indicators: assert exact "↑ N more"/"↓ N more" digits (`picker_test.go:130` currently substring-only).
3. Input-modal guard arms: table over `pending/picker/inputModal non-nil` (`input_modal.go:68`); nil-`validate` footgun (`input_modal.go:122`): add a nil-guard (skip validation) + test, since a latent panic beats a convention.
4. Embeddable no-op `stubProvider` (test file): implements all 8 `DataProvider` methods with zero values; migrate the worst boilerplate fakes (start with `app_test.go`'s `failingProvider`/`emptyProvider`/`sentinelUpdatesProvider` family — embed + override one method); do NOT force-migrate fakes where explicitness aids the test.
5. Resolver drop-window sweep: table-driven "dropped while busy" test over the unpinned resolvers (`resolveCheckUpdatesResult/Failure`, `resolveChangelogPicked`, `resolveImport*`, `resolveExportSubmitted`, `resolvePlanResult/Failure`, `resolveInstallPlanResult/Failure` — `mutations.go:980,1031,1090,1686,1730,1768,1849,590,624,782,807`): each entry = message constructor + precondition setter; assert no state change/dispatch while `running`/`pending`. If a resolver resists uniform harness treatment, cover it individually or document why not.
Commit: `test(tui): pickerWindow table, guard arms, stub provider, resolver drop sweep (#68)`.

### Task 8: Finalize

1. Full verification: build, gofmt (scoped `cmd internal`), vet, `go test ./...`; regenerate snapshots — expected changes ONLY from Task 1's nav marker and Task 3's dashboard rows (+ Task 2's flags column at 80 wide if visible); commit regenerated goldens with that justification.
2. CHANGELOG under new `[1.18.0]` (Added: marker, last-deploy TUI+CLI+JSON field; Fixed: --no-color, nav color-only, picker-drop hint, input-modal errMsg, status stomps; Internal/Changed: snapshot naming already shipped in 1.17.1 — do not repeat). Bump `version` to 1.18.0 (`cmd/lmm/root.go:36`), links block. Separate `chore: bump version to 1.18.0` commit.
3. Issue notes: #68 — check off delivered items; close-as-documented rationale for triple-policy-set + rebindGame; list what item 12's sweep now covers. #106 — note the two stale premises (conflicts already live, updates already refresh) and that cross-session updates persistence is explicitly deferred as a design decision. #77/#91 — brief completion notes. (Issues auto-close via PR "Closes" lines: #68 only if every item is done/dispositioned — decide at PR time; #77, #91, #106 close.)
4. PR: `feat(tui): polish batch — no-color, updated marker, live dashboard data, Phase 6a sweep (#68 #77 #91 #106)`; body lists per-issue outcomes + scope corrections; smoke-test gate note. Plan doc → `docs/plans/archive/` in-PR (new precedent from #105).

## Execution notes

- Order: Tasks 1→4 are behavior (TDD); 5 is pure refactor; 6-7 mix; 8 finalizes. Tasks are sequential (shared files: app.go/mutations.go throughout). Task 3 is the largest; Task 5 is mechanical.
- Model selection: Task 5 cheap-tier; Tasks 1-4, 6-7 mid-tier; reviews mid-tier; final whole-branch review most-capable.
- #106 updates-persistence and #85-style decisions: DO NOT implement; note-and-defer.
