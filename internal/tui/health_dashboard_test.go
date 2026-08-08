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
		// Round 1 fix Finding 3: OK requires BOTH counts zero - warnings
		// alone (zero issues) must still render the counted form, not "OK".
		{"warnings-only-not-ok", 0, 2, false, "Health: 0 issue(s), 2 warning(s) (local)"},
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
// ScreenHealth. #224 Task 15 folded the former separate "Consult Conflict
// Oracle"/"ASK CONFLICT ORACLE" entry into this one (dashboardMenu's own doc
// comment: two rows pointing at the same retired-Conflicts-screen
// destination would have been redundant), so this is now the menu's sole
// "go look at a standing signal" entry - no separate Conflicts-entry
// ordering to prove anymore.
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

			idx := -1
			for i, item := range items {
				if item.label == tc.label {
					idx = i
				}
			}
			require.NotEqual(t, -1, idx, "menu must contain the %q entry", tc.label)
			require.True(t, items[idx].hasTarget)
			require.Equal(t, ScreenHealth, items[idx].target)

			for range idx {
				model = updateWithRunes(t, model, "j")
			}
			opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			require.Equal(t, ScreenHealth, opened.(Model).CurrentScreen())
		})
	}
}

// TestGameSwitchClearsHealth is round 1's Finding 2 regression test:
// resolveGameSwitch's reset block (mutations.go) clears m.mods/m.profiles/
// m.conflicts for the OLD game, but before the fix left m.health/
// m.healthAt/m.healthErr standing - so ScreenHealth's home view would
// render game A's findings and scan age while game B's fresh load was
// still in flight. Populates health for game A (via the initial load,
// through recordingProvider's delegate to the prototype's canned Health()
// view), drives a game switch, and asserts the reset lands BEFORE the
// fresh load's own dataLoadedMsg does: unknown counts (the "?" sentinel
// line) and "no scan yet" in the immediately-rendered header - mirroring
// TestGameSwitchRebindsProvidersResetsAndReloads's own "assert on the
// Model the switch returns, before running loadCmd" structure.
func TestGameSwitchClearsHealth(t *testing.T) {
	t.Parallel()

	games := []GameInfo{
		{ID: "fallout4", Name: "Fallout 4", Active: true},
		{ID: "skyrim", Name: "Skyrim", Active: false},
	}
	provider := &recordingProvider{delegate: NewPrototypeProvider(), ListGamesResult: games}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: &recordingActions{}})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	// Sanity: the initial load populated game A's health from the
	// prototype delegate's canned (non-empty) view.
	require.NotEmpty(t, model.health.Findings, "sanity: the initial load must have populated health")
	require.NotNil(t, model.healthAt, "sanity: the initial load must have stamped healthAt")

	updated, _ := model.Update(keyRunes("g"))
	model = updated.(Model)
	updated, chooseCmd := model.Update(keyRunes("2")) // "Skyrim", the non-active game
	model = updated.(Model)
	require.NotNil(t, chooseCmd)
	gameMsg := chooseCmd()

	updated, loadCmd := model.Update(gameMsg)
	model = updated.(Model)
	require.NotNil(t, loadCmd)

	// Asserted on the switch's own return, BEFORE loadCmd (game B's fresh
	// load) ever runs - proves the reset itself clears the old game's
	// health, not just that the subsequent reload happens to overwrite it.
	require.Equal(t, HealthView{}, model.health, "the OLD game's findings must not survive the switch")
	require.Nil(t, model.healthAt, "the OLD game's scan age must not survive the switch")
	require.Equal(t, "", model.healthErr)
	require.Equal(t, -1, model.summary.HealthIssues)
	require.Equal(t, -1, model.summary.HealthWarnings)
	require.Equal(t, "Health: ?", model.healthDashboardLine())

	// state is stateLoading here (the fresh load hasn't landed) - stamped
	// stateReady by hand so View() renders ScreenHealth's actual content
	// instead of the "Consulting the archives..." loading screen; this
	// only proves what the RESET itself left behind, independent of state.
	model.state = stateReady
	model.screen = ScreenHealth
	view := model.View()
	require.Contains(t, view, "no scan yet", "the reset must render as an unscanned session, not the old game's age")
}
