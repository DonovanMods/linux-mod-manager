package tui

import (
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
func TestHealthHomeViewEmptyState(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 24)
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
