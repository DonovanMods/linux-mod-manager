# TUI Terminal Hardening Implementation Plan (#42, EPIC #104)

**Goal:** Every TUI screen renders within its height budget at any terminal size (clamp or scroll-window instead of silently overflowing), plus the test/snapshot/cosmetic polish items from issue #42.

**Architecture:** Two shared primitives in a new `internal/tui/clamp.go` — `clampLines` ("+N more" tail collapse, the `helpView`/`actionModalView` idiom extracted) and `windowedRows` (scroll-follow-selection windowing reusing `pickerWindow` from `picker.go:171`) — then applied per screen. Selectable lists get windowing (selection always visible); static content gets clamping.

**Tech Stack:** Go, Bubble Tea, lipgloss, testify `require`. Tests drive `Model.Update` directly (no real terminal).

## Global Constraints

- Branch: `fix/tui-terminal-hardening` off `main`; PRs only (protected main).
- TDD: every behavioral change starts with a failing test. Run `go test ./internal/tui/ -run <Name> -v` per step; full `go test ./...` before PR.
- `gofmt` (tabs); doc comments explain *why*, matching the package's dense comment style.
- Invariants that must keep holding: `TestScreenViewsUseExactAvailableHeightOnLargeTerminals` (app_test.go:339) — screens use *exactly* `availableContentHeight()` at large sizes; the new work adds "never *more* than" at small sizes.
- `availableContentHeight()`'s floor of 8 and `availableWidth()`'s floor of 40 stay. Below the floors the *terminal* still can't fit the app; the invariant being fixed is `screenView()` ≤ budget, not "fits any physically tiny terminal".
- Stale items — do NOT re-fix: `helpView` already clamps (Phase 6a, commit 72e056d); `m.search.cancel` on quit already resolved (Phase 5a `quitCmd`). These get noted on the issue in Task 9, not coded.
- `m.selected[screen]` is the absolute selection index (`app.go:147`); `m.row(i, …)` (app.go:1811) compares against absolute index — windowed rendering must pass absolute indices so highlighting keeps working.

---

### Task 1: Shared clamp/window helpers

**Files:**
- Create: `internal/tui/clamp.go`
- Test: `internal/tui/clamp_test.go`

**Interfaces:**
- Produces: `func (m Model) clampLines(lines []string, budget int) []string` — returns `lines` unchanged when `len(lines) <= budget`; otherwise first `budget-1` lines plus a muted `"+N more"` tail; never more than `budget` lines; `budget <= 0` → nil.
- Produces: `func (m Model) windowedRows(n, selected, budget int, render func(i int) string) []string` — scroll-follow-selection window over an n-row list; total returned lines never exceed `budget`; the row for `selected` is always among them (when `budget >= 1`); clipped edges become `"↑ N more"`/`"↓ N more"` muted indicator lines when `budget >= 3` (below that, indicators are dropped to honor the budget).

- [ ] **Step 1: Write the failing tests**

```go
package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// clampLines is the shared "+N more" tail-collapse primitive (the idiom
// helpView and actionModalView each hand-roll today). These tests pin the
// budget contract every screen view will rely on.
func TestClampLines(t *testing.T) {
	t.Parallel()
	m := sizedPrototypeModel(t, "wizardry", 80, 24)
	lines := []string{"a", "b", "c", "d", "e"}

	require.Equal(t, lines, m.clampLines(lines, 5), "exact fit is untouched")
	require.Equal(t, lines, m.clampLines(lines, 9), "surplus budget is untouched")
	require.Nil(t, m.clampLines(lines, 0), "zero budget renders nothing")

	got := m.clampLines(lines, 3)
	require.Len(t, got, 3)
	require.Equal(t, []string{"a", "b"}, got[:2])
	require.Contains(t, got[2], "+3 more")

	got = m.clampLines(lines, 1)
	require.Len(t, got, 1)
	require.Contains(t, got[0], "+5 more", "budget 1 is only the indicator")
}

// windowedRows must keep the selected row visible at any budget and never
// exceed the budget — including budgets below pickerWindow's practical
// floor of 3, where indicator rows would themselves cause overflow.
func TestWindowedRows(t *testing.T) {
	t.Parallel()
	m := sizedPrototypeModel(t, "wizardry", 80, 24)
	render := func(i int) string { return fmt.Sprintf("row-%d", i) }

	t.Run("all fit, no indicators", func(t *testing.T) {
		got := m.windowedRows(4, 2, 6, render)
		require.Equal(t, []string{"row-0", "row-1", "row-2", "row-3"}, got)
	})

	t.Run("selection past the fold stays visible", func(t *testing.T) {
		got := m.windowedRows(20, 19, 5, render)
		require.LessOrEqual(t, len(got), 5)
		require.Contains(t, strings.Join(got, "\n"), "row-19")
		require.Contains(t, got[0], "more", "clipped rows above are named")
	})

	t.Run("selection at the top", func(t *testing.T) {
		got := m.windowedRows(20, 0, 5, render)
		require.LessOrEqual(t, len(got), 5)
		require.Contains(t, got[0], "row-0")
		require.Contains(t, got[len(got)-1], "more", "clipped rows below are named")
	})

	t.Run("tiny budgets never overflow", func(t *testing.T) {
		for budget := 1; budget <= 2; budget++ {
			for selected := 0; selected < 6; selected++ {
				got := m.windowedRows(6, selected, budget, render)
				require.LessOrEqual(t, len(got), budget, "budget %d selected %d", budget, selected)
				require.Contains(t, strings.Join(got, "\n"), fmt.Sprintf("row-%d", selected))
			}
		}
	})

	t.Run("zero budget renders nothing", func(t *testing.T) {
		require.Nil(t, m.windowedRows(6, 3, 0, render))
	})
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestClampLines|TestWindowedRows' -v`
Expected: FAIL — `m.clampLines undefined` / `m.windowedRows undefined` (compile error).

- [ ] **Step 3: Implement `clamp.go`**

```go
package tui

import "fmt"

// clampLines returns lines unchanged when they fit budget; otherwise the
// first budget-1 lines plus a muted "+N more" tail, so the result can never
// exceed budget lines. This is the tail-collapse idiom helpView (app.go)
// and actionModalView (actions.go) established, extracted so every screen
// view can honor its height budget the same way: lipgloss's Height() pads
// short content but never clips tall content, so any view that renders more
// lines than its panel's budget silently grows past the terminal (#42).
func (m Model) clampLines(lines []string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	if len(lines) <= budget {
		return lines
	}
	keep := budget - 1
	clamped := make([]string, 0, budget)
	clamped = append(clamped, lines[:keep]...)
	return append(clamped, m.theme.MutedText.Render(fmt.Sprintf("+%d more", len(lines)-keep)))
}

// windowedRows renders an n-row selectable list clipped to budget total
// lines using pickerWindow's scroll-follow-selection windowing, so the
// selected row is always visible and highlighted no matter how far past
// the fold it is navigated — unlike a first-N slice, where the highlight
// simply vanishes off-screen (#42). render(i) receives the ABSOLUTE row
// index, so m.row(i, ...)'s selected-index comparison keeps working
// unchanged. Clipped edges are named with "↑/↓ N more" indicator lines,
// except at budgets below 3 where the indicators themselves would overflow
// the budget: there the window shrinks to exactly budget rows centered on
// the selection instead — still honest, just unlabeled.
func (m Model) windowedRows(n, selected, budget int, render func(i int) string) []string {
	if budget <= 0 || n <= 0 {
		return nil
	}
	if n <= budget {
		rows := make([]string, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, render(i))
		}
		return rows
	}
	if budget < 3 {
		windowSize := budget
		start := min(max(selected-(windowSize-1)/2, 0), n-windowSize)
		rows := make([]string, 0, windowSize)
		for i := start; i < start+windowSize; i++ {
			rows = append(rows, render(i))
		}
		return rows
	}
	start, windowSize := pickerWindow(n, selected, budget)
	rows := make([]string, 0, windowSize+2)
	if start > 0 {
		rows = append(rows, m.theme.MutedText.Render(fmt.Sprintf("↑ %d more", start)))
	}
	for i := start; i < start+windowSize; i++ {
		rows = append(rows, render(i))
	}
	if below := n - (start + windowSize); below > 0 {
		rows = append(rows, m.theme.MutedText.Render(fmt.Sprintf("↓ %d more", below)))
	}
	return rows
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/tui/ -run 'TestClampLines|TestWindowedRows' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/clamp.go internal/tui/clamp_test.go
git commit -m "feat(tui): shared clampLines/windowedRows height-budget helpers (#42)"
```

---

### Task 2: Dashboard layouts respect the height budget

**Files:**
- Modify: `internal/tui/app.go:1029-1113` (`partyDashboardView`, `terminalDashboardView`, `commanderDashboardView`, `crtDashboardView`)
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `m.clampLines` (Task 1).

**Known repro:** at 120×21 the budget is 14 but `partyDashboardView` renders 15 — the menu panel is asked for height 7 (content budget 5) but its content is 6 lines (title + 5 menu rows), and lipgloss pads-not-clips (`app.go:1035-1036` splits the height without checking content).

- [ ] **Step 1: Write the failing test** (in `app_test.go`, next to `TestDashboardLayoutsDoNotOverflowNarrowTerminals` at app_test.go:325)

```go
// Dashboards must never render more lines than availableContentHeight():
// lipgloss pads short panels but never clips tall ones, so any layout whose
// content exceeds its panel's height budget silently overflows the terminal
// (#42; reproduced at 120x21 where the party layout rendered 15 lines into
// a 14-line budget). Every theme layout is checked at each size because the
// four dashboard views split the budget differently.
func TestDashboardLayoutsFitHeightBudgetOnShortTerminals(t *testing.T) {
	t.Parallel()
	sizes := []struct{ width, height int }{{120, 21}, {80, 14}, {40, 12}, {40, 10}}
	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", themeName, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				model := sizedPrototypeModel(t, themeName, size.width, size.height)
				view := model.screenView()
				require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
			})
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestDashboardLayoutsFitHeightBudgetOnShortTerminals -v`
Expected: FAIL at least for `wizardry-120x21` (15 > 14). Record which theme/size combos fail.

- [ ] **Step 3: Clamp each layout's panel content to its panel's content budget**

`partyDashboardView` (app.go:1029): compute per-panel content budgets and clamp each content block. Replace the three `strings.Join(...)` builds:

```go
	topBudget := max(topHeight-m.theme.Panel.GetVerticalBorderSize(), 1)
	menuBudget := max(menuHeight-m.theme.Panel.GetVerticalBorderSize(), 1)

	party := strings.Join(m.clampLines([]string{
		m.theme.PanelTitle.Render("PARTY"),
		fmt.Sprintf("Game:    %s", m.summary.GameName),
		fmt.Sprintf("Profile: %s", m.summary.ProfileName),
		fmt.Sprintf("Mods:    %d installed / %d enabled", m.summary.Installed, m.summary.Enabled),
	}, topBudget), "\n")

	quest := strings.Join(m.clampLines([]string{
		m.theme.PanelTitle.Render("QUEST LOG"),
		fmt.Sprintf("%s updates available", m.theme.WarningText.Render(countLabel(m.summary.Updates))),
		fmt.Sprintf("%s file conflict", m.theme.DangerText.Render(countLabel(m.summary.Conflicts))),
		"Last deploy: ?",
	}, topBudget), "\n")

	menu := strings.Join(m.clampLines(
		append([]string{m.theme.PanelTitle.Render("COMMANDS")}, m.dashboardMenuRows()...),
		menuBudget), "\n")
```

`terminalDashboardView` (app.go:1063) and `crtDashboardView` (app.go:1102) — clamp `rows` before rendering:

```go
	rows = append(rows, m.dashboardMenuRows()...)
	budget := max(m.availableContentHeight()-m.theme.Panel.GetVerticalBorderSize(), 1)
	rows = m.clampLines(rows, budget)
	return m.panelWithHeight(m.availableWidth(), m.availableContentHeight()).Render(strings.Join(rows, "\n"))
```

`commanderDashboardView` (app.go:1076) — clamp both sides to `max(height-m.theme.Panel.GetVerticalBorderSize(), 1)`:

```go
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)
	left := strings.Join(m.clampLines([]string{
		m.theme.PanelTitle.Render("ACTIVE PROFILE"),
		m.summary.ProfileName,
		"",
		fmt.Sprintf("Game     %s", m.summary.GameName),
		fmt.Sprintf("Enabled  %d", m.summary.Enabled),
		fmt.Sprintf("Updates  %s", countLabel(m.summary.Updates)),
	}, contentBudget), "\n")
	right := strings.Join(m.clampLines(
		append([]string{m.theme.PanelTitle.Render("OPERATIONS")}, m.dashboardMenuRows()...),
		contentBudget), "\n")
```

Add a short doc-comment line to each view noting the clamp and referencing #42 (match the package's comment style).

- [ ] **Step 4: Run to verify pass, plus the guard invariants**

Run: `go test ./internal/tui/ -run 'TestDashboardLayoutsFitHeightBudgetOnShortTerminals|TestScreenViewsUseExactAvailableHeightOnLargeTerminals|TestDashboardLayoutsDoNotOverflowNarrowTerminals|TestViewFitsTerminalBoundsWithHelpVisible' -v`
Expected: all PASS (large-terminal exact-height must not regress — clampLines leaves fitting content untouched).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "fix(tui): clamp all four dashboard layouts to the height budget (#42)"
```

---

### Task 3: Installed Mods / Profiles / Sources lists get selection windowing

**Files:**
- Modify: `internal/tui/app.go:1115-1125` (`modsView`), `app.go:1401-1407` (`profilesView`), `app.go:1447-1471` (`sourcesView`)
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `m.windowedRows` (Task 1); `m.row(i, …)` with absolute indices.

- [ ] **Step 1: Write the failing tests**

```go
// Selectable list screens must (a) never exceed the height budget and
// (b) keep the selected row visible when navigation walks past the fold —
// previously rows were rendered unbounded, so on short terminals the
// highlight scrolled out of the panel while the detail/selection state kept
// updating invisibly (#42).
func TestListScreensFitHeightBudgetAndFollowSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		key    string // number key jumping to the screen
		screen Screen
	}{
		{"installed mods", "2", ScreenInstalledMods},
		{"profiles", "4", ScreenProfiles},
		{"sources", "5", ScreenSources},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := sizedPrototypeModel(t, "wizardry", 80, 12)
			model = updateWithRunes(t, model, tc.key)

			// Walk selection to the last row; the list is longer than the
			// short terminal's budget for at least installed mods.
			for i := 0; i < model.itemCount(tc.screen); i++ {
				model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
			}
			view := model.screenView()
			require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())

			// The selected (last) row must be rendered: its highlighted "> "
			// marker must appear somewhere in the view.
			require.Contains(t, view, "> ", "selection marker must stay visible after walking past the fold")
		})
	}
}
```

Note: `itemCount` is an existing unexported method (app.go:931). If the marker assertion proves too weak for a screen whose rows fit (profiles has few items), additionally assert `lipgloss.Height(view) <= model.availableContentHeight()` still holds at 80×10 — the budget check is the load-bearing half. For installed mods, prototype data has enough mods to overflow a 12-row terminal; verify with `require.Greater(t, model.itemCount(ScreenInstalledMods), ...)` guard if needed.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestListScreensFitHeightBudgetAndFollowSelection -v`
Expected: FAIL (height exceeds budget for installed mods at 80×12, and/or missing "> " marker).

- [ ] **Step 3: Implement windowing in the three views**

`modsView` (app.go:1115):

```go
func (m Model) modsView() string {
	width := m.availableWidth()
	height := m.availableContentHeight()
	contentBudget := max(height-m.theme.Panel.GetVerticalBorderSize(), 1)

	rows := []string{m.theme.PanelTitle.Render("SPELLBOOK: INSTALLED MODS"), "[/] Search"}
	if len(m.mods) == 0 {
		rows = append(rows, m.theme.MutedText.Render("No mods installed yet. 'lmm install <mod>' begins the quest."))
	}
	listBudget := max(contentBudget-len(rows), 0)
	rows = append(rows, m.windowedRows(len(m.mods), m.selected[ScreenInstalledMods], listBudget, func(i int) string {
		return m.modRow(i, width, m.mods[i])
	})...)
	return m.panelWithHeight(width, height).Render(strings.Join(rows, "\n"))
}
```

`profilesView` (app.go:1401): same shape — fixed rows = title only; `m.windowedRows(len(m.profiles), m.selected[ScreenProfiles], listBudget, func(i int) string { return m.profileRow(i, m.availableWidth(), m.profiles[i]) })`.

`sourcesView` (app.go:1447): fixed rows = title + header line; `m.windowedRows(len(m.sources), m.selected[ScreenSources], listBudget, func(i int) string { ... existing row build for m.sources[i] ... })` — move the existing per-source formatting into the render closure unchanged.

Update each view's doc comment (why: budget + follow-selection, ref #42).

- [ ] **Step 4: Run to verify pass + guards**

Run: `go test ./internal/tui/ -run 'TestListScreensFitHeightBudget|TestScreenViewsUseExactAvailableHeight|TestSourcesView|TestModRow|TestProfile' -v`
Expected: PASS, including existing sources/mods/profile row tests.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "fix(tui): window installed/profiles/sources lists to the height budget with follow-selection (#42)"
```

---

### Task 4: Search — results windowing, detail-pane clamp, zero-width values, Status styling

**Files:**
- Modify: `internal/tui/app.go:1275-1375` (`searchResultsPane`, `searchDetailPane`)
- Test: `internal/tui/search_test.go`

**Interfaces:**
- Consumes: `m.windowedRows`, `m.clampLines` (Task 1).

- [ ] **Step 1: Write the failing tests** (in `search_test.go`; build the populated model exactly the way `TestSearchReadyViewFitsNarrowTerminals` does at search_test.go:385-402 — jump to search with `"3"`, then set `model.search.state = searchReady` and `model.search.page` directly from prototype data)

```go
// Selection walking past the visible rows previously left NO highlighted
// row anywhere (the pane rendered results[:maxLines] while the selection
// index kept climbing) even though the detail pane tracked the invisible
// selection (#42). The pane now scroll-follows the selection.
func TestSearchResultsPaneFollowsSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 80, 12)
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = /* mirror search_test.go:398-402's populated SearchPage */

	last := len(model.search.page.Results) - 1
	for i := 0; i < last; i++ {
		model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, last, model.selected[ScreenSearch])

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
	require.Contains(t, view, "> ", "selected row must be visible")
	require.Contains(t, view, "↑", "clipped rows above must be named")
}

// The detail pane's 8 fixed field lines previously ignored maxLines
// entirely — on a floor-height terminal the pane's content budget can be
// as low as 3, so the fixed fields alone overflowed the pane (#42).
func TestSearchDetailPaneClampsFixedFieldsOnShortTerminals(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 80, 8) // floor height
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = /* same populated SearchPage */

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
}

// valueWidth's floor of 1 could exceed innerWidth in pathologically narrow
// panes (innerWidth 1 → label 1 + value 1 = 2 columns) (#42). Zero-width
// values must render nothing rather than overflow; every rendered line
// stays within the pane width.
func TestSearchDetailPaneAllowsZeroWidthValues(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 40, 24)
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = /* same populated SearchPage */

	view := model.screenView()
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), model.availableWidth())
	}
}

// The results list styles "installed" via WarningText but the detail
// pane's Status field was plain — the one place the status is spelled out
// was the one place it didn't pop (#42 cosmetic item).
func TestSearchDetailPaneStylesInstalledStatus(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = /* populated SearchPage whose selected result has Status "installed" */

	// WarningText produces ANSI styling; the plain-rendered field would
	// contain the literal unstyled value. Assert the styled form appears.
	view := model.screenView()
	require.Contains(t, view, model.theme.WarningText.Render("installed"))
}
```

(Adapt the `SearchPage` literal and any selection preconditions from the existing populated-state tests at search_test.go:385-430 — do not invent new fixture shapes.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestSearchResultsPaneFollowsSelection|TestSearchDetailPaneClamps|TestSearchDetailPaneAllowsZeroWidth|TestSearchDetailPaneStylesInstalled' -v`
Expected: FAIL — follows-selection (no marker/indicator), clamp (height over budget), styling (missing). Zero-width may already pass at 40 wide (floor keeps innerWidth comfortable) — if it passes, keep it as a regression guard and note it.

- [ ] **Step 3: Implement**

`searchResultsPane` (app.go:1275) — replace the first-N slice (lines 1296-1299) and loop with windowing over the FULL result set using absolute indices:

```go
	results := m.search.page.Results
	rowFor := func(i int) string {
		item := results[i]
		status := fmt.Sprintf("%-*s", statusWidth, truncate(item.Status, statusWidth))
		if item.Status == "installed" {
			status = m.theme.WarningText.Render(status)
		}
		var line string
		if withSource {
			line = fmt.Sprintf("%-*s %-*s %-*s %s",
				nameWidth, truncate(item.Name, nameWidth),
				versionWidth, truncate(item.Version, versionWidth),
				sourceWidth, truncate(item.Source, sourceWidth),
				status)
		} else {
			line = fmt.Sprintf("%-*s %-*s %s",
				nameWidth, truncate(item.Name, nameWidth),
				versionWidth, truncate(item.Version, versionWidth),
				status)
		}
		return m.row(i, line)
	}
	return strings.Join(m.windowedRows(len(results), m.selected[ScreenSearch], maxLines, rowFor), "\n")
```

`searchDetailPane` (app.go:1332):
- Change `valueWidth := max(innerWidth-labelWidth, 1)` → `max(innerWidth-labelWidth, 0)` (`truncate` already returns `""` for width ≤ 0, app.go:1924-1929).
- Style the Status field: `field("Status", item.Status)` → keep `field` for layout but wrap the value: build `statusValue := truncate(item.Status, valueWidth); if item.Status == "installed" { statusValue = m.theme.WarningText.Render(statusValue) }` and construct that line as `fmt.Sprintf("%-*s%s", labelWidth, truncate("Status", labelWidth), statusValue)`.
- Clamp the fixed lines before the summary block: after building `lines`, insert `lines = m.clampLines(lines, maxLines)` (the existing `summaryBudget := maxLines - len(lines) - 1` then naturally yields ≤ 0 when the fields filled the budget).
- Update the doc comment (fixed fields now clamped too, not just the summary).

- [ ] **Step 4: Run to verify pass + the search suite**

Run: `go test ./internal/tui/ -run 'TestSearch' -v`
Expected: all search tests PASS (including existing `TestSearchReadyViewUsesExactAvailableHeight`-style invariants at search_test.go:236,389,430).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/search_test.go
git commit -m "fix(tui): search results follow selection; detail pane honors budget, zero-width values, styled status (#42)"
```

---

### Task 5: Conflicts list windowing

**Files:**
- Modify: `internal/tui/app.go:1519-1559` (`conflictsListPane`)
- Test: `internal/tui/conflicts_view_test.go`

**Interfaces:**
- Consumes: `m.windowedRows` (Task 1).

- [ ] **Step 1: Write the failing test** (in `conflicts_view_test.go`, following its existing populated-model pattern)

```go
// conflictsListPane previously rendered items[:budget], so walking the
// selection below the fold hid the highlight while the detail pane kept
// tracking the invisible selection — same defect class as the search
// results pane (#42). The list now scroll-follows the selection.
func TestConflictsListFollowsSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 80, 12)
	model = updateWithRunes(t, model, "6")

	last := len(model.conflicts) - 1
	for i := 0; i < last; i++ {
		model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, last, model.selected[ScreenConflicts])

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
	require.Contains(t, view, "> ", "selected conflict must be visible")
}
```

If prototype data's conflict count doesn't exceed the 80×12 budget, use a taller list via the custom-provider pattern (`NewModel(Options{Provider: ...})`, see `longSourcesProvider` in sources_view_test.go) instead of shrinking the terminal below the floor.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestConflictsListFollowsSelection -v`
Expected: FAIL (marker invisible once selection passes the fold), or — if prototype data is too short — adjust per the note above until it exercises the fold, then FAIL.

- [ ] **Step 3: Implement** — replace the slice logic (app.go:1538-1545) and loop:

```go
	budget := max(maxLines-len(rows), 0)
	rowFor := func(i int) string {
		c := m.conflicts[i]
		marker := "  "
		if c.Stale {
			marker = m.theme.WarningText.Render("! ")
		}
		line := marker + fmt.Sprintf("%-*s %-*s %s",
			pathWidth, truncate(c.Path, pathWidth),
			ownerWidth, truncate(c.Owner, ownerWidth),
			truncate(c.Winner, winnerWidth))
		return m.row(i, line)
	}
	rows = append(rows, m.windowedRows(len(m.conflicts), m.selected[ScreenConflicts], budget, rowFor)...)
	return strings.Join(rows, "\n")
```

Update the doc comment ("rows beyond maxLines are omitted" → follow-selection windowing).

- [ ] **Step 4: Run to verify pass + conflicts suite**

Run: `go test ./internal/tui/ -run TestConflicts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/conflicts_view_test.go
git commit -m "fix(tui): conflicts list scroll-follows selection within the height budget (#42)"
```

---

### Task 6: Umbrella invariant — every screen fits its budget at short/narrow sizes

**Files:**
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: everything above; `screens` slice (navigation.go:17-24).

- [ ] **Step 1: Write the sweep test** (the short-size analogue of `TestScreenViewsUseExactAvailableHeightOnLargeTerminals`, app_test.go:339)

```go
// The short-terminal companion to
// TestScreenViewsUseExactAvailableHeightOnLargeTerminals: at ANY size,
// screenView() must render at most availableContentHeight() lines — the
// large-terminal test pins "exactly", this one pins "never more", which is
// the half lipgloss cannot enforce (it pads but never clips) (#42).
func TestScreenViewsFitHeightBudgetAtAllSizes(t *testing.T) {
	t.Parallel()
	sizes := []struct{ width, height int }{{120, 21}, {80, 14}, {80, 12}, {40, 12}, {40, 10}, {80, 8}}
	for _, size := range sizes {
		for i, screen := range screens {
			t.Run(fmt.Sprintf("%v-%dx%d", screen, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
				model = updateWithRunes(t, model, fmt.Sprintf("%d", i+1))
				if screen == ScreenSearch {
					model.search.state = searchReady
					model.search.page = /* populated SearchPage, same fixture as Task 4 */
				}
				view := model.screenView()
				require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
					"screen %v must not overflow at %dx%d", screen, size.width, size.height)
			})
		}
	}
}
```

- [ ] **Step 2: Run — expect PASS** (Tasks 2-5 fixed every screen). If any combination fails, that is a missed spot: fix it in `app.go` with the Task-1 helpers before proceeding, adding the failing case's specifics to the commit message.

Run: `go test ./internal/tui/ -run TestScreenViewsFitHeightBudgetAtAllSizes -v`

- [ ] **Step 3: Full package + vet**

Run: `go test ./internal/tui/ && go vet ./internal/tui/`
Expected: PASS, no vet findings.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app_test.go
git commit -m "test(tui): pin the fits-height-budget invariant for every screen at short sizes (#42)"
```

---

### Task 7: Restore full Summary assertions (independent — parallelizable)

**Files:**
- Modify: `internal/tui/service_test.go:13-27` (`TestPrototypeProviderOverviewMirrorsFakeData`)

- [ ] **Step 1: Add the missing assertions** (data source: `prototype.Load()` → `data.Profile.Name`, `data.Stats.Enabled/Updates/Conflicts`; mirror the existing `GameName`/`Installed` lines at service_test.go:21-26)

```go
	require.Equal(t, data.Profile.Name, summary.ProfileName)
	require.Equal(t, data.Stats.Enabled, summary.Enabled)
	require.Equal(t, data.Stats.Updates, summary.Updates)
	require.Equal(t, data.Stats.Conflicts, summary.Conflicts)
```

(Adopt the test's actual local variable names for `data`/`summary`.)

- [ ] **Step 2: Run to verify pass**

Run: `go test ./internal/tui/ -run TestPrototypeProviderOverviewMirrorsFakeData -v`
Expected: PASS (this is coverage restoration; if any assertion FAILS, the provider passthrough has a real bug — stop and report, do not adjust the expectation).

- [ ] **Step 3: Commit**

```bash
git add internal/tui/service_test.go
git commit -m "test(tui): restore full Summary field coverage in prototype provider test (#42)"
```

---

### Task 8: Snapshot harness covers every screen (independent — parallelizable)

**Files:**
- Modify: `internal/tui/snapshot_test.go:18-46`
- Create/regenerate: `docs/assets/tui/*.ansi`

**Current state:** `TestGenerateThemeSnapshots` (gated on `UPDATE_TUI_SNAPSHOTS=1`) writes dashboard-only goldens named `{theme}-{width}x{height}.ansi` for 4 themes × 80x24/120x36.

- [ ] **Step 1: Check for references to the existing filenames**

Run: `grep -rn "docs/assets/tui" --include="*.md" .` and `grep -rln "80x24.ansi\|120x36.ansi" .`
Expected per prior exploration: only `snapshot_test.go`. If docs reference specific filenames, keep those names valid (adjust the naming plan below accordingly and note it in the commit).

- [ ] **Step 2: Extend the generator.** New naming: `{theme}-{screen}-{width}x{height}.ansi` (e.g. `wizardry-dashboard-80x24.ansi`). Coverage: all 4 themes × dashboard (preserves the theme-comparison purpose) + `wizardry` × every other screen (`installed-mods`, `search`, `profiles`, `sources`, `conflicts`), each at 80x24 and 120x36. The search screen must be captured populated (search-ready): drive `"3"` then set `search.state = searchReady` / `search.page` exactly as the Task 4/6 tests do. Screens are reached by number key (`"1"`-`"6"`, index in `screens` + 1); slugs derive from the screen: use a `map[Screen]string{ScreenDashboard: "dashboard", ScreenInstalledMods: "installed-mods", ...}` local to the test. Delete the 8 old-name files in the same change (`git rm docs/assets/tui/{wizardry,amber,dos,green}-{80x24,120x36}.ansi`).

- [ ] **Step 3: Regenerate**

Run: `UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots -v`
Expected: PASS; `ls docs/assets/tui/` shows 8 dashboard files (4 themes × 2 sizes) + 10 wizardry screen files (5 screens × 2 sizes) = 18 files, all non-empty.

- [ ] **Step 4: Verify the harness stays skipped in normal runs**

Run: `go test ./internal/tui/ -run TestGenerateThemeSnapshots -v`
Expected: SKIP.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/snapshot_test.go docs/assets/tui/
git commit -m "test(tui): snapshot every screen (populated search included) across sizes (#42)"
```

---

### Task 9: Finalize — full verification, CHANGELOG, PR

**Files:**
- Modify: `CHANGELOG.md` (under `[Unreleased]`)
- Modify: issue #42 (comments), PR creation

- [ ] **Step 1: Full verification**

Run: `go fmt ./... && go vet ./... && go test ./... && trunk check --filter=go 2>/dev/null || trunk check`
Expected: no diffs from fmt, no vet findings, all tests pass, trunk clean (or note pre-existing findings untouched by this branch).

- [ ] **Step 2: CHANGELOG** — add under `[Unreleased]` → `### Fixed`:

```markdown
- TUI: all screens now render within the terminal height budget on short terminals — dashboards clamp overflowing panels with a "+N more" tail; installed-mods/profiles/sources/search-results/conflicts lists scroll-follow the selection with "↑/↓ N more" indicators instead of letting the highlight walk off-screen ([#42])
- TUI: search detail pane clamps its fixed fields to the pane budget, allows zero-width value columns in pathologically narrow panes, and styles an "installed" status to match the results list ([#42])
```

Add `[#42]: https://github.com/DonovanMods/linux-mod-manager/issues/42` to the link block if the file uses reference links (match existing style).

- [ ] **Step 3: Commit, push, PR**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entries for TUI terminal hardening (#42)"
git push -u origin fix/tui-terminal-hardening
gh pr create --title "fix(tui): terminal hardening — clamp/window every screen to its height budget (#42)" --body "<summary of the two defect classes fixed, test additions, snapshot extension; Closes #42; Part of EPIC #104>"
```

- [ ] **Step 4: Update issue #42** with the two stale-item corrections (comment): `helpView` static-35-lines was fixed in Phase 6a (72e056d) — not re-fixed here; `m.search.cancel` lifecycle bullet resolved by Phase 5a `quitCmd` (already noted in the issue's own comment).

- [ ] **Step 5: Copilot review triage** per repo convention (`gh-await-review` via background Bash; rounds until clean), TUI smoke-test gate: request the user run a quick smoke test before merge (repo convention for TUI changes).

---

## Execution notes

- Tasks 1→6 are sequential (all touch `app.go` or depend on helpers). Tasks 7 and 8 are independent files and can run in parallel with any of 2-6 (7/8 touch only `service_test.go` / `snapshot_test.go` + assets).
- Version bump is NOT in this plan: per EPIC #104, the release bump lands once at the end of the epic (or per-merge if the repo's convention of bump-per-PR is preferred — decide at PR time; recent history shows per-PR PATCH/MINOR bumps, so a PATCH bump `1.17.1` in Task 9 is the safe default if CI/convention expects it).
