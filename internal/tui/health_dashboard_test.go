package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// healthViewProvider embeds stubProvider and overrides only Health - the
// dashboard-signal fetch Task 10 wires into loadData. Overview/Profiles/
// Conflicts fall back to stubProvider's zero-value defaults, which is fine:
// these tests only care about the Health-shaped fields loadData/dataLoadedMsg
// produce.
type healthViewProvider struct {
	stubProvider
	view HealthView
	err  error
}

func (p healthViewProvider) Health(context.Context) (HealthView, error) {
	return p.view, p.err
}

// TestLoadDataPopulatesHealthViewAndSummaryCounts covers the success path:
// loadData's provider.Health call (dispatched after Conflicts, per the
// brief) lands in dataLoadedMsg, and Update stores the view on the Model,
// stamps healthAt via m.now(), copies Issues/Warnings onto Summary, and
// leaves healthErr empty.
func TestLoadDataPopulatesHealthViewAndSummaryCounts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	view := HealthView{
		Findings: []HealthFinding{{ModID: "101", ModName: "SkyUI", Status: "missing"}},
		Issues:   1,
		Warnings: 2,
	}
	model, err := NewModel(Options{Theme: "wizardry", Provider: healthViewProvider{view: view}})
	require.NoError(t, err)
	model.now = func() time.Time { return now }

	msg := model.Init()()
	updated, _ := model.Update(msg)
	m := updated.(Model)

	require.Equal(t, view, m.health, "the Health view must be stored on the Model")
	require.NotNil(t, m.healthAt)
	require.Equal(t, now, *m.healthAt, "healthAt must be stamped via m.now() at receipt")
	require.Equal(t, "", m.healthErr)
	require.Equal(t, 1, m.summary.HealthIssues)
	require.Equal(t, 2, m.summary.HealthWarnings)
}

// TestLoadDataHealthErrorKeepsSentinelsAndSurfacesStatusLine covers the
// error path: unlike Conflicts (which fails the WHOLE load via the
// early-return pattern), a failed Health scan must not fail loadData at
// all - the load still completes (state stays/reaches stateReady), the
// summary's HealthIssues/HealthWarnings stay at the -1 "unknown" sentinel,
// healthErr carries the message, and the scan error surfaces on the status
// line (spec posture: "health shows ?, error on the status line").
func TestLoadDataHealthErrorKeepsSentinelsAndSurfacesStatusLine(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: healthViewProvider{err: errors.New("checking health for skyrim/default: disk read failed")}})
	require.NoError(t, err)

	msg := model.Init()()
	updated, _ := model.Update(msg)
	m := updated.(Model)

	require.Equal(t, stateReady, m.state, "a failed health scan must not fail the whole load")
	require.Equal(t, -1, m.summary.HealthIssues, "HealthIssues must stay at the unknown sentinel on a scan error")
	require.Equal(t, -1, m.summary.HealthWarnings, "HealthWarnings must stay at the unknown sentinel on a scan error")
	require.Contains(t, m.healthErr, "disk read failed")
	require.Nil(t, m.healthAt, "a failed scan must not stamp healthAt")

	require.True(t, m.action.statusIsError)
	require.Contains(t, m.action.status, "disk read failed")
	require.Contains(t, m.View(), "disk read failed", "the scan error must surface on the status line")
}

// TestDashboardLayoutsRenderHealthLine covers all four dashboard layouts
// (party-sheet/wizardry, monochrome-terminal/amber, commander/dos,
// crt-stack/green): every layout's summary block must show the dashboard
// Health signal.
func TestDashboardLayoutsRenderHealthLine(t *testing.T) {
	t.Parallel()

	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		t.Run(themeName, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, themeName, 120, 30)
			view := model.View()
			require.Contains(t, view, "Health:", "the %s layout must render the dashboard Health line", themeName)
		})
	}
}

// TestHealthDashboardLinePhrasing pins the exact wording for each of the
// three summary states (interface section of task-10-brief.md).
func TestHealthDashboardLinePhrasing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		issues   int
		warnings int
		full     bool
		want     string
	}{
		{"unknown", -1, -1, false, "Health: ?"},
		{"ok-local", 0, 0, false, "Health: OK (local)"},
		{"ok-full", 0, 0, true, "Health: OK (full)"},
		{"issues-and-warnings-local", 1, 2, false, "Health: 1 issue(s), 2 warning(s) (local)"},
		{"issues-and-warnings-full", 3, 1, true, "Health: 3 issue(s), 1 warning(s) (full)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model, err := NewPrototypeModel(Options{Theme: "wizardry"})
			require.NoError(t, err)
			model.summary.HealthIssues = tt.issues
			model.summary.HealthWarnings = tt.warnings
			model.health.Full = tt.full

			require.Equal(t, tt.want, model.healthDashboardLine())
		})
	}
}

// TestDashboardMenuVerifyIntegrityEntryOpensHealth proves the "Verify
// Integrity" dashboard menu entry (both theme variants) is wired to
// ScreenHealth and sits before the Conflicts entry, per the brief.
func TestDashboardMenuVerifyIntegrityEntryOpensHealth(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ theme, label string }{
		{"wizardry", "Verify Integrity"},
		{"amber", "VERIFY INTEGRITY"},
	} {
		t.Run(tc.theme, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, tc.theme, 100, 30)
			items := model.dashboardMenu()

			idx, conflictsIdx := -1, -1
			for i, item := range items {
				if item.label == tc.label {
					idx = i
				}
				if item.target == ScreenConflicts {
					conflictsIdx = i
				}
			}
			require.NotEqual(t, -1, idx, "menu must contain the %q entry", tc.label)
			require.NotEqual(t, -1, conflictsIdx, "sanity: menu must still contain a Conflicts entry")
			require.True(t, items[idx].hasTarget)
			require.Equal(t, ScreenHealth, items[idx].target)
			require.Less(t, idx, conflictsIdx, "Verify Integrity must precede the Conflicts entry")

			for range idx {
				model = updateWithRunes(t, model, "j")
			}
			opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			require.Equal(t, ScreenHealth, opened.(Model).CurrentScreen())
		})
	}
}
