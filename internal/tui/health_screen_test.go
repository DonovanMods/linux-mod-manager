package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// TestHealthScreenNavigation proves ScreenHealth is a fully wired 7th
// screen (task-9-brief.md): the "7" direct-jump binding reaches it, tab
// cycling from Conflicts (the previous last screen) rotates onto it and
// then wraps back to Dashboard, and its String() renders a real name
// rather than navigation.go's Screen(N) fallback.
func TestHealthScreenNavigation(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithRunes(t, model, "7")
	require.Equal(t, ScreenHealth, updated.CurrentScreen())
	require.Equal(t, "Health", ScreenHealth.String())

	onConflicts := updateWithRunes(t, model, "6")
	require.Equal(t, ScreenConflicts, onConflicts.CurrentScreen())
	onHealth := updateWithKeyType(t, onConflicts, tea.KeyTab)
	require.Equal(t, ScreenHealth, onHealth.CurrentScreen())
	onDashboard := updateWithKeyType(t, onHealth, tea.KeyTab)
	require.Equal(t, ScreenDashboard, onDashboard.CurrentScreen())
}

// TestHealthHomeViewRendersFindingsAndHeaderAge covers the non-empty path:
// a "missing" finding and a lock-pending "ok" finding. The header must show
// the relative scan age via lastDeployLabel's own computation, the list
// must show both rows, and the detail pane for whichever row is selected
// must show that status's remedy copy.
func TestHealthHomeViewRendersFindingsAndHeaderAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	scannedAt := now.Add(-3 * time.Minute)

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.now = func() time.Time { return now }
	model.healthAt = &scannedAt
	model.health = HealthView{
		Findings: []HealthFinding{
			{ModID: "101", ModName: "SkyUI", FileID: "main", Status: "missing"},
			{ModID: "bear-mount", ModName: "Bear Mount", Status: "ok", Note: "lock pending convergence at v2.0"},
		},
	}
	model.screen = ScreenHealth

	view := model.View()
	require.Contains(t, view, "last scan: local, 3m ago")
	require.Contains(t, view, "SkyUI")
	require.Contains(t, view, "MISSING")
	require.Contains(t, view, "Bear Mount")

	// Row 0 (missing) is selected by default.
	require.Contains(t, view, "run a fix (F) to redownload")

	model.selected[ScreenHealth] = 1
	view = model.View()
	require.Contains(t, view, "lock pending convergence at v2.0 — run 'lmm profile apply'")
}

// TestHealthHomeViewEmptyState covers a fresh session that hasn't scanned
// yet: healthAt is nil (its zero value) and m.health carries no findings.
// Deliberately skips sizedPrototypeModel's Init()/loadData round trip (#224
// Task 10 wired DataProvider.Health into loadData, and the prototype
// provider's own Health() returns a canned non-empty view - see
// prototypeHealthFindings - so running a real load here would defeat the
// "hasn't scanned yet" premise); sizing the model directly via
// WindowSizeMsg leaves m.health/m.healthAt/m.healthErr at their zero
// values instead.
func TestHealthHomeViewEmptyState(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	model = sized.(Model)
	// Skips Init()/loadData (see doc comment above), so state must be
	// stamped ready by hand - otherwise View() renders the "Consulting the
	// archives..." loading screen instead of ScreenHealth's content.
	model.state = stateReady
	model.screen = ScreenHealth

	view := model.View()
	require.Contains(t, view, "no scan yet")
	require.Contains(t, view, "no findings (local) — run a full check (c)")
}

// fakeContextContent is a test-local contextContent implementation - the
// SPEC-CRITERION-4 proof that ScreenHealth's host demonstrably supports a
// second content view without building a real one (#86's mod-details view
// will be the first real second implementation).
type fakeContextContent struct {
	title   string
	lines   []string
	presses int
}

func (f *fakeContextContent) Title() string { return f.title }

func (f *fakeContextContent) Lines(_, _ int) []string { return f.lines }

func (f *fakeContextContent) HandleKey(msg tea.KeyMsg) (contextContent, tea.Cmd, bool) {
	if msg.String() == "x" {
		f.presses++
		return f, nil, true
	}
	return f, nil, false
}

func (f *fakeContextContent) HelpGroup() helpGroup {
	return helpGroup{name: "fake", entries: []string{"x", "press"}}
}

// TestHealthContextHostPushRenderKeyAndEscPop is the SPEC-CRITERION-4 proof:
// pushing a fake content onto ScreenHealth renders its title and lines, its
// HandleKey consumes a key (and the returned "next" is what the host keeps
// rendering), and esc - which the fake declines - pops back to the screen
// that pushed it.
func TestHealthContextHostPushRenderKeyAndEscPop(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 24)
	model.screen = ScreenConflicts

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line one", "fake line two"}}
	model.pushContext(fake, ScreenConflicts)

	require.Equal(t, ScreenHealth, model.CurrentScreen(), "pushContext must jump to ScreenHealth")
	view := model.screenView()
	require.Contains(t, view, "FAKE DETAIL")
	require.Contains(t, view, "fake line one")
	require.Contains(t, view, "fake line two")

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, ScreenHealth, model.CurrentScreen(), "a consumed key must not leave ScreenHealth")
	require.Equal(t, 1, fake.presses, "HandleKey must have consumed the key")

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Equal(t, ScreenConflicts, model.CurrentScreen(), "esc must pop back to the pushing screen")
	require.Nil(t, model.contextContent, "popContext must clear the pushed content")
}

// TestHealthContextHostPromotesPushedContentHelpGroup is fix round 1's
// Finding 2 regression test: while content is pushed onto ScreenHealth, the
// help panel's promoted group (immediately after "global") must be the
// pushed content's own HelpGroup(), not the ambient static "health" group's
// unrelated 7/c/F bindings - otherwise HelpGroup() would be an orphaned
// interface member. Uses a zero-size model (like
// TestHelpViewCurrentScreenGroupFirst) so helpBodyBudget's generous unsized
// default (50 lines) has the most room to work with; even so, the full
// grouped list (now with the pushed "fake" group added on top) still runs
// past that budget and tail-collapses into "+N more" before reaching
// "health" itself, so this compares against "dashboard" (the group that
// would normally be first after global, and one of the fixed groups still
// guaranteed to survive the cap) rather than "health" directly.
func TestHealthContextHostPromotesPushedContentHelpGroup(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.screen = ScreenConflicts

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake, ScreenConflicts)
	model.showHelp = true

	view := model.helpView()
	fakeIdx := strings.Index(view, "fake")
	globalIdx := strings.Index(view, "global")
	dashboardIdx := strings.Index(view, "dashboard")
	require.NotEqual(t, -1, fakeIdx, "pushed content's HelpGroup title must appear")
	require.Less(t, globalIdx, fakeIdx, "global must still render first")
	require.Less(t, fakeIdx, dashboardIdx, "pushed content's group must be promoted ahead of the ordinary fixed groups")
}

// TestGotoScreenAwayFromHealthClearsPushedContext is fix round 1's Finding
// 1 regression test: a global nav key the pushed content DECLINES (a digit
// jump, here "2") must not strand contextContent set while the session
// sits on a different screen - gotoScreen (app.go) must clear it, or
// returning to Health later would re-render the stale pushed content
// instead of the home view.
func TestGotoScreenAwayFromHealthClearsPushedContext(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 24)
	model.screen = ScreenConflicts

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake, ScreenConflicts)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	// "2" is declined by the fake (only "x" is handled=true), so it falls
	// through to the outer switch's InstalledMods jump.
	updated, _ := model.Update(keyRunes("2"))
	model = updated.(Model)
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen(), "the declined nav key must still navigate")
	require.Nil(t, model.contextContent, "navigating away from Health must clear the stranded pushed content")

	// Returning to Health must render the home view, not the stale fake.
	updated, _ = model.Update(keyRunes("7"))
	model = updated.(Model)
	require.Equal(t, ScreenHealth, model.CurrentScreen())
	view := model.screenView()
	require.NotContains(t, view, "FAKE DETAIL", "the stale pushed content must not resurface")
	require.NotContains(t, view, "fake line")
}

// TestPopContextNoopWhenNothingPushed proves popContext is safe to call (or
// reach via esc) with nothing pushed: the screen stays put and no panic
// occurs.
func TestPopContextNoopWhenNothingPushed(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.screen = ScreenHealth
	require.Nil(t, model.contextContent, "sanity: nothing pushed yet")

	target := model.popContext()
	require.Equal(t, ScreenHealth, target)
	require.Equal(t, ScreenHealth, model.CurrentScreen())
	require.Nil(t, model.contextContent)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Equal(t, ScreenHealth, model.CurrentScreen(), "esc with nothing pushed must not move the screen")
}
