# TUI Implementation and Visual Iteration Plan

**Date:** 2026-04-28
**Scope:** Add a Bubble Tea/Lip Gloss TUI to `lmm`, starting with visual prototypes and iterating toward a real service-backed interface.
**Out of scope:** Replacing the existing CLI, changing config formats, implementing image thumbnails, background update daemons, or redesigning core mod-management behavior.

> **Status (2026-07-13): Phases 0–3 are COMPLETE**, shipped as **v1.4.0** on `main`
> (PRs #31, #33, #34, #36, promotion PR #38; issues #30, #32, #35 closed; tag `v1.4.0`).
> **Phase 4 (search and detail browsing) is next.** Before planning it, read:
>
> - the **"CLI-parity coverage and roadmap gaps"** section at the bottom of this file
>   (adds `uninstall` to Phase 5 and several items to Phase 6), and
> - **issue #37**, which tracks those additions plus the Phase 4 carry-forwards
>   (thread the program context into `loadData` once providers take a ctx,
>   single-fetch summary/mods load, optional cmd test-globals helper).
>
> The `feat/tui` integration branch is deleted — Phase 4+ branches from `main`.
> Execution record for Phases 2–3: `docs/plans/2026-07-13-tui-phase2-close-and-phase3.md`.

The TUI should feel like an 80s console RPG / DOS utility: Wizardry or Ultima in spirit, but still useful for managing real mod lists. Haunted terminal artifact, not hostile UX.

This plan is intentionally phased. Early phases optimise for fast visual iteration with fake data. Later phases wire the selected look into the existing `core.Service` boundary and add real actions.

---

## Guiding principles

1. **Prototype look-and-feel before wiring behavior.** Visual direction should be cheap to change.
2. **Keep the TUI thin.** Business logic stays in `internal/core`; TUI models adapt/present it.
3. **Theme, do not sprinkle styles.** Retro styling must live in shared theme primitives, not random `lipgloss.NewStyle()` calls in every view.
4. **Read-only before mutating.** List, status, search, and details come before install/update/deploy/profile switching.
5. **Mutations require explicit confirmation.** Anything that installs, updates, disables, deploys, deletes, or switches profile gets a confirmation screen.
6. **Keyboard-first.** Support arrows and Enter, but also `hjkl`, `/`, `?`, `q`, and direct-number navigation where practical.
7. **Small screens still matter.** The TUI should degrade gracefully around 80x24.
8. **No CLI regression.** Existing Cobra commands remain scriptable and tested.

---

## Proposed dependency stack

Add only when Phase 1 implementation begins:

```bash
go get github.com/charmbracelet/bubbletea github.com/charmbracelet/bubbles github.com/charmbracelet/lipgloss
```

Rationale:

- Bubble Tea fits Go and the existing CLI architecture.
- Lip Gloss makes theme iteration practical.
- Bubbles provides list/table/textinput/help components so we avoid hand-rolling every widget like a tiny ANSI gremlin.

---

## Target command shape

Initial prototype:

```bash
lmm tui --prototype
lmm tui --prototype --theme amber
lmm tui --prototype --theme wizardry
lmm tui --prototype --theme dos
```

Later real app:

```bash
lmm tui
lmm tui --theme wizardry
lmm tui --game skyrim-se
```

Possible alias after the command settles:

```bash
lmm ui
```

Keep `tui` as the canonical command unless there is a strong reason to prefer `ui`.

---

## Proposed file layout

```text
cmd/lmm/
├── tui.go                         # Cobra command and flags
└── tui_test.go

internal/tui/
├── app.go                         # Root Bubble Tea model, routing, update/view glue
├── app_test.go
├── keys.go                        # Shared key map
├── navigation.go                  # Screen IDs and routing helpers
├── navigation_test.go
├── service.go                     # Interfaces/adapters between TUI and core.Service
├── service_test.go
├── prototype/
│   ├── data.go                    # Static fake data used for visual iteration
│   └── data_test.go
├── theme/
│   ├── theme.go                   # Theme struct and shared styles
│   ├── presets.go                 # amber, wizardry, dos, green phosphor
│   └── theme_test.go
├── widgets/
│   ├── panel.go                   # Reusable titled panels / RPG boxes
│   ├── status.go                  # Badges, status labels, counters
│   └── help.go                    # Keybinding help footer
└── views/
    ├── dashboard.go
    ├── installed_mods.go
    ├── search.go
    ├── profiles.go
    ├── details.go
    └── confirm.go
```

Keep view files focused on rendering and local interaction. If a view starts doing install/update logic directly, it has wandered into the swamp.

---

## Visual directions to prototype

### 1. Amber Mainframe Dungeon

Monochrome amber/black, heavy borders, compact terminal-dungeon mood.

```text
╔══════════════════════ LMM ══════════════════════╗
║ GAME: Skyrim SE        PROFILE: survival         ║
╠══════════════════════════════════════════════════╣
║ > Installed Mods                                 ║
║   Search Archives                                ║
║   Conflicts                                      ║
║   Profiles                                       ║
║                                                  ║
║ STATUS: 42 mods installed / 3 updates / 1 cursed ║
╚══════════════════════════════════════════════════╝
```

### 2. Wizardry Party Sheet

Panels as character sheets: game, profile, mods, warnings, update status. This is likely the strongest identity for `lmm`.

```text
┌─ PARTY ───────────────┐ ┌─ QUEST LOG ───────────────┐
│ Skyrim Special Edition│ │ 3 updates available        │
│ Profile: Default      │ │ 1 file conflict            │
│ Mods: 42              │ │ Last deploy: 2h ago        │
└───────────────────────┘ └───────────────────────────┘

┌─ SPELLBOOK: MODS ───────────────────────────────────┐
│ [E] Enable  [D] Disable  [U] Update  [/] Search      │
│ > SkyUI                  installed       v5.2        │
│   USSEP                  update avail    v4.3        │
│   Immersive Armors       conflict        v8.1        │
└──────────────────────────────────────────────────────┘
```

### 3. DOS Utility / Norton Commander

Two-pane, practical, power-user layout with function-key affordances.

```text
┌─ Installed ──────────────────┐┌─ Details ───────────────┐
│ > SkyUI              v5.2    ││ Source: NexusMods        │
│   USSEP              v4.3    ││ Status: Update available │
│   SKSE Address Lib   v11     ││ Files: 14                │
│   Immersive Armors   v8.1    ││ Conflicts: 2             │
└──────────────────────────────┘└─────────────────────────┘
[F1 Help] [F2 Profile] [F3 Search] [F5 Deploy] [F10 Quit]
```

### 4. Green Phosphor Archive

Green-on-black CRT terminal, less ornate than Amber Mainframe, good fallback for users who want retro but clean.

```text
LMM ARCHIVE TERMINAL :: SKYRIM-SE :: DEFAULT

> STATUS
  Installed: 42
  Enabled:   39
  Updates:   3
  Conflicts: 1

[A] Archives  [M] Mods  [P] Profiles  [C] Conflicts  [?] Help
```

---

## Shared theme shape

Implement themes through a single struct similar to:

```go
type Theme struct {
    Name       string
    Background lipgloss.Color
    Foreground lipgloss.Color
    Accent     lipgloss.Color
    Muted      lipgloss.Color
    Warning    lipgloss.Color
    Danger     lipgloss.Color
    Success    lipgloss.Color

    App       lipgloss.Style
    Title     lipgloss.Style
    Panel     lipgloss.Style
    PanelTitle lipgloss.Style
    Selected  lipgloss.Style
    MutedText lipgloss.Style
    Help      lipgloss.Style
    Badge     lipgloss.Style
}
```

Theme selection should be testable without starting Bubble Tea:

```go
theme, err := theme.ByName("wizardry")
require.NoError(t, err)
require.Equal(t, "wizardry", theme.Name)
```

---

## Phase 0 — Discovery and issue setup ✅ (complete)

**Goal:** Start the TUI work with current repo context and a GitHub issue trail.

**Tasks:**

- Check for an existing open TUI-related issue.
- If none exists, create one for the visual prototype milestone.
- Confirm current `go test ./...` baseline.
- Re-read:
  - `CLAUDE.md`
  - `~/.claude/DEV.md`
  - `~/.claude/GO.md`
  - `docs/PRD.md`, especially the TUI section
- Decide the initial command name: default recommendation is `lmm tui`.

**Exit criteria:**

- Issue exists and is referenced by implementation commits.
- Baseline test result is known.
- No code changes yet except this plan if needed.

---

## Phase 1 — Visual prototype shell with fake data ✅ (complete, PR #31)

**Goal:** Build a prototype TUI that renders navigable screens with fake data and theme switching. No real service calls. No mutations.

**User-facing behavior:**

```bash
lmm tui --prototype
lmm tui --prototype --theme amber
lmm tui --prototype --theme wizardry
lmm tui --prototype --theme dos
```

**Tasks:**

1. Add Charmbracelet dependencies.
2. Add `cmd/lmm/tui.go` with a `tui` command and flags:
   - `--prototype`
   - `--theme`
3. Add `internal/tui/theme` with initial presets:
   - `amber`
   - `wizardry`
   - `dos`
   - `green`
4. Add fake prototype data:
   - configured game
   - active profile
   - installed mods
   - update count
   - conflict count
   - search results
5. Add root Bubble Tea model with screen routing.
6. Add four fake-data views:
   - Dashboard
   - Installed Mods
   - Search
   - Profiles
7. Add keyboard support:
   - `q` / `ctrl+c`: quit
   - arrows / `hjkl`: move selection
   - `tab` / `shift+tab`: cycle screens
   - `1`-`4`: jump screens
   - `?`: toggle help overlay
8. Add tests for:
   - theme lookup
   - invalid theme handling
   - screen navigation
   - key bindings that do not require a real terminal

**Exit criteria:**

- `go test ./...` passes.
- `lmm tui --prototype --theme amber` runs.
- `lmm tui --prototype --theme wizardry` runs.
- `lmm tui --prototype --theme dos` runs.
- No real DB/API/file-modifying operations occur in prototype mode.

---

## Phase 2 — Visual iteration and selection ✅ (complete, PRs #33/#34)

**Goal:** Compare the proposed looks and settle on the base visual language before real integration.

**Tasks:**

1. Capture screenshots or terminal recordings for each theme.
2. Review each theme against:
   - readability at 80x24
   - readability at larger terminal sizes
   - charm vs. annoyance ratio
   - ability to show dense mod tables
   - clarity of status/error/warning states
3. Adjust colors, borders, spacing, and language.
4. Pick one default theme.
5. Keep alternate themes available if they are cheap to maintain.
6. Document the decision in this plan or a short follow-up note.

**Selected default:** `wizardry`. It has the strongest RPG/tool identity for `lmm` and best matches the “Wizardry/Ultima in spirit, useful mod manager in practice” direction.

**Retained alternates:** `amber`, `dos`, and `green` stay available while they remain cheap to maintain. `amber` is explicitly kept for the VAX/VMS-era terminal nostalgia lane.

**Exit criteria:**

- A default theme is selected.
- Prototype screenshots/recordings exist in an agreed location, such as `docs/assets/tui/` if assets are worth committing.
- Theme names and basic style contracts are stable enough for real view work.

---

## Phase 3 — Read-only service-backed TUI ✅ (complete, PR #36, v1.4.0)

**Goal:** Replace fake data with real app data for safe, read-only screens.

**Tasks:**

1. Add a narrow TUI service interface in `internal/tui/service.go`.
2. Implement a real adapter over `*core.Service`.
3. Keep a fake adapter for tests and prototype/demo mode.
4. Wire `lmm tui` without `--prototype` to initialize config and service like existing CLI commands.
5. Load the user's configured default TUI theme when no `--theme` flag is provided, while still defaulting fresh installs to `wizardry`.
6. Add a config path for setting the default TUI theme, analogous to the existing default game workflow, so users can make `amber`, `dos`, `green`, or future themes their personal default.
7. Load real data for:
   - current/configured games
   - active/default profile
   - installed mods
   - profile list
   - status/update/conflict summaries where existing core methods support it
8. Preserve `--prototype` as a safe design/demo mode.
9. Add loading/error states for missing config, missing game, auth-required, and empty mod lists.

**Exit criteria:**

- `lmm tui` starts with real config/service initialization.
- A user-configured default TUI theme is honored when `--theme` is omitted; `--theme` remains an explicit per-run override.
- Dashboard and Installed Mods views show real local data.
- Search/Profile views either show real read-only data or honest placeholder states.
- `go test ./...` passes.
- Existing CLI behavior is unchanged.

---

## Phase 4 — Search and detail browsing

**Goal:** Make the TUI useful for browsing source results without installing anything yet.

**Tasks:**

1. Add search text input view using Bubbles `textinput`.
2. Route `/` to focus search where appropriate.
3. Execute search through existing source/core behavior.
4. Show result list with source, name, author, version, downloads/endorsements if available.
5. Add detail panel for selected result.
6. Handle auth-required errors with clear instructions.
7. Add cancellation behavior for in-flight searches when user exits or starts another search.

**Exit criteria:**

- User can search from the TUI.
- Result navigation and detail rendering work.
- Auth and network errors are displayed without crashing the TUI.
- `go test ./...` passes.

---

## Phase 5 — Safe mutating actions

**Goal:** Add install/update/enable/disable/profile actions behind explicit confirmations.

**Initial action set:**

- Enable/disable installed mod.
- Switch profile.
- Deploy current profile.
- Install selected search result.
- Check/apply updates.

**Tasks:**

1. Add shared confirmation view:
   - action summary
   - affected game/profile/mods
   - expected file impact when known
   - confirm/cancel keys
2. Add async command execution pattern for long-running operations.
3. Add progress/status messages for downloads, deploys, and updates.
4. Prevent duplicate actions while one is running.
5. Refresh affected views after successful action.
6. Surface failure details without losing the user's current screen.
7. Add tests for confirmation routing and cancellation.

**Exit criteria:**

- Mutating actions require confirmation.
- Cancelling leaves state unchanged.
- Successful actions refresh visible data.
- Errors are visible and recoverable.
- `go test ./...` passes.

---

## Phase 6 — Conflict/update/profile workflows

**Goal:** Move from basic actions to the workflows that make a TUI materially better than the CLI.

**Tasks:**

1. Add conflict view:
   - conflicting files
   - owning mods
   - load-order winner
   - suggested resolution hints
2. Add load-order/profile management affordances:
   - move mod up/down where supported
   - switch active profile
   - export/import profile entry points
3. Add update workflow:
   - list available updates
   - show changelog/details where available
   - apply selected/all eligible updates
   - respect per-mod update policies
4. Add help overlay that explains workflow-specific keys.

**Exit criteria:**

- TUI handles the common daily mod-management loop.
- Conflicts and updates are easier to inspect than via CLI tables.
- `go test ./...` passes.

---

## Phase 7 — Polish, docs, and release

**Goal:** Make the TUI shippable rather than merely neat in a demo.

**Tasks:**

1. Add docs:
   - README or docs page section for `lmm tui`
   - keybindings
   - theme options
   - prototype/demo mode if retained
2. Add man page updates for `lmm tui`.
3. Add CHANGELOG entries under `[Unreleased]`.
4. Verify small and large terminal sizes.
5. Verify `NO_COLOR` and `--no-color` behavior where applicable.
6. Run:
   - `go fmt ./...`
   - `go test ./... -v`
   - `go vet ./...`
   - `trunk check` if available/configured
7. Bump version according to `CLAUDE.md` when the feature is complete.

**Exit criteria:**

- Docs updated.
- Tests and vet pass.
- TUI is discoverable via help/man docs.
- Version and changelog updated for release.

---

## Testing strategy

### Unit tests

- Theme lookup and invalid theme errors.
- Navigation transitions.
- Key binding behavior.
- Fake service adapter behavior.
- Confirmation model confirm/cancel behavior.
- View-model data transformations.

### Integration-ish tests

Avoid requiring a real terminal. Test Bubble Tea model updates by sending messages/key events directly.

Examples:

```go
model, _ := tui.NewPrototypeApp(tui.Options{Theme: "wizardry"})
updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
require.Equal(t, tui.ScreenInstalledMods, updated.(tui.Model).CurrentScreen())
```

### Manual verification

For each milestone:

```bash
go test ./... -v
go vet ./...
go build -o lmm ./cmd/lmm
./lmm tui --prototype --theme wizardry
./lmm tui --prototype --theme amber
./lmm tui --prototype --theme dos
```

Later, with real local config:

```bash
./lmm tui --game skyrim-se
```

---

## Design review checklist

Use this after Phase 1/2 prototypes:

- Can the selected item be identified instantly?
- Are warnings/errors visible without being obnoxious?
- Is the app usable in 80x24?
- Does the retro language add character without hiding meaning?
- Are dense mod lists readable?
- Does the help overlay make keyboard behavior obvious?
- Can a user recover from an error without restarting?
- Are destructive/mutating actions clearly separated from browsing?

---

## Recommended first milestone issue

Title:

```text
Add prototype TUI with retro theme switching
```

Body:

```markdown
Build a safe visual prototype for the upcoming `lmm tui` interface.

Acceptance criteria:

- `lmm tui --prototype` starts Bubble Tea using fake data.
- `--theme amber`, `--theme wizardry`, `--theme dos`, and `--theme green` work.
- Prototype includes Dashboard, Installed Mods, Search, and Profiles screens.
- Keyboard navigation supports arrows, hjkl, tab, number keys, ?, q, and ctrl+c.
- Prototype performs no real installs, updates, deploys, DB writes, or API calls.
- Theme lookup and navigation are covered by tests.
- `go test ./...` passes.
```

---

## Open decisions

1. Should the canonical command be `lmm tui` or `lmm ui`?
   - Recommendation: `lmm tui`, optionally add `ui` alias later.
2. Should alternate themes be supported long-term?
   - Recommendation: keep them if they remain cheap; otherwise choose one default and prune.
3. Should prototype/demo mode remain in released builds?
   - Recommendation: yes, as `--prototype`, because it is useful for screenshots, QA, and visual regression-ish review.
4. Should terminal image protocols be supported for mod thumbnails?
   - Recommendation: defer. Nice trick, but not part of the first useful TUI.

---

## Versioning notes

Per `CLAUDE.md`, a full TUI is a new feature and should be a **MINOR** release when completed. Prototype-only work can live under `[Unreleased]` until the service-backed TUI is useful enough to ship.

Do the version bump and `CHANGELOG.md` update as a separate release commit at the end of the shippable TUI milestone, not after every visual-prototype tweak.

---

## CLI-parity coverage and roadmap gaps

**Added:** 2026-07-13, after the Phase 3 (read-only service-backed) milestone shipped as v1.4.0.

This section audits every CLI capability against the TUI and the phases above, so nothing is missing from the roadmap by omission. Items marked *planned* were already covered by a phase; items marked **added** were roadmap gaps discovered in this audit and are now assigned; items marked *CLI-only for now* are deliberate deferrals, not oversights.

### Covered by the shipped TUI (v1.4.0, read-only)

| CLI capability | TUI status |
| --- | --- |
| `status` (game/profile/mod counts) | Dashboard summary; update/conflict counts render `?` until Phase 6 |
| `list` (installed mods) | Installed Mods view (read-only) |
| `list --profiles` | Profile roster (read-only, active marker) |
| Game selection at launch | Works today via the global `-g/--game` flag (`lmm tui -g skyrim-se`) |

### Already planned (no change)

| CLI capability | Phase |
| --- | --- |
| `search`, `show` (details), auth-required messaging | Phase 4 |
| `install`, `enable`, `disable`, `deploy`, `profile switch`, `update` (check/apply) | Phase 5 |
| `conflicts`, `profile reorder` (load order), `profile export`/`import` entry points, update changelogs, respecting per-mod policies | Phase 6 |

### Roadmap gaps — **added** to phases by this audit

| Capability | Assigned | Rationale |
| --- | --- | --- |
| `uninstall <mod-id>` | **Phase 5** (add to the initial action set) | Same confirmation/async machinery as install; its omission from the P5 list was an oversight |
| `update rollback <mod-id>` | **Phase 6** (update workflow) | Belongs beside apply-updates; rollback is the safety net the update view should surface |
| `mod set-update` (notify/auto/pin) | **Phase 6** (update workflow) | P6 only *respects* policies; editing them from the update list is the natural affordance |
| In-TUI game selector/switcher | **Phase 6** | Original BACKLOG feature ("Game selector view") dropped when this plan was drafted; multi-game users otherwise must restart the TUI. Switching only — game add/detect/set-default stay CLI-only |
| `purge` | **Phase 6** | Destructive; requires the P5 confirmation view, and pairs with the deploy workflow |
| `files <mod-id>` (deployed file listing) | **Phase 6** (fold into conflict/detail panels) | The P6 conflict view already renders per-file data; a per-mod file panel reuses it |
| `profile create` / `profile delete` | **Phase 6** (profile management affordances) | P6 has switch/reorder/export/import; create/delete complete the management loop |

### Deliberately CLI-only for now (revisit post-Phase 6)

| Capability | Reason |
| --- | --- |
| `auth login`/`logout`/`status` | Login requires API-key entry and browser round-trips; Phase 4 shows clear "run `lmm auth login <source>`" instructions instead. Revisit if/when the NexusMods OAuth flow (currently deferred in TODO.local.md) lands |
| `import` (local archives / mod_path scan) | Interactive file-picking in a TUI is heavy; the CLI flow includes fuzzy matching prompts (#27) that don't map cleanly yet |
| `verify` (cache checksums) | Maintenance task with long runtime and rare use; no interactive benefit over the CLI |
| `mod edit` (metadata fixes) | Rare corrective surgery; form-style editing needs widgets none of the planned views require |
| `profile sync` / `profile apply` | Power-user reconciliation commands; semantics are easier to express (and audit) as explicit CLI invocations |
| `game add` / `game detect` / `game set-default` / `game clear-default` | Setup-time operations, usually once per machine; keep setup in the CLI, browsing/management in the TUI |
| Settings view; configurable keybindings (vim/standard) | Original BACKLOG wish-list items; post-v1 TUI polish once the workflows above exist. Theme is already configurable per the Phase 2 decision note |

Track execution of the **added** items in the corresponding phase issues as those phases begin; this table is the checklist to copy from.
