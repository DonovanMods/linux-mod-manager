# TUI Phase 2 Close-out and Phase 3 (Service-Backed Read-Only TUI) Implementation Plan

> **EXECUTED — 2026-07-13.** All 11 tasks completed via subagent-driven development
> and shipped as **v1.4.0** (PRs #34, #36; promotion PR #38; issues #32, #35 closed;
> tag `v1.4.0` on `main`). A final whole-branch review added one fix commit
> (prototype-chrome removal) beyond the tasks below. Deviations from this plan as
> written: the TUI stacked on integration branch `feat/tui` (now deleted), not `main`;
> commit references use `Refs #35`; a CLI-parity gap audit was appended to
> `docs/plans/2026-04-28-tui-implementation.md` post-execution. Session ledger:
> `.superpowers/sdd/progress.md`. **Do not re-execute.** Next work: TUI Phase 4
> (see the 2026-04-28 plan's status block and issue #37).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the in-flight Phase 2 visual-iteration PR, harden the prototype TUI's internals, and wire `lmm tui` to real read-only app data through a narrow `DataProvider` boundary.

**Architecture:** The TUI stays thin: Bubble Tea `Model` in `internal/tui` renders view-model types (`Summary`, `ModItem`, `ProfileItem`) that it receives from a `DataProvider` interface. Two providers implement it — a static prototype provider (existing fake data) and a `CoreProvider` adapter over `*core.Service`. `cmd/lmm/tui.go` picks the provider based on `--prototype`, reusing the existing `withGameService` lifecycle helper.

**Tech Stack:** Go 1.25, Bubble Tea + Bubbles + Lip Gloss (already in go.mod), testify, Cobra, `modernc.org/sqlite` via `core.Service`.

## Global Constraints

- Go 1.25.6 (`go.mod`); use builtin `min`/`max`, no hand-rolled versions.
- No new dependencies — Charmbracelet stack, testify, Cobra are already present.
- TDD for every behavioral change: failing test → implement → pass (per `~/.claude/CLAUDE.md`). Trivial non-behavioral refactors (deleting a shadowed helper) need only a green test run.
- All code `gofmt`-formatted; `go vet ./...` clean; `go test ./...` green after every task.
- Commits are atomic, conventional-commit style, and reference GitHub issues (`Refs #NN`). Work is tracked via GitHub issues (project `CLAUDE.md`).
- Prototype mode must remain side-effect-free: no DB, API, file, or network access.
- TUI business logic stays in `internal/core`; `internal/tui` only adapts and presents.
- CHANGELOG entries go under `[Unreleased]` per task; the version bump (1.4.0) happens once at the end as its own commit.
- The TUI must stay usable at 80x24.

**Baseline facts (verified 2026-07-13):**

- PR **#31** (prototype shell) is merged. PR **#33** (`feat/tui-visual-iteration`, "stretch prototype panels to available space") is **open**; all 5 Copilot review comments were addressed by follow-up commits (`962be5fc`…`7f4df610`) and all review threads are outdated. `go test ./...` and `go vet ./...` pass on its head.
- Issue **#32** (TUI Phase 2) is open; PR #33 satisfies everything except "capture/review the theme layouts". Default theme (`wizardry`) is already documented in the plan doc by PR #33.
- The local checkout sits on stale branch `feat/tui-prototype` (already merged as #31); local `main` is behind `origin/main`.
- `internal/tui/app.go` on PR #33's head still has: raw-string key handling that ignores `KeyMap`, a hand-rolled `max`, ad-hoc `statusValue` styling, a hard-coded dashboard `itemCount` of 4, and duplicated menu labels across four layout functions.
- Missing planned test files: `cmd/lmm/tui_test.go`, `internal/tui/navigation_test.go`, `internal/tui/prototype/data_test.go`.

---

### Task 1: Sync workspace and merge PR #33

**Files:** none created/modified (git/GitHub operations only).

**Interfaces:**
- Consumes: open PR #33 on `feat/tui-visual-iteration`.
- Produces: `main` containing all Phase 2 layout work; local checkout on fresh `main`.

- [ ] **Step 1: Fetch and inspect the PR one last time**

```bash
git fetch origin
gh pr view 33
gh pr diff 33 --name-only
```

Expected files: `CHANGELOG.md`, `docs/plans/2026-04-28-tui-implementation.md`, `internal/tui/app.go`, `internal/tui/app_test.go`, `internal/tui/theme/theme.go`, `internal/tui/theme/theme_test.go`.

- [ ] **Step 2: Verify the PR head is green locally**

```bash
git worktree add /tmp/pr33-check origin/feat/tui-visual-iteration
cd /tmp/pr33-check && go test ./... && go vet ./...
cd - && git worktree remove /tmp/pr33-check
```

Expected: all packages `ok`, vet silent.

- [ ] **Step 3: Merge PR #33**

```bash
gh pr merge 33 --squash --delete-branch
```

(Repo history shows squash-style merges for feature PRs; if the merge button offers only merge-commit, use `--merge` to match PR #29/#31 style — check `gh pr view 31 --json mergeCommit` first if unsure.)

- [ ] **Step 4: Reset local workspace onto fresh main**

```bash
git checkout main
git pull
git branch -d feat/tui-prototype 2>/dev/null || git branch -D feat/tui-prototype
git log --oneline -3   # confirm the PR #33 squash commit is present
```

- [ ] **Step 5: Confirm baseline**

```bash
go test ./... && go vet ./...
```

Expected: green. Do NOT close issue #32 yet — the capture/review criterion lands in Task 2.

---

### Task 2: Phase 2 close-out — theme snapshot captures

Issue #32's remaining acceptance criterion is "Capture/review the existing `amber`, `wizardry`, `dos`, and `green` layouts." `Model.View()` is pure (no TTY needed), so snapshots can be generated deterministically by a regeneration-gated test and committed for review.

**Files:**
- Create: `internal/tui/snapshot_test.go`
- Create: `docs/assets/tui/` (generated `*.ansi` files, one per theme × size)
- Modify: `CHANGELOG.md` (under `[Unreleased]` → `Added`)

**Interfaces:**
- Consumes: `NewPrototypeModel(Options{Theme: string}) (Model, error)`, `Model.Update(tea.WindowSizeMsg)`, `Model.View() string` (all existing).
- Produces: committed snapshot files reviewable with `cat docs/assets/tui/<theme>-<WxH>.ansi`.

- [ ] **Step 1: Start the close-out branch**

```bash
git checkout -b chore/tui-phase2-closeout main
```

- [ ] **Step 2: Write the snapshot generator test**

Create `internal/tui/snapshot_test.go`:

```go
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// TestGenerateThemeSnapshots regenerates the committed theme captures under
// docs/assets/tui. It only runs when UPDATE_TUI_SNAPSHOTS=1 so normal test
// runs never write into the repo.
//
//	UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots
func TestGenerateThemeSnapshots(t *testing.T) {
	if os.Getenv("UPDATE_TUI_SNAPSHOTS") != "1" {
		t.Skip("set UPDATE_TUI_SNAPSHOTS=1 to regenerate theme snapshots")
	}

	outDir := filepath.Join("..", "..", "docs", "assets", "tui")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	sizes := []struct{ width, height int }{{80, 24}, {120, 36}}
	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		for _, size := range sizes {
			model, err := NewPrototypeModel(Options{Theme: themeName})
			require.NoError(t, err)

			// Run the init command if the model has one, so snapshots keep
			// capturing loaded data once async loading lands (Phase 3).
			if cmd := model.Init(); cmd != nil {
				loaded, _ := model.Update(cmd())
				model = loaded.(Model)
			}

			updated, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			view := updated.(Model).View()

			name := fmt.Sprintf("%s-%dx%d.ansi", themeName, size.width, size.height)
			require.NoError(t, os.WriteFile(filepath.Join(outDir, name), []byte(view+"\n"), 0o644))
		}
	}
}
```

- [ ] **Step 3: Run it in skip mode, then in generate mode**

```bash
go test ./internal/tui -run TestGenerateThemeSnapshots -v
```

Expected: `SKIP`.

```bash
UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots -v
ls docs/assets/tui
```

Expected: PASS; 8 files (`wizardry-80x24.ansi`, `wizardry-120x36.ansi`, `amber-…`, `dos-…`, `green-…`).

- [ ] **Step 4: Review the captures against the design checklist**

```bash
for f in docs/assets/tui/*-80x24.ansi; do echo "== $f =="; cat "$f"; done
```

Check (from the plan doc's design review checklist): selected item identifiable, warnings visible, readable at 80x24, dense mod list readable. If any layout is broken at 80x24, file it as a follow-up comment on issue #32 rather than expanding this task.

- [ ] **Step 5: Update CHANGELOG and commit**

Add under `## [Unreleased]` → `### Added`:

```markdown
- **TUI theme snapshots**: Committed ANSI captures of all four prototype themes at 80x24 and 120x36 under `docs/assets/tui/`, regenerable via `UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots`.
```

```bash
go test ./... && go vet ./...
git add internal/tui/snapshot_test.go docs/assets/tui CHANGELOG.md
git commit -m "test(tui): add regeneration-gated theme snapshot captures

Refs #32"
```

---

### Task 3: Route key handling through KeyMap

`internal/tui/keys.go` defines a `KeyMap` (used only by tests), while `Model.updateKey` matches raw strings — two sources of truth that can drift. Wire `updateKey` through `key.Matches` and give `Model` a `keys` field. Add explicit jump bindings for screens 1/2/4 (search already owns `/` and `3`).

**Files:**
- Modify: `internal/tui/keys.go`
- Modify: `internal/tui/app.go` (Model struct, `NewPrototypeModel`, `updateKey`)
- Test: `internal/tui/app_test.go`, `internal/tui/keys_test.go`

**Interfaces:**
- Consumes: `github.com/charmbracelet/bubbles/key` (`key.Binding`, `key.Matches`).
- Produces: `Model.keys KeyMap` field; `KeyMap` gains `Dashboard`, `InstalledMods`, `Profiles key.Binding`. Task 5 adds `Select` to this same struct.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go` (add `"github.com/charmbracelet/bubbles/key"` to imports):

```go
// TestUpdateKeyConsultsKeyMap proves key handling reads the KeyMap rather
// than hard-coded strings: rebinding NextScreen must change which key cycles.
func TestUpdateKeyConsultsKeyMap(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model.keys.NextScreen = key.NewBinding(key.WithKeys("n"))

	moved := updateWithRunes(t, model, "n")
	require.Equal(t, ScreenInstalledMods, moved.CurrentScreen())

	// The old default must no longer cycle once rebound away.
	stay := updateWithRunes(t, model, "l")
	require.Equal(t, ScreenDashboard, stay.CurrentScreen())
}

func TestNumberKeysJumpToScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	for keyPress, want := range map[string]Screen{
		"1": ScreenDashboard,
		"2": ScreenInstalledMods,
		"3": ScreenSearch,
		"4": ScreenProfiles,
	} {
		require.Equal(t, want, updateWithRunes(t, model, keyPress).CurrentScreen(), "key %q", keyPress)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./internal/tui -run 'TestUpdateKeyConsultsKeyMap|TestNumberKeysJumpToScreens' -v
```

Expected: compile FAIL — `model.keys undefined`.

- [ ] **Step 3: Implement**

In `internal/tui/keys.go`, add the jump bindings to `KeyMap` and `DefaultKeyMap`:

```go
type KeyMap struct {
	Quit          key.Binding
	Help          key.Binding
	NextScreen    key.Binding
	PrevScreen    key.Binding
	Up            key.Binding
	Down          key.Binding
	Search        key.Binding
	Dashboard     key.Binding
	InstalledMods key.Binding
	Profiles      key.Binding
}
```

In `DefaultKeyMap()`, append:

```go
		Dashboard: key.NewBinding(
			key.WithKeys("1"),
			key.WithHelp("1", "dashboard"),
		),
		InstalledMods: key.NewBinding(
			key.WithKeys("2"),
			key.WithHelp("2", "installed mods"),
		),
		Profiles: key.NewBinding(
			key.WithKeys("4"),
			key.WithHelp("4", "profiles"),
		),
```

In `internal/tui/app.go`: add `keys KeyMap` to `Model`; set `keys: DefaultKeyMap()` in `NewPrototypeModel`; replace `updateKey` (add `"github.com/charmbracelet/bubbles/key"` import):

```go
func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, m.keys.NextScreen):
		m.screen = screenAt((m.screenIndex() + 1) % len(screens))
		return m, nil
	case key.Matches(msg, m.keys.PrevScreen):
		m.screen = screenAt((m.screenIndex() - 1 + len(screens)) % len(screens))
		return m, nil
	case key.Matches(msg, m.keys.Dashboard):
		m.screen = ScreenDashboard
		return m, nil
	case key.Matches(msg, m.keys.InstalledMods):
		m.screen = ScreenInstalledMods
		return m, nil
	case key.Matches(msg, m.keys.Search):
		m.screen = ScreenSearch
		return m, nil
	case key.Matches(msg, m.keys.Profiles):
		m.screen = ScreenProfiles
		return m, nil
	case key.Matches(msg, m.keys.Up):
		m.moveSelection(-1)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.moveSelection(1)
		return m, nil
	default:
		return m, nil
	}
}
```

Behavior note: `Search` keeps keys `/` and `3` (already in `DefaultKeyMap`), so `3` still reaches the search screen exactly as before.

- [ ] **Step 4: Run the full package tests**

```bash
go test ./internal/tui/... -v
```

Expected: all PASS, including the pre-existing navigation tests (proving no regression).

- [ ] **Step 5: Commit**

```bash
go vet ./... && gofmt -l internal/tui
git add internal/tui/keys.go internal/tui/app.go internal/tui/app_test.go
git commit -m "refactor(tui): route key handling through KeyMap

updateKey now uses key.Matches against Model.keys instead of raw strings,
making KeyMap the single source of truth for bindings.

Refs #32"
```

---

### Task 4: Theme-owned status styles; delete hand-rolled max

`statusValue` in `app.go` builds an ad-hoc `lipgloss.NewStyle()` per call, violating the plan's "theme, do not sprinkle styles" principle — and it drops the theme background (the PR #33 follow-ups specifically fixed default-background bleed). Move the styles into `theme.Theme`. Also delete the hand-rolled `max` (Go 1.25 builtin).

**Files:**
- Modify: `internal/tui/theme/theme.go`
- Modify: `internal/tui/app.go`
- Test: `internal/tui/theme/theme_test.go`

**Interfaces:**
- Produces: `theme.Theme.WarningText`, `theme.Theme.DangerText lipgloss.Style` — bold, foregrounded with `Warning`/`Danger`, backgrounded with the theme background. Later tasks may use them for any warning/danger copy.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/theme/theme_test.go`:

```go
func TestStatusTextStylesMatchStatusColors(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"wizardry", "amber", "dos", "green"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			th, err := ByName(name)
			require.NoError(t, err)

			require.Equal(t, th.Warning, th.WarningText.GetForeground(), "WarningText foreground")
			require.Equal(t, th.Danger, th.DangerText.GetForeground(), "DangerText foreground")
			require.Equal(t, th.Background, th.WarningText.GetBackground(), "WarningText background")
			require.Equal(t, th.Background, th.DangerText.GetBackground(), "DangerText background")
			require.True(t, th.WarningText.GetBold())
			require.True(t, th.DangerText.GetBold())
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/tui/theme -run TestStatusTextStylesMatchStatusColors -v
```

Expected: compile FAIL — `th.WarningText undefined`.

Note: `wizardry` overrides `Warning` *after* `base()` returns, so a naive "set the style inside base()" implementation passes for amber/dos/green but fails for wizardry. The builder below exists precisely for that case.

- [ ] **Step 3: Implement**

In `internal/tui/theme/theme.go`, add fields to `Theme`:

```go
	WarningText lipgloss.Style
	DangerText  lipgloss.Style
```

Add a builder next to `withMuted` and use it everywhere status colors are set:

```go
// withStatusColors sets the status palette and the derived text styles
// together so they can never drift apart.
func (t Theme) withStatusColors(warning, danger, success lipgloss.Color) Theme {
	t.Warning = warning
	t.Danger = danger
	t.Success = success
	t.WarningText = lipgloss.NewStyle().Foreground(warning).Background(t.Background).Bold(true)
	t.DangerText = lipgloss.NewStyle().Foreground(danger).Background(t.Background).Bold(true)
	return t
}
```

In `base()`, replace the direct `Warning`/`Danger`/`Success` struct-field assignments with a final `return t.withMuted(muted).withStatusColors(warning, danger, success)` (keep the local color variables). In `Wizardry()`, replace:

```go
	t.Warning = lipgloss.Color("215")
	t.Success = lipgloss.Color("150")
```

with:

```go
	t = t.withStatusColors(lipgloss.Color("215"), t.Danger, lipgloss.Color("150"))
```

In `internal/tui/app.go`:
1. Delete `statusValue` and the `func max(a, b int) int { … }` at the bottom of the file (the Go builtin takes over — no call-site changes needed).
2. Replace the two `statusValue` call sites in `partyDashboardView`:

```go
		fmt.Sprintf("%s updates available", m.theme.WarningText.Render(fmt.Sprintf("%d", m.data.Stats.Updates))),
		fmt.Sprintf("%s file conflict", m.theme.DangerText.Render(fmt.Sprintf("%d", m.data.Stats.Conflicts))),
```

and the one in `terminalDashboardView`:

```go
		fmt.Sprintf("> ALERTS   %s UPDATES // %s CONFLICT", m.theme.WarningText.Render(fmt.Sprintf("%d", m.data.Stats.Updates)), m.theme.DangerText.Render(fmt.Sprintf("%d", m.data.Stats.Conflicts))),
```

(If Task 8 has already renamed `m.data.Stats` fields when you execute this out of order, use the summary fields instead — the render calls are what matter.)

- [ ] **Step 4: Run tests**

```bash
go test ./internal/tui/... -v && go vet ./...
```

Expected: PASS (including layout/height tests — the rendered rune widths are unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/theme/theme.go internal/tui/theme/theme_test.go internal/tui/app.go
git commit -m "refactor(tui): move status text styles into theme; drop hand-rolled max

Themes now own WarningText/DangerText styles (with correct backgrounds)
instead of app.go building ad-hoc lipgloss styles per render. The custom
max helper is deleted in favor of the Go builtin.

Refs #32"
```

---

### Task 5: Single-source dashboard menu with Enter activation

The four dashboard layouts each hard-code the same four menu entries, and `itemCount` separately hard-codes `4`. Pressing Enter on a menu item does nothing. Define the menu once (per layout flavor), derive the count from it, and make Enter navigate.

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/keys.go`
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `KeyMap` from Task 3.
- Produces: `KeyMap.Select key.Binding` ("enter"); unexported `menuItem{label string; target Screen; hasTarget bool}` and `Model.dashboardMenu() []menuItem`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/app_test.go`:

```go
func TestDashboardEnterOpensSelectedMenuEntry(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	// Initial selection is the first menu entry: Installed Mods.
	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, opened.(Model).CurrentScreen())

	// Second entry opens Search.
	moved := updateWithRunes(t, model, "j")
	opened, _ = moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenSearch, opened.(Model).CurrentScreen())
}

func TestDashboardEnterOnOracleEntryStaysPut(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	// Move to the last entry (Conflict Oracle) — no screen exists for it yet.
	for range 3 {
		model = updateWithRunes(t, model, "j")
	}
	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenDashboard, opened.(Model).CurrentScreen())
}

func TestEnterOutsideDashboardIsANoop(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model = updateWithRunes(t, model, "2")

	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, opened.(Model).CurrentScreen())
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./internal/tui -run 'TestDashboardEnter|TestEnterOutside' -v
```

Expected: FAIL — Enter currently falls through `updateKey`'s default case, so `CurrentScreen()` stays `ScreenDashboard` in the first test.

- [ ] **Step 3: Implement**

`internal/tui/keys.go` — add to `KeyMap`:

```go
	Select key.Binding
```

and to `DefaultKeyMap()`:

```go
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open"),
		),
```

`internal/tui/app.go` — add the menu model:

```go
// menuItem is one dashboard menu entry. hasTarget is false for flavor-only
// entries (like the Conflict Oracle) that have no screen yet.
type menuItem struct {
	label     string
	target    Screen
	hasTarget bool
}

func (m Model) dashboardMenu() []menuItem {
	if m.layout == LayoutMonochromeTerminal {
		return []menuItem{
			{label: "RUN SPELLBOOK SCAN", target: ScreenInstalledMods, hasTarget: true},
			{label: "QUERY ARCHIVE INDEX", target: ScreenSearch, hasTarget: true},
			{label: "LOAD PROFILE ROSTER", target: ScreenProfiles, hasTarget: true},
			{label: "ASK CONFLICT ORACLE"},
		}
	}
	return []menuItem{
		{label: "Installed Mods", target: ScreenInstalledMods, hasTarget: true},
		{label: "Search Archives", target: ScreenSearch, hasTarget: true},
		{label: "Profiles", target: ScreenProfiles, hasTarget: true},
		{label: "Consult Conflict Oracle"},
	}
}

func (m Model) dashboardMenuRows() []string {
	items := m.dashboardMenu()
	rows := make([]string, 0, len(items))
	for i, item := range items {
		rows = append(rows, m.row(i, item.label))
	}
	return rows
}

func (m Model) openSelectedMenuEntry() Model {
	if m.screen != ScreenDashboard {
		return m
	}
	items := m.dashboardMenu()
	selected := m.selected[ScreenDashboard]
	if selected >= len(items) || !items[selected].hasTarget {
		return m
	}
	m.screen = items[selected].target
	return m
}
```

Notes for the existing view functions:
- `partyDashboardView`, `commanderDashboardView`, `crtDashboardView`: replace their four `m.row(0, …)`…`m.row(3, …)` menu lines with `m.dashboardMenuRows()...` appended to the surrounding `[]string` (e.g. `append([]string{m.theme.PanelTitle.Render("COMMANDS")}, m.dashboardMenuRows()...)`). The commander layout's old label "Conflict Oracle" becomes "Consult Conflict Oracle" — acceptable copy unification; the layout tests compare layouts, not exact strings.
- `terminalDashboardView`: same replacement for its four `m.row` lines (it hits the `LayoutMonochromeTerminal` branch so labels are unchanged).
- `itemCount`: change the `default:` branch from `return 4` to `return len(m.dashboardMenu())`.
- `updateKey`: add a case (before `default`):

```go
	case key.Matches(msg, m.keys.Select):
		return m.openSelectedMenuEntry(), nil
```

- [ ] **Step 4: Run the full package tests**

```bash
go test ./internal/tui/... -v && go vet ./...
```

Expected: PASS, including the width/height layout tests.

- [ ] **Step 5: Regenerate snapshots (labels changed for dos layout) and commit**

```bash
UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots
git add internal/tui/app.go internal/tui/keys.go internal/tui/app_test.go docs/assets/tui
git commit -m "feat(tui): single-source dashboard menu with enter-to-open

The four layouts now share one menu definition, itemCount derives from it,
and Enter opens the selected entry's screen.

Refs #32"
```

---

### Task 6: Fill test gaps — cmd, navigation, prototype data

The implementation plan called for `cmd/lmm/tui_test.go`, `internal/tui/navigation_test.go`, and `internal/tui/prototype/data_test.go`; none exist. These are tests-only additions (no production code should need to change — if a test exposes a bug, stop and fix it test-first within this task).

**Files:**
- Create: `cmd/lmm/tui_test.go`
- Create: `internal/tui/navigation_test.go`
- Create: `internal/tui/prototype/data_test.go`

**Interfaces:**
- Consumes: `runTUI` (unexported, same package), `screenAt`, `Screen.String`, `prototype.Load`.

- [ ] **Step 1: Write the cmd tests**

Create `cmd/lmm/tui_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunTUIRejectsRealModeUntilImplemented(t *testing.T) {
	tuiOptions.prototype = false
	t.Cleanup(func() { tuiOptions.prototype = false })

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, "use --prototype")
}

func TestRunTUIRejectsUnknownTheme(t *testing.T) {
	tuiOptions.prototype = true
	tuiOptions.theme = "vaporwave"
	t.Cleanup(func() {
		tuiOptions.prototype = false
		tuiOptions.theme = "wizardry"
	})

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, `unknown TUI theme "vaporwave"`)
}
```

(These never start the Bubble Tea program: both paths error before `tea.NewProgram`. Task 10 rewrites the first test when real mode exists.)

- [ ] **Step 2: Write the navigation tests**

Create `internal/tui/navigation_test.go`:

```go
package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScreenAtClampsOutOfRangeIndexes(t *testing.T) {
	t.Parallel()

	require.Equal(t, ScreenDashboard, screenAt(-1))
	require.Equal(t, ScreenProfiles, screenAt(len(screens)))
	require.Equal(t, ScreenInstalledMods, screenAt(1))
}

func TestScreenStringNamesEveryScreen(t *testing.T) {
	t.Parallel()

	for _, s := range screens {
		require.NotContains(t, s.String(), "Screen(", "screen %d needs a display name", int(s))
	}
	require.Equal(t, "Screen(99)", Screen(99).String())
}
```

- [ ] **Step 3: Write the prototype data tests**

Create `internal/tui/prototype/data_test.go`:

```go
package prototype

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadProvidesConsistentDemoData(t *testing.T) {
	t.Parallel()

	data := Load()

	require.NotEmpty(t, data.InstalledMods)
	require.NotEmpty(t, data.SearchResults)
	require.NotEmpty(t, data.Profiles)

	active := 0
	for _, p := range data.Profiles {
		if p.Active {
			active++
			require.Equal(t, data.Profile.Name, p.Name, "active roster entry must match the current profile")
		}
	}
	require.Equal(t, 1, active, "exactly one profile should be active")

	require.Equal(t, data.Stats.Installed, data.Profile.ModCount, "dashboard stats should agree with the profile mod count")
}
```

- [ ] **Step 4: Run everything**

```bash
go test ./cmd/... ./internal/tui/... -v && go vet ./...
```

Expected: PASS. (If `TestLoadProvidesConsistentDemoData` fails on the invariants, fix `prototype.Load`'s data — not the test — so the demo data is self-consistent.)

- [ ] **Step 5: Commit and open the close-out PR**

```bash
git add cmd/lmm/tui_test.go internal/tui/navigation_test.go internal/tui/prototype/data_test.go
git commit -m "test(tui): cover cmd flags, navigation clamping, and prototype data

Refs #32"
git push -u origin chore/tui-phase2-closeout
gh pr create --title "chore(tui): Phase 2 close-out — snapshots, keymap wiring, menu single-sourcing" \
  --body "$(cat <<'EOF'
## Summary
- Committed ANSI captures of all four themes (80x24 and 120x36) with a regeneration-gated test — the last open acceptance criterion of #32.
- Key handling now flows through KeyMap (single source of truth); dashboard menu is defined once with Enter-to-open.
- Status text styles moved into the theme; hand-rolled max deleted.
- Added the test files the TUI plan called for: cmd/lmm/tui_test.go, navigation_test.go, prototype/data_test.go.

Closes #32

## Test Plan
- [x] go test ./...
- [x] go vet ./...
- [x] Manual: ./lmm tui --prototype --theme wizardry (navigate, enter on menu, ?, q)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

After review, merge and pull main (same flow as Task 1 steps 3–5). Issue #32 closes automatically.

---

### Task 7: Phase 3a — view models, DataProvider interface, prototype provider

Start of Phase 3 (read-only service-backed TUI). Create a GitHub issue first, then define the narrow boundary: view-model types the views render, a `DataProvider` interface, and the prototype implementation over the existing fake data.

**Files:**
- Create: `internal/tui/service.go`
- Test: `internal/tui/service_test.go`

**Interfaces:**
- Consumes: `prototype.Load() prototype.Data` (existing).
- Produces (used by Tasks 8–10):

```go
type Summary struct {
	GameName    string
	ProfileName string
	Installed   int
	Enabled     int
	Updates     int // -1 = unknown (no update check has run)
	Conflicts   int // -1 = unknown
}
type ModItem struct{ Name, Author, Version, Source, Status string }
type ProfileItem struct {
	Name     string
	Active   bool
	ModCount int
}
type DataProvider interface {
	Summary(ctx context.Context) (Summary, error)
	InstalledMods(ctx context.Context) ([]ModItem, error)
	SearchResults(ctx context.Context) ([]ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
}
func NewPrototypeProvider() DataProvider
```

- [ ] **Step 1: Create the Phase 3 issue and branch**

```bash
gh issue create --title "TUI Phase 3: read-only service-backed TUI" --body "$(cat <<'EOF'
Replace prototype fake data with real app data for safe, read-only screens, per docs/plans/2026-04-28-tui-implementation.md Phase 3 and docs/plans/2026-07-13-tui-phase2-close-and-phase3.md Tasks 7-11.

Acceptance criteria:
- A narrow DataProvider interface in internal/tui/service.go with prototype and core.Service-backed implementations.
- `lmm tui` (without --prototype) initializes config/service like existing CLI commands and shows real Dashboard, Installed Mods, and Profiles data.
- Search view shows an honest placeholder until Phase 4.
- Loading and error states render without crashing; --prototype remains side-effect-free.
- Existing CLI behavior unchanged; go test ./... and go vet ./... pass.
EOF
)"
git checkout -b feat/tui-service-backed main
```

Note the issue number printed by `gh issue create` — commits below use `Refs #<phase3-issue>`; substitute the real number.

- [ ] **Step 2: Write the failing tests**

Create `internal/tui/service_test.go`:

```go
package tui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

func TestPrototypeProviderMirrorsFakeData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()
	data := prototype.Load()

	summary, err := provider.Summary(ctx)
	require.NoError(t, err)
	require.Equal(t, data.Game.Name, summary.GameName)
	require.Equal(t, data.Profile.Name, summary.ProfileName)
	require.Equal(t, data.Stats.Installed, summary.Installed)
	require.Equal(t, data.Stats.Enabled, summary.Enabled)
	require.Equal(t, data.Stats.Updates, summary.Updates)
	require.Equal(t, data.Stats.Conflicts, summary.Conflicts)

	mods, err := provider.InstalledMods(ctx)
	require.NoError(t, err)
	require.Len(t, mods, len(data.InstalledMods))
	require.Equal(t, data.InstalledMods[0].Name, mods[0].Name)
	require.Equal(t, data.InstalledMods[0].Status, mods[0].Status)

	results, err := provider.SearchResults(ctx)
	require.NoError(t, err)
	require.Len(t, results, len(data.SearchResults))

	profiles, err := provider.Profiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, len(data.Profiles))
	require.Equal(t, data.Profiles[0].Name, profiles[0].Name)
	require.True(t, profiles[0].Active)
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/tui -run TestPrototypeProviderMirrorsFakeData -v
```

Expected: compile FAIL — `NewPrototypeProvider` undefined.

- [ ] **Step 4: Implement**

Create `internal/tui/service.go`:

```go
package tui

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// Summary is the dashboard header data.
type Summary struct {
	GameName    string
	ProfileName string
	Installed   int
	Enabled     int
	Updates     int // -1 = unknown (no update check has run)
	Conflicts   int // -1 = unknown
}

// ModItem is one renderable mod row.
type ModItem struct {
	Name    string
	Author  string
	Version string
	Source  string
	Status  string
}

// ProfileItem is one renderable profile row.
type ProfileItem struct {
	Name     string
	Active   bool
	ModCount int
}

// DataProvider is the narrow, read-only boundary between the TUI and app
// data. Implementations must be safe to call from a Bubble Tea command
// goroutine.
type DataProvider interface {
	Summary(ctx context.Context) (Summary, error)
	InstalledMods(ctx context.Context) ([]ModItem, error)
	SearchResults(ctx context.Context) ([]ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
}

// prototypeProvider serves the static demo data set. It must never touch
// disk, network, DB, or APIs.
type prototypeProvider struct {
	data prototype.Data
}

// NewPrototypeProvider returns the side-effect-free demo DataProvider used
// by --prototype mode and tests.
func NewPrototypeProvider() DataProvider {
	return prototypeProvider{data: prototype.Load()}
}

func (p prototypeProvider) Summary(_ context.Context) (Summary, error) {
	return Summary{
		GameName:    p.data.Game.Name,
		ProfileName: p.data.Profile.Name,
		Installed:   p.data.Stats.Installed,
		Enabled:     p.data.Stats.Enabled,
		Updates:     p.data.Stats.Updates,
		Conflicts:   p.data.Stats.Conflicts,
	}, nil
}

func (p prototypeProvider) InstalledMods(_ context.Context) ([]ModItem, error) {
	return modItems(p.data.InstalledMods), nil
}

func (p prototypeProvider) SearchResults(_ context.Context) ([]ModItem, error) {
	return modItems(p.data.SearchResults), nil
}

func (p prototypeProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	items := make([]ProfileItem, 0, len(p.data.Profiles))
	for _, profile := range p.data.Profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Active,
			ModCount: profile.ModCount,
		})
	}
	return items, nil
}

func modItems(mods []prototype.Mod) []ModItem {
	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		items = append(items, ModItem{
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.Source,
			Status:  mod.Status,
		})
	}
	return items
}
```

- [ ] **Step 5: Run tests and commit**

```bash
go test ./internal/tui/... -v && go vet ./...
git add internal/tui/service.go internal/tui/service_test.go
git commit -m "feat(tui): add DataProvider boundary with prototype implementation

Refs #<phase3-issue>"
```

---

### Task 8: Phase 3b — async load lifecycle in the Model

Make the `Model` load its data through `DataProvider` via a Bubble Tea command, with explicit loading / ready / failed states. The prototype provider stays synchronous, so prototype behavior is unchanged apart from one `Init` message cycle.

**Files:**
- Modify: `internal/tui/app.go` (Model fields, `NewPrototypeModel`, new `NewModel`, `Init`, `Update`, views)
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `DataProvider`, `Summary`, `ModItem`, `ProfileItem` (Task 7).
- Produces: `NewModel(options Options) (Model, error)` where `Options` gains `Provider DataProvider`; unexported `dataLoadedMsg`, `loadFailedMsg`; `Model.state` (`loadState` enum). `NewPrototypeModel` stays as a convenience wrapper (used by existing tests and `--prototype`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/app_test.go`:

```go
type failingProvider struct{ err error }

func (f failingProvider) Summary(context.Context) (Summary, error)        { return Summary{}, f.err }
func (f failingProvider) InstalledMods(context.Context) ([]ModItem, error) { return nil, f.err }
func (f failingProvider) SearchResults(context.Context) ([]ModItem, error) { return nil, f.err }
func (f failingProvider) Profiles(context.Context) ([]ProfileItem, error)  { return nil, f.err }

func TestModelShowsLoadingBeforeDataArrives(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider()})
	require.NoError(t, err)

	require.Contains(t, model.View(), "Consulting the archives")
}

func TestInitLoadsDataThroughProvider(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider()})
	require.NoError(t, err)

	msg := model.Init()()
	updated, _ := model.Update(msg)
	view := updated.(Model).View()

	require.Contains(t, view, "Skyrim Special Edition")
	require.NotContains(t, view, "Consulting the archives")
}

func TestLoadFailureRendersErrorState(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: failingProvider{err: errors.New("the archive door is sealed")}})
	require.NoError(t, err)

	msg := model.Init()()
	updated, _ := model.Update(msg)
	view := updated.(Model).View()

	require.Contains(t, view, "the archive door is sealed")
	require.Contains(t, view, "q: quit")
}

func TestNewModelRequiresProvider(t *testing.T) {
	t.Parallel()

	_, err := NewModel(Options{Theme: "wizardry"})
	require.ErrorContains(t, err, "provider")
}
```

Add `"context"` and `"errors"` to the test file imports.

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./internal/tui -run 'TestModelShowsLoading|TestInitLoadsData|TestLoadFailure|TestNewModelRequiresProvider' -v
```

Expected: compile FAIL — `NewModel` undefined.

- [ ] **Step 3: Implement**

In `internal/tui/app.go`:

1. `Options` becomes:

```go
// Options configures the TUI app.
type Options struct {
	Theme    string
	Provider DataProvider
}
```

2. Replace `data prototype.Data` in `Model` with loaded state (drop the `prototype` import from this file when nothing else uses it):

```go
type Model struct {
	theme    theme.Theme
	layout   Layout
	keys     KeyMap
	provider DataProvider

	state   loadState
	loadErr error

	summary       Summary
	mods          []ModItem
	searchResults []ModItem
	profiles      []ProfileItem

	screen   Screen
	selected map[Screen]int
	showHelp bool
	width    int
	height   int
}

type loadState int

const (
	stateLoading loadState = iota
	stateReady
	stateFailed
)
```

3. Constructors:

```go
// NewModel creates the TUI model backed by the given DataProvider.
func NewModel(options Options) (Model, error) {
	if options.Provider == nil {
		return Model{}, fmt.Errorf("TUI options: provider is required")
	}
	t, err := theme.ByName(options.Theme)
	if err != nil {
		return Model{}, err
	}

	return Model{
		theme:    t,
		layout:   layoutForTheme(t.Name),
		keys:     DefaultKeyMap(),
		provider: options.Provider,
		state:    stateLoading,
		screen:   ScreenDashboard,
		selected: map[Screen]int{
			ScreenDashboard:     0,
			ScreenInstalledMods: 0,
			ScreenSearch:        0,
			ScreenProfiles:      0,
		},
	}, nil
}

// NewPrototypeModel creates a side-effect-free TUI model backed by fake data.
func NewPrototypeModel(options Options) (Model, error) {
	options.Provider = NewPrototypeProvider()
	return NewModel(options)
}
```

4. Load command and messages:

```go
type dataLoadedMsg struct {
	summary       Summary
	mods          []ModItem
	searchResults []ModItem
	profiles      []ProfileItem
}

type loadFailedMsg struct{ err error }

func (m Model) Init() tea.Cmd {
	return m.loadData
}

func (m Model) loadData() tea.Msg {
	ctx := context.Background()

	summary, err := m.provider.Summary(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	mods, err := m.provider.InstalledMods(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	results, err := m.provider.SearchResults(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}
	profiles, err := m.provider.Profiles(ctx)
	if err != nil {
		return loadFailedMsg{err: err}
	}

	return dataLoadedMsg{summary: summary, mods: mods, searchResults: results, profiles: profiles}
}
```

(Add `"context"` to the imports. `context.Background()` is acceptable here for now: `tea.WithContext` in cmd already bounds the program's lifetime, and per-request cancellation arrives with Phase 4's in-flight searches.)

5. Handle the messages in `Update` (new cases before `tea.KeyMsg`):

```go
	case dataLoadedMsg:
		m.state = stateReady
		m.summary = msg.summary
		m.mods = msg.mods
		m.searchResults = msg.searchResults
		m.profiles = msg.profiles
		return m, nil
	case loadFailedMsg:
		m.state = stateFailed
		m.loadErr = msg.err
		return m, nil
```

6. Gate `screenView` on state:

```go
func (m Model) screenView() string {
	switch m.state {
	case stateLoading:
		return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
			Render(m.theme.PanelTitle.Render("Consulting the archives..."))
	case stateFailed:
		return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).
			Render(strings.Join([]string{
				m.theme.PanelTitle.Render("THE RITUAL FAILED"),
				m.theme.DangerText.Render(m.loadErr.Error()),
				"",
				m.theme.MutedText.Render("q: quit"),
			}, "\n"))
	}

	switch m.screen {
	// ... existing screen switch unchanged ...
	}
}
```

7. Mechanical data renames throughout the view functions: `m.data.Game.Name` → `m.summary.GameName`, `m.data.Profile.Name` → `m.summary.ProfileName`, `m.data.Stats.X` → `m.summary.X`, `m.data.InstalledMods` → `m.mods`, `m.data.SearchResults` → `m.searchResults`, `m.data.Profiles` → `m.profiles` (its element type becomes `ProfileItem` — field names `Name`/`Active`/`ModCount` are identical). `modRow`'s parameter type changes `prototype.Mod` → `ModItem`. Where the summary can be unknown (`Updates`/`Conflicts` == -1), render `?` instead of a number:

```go
func countLabel(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
}
```

and use `m.theme.WarningText.Render(countLabel(m.summary.Updates))` / `m.theme.DangerText.Render(countLabel(m.summary.Conflicts))` at the three status call sites from Task 4.

8. Update the existing test helper so all visual tests exercise the loaded state — in `app_test.go`, `sizedPrototypeModel` runs the init cycle:

```go
func sizedPrototypeModel(t *testing.T, themeName string, width, height int) Model {
	t.Helper()

	model, err := NewPrototypeModel(Options{Theme: themeName})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	updated, _ := loaded.Update(tea.WindowSizeMsg{Width: width, Height: height})
	updatedModel, ok := updated.(Model)
	require.True(t, ok)
	return updatedModel
}
```

Any other test that renders data (not just navigation) should be routed through this helper or given the same two-line load cycle. Tests passing `Options{…, Prototype: true}` lose that field — delete the field usage (the `Prototype` field is removed from `Options`; `--prototype` is now expressed by choosing the provider).

- [ ] **Step 4: Run the full test suite**

```bash
go test ./... && go vet ./...
```

Expected: PASS. Then a manual smoke test:

```bash
go build -o /tmp/lmm-dev ./cmd/lmm && /tmp/lmm-dev tui --prototype --theme wizardry
```

Expected: identical prototype behavior (data appears immediately; navigate, `?`, `q`).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go cmd/lmm/tui.go
git commit -m "feat(tui): async data loading with loading/error states

Model now loads through DataProvider via a Bubble Tea command and renders
explicit loading, ready, and failed states. Prototype mode is a provider
choice instead of an Options flag.

Refs #<phase3-issue>"
```

(`cmd/lmm/tui.go` needs its `tui.Options{…, Prototype: true}` call updated to `tui.NewPrototypeModel(tui.Options{Theme: tuiOptions.theme})` for the build to stay green — include it here.)

---

### Task 9: Phase 3c — CoreProvider adapter over core.Service

The real read-only adapter. `Updates`/`Conflicts` are reported as `-1` (rendered `?`) in Phase 3: an update check is a network operation and conflict detection (`Installer.GetConflicts`) is per-mod and filesystem-heavy — both belong to later phases; the TUI must stay honest rather than guess.

**Files:**
- Create: `internal/tui/service_core.go`
- Test: `internal/tui/service_core_test.go`

**Interfaces:**
- Consumes: `core.Service.GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error)`, `core.Service.NewProfileManager() *core.ProfileManager`, `ProfileManager.List(gameID string) ([]*domain.Profile, error)`, `domain.InstalledMod{Mod domain.Mod; Enabled, Deployed bool; …}`, `domain.Profile{Name string; Mods []domain.ModReference; IsDefault bool}`.
- Deliberate choice: use `GetInstalledMods` (DB order, shows everything), NOT `GetInstalledModsInProfileOrder` — the latter **omits mods missing from the profile YAML** (`internal/core/service.go:319-320`), and `lmm list` uses `GetInstalledMods` (`cmd/lmm/list.go:69`); the TUI must agree with the CLI. Profile load-order display arrives with the Phase 6 load-order workflows.
- Produces: `NewCoreProvider(svc *core.Service, game *domain.Game, profileName string) DataProvider` (used by Task 10).

- [ ] **Step 1: Write the failing test**

Create `internal/tui/service_core_test.go`:

```go
package tui_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

func newCoreProviderFixture(t *testing.T) (tui.DataProvider, *core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "101",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "SkyUI",
			Author:   "schlangster",
			Version:  "5.2",
		},
		ProfileName: "default",
		Enabled:     true,
		Deployed:    true,
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "102",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "USSEP",
			Author:   "Arthmoor",
			Version:  "4.3",
		},
		ProfileName: "default",
		Enabled:     false,
	}))

	return tui.NewCoreProvider(svc, game, "default"), svc, game
}

func TestCoreProviderSummary(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	summary, err := provider.Summary(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Test Game", summary.GameName)
	require.Equal(t, "default", summary.ProfileName)
	require.Equal(t, 2, summary.Installed)
	require.Equal(t, 1, summary.Enabled)
	require.Equal(t, -1, summary.Updates, "updates are unknown until an update check runs")
	require.Equal(t, -1, summary.Conflicts, "conflicts are unknown in the read-only phase")
}

func TestCoreProviderInstalledMods(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	mods, err := provider.InstalledMods(context.Background())
	require.NoError(t, err)
	require.Len(t, mods, 2)

	byName := map[string]tui.ModItem{}
	for _, m := range mods {
		byName[m.Name] = m
	}
	require.Equal(t, "deployed", byName["SkyUI"].Status)
	require.Equal(t, "nexusmods", byName["SkyUI"].Source)
	require.Equal(t, "5.2", byName["SkyUI"].Version)
	require.Equal(t, "disabled", byName["USSEP"].Status)
}

func TestCoreProviderSearchResultsAreEmptyUntilPhase4(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	results, err := provider.SearchResults(context.Background())
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestCoreProviderProfiles(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)

	_, err := svc.NewProfileManager().Create(game.ID, "hardcore")
	require.NoError(t, err)

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	byName := map[string]tui.ProfileItem{}
	for _, p := range profiles {
		byName[p.Name] = p
	}
	require.True(t, byName["default"].Active)
	require.False(t, byName["hardcore"].Active)
}
```

(Note the `tui_test` external test package: the fixture exercises only exported API, and it avoids an import cycle with `core` in-package.)

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/tui -run TestCoreProvider -v
```

Expected: compile FAIL — `tui.NewCoreProvider` undefined. (If the fixture itself errors — e.g. `pm.Create` already creates a default differently, or `AddGame` validates paths — adjust the fixture to the actual core API before writing the implementation; the assertions are the contract, the fixture is plumbing.)

- [ ] **Step 3: Implement**

Create `internal/tui/service_core.go`:

```go
package tui

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// coreProvider adapts *core.Service to the read-only DataProvider boundary.
type coreProvider struct {
	svc     *core.Service
	game    *domain.Game
	profile string
}

// NewCoreProvider returns a DataProvider backed by the real app service for
// one game/profile pair.
func NewCoreProvider(svc *core.Service, game *domain.Game, profileName string) DataProvider {
	return &coreProvider{svc: svc, game: game, profile: profileName}
}

func (p *coreProvider) Summary(_ context.Context) (Summary, error) {
	mods, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return Summary{}, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}

	enabled := 0
	for _, mod := range mods {
		if mod.Enabled {
			enabled++
		}
	}

	return Summary{
		GameName:    p.game.Name,
		ProfileName: p.profile,
		Installed:   len(mods),
		Enabled:     enabled,
		Updates:     -1, // unknown: update checks are a Phase 6 workflow
		Conflicts:   -1, // unknown: conflict detection is a Phase 6 workflow
	}, nil
}

func (p *coreProvider) InstalledMods(_ context.Context) ([]ModItem, error) {
	mods, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}

	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		items = append(items, ModItem{
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.SourceID,
			Status:  installedModStatus(mod),
		})
	}
	return items, nil
}

// SearchResults is an honest placeholder until Phase 4 wires real source
// search into the TUI.
func (p *coreProvider) SearchResults(_ context.Context) ([]ModItem, error) {
	return nil, nil
}

func (p *coreProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	profiles, err := p.svc.NewProfileManager().List(p.game.ID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles for %s: %w", p.game.ID, err)
	}

	items := make([]ProfileItem, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Name == p.profile,
			ModCount: len(profile.Mods),
		})
	}
	return items, nil
}

func installedModStatus(mod domain.InstalledMod) string {
	switch {
	case mod.Enabled && mod.Deployed:
		return "deployed"
	case mod.Enabled:
		return "enabled"
	default:
		return "disabled"
	}
}
```

Also give the search view an empty-state line so real mode is honest — in `app.go` `searchView`, after the query line:

```go
	if len(m.searchResults) == 0 {
		rows = append(rows, m.theme.MutedText.Render("The archive index opens in a later chapter. (Search arrives in Phase 4.)"))
	}
```

and the mods view an empty state after its header rows:

```go
	if len(m.mods) == 0 {
		rows = append(rows, m.theme.MutedText.Render("No mods installed yet. 'lmm install <mod>' begins the quest."))
	}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/tui/... -v && go vet ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/service_core.go internal/tui/service_core_test.go internal/tui/app.go
git commit -m "feat(tui): add core.Service-backed DataProvider

Read-only adapter mapping installed mods, profiles, and summary counts to
TUI view models. Updates/conflicts report unknown (-1 -> '?') until the
Phase 6 workflows land. Empty states added for mods and search views.

Refs #<phase3-issue>"
```

---

### Task 10: Phase 3d — wire `lmm tui` real mode

`lmm tui` without `--prototype` initializes config and service exactly like other CLI commands (via `withGameService`), resolves the default profile, and runs the model with `CoreProvider`. Missing game/config errors surface through the existing helper error paths before the TUI starts (matching CLI behavior); provider errors after startup render in the TUI's failed state.

**Files:**
- Modify: `cmd/lmm/tui.go`
- Test: `cmd/lmm/tui_test.go`

**Interfaces:**
- Consumes: `withGameService(cmd, fn)` (`cmd/lmm/helpers.go:35`), `core.Service.NewProfileManager().GetDefault(gameID string) (*domain.Profile, error)`, `tui.NewModel`, `tui.NewCoreProvider`, `tui.NewPrototypeProvider`.
- Produces: user-facing `lmm tui [--theme X]` real mode; `lmm tui --prototype` unchanged.

- [ ] **Step 1: Rewrite the failing cmd test**

In `cmd/lmm/tui_test.go`, replace `TestRunTUIRejectsRealModeUntilImplemented` with a test that real mode goes through the game-resolution path (no TTY needed — it fails before the program starts):

```go
func TestRunTUIRealModeRequiresAGame(t *testing.T) {
	tuiOptions.prototype = false
	tuiOptions.theme = "wizardry"
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""
	t.Cleanup(func() {
		configDir = ""
		dataDir = ""
		gameID = ""
	})

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, "no game specified")
}
```

(Match the variable names used by other `_NoGame` cmd tests — see `TestSearchCmd_NoGame` in `cmd/lmm/search_test.go` for the exact globals to set; reuse its setup pattern verbatim if it differs from the above.)

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./cmd/lmm -run TestRunTUIRealModeRequiresAGame -v
```

Expected: FAIL — current `runTUI` returns "real TUI mode is not implemented yet; use --prototype".

- [ ] **Step 3: Implement**

Rewrite `cmd/lmm/tui.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

var tuiOptions struct {
	prototype bool
	theme     string
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the interactive terminal UI",
	Long: `Launch the interactive terminal UI.

Shows the configured game's installed mods, profiles, and status using the
same config, database, and game resolution as the CLI commands. Read-only:
browsing never installs, updates, deploys, or deletes anything.

Use --prototype for a demo mode backed by static fake data:

  lmm tui --prototype --theme amber`,
	RunE: runTUI,
}

func init() {
	tuiCmd.Flags().BoolVar(&tuiOptions.prototype, "prototype", false, "run the side-effect-free fake-data TUI prototype")
	tuiCmd.Flags().StringVar(&tuiOptions.theme, "theme", "wizardry", "TUI theme (wizardry, amber, dos, green)")
	rootCmd.AddCommand(tuiCmd)
}

func runTUI(cmd *cobra.Command, args []string) error {
	if tuiOptions.prototype {
		model, err := tui.NewModel(tui.Options{Theme: tuiOptions.theme, Provider: tui.NewPrototypeProvider()})
		if err != nil {
			return err
		}
		return runTUIProgram(cmd.Context(), model)
	}

	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		// Match the CLI's behavior (profileOrDefault): fall back to the
		// literal "default" profile when none exists yet, so a fresh setup
		// opens an empty TUI instead of erroring.
		profileName := "default"
		if profile, err := svc.NewProfileManager().GetDefault(game.ID); err == nil {
			profileName = profile.Name
		} else if !errors.Is(err, domain.ErrProfileNotFound) {
			return fmt.Errorf("resolving default profile for %s: %w", game.ID, err)
		}

		model, err := tui.NewModel(tui.Options{
			Theme:    tuiOptions.theme,
			Provider: tui.NewCoreProvider(svc, game, profileName),
		})
		if err != nil {
			return err
		}
		return runTUIProgram(ctx, model)
	})
}

func runTUIProgram(ctx context.Context, model tui.Model) error {
	if _, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}
```

Note `tea.WithAltScreen()`: the app now fills the terminal and restores the shell on exit — appropriate once real data is shown. It applies to prototype mode too; if the snapshot review in Task 2 found reasons to keep inline rendering, drop the option and note why in the commit body.

- [ ] **Step 4: Run tests, then verify end-to-end**

```bash
go test ./... && go vet ./...
go build -o /tmp/lmm-dev ./cmd/lmm
/tmp/lmm-dev tui --prototype                 # demo mode still works
/tmp/lmm-dev tui                             # real mode with your local config
/tmp/lmm-dev tui --theme amber               # theme flag in real mode
```

Expected: real mode shows your actual configured game, installed mods, and profiles; `?` renders for updates/conflicts; navigation and `q` work; the shell restores cleanly. If no local game is configured, expect the CLI error `no game specified; use --game or -g flag…` before the TUI starts.

- [ ] **Step 5: Commit**

```bash
git add cmd/lmm/tui.go cmd/lmm/tui_test.go
git commit -m "feat(tui): wire lmm tui to real read-only app data

lmm tui without --prototype now initializes the service via
withGameService, resolves the default profile, and browses real installed
mods and profiles through CoreProvider. Prototype mode is unchanged.

Refs #<phase3-issue>"
```

---

### Task 11: Docs, changelog, version 1.4.0, and PR

**Files:**
- Modify: `README.md` (usage section — add `lmm tui`)
- Modify: `BACKLOG.md` (TUI section is stale: says "Code removed, to be reimplemented")
- Modify: `CHANGELOG.md`
- Modify: `cmd/lmm/root.go:25` (`version = "1.3.10"` → `"1.4.0"`)

**Interfaces:**
- Consumes: completed Tasks 7–10.
- Produces: shippable, documented `lmm tui`.

- [ ] **Step 1: Update README**

Add a `### Terminal UI` subsection to README's usage docs (place it alongside the other command sections):

```markdown
### Terminal UI

Browse your configured game, installed mods, and profiles interactively:

​```bash
lmm tui                     # real data, read-only
lmm tui --theme amber       # themes: wizardry (default), amber, dos, green
lmm tui --prototype         # demo mode with static fake data
​```

Keys: `tab`/`h`/`l` cycle screens, `1`–`4` jump, `↑↓`/`j`/`k` move, `enter` open,
`/` search screen, `?` help, `q` quit. Browsing is read-only — install/update/deploy
actions from the TUI arrive in a later release.
```

(Remove the zero-width characters around the inner code fence when pasting — they exist only to nest the fence in this plan.)

- [ ] **Step 2: Update BACKLOG.md**

Replace the stale "Terminal UI (TUI)" deferred section's **Status** line with:

```markdown
**Status:** Reimplemented. Prototype shell (v1.3.x, #31/#32) and read-only
service-backed TUI (v1.4.0) are done; search, mutations, and workflows are
tracked by the Phase 4-6 sections of docs/plans/2026-04-28-tui-implementation.md.
```

- [ ] **Step 3: Update CHANGELOG and bump version**

In `CHANGELOG.md`, ensure `[Unreleased]` contains the accumulated TUI entries, then move them to a new section:

```markdown
## [1.4.0] - <today's date>

### Added

- **`lmm tui` (read-only)**: The TUI now runs against real app data — Dashboard, Installed Mods, and Profiles views load the configured game, default profile, and installed mods through a narrow read-only provider. Search shows an honest placeholder until source search is wired in. `--prototype` remains as a side-effect-free demo mode.
- **TUI theme snapshots**: Committed ANSI captures of all four themes at 80x24 and 120x36 under `docs/assets/tui/`, regenerable with `UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots`.
- **Dashboard enter-to-open**: The dashboard menu is defined once, and Enter opens the selected entry's screen.

### Changed

- **TUI internals**: Key handling flows through the shared KeyMap; status text styles live in the theme; the TUI uses the terminal's alternate screen.
```

Add the comparison link at the bottom of the file following the existing pattern, and update `cmd/lmm/root.go` `version = "1.4.0"`.

- [ ] **Step 4: Final verification**

```bash
go fmt ./... && go vet ./... && go test ./... -v | tail -25
go build -o /tmp/lmm-dev ./cmd/lmm && /tmp/lmm-dev --version && /tmp/lmm-dev tui --help
```

Expected: everything green; `--version` prints 1.4.0; help shows the updated long description.

- [ ] **Step 5: Commit the version bump separately, push, open PR**

```bash
git add README.md BACKLOG.md CHANGELOG.md
git commit -m "docs: document lmm tui usage and refresh backlog status

Refs #<phase3-issue>"
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 1.4.0"
git push -u origin feat/tui-service-backed
gh pr create --title "feat(tui): read-only service-backed TUI (Phase 3)" \
  --body "$(cat <<'EOF'
## Summary
- Narrow read-only DataProvider boundary between the TUI and core.Service, with prototype and real implementations.
- `lmm tui` initializes config/service via withGameService, resolves the default profile, and browses real installed mods and profiles.
- Async load with explicit loading/error states; honest placeholders for search (Phase 4) and update/conflict counts (Phase 6).
- Docs, changelog, and version 1.4.0.

Closes #<phase3-issue>

## Test Plan
- [x] go test ./... / go vet ./... / go fmt ./...
- [x] Manual: lmm tui with a real configured game; lmm tui --prototype; all four themes; 80x24 terminal

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Follow-ups deliberately NOT in this plan

Each of these is its own plan when its phase begins (per the phased TUI plan doc):

- **Phase 4** — search input (`bubbles/textinput`), real source search with cancellation, detail panel, auth-required messaging.
- **Phase 5** — mutating actions behind confirmations (enable/disable, switch profile, deploy, install, update).
- **Phase 6** — conflict/update/load-order workflows (this is when `Summary.Updates`/`Conflicts` stop being `-1`).
- **Structural split** of `internal/tui/app.go` into `views/` + `widgets/` packages: deferred until Phase 4 adds stateful sub-models (text input, spinners) that justify the package boundary; splitting a ~500-line file of pure render funcs earlier adds churn without payoff.
- `TODO.local.md` "Schedule" items (encrypted tokens, download history, profile-level link_method, etc.) — unrelated to the TUI track.
