package tui

import (
	"context"
	"errors"
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

// TestHealthViewRendersSubjectFallbackForModlessFinding covers a real case
// (#224 Copilot round 1): a stale_deployment finding from a dangling cache
// link carries no owning mod at all, only a FileID - ModName and ModID are
// both empty. Both the list pane row and the detail pane's "Mod:" line must
// fall back to the FileID as a meaningful subject rather than rendering a
// blank/stray label (e.g. list pane's old "%s (%s)" shape produced a bare
// " (stray.pak)"). The list row must also avoid the redundant "X (X)" shape
// since the fallback subject IS the FileID here.
func TestHealthViewRendersSubjectFallbackForModlessFinding(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.health = HealthView{
		Findings: []HealthFinding{
			{FileID: "stray.pak", Status: "stale_deployment"},
		},
	}
	model.screen = ScreenHealth
	model.selected[ScreenHealth] = 0

	view := model.View()
	require.Contains(t, view, "stray.pak")
	require.NotContains(t, view, "stray.pak (stray.pak)", "list row must not repeat the fallback subject as its own parenthetical")
	require.NotContains(t, view, " (stray.pak)", "list row must not render a blank-subject label like \" (stray.pak)\"")
	require.Contains(t, view, "Mod:    stray.pak", "detail pane's Mod: line must fall back to the FileID")
}

// TestHealthFixedStatusesRenderAsResolved covers two Copilot round-2
// findings together: (1) healthRemedy had no case for fixed_needs_reingest
// (the engine emits it after a --fix re-ingests a pak - verify.go's
// resolveLast("fixed_needs_reingest", ...) call) so the detail pane fell to
// the default empty remedy with no explanation, and (3) healthStatusClass
// bucketed every fixed_* status as "warning", so a resolved row tinted/
// glyphed exactly like an outstanding problem in the post-fix list. Both
// fixed_stale_deployment and fixed_needs_reingest must read "resolved" in
// the detail pane and render the untinted "fine" glyph ("+"), never the
// "warning" class's "?" glyph.
func TestHealthFixedStatusesRenderAsResolved(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.health = HealthView{
		Findings: []HealthFinding{
			{ModID: "a", ModName: "Mod A", FileID: "f1", Status: "fixed_stale_deployment"},
			{ModID: "b", ModName: "Mod B", FileID: "f2", Status: "fixed_needs_reingest"},
		},
	}
	model.screen = ScreenHealth

	model.selected[ScreenHealth] = 0
	view := model.View()
	require.Contains(t, view, "resolved", "fixed_stale_deployment must show the resolved remedy")
	require.Contains(t, view, "+ FIXED STALE DEPLOYMENT", "a resolved row must render the 'fine' glyph, not the 'warning' glyph")
	require.NotContains(t, view, "? FIXED STALE DEPLOYMENT")

	model.selected[ScreenHealth] = 1
	view = model.View()
	require.Contains(t, view, "resolved", "fixed_needs_reingest must show the resolved remedy")
	require.Contains(t, view, "+ FIXED NEEDS REINGEST", "a resolved row must render the 'fine' glyph, not the 'warning' glyph")
	require.NotContains(t, view, "? FIXED NEEDS REINGEST")
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
//
// Also covers the tier-aware variant (#224 Copilot round 2): after a
// successful FULL check (m.health.Full true) the "run a full check (c)"
// hint is misleading - the user just ran one - so the full-tier empty state
// must drop the hint entirely rather than repeat the local-tier copy.
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

	model.health.Full = true
	view = model.View()
	require.Contains(t, view, "no findings (full)")
	require.NotContains(t, view, "run a full check (c)", "a full-tier empty state must not hint at running the full check it just ran")
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

// --- Task 11: 'c' full (network) health check ---

// TestFullCheckKeyDispatchesOnHealthScreen proves 'c' on ScreenHealth
// dispatches RunHealthCheck's Full tier asynchronously - mirroring
// TestCheckUpdatesKeyDispatchesAsyncFetchFromDashboard's own "running set
// synchronously, provider call happens when the returned cmd runs" shape.
func TestFullCheckKeyDispatchesOnHealthScreen(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{RunHealthCheckOutcome: HealthView{Full: true}}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Running full health check…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Empty(t, rec.RunHealthCheckCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := runActionCmd(t, cmd)
	require.IsType(t, fullHealthCheckResultMsg{}, msg)
	require.Len(t, rec.RunHealthCheckCalls, 1)
	require.True(t, rec.RunHealthCheckCalls[0].Full, "must request the Full (network) tier")
	require.False(t, rec.RunHealthCheckCalls[0].Fix, "must be a dry run - fix is Task 12's own binding")
}

// TestFullCheckLandsViewAndStatusLine covers both status-line phrasings
// task-11-brief.md specifies, plus the view/summary tie-in a successful
// check must apply.
func TestFullCheckLandsViewAndStatusLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		view       HealthView
		wantStatus string
	}{
		{
			name:       "all ok",
			view:       HealthView{Full: true},
			wantStatus: "full check: all OK",
		},
		{
			name: "issues and warnings",
			view: HealthView{
				Full:     true,
				Issues:   2,
				Warnings: 1,
				Findings: []HealthFinding{{ModID: "skyui", ModName: "SkyUI", Status: "missing"}},
			},
			wantStatus: "full check: 2 issue(s), 1 warning(s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{RunHealthCheckOutcome: tt.view}
			model := modelWithActions(t, rec)
			model.screen = ScreenHealth

			updated, cmd := model.Update(keyRunes("c"))
			model = updated.(Model)
			msg := runActionCmd(t, cmd)

			updated, cmd2 := model.Update(msg)
			model = updated.(Model)
			require.Nil(t, cmd2)
			require.False(t, model.action.running)
			require.Equal(t, tt.wantStatus, model.action.status)
			require.False(t, model.action.statusIsError)
			require.Equal(t, tt.view, model.health)
			require.NotNil(t, model.healthAt)
			require.Empty(t, model.healthErr)
			require.Equal(t, tt.view.Issues, model.summary.HealthIssues)
			require.Equal(t, tt.view.Warnings, model.summary.HealthWarnings)
		})
	}
}

// TestFullCheckFailurePreservesPreviousViewAndSetsErrorStatus proves a
// failed Full-tier call reports the error on the status line but leaves
// m.health/healthAt exactly as they were (task-11-brief.md: "on failure...
// KEEP the previous view"), mirroring dataLoadedMsg's identical posture for
// a failed background scan (app.go).
func TestFullCheckFailurePreservesPreviousViewAndSetsErrorStatus(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{RunHealthCheckErr: errors.New("network unreachable")}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	prevAt := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	model.healthAt = &prevAt
	model.health = HealthView{Findings: []HealthFinding{{ModID: "x", ModName: "X", Status: "missing"}}, Issues: 1}

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	msg := runActionCmd(t, cmd)
	require.IsType(t, fullHealthCheckFailedMsg{}, msg)

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "network unreachable")
	require.Equal(t, prevAt, *model.healthAt, "a failed full check must keep the previous scan's timestamp")
	require.Len(t, model.health.Findings, 1, "a failed full check must keep the previous view")
}

// TestFullCheckKeyRefusedWhileRunning mirrors TestCheckUpdatesKeyInertWhileRunning:
// the standard single-flight "busy" refusal, no RunHealthCheck call at all.
func TestFullCheckKeyRefusedWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.action.running = true

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFullCheckKeyInertWhileAnotherModalPending mirrors
// TestCheckUpdatesKeyInertWhileAnotherModalPending: a DIFFERENT already-
// pending confirmation modal is left completely undisturbed by 'c'.
func TestFullCheckKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFullCheckKeyOtherScreensNoop proves 'c' is inert on every screen other
// than Health (ScreenProfiles is deliberately excluded - see
// TestFullCheckKeyDoesNotStealCFromCreateProfile below, which proves the
// opposite: 'c' DOES still do something there, just not a full check).
func TestFullCheckKeyOtherScreensNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenInstalledMods, ScreenSearch, ScreenSources, ScreenConflicts} {
		rec := &recordingActions{}
		model := modelWithActions(t, rec)
		model.screen = screen

		updated, cmd := model.Update(keyRunes("c"))
		model = updated.(Model)
		require.Nil(t, cmd, "screen %v", screen)
		require.False(t, model.action.running, "screen %v", screen)
		require.Empty(t, rec.RunHealthCheckCalls, "screen %v", screen)
	}
}

// TestFullCheckKeyDoesNotStealCFromCreateProfile is the collision proof
// keys.go's FullCheck doc comment promises: FullCheck and CreateProfile
// share the physical key "c", but updateKey's compound guard (app.go) keeps
// CreateProfile fully functional on ScreenProfiles - see that switch case's
// own doc comment for the "first matching case wins" mechanics this pins.
func TestFullCheckKeyDoesNotStealCFromCreateProfile(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles

	updated, _ := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, model.inputModal, "'c' on ScreenProfiles must still open CreateProfile's input modal")
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFullCheckKeyDeclinedWithPushedContextOnHealth proves the "&&
// m.contextContent == nil" half of updateKey's compound guard: 'c' while
// ScreenHealth has pushed context content (contextview.go) must not
// dispatch a full check - it falls through to CreateProfile's own
// unconditional case instead, which no-ops off-Profiles exactly as it
// always has (see TestFullCheckKeyDoesNotStealCFromCreateProfile above for
// the case where "c" DOES reach CreateProfile productively).
func TestFullCheckKeyDeclinedWithPushedContentOnHealth(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenConflicts

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake, ScreenConflicts)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.RunHealthCheckCalls, "'c' must not dispatch a full check while context content is pushed")
	require.NotNil(t, model.contextContent, "the pushed content must remain")
}

// TestFullCheckProgressTicksReachStatusLine drives the FULL pump pipeline
// through Model.Update, mirroring
// TestActionProgressStreamsWhileRunningThenActionDoneClearsIt's identical
// shape (actions_test.go): runFullHealthCheck's tea.Batch(actionCmd,
// listenerCmd) is exactly what buildAction's own confirm returns, so the
// same drive-both-sub-cmds-through-Update technique proves the Full tier's
// progress ticks reach the status line too.
func TestFullCheckProgressTicksReachStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		RunHealthCheckOutcome: HealthView{Full: true},
		RunHealthCheckTicks:   []ActionProgress{{Line: "Checking SkyUI: 3/10", Percent: 30}},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, cmd)

	batchMsg := cmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	require.True(t, ok, "runFullHealthCheck must return tea.Batch(actionCmd, listenerCmd)")
	require.Len(t, batch, 2)

	actionMsg := batch[0]()
	require.IsType(t, fullHealthCheckResultMsg{}, actionMsg)

	// The action cmd already ran do() to completion (sending its one tick and
	// closing the channel) before actionMsg was produced above, so the
	// listener's first receive gets that buffered tick even though the
	// channel is already closed (Go delivers buffered values before
	// signaling closed - see waitForActionProgress's own doc comment).
	progressMsg := batch[1]()
	require.IsType(t, actionProgressMsg{}, progressMsg)

	updated, reissue := model.Update(progressMsg)
	model = updated.(Model)
	require.Equal(t, "Checking SkyUI: 3/10", model.action.progress.Line)
	require.Contains(t, model.statusLine(), "Checking SkyUI: 3/10")
	require.NotNil(t, reissue, "a fresh tick must re-issue the listener")
}

// TestFullHealthCheckSettleClearsStaleProgressLine proves resolveFullHealthCheckResult
// and resolveFullHealthCheckFailure clear m.action.progress on settle, mirroring
// actionDoneMsg/actionFailedMsg's own clearing (app.go) - Copilot round 3 finding:
// without this, a leftover "checking versions..." tick from the JUST-SETTLED full
// check survives into the NEXT action's own run (statusLine prefers a running
// action's progress.Line over its stored status text - see statusLine's own doc
// comment - and some actions, like actionSetPolicy, never post a progress tick of
// their own), so the stale tick would wrongly surface as that next action's status
// line instead of nothing/its own text.
func TestFullHealthCheckSettleClearsStaleProgressLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"result", func(gen int) tea.Msg {
			return fullHealthCheckResultMsg{gen: gen, view: HealthView{Full: true}}
		}},
		{"failure", func(gen int) tea.Msg {
			return fullHealthCheckFailedMsg{gen: gen, err: errors.New("network unreachable")}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := modelWithActions(t, &recordingActions{})
			model.screen = ScreenHealth
			model.action.gen = 7
			model.action.running = true
			model.action.progress = ActionProgress{Line: "checking versions 1/2: Foo", Percent: 50}

			updated, _ := model.Update(tt.msg(7))
			model = updated.(Model)
			require.False(t, model.action.running)
			require.Empty(t, model.action.progress.Line, "settling a full health check must clear the stale progress line")

			// Simulate the NEXT action starting without ever posting a progress
			// tick of its own (statusLine's "some actions emit none" case) - the
			// stale tick from the settled check above must not resurface.
			model.action.running = true
			require.NotContains(t, model.statusLine(), "checking versions",
				"a stale progress tick from a settled full health check must not surface as the next action's status line")
		})
	}
}

// --- Task 12: 'F' batch fix behind confirmation ---

// TestFixHealthKeyOpensModalWithCategoryDetail proves 'F' on ScreenHealth
// with fixable findings opens the standard confirmation modal, titled with
// the total fixable count and one detail line per status class present -
// task-12-brief.md's exact phrasings - while the "ok" row (unfixable) is
// excluded from both the count and the detail.
func TestFixHealthKeyOpensModalWithCategoryDetail(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{
		Findings: []HealthFinding{
			{ModID: "a", ModName: "A", Status: "missing"},
			{ModID: "b", ModName: "B", Status: "missing"},
			{ModID: "c", ModName: "C", Status: "version_mismatch"},
			{ModID: "d", ModName: "D", Status: "stale_deployment"},
			{ModID: "e", ModName: "E", Status: "needs_reingest"},
			{ModID: "f", ModName: "F", Status: "ok"},
		},
	}

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd, "opening the modal does not itself dispatch anything")
	require.NotNil(t, model.action.pending)
	require.Equal(t, "Fix 5 finding(s)?", model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "2 missing file(s) — re-download")
	require.Contains(t, model.action.pending.detail, "1 version mismatch — re-key records (locked mods refused)")
	require.Contains(t, model.action.pending.detail, "1 stale deployment — remove")
	require.Contains(t, model.action.pending.detail, "1 pak needs re-ingest")
	require.Empty(t, rec.RunHealthCheckCalls, "nothing runs until confirmed")
}

// TestFixHealthKeyRefusedWhenNothingFixable proves the "nothing actionable"
// refusal (task-12-brief.md): an empty view, or one whose findings are ALL
// among the four statuses fix cannot touch, refuses on the status line with
// no modal and no provider call.
func TestFixHealthKeyRefusedWhenNothingFixable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		findings []HealthFinding
	}{
		{name: "empty view", findings: nil},
		{
			name: "all unfixable statuses",
			findings: []HealthFinding{
				{ModID: "a", Status: "skipped"},
				{ModID: "b", Status: "version_unverifiable"},
				{ModID: "c", Status: "file_count_mismatch"},
				{ModID: "d", Status: "ok", Note: "lock pending convergence"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{}
			model := modelWithActions(t, rec)
			model.screen = ScreenHealth
			model.health = HealthView{Findings: tt.findings}

			updated, cmd := model.Update(keyRunes("F"))
			model = updated.(Model)
			require.Nil(t, cmd)
			require.Nil(t, model.action.pending)
			require.Equal(t, "nothing fixable — run a full check (c) first", model.action.status)
			require.True(t, model.action.statusIsError)
			require.Empty(t, rec.RunHealthCheckCalls)
		})
	}
}

// TestFixHealthCancelLeavesStateUntouched proves n/esc on the fix confirm
// modal leaves m.health/healthAt and every other piece of state exactly as
// they were, with no provider call - mirroring updatePendingActionKey's
// CancelAction contract for every other confirmation modal.
func TestFixHealthCancelLeavesStateUntouched(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	before := HealthView{Findings: []HealthFinding{{ModID: "a", ModName: "A", Status: "missing"}}}
	model.health = before

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("n"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Equal(t, before, model.health)
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFixHealthConfirmDispatchesFullFixModeAndLandsView is the enforcement
// point T8's review deferred to this task (task-12-brief.md): confirming
// ALWAYS calls RunHealthCheck with full=true (CLI --fix parity includes the
// version pass), never a Local-tier fix. On a successful result: the
// returned view replaces m.health/healthAt/healthErr, summary counts follow
// it, a "fix results" info overlay lists the fixed_* row, and the ordinary
// data refresh (m.loadData) is returned so the dashboard picks it up.
func TestFixHealthConfirmDispatchesFullFixModeAndLandsView(t *testing.T) {
	t.Parallel()

	resultView := HealthView{
		Full:     true,
		Findings: []HealthFinding{{ModID: "x", ModName: "X", FileID: "f1", Status: "fixed_stale_deployment"}},
	}
	rec := &recordingActions{RunHealthCheckOutcome: resultView}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "x", ModName: "X", FileID: "f1", Status: "stale_deployment"}}}

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.True(t, model.action.running)
	msg := runActionCmd(t, cmd)
	require.IsType(t, fixHealthCheckResultMsg{}, msg)

	require.Len(t, rec.RunHealthCheckCalls, 1)
	require.True(t, rec.RunHealthCheckCalls[0].Full, "fix mode must always request the Full tier")
	require.True(t, rec.RunHealthCheckCalls[0].Fix)

	updated, refresh := model.Update(msg)
	model = updated.(Model)
	require.False(t, model.action.running)
	require.Equal(t, resultView, model.health)
	require.NotNil(t, model.healthAt)
	require.Empty(t, model.healthErr)
	require.Equal(t, resultView.Issues, model.summary.HealthIssues)
	require.Equal(t, resultView.Warnings, model.summary.HealthWarnings)

	require.NotNil(t, model.overlay)
	require.Equal(t, "fix results", model.overlay.title)
	require.Contains(t, model.overlay.lines, "✓ X: FIXED STALE DEPLOYMENT")

	require.NotNil(t, refresh, "a successful fix must return the ordinary data refresh")
	require.IsType(t, dataLoadedMsg{}, refresh())
}

// TestFixHealthCheckSettleClearsStaleProgressLine is
// TestFullHealthCheckSettleClearsStaleProgressLine's own sibling for the 'F'
// fix flow's resolvers (Copilot round 3 finding): resolveFixHealthCheckResult
// and resolveFixHealthCheckFailure must clear m.action.progress on settle
// exactly like the full-check pair above, or a stale "checking versions..."
// tick from a just-settled fix survives into whatever action runs next.
func TestFixHealthCheckSettleClearsStaleProgressLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"result", func(gen int) tea.Msg {
			return fixHealthCheckResultMsg{gen: gen, view: HealthView{Full: true}}
		}},
		{"failure", func(gen int) tea.Msg {
			return fixHealthCheckFailedMsg{gen: gen, err: errors.New("network unreachable")}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := modelWithActions(t, &recordingActions{})
			model.screen = ScreenHealth
			model.action.gen = 9
			model.action.running = true
			model.action.progress = ActionProgress{Line: "checking versions 1/2: Foo", Percent: 50}

			updated, _ := model.Update(tt.msg(9))
			model = updated.(Model)
			require.False(t, model.action.running)
			require.Empty(t, model.action.progress.Line, "settling a fix health check must clear the stale progress line")

			model.action.running = true
			require.NotContains(t, model.statusLine(), "checking versions",
				"a stale progress tick from a settled fix health check must not surface as the next action's status line")
		})
	}
}

// TestHealthCheckResultClampsSelectionOnShorterFindingsList proves both
// success resolvers (resolveFullHealthCheckResult, resolveFixHealthCheckResult)
// clamp the Health screen's selection exactly like dataLoadedMsg's own
// clampSelections call does (app.go) - Copilot round 3 finding: overwriting
// m.health with a SHORTER findings list (e.g. a fix resolving several
// findings down to a handful) without reclamping leaves a selection index
// from the old, longer list pointing past the end of the new one, so
// healthDetailPane's bounds check (idx >= len(m.health.Findings)) falls
// through to "No selection." even though the new list is non-empty.
func TestHealthCheckResultClampsSelectionOnShorterFindingsList(t *testing.T) {
	t.Parallel()

	longFindings := []HealthFinding{
		{ModID: "a", ModName: "A", Status: "missing"},
		{ModID: "b", ModName: "B", Status: "missing"},
		{ModID: "c", ModName: "C", Status: "missing"},
		{ModID: "d", ModName: "D", Status: "missing"},
		{ModID: "e", ModName: "E", Status: "missing"},
	}
	shortView := HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A", Status: "missing"},
		{ModID: "b", ModName: "B", Status: "missing"},
	}}

	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"full check", func(gen int) tea.Msg {
			return fullHealthCheckResultMsg{gen: gen, view: shortView}
		}},
		{"fix check", func(gen int) tea.Msg {
			return fixHealthCheckResultMsg{gen: gen, view: shortView}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := modelWithActions(t, &recordingActions{})
			model.screen = ScreenHealth
			model.health = HealthView{Findings: longFindings}
			model.selected[ScreenHealth] = len(longFindings) - 1
			model.action.gen = 11
			model.action.running = true

			updated, _ := model.Update(tt.msg(11))
			model = updated.(Model)

			require.Equal(t, len(shortView.Findings)-1, model.selected[ScreenHealth],
				"selection must clamp to the new, shorter findings list")
			detail := model.healthDetailPane(80, 20)
			require.NotContains(t, detail, "No selection.")
			require.Contains(t, detail, "B", "the clamped selection must land on a real finding, not walk off the end")
		})
	}
}

// TestFixHealthResultOverlaySubjectFallbackForModlessFinding proves the fix
// results overlay line (healthFixResultLine) also uses the ModName->ModID->
// FileID subject fallback (#224 Copilot round 1): a fixed_stale_deployment
// row from a dangling cache link carries no mod at all, only a FileID, and
// must not render the bare "✓ : FIXED STALE DEPLOYMENT" the old
// f.ModName-only line produced.
func TestFixHealthResultOverlaySubjectFallbackForModlessFinding(t *testing.T) {
	t.Parallel()

	resultView := HealthView{
		Full:     true,
		Findings: []HealthFinding{{FileID: "stray.pak", Status: "fixed_stale_deployment"}},
	}
	rec := &recordingActions{RunHealthCheckOutcome: resultView}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{FileID: "stray.pak", Status: "stale_deployment"}}}

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	msg := runActionCmd(t, cmd)

	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.overlay)
	require.Contains(t, model.overlay.lines, "✓ stray.pak: FIXED STALE DEPLOYMENT")
}

// TestFixHealthLockedVersionMismatchRemainsInOverlay proves the engine's own
// lock refusal survives the fix pass unchanged (task-12-brief.md): a locked
// mod's version_mismatch finding is returned by RunHealthCheck exactly as
// the engine reports it (status still "version_mismatch", Note "locked")
// rather than disappearing or flipping to a fixed_* status - and the fix
// results overlay surfaces it under "remaining", not "fixed".
func TestFixHealthLockedVersionMismatchRemainsInOverlay(t *testing.T) {
	t.Parallel()

	resultView := HealthView{
		Full:     true,
		Issues:   1,
		Findings: []HealthFinding{{ModID: "locked-mod", ModName: "Locked Mod", Status: "version_mismatch", Note: "locked"}},
	}
	rec := &recordingActions{RunHealthCheckOutcome: resultView}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "locked-mod", ModName: "Locked Mod", Status: "version_mismatch"}}}

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	msg := runActionCmd(t, cmd)

	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.Equal(t, resultView, model.health, "the engine's own refusal must survive unchanged")
	require.Len(t, model.health.Findings, 1)
	require.Equal(t, "version_mismatch", model.health.Findings[0].Status, "a locked mod's row is never rewritten to fixed_*")

	require.NotNil(t, model.overlay)
	found := false
	for _, line := range model.overlay.lines {
		if strings.Contains(line, "Locked Mod") && strings.Contains(line, "locked") {
			found = true
		}
	}
	require.True(t, found, "the locked refusal must surface in the fix results overlay: %v", model.overlay.lines)
}

// TestFixHealthKeyRefusedWhileRunning mirrors TestFullCheckKeyRefusedWhileRunning:
// the standard single-flight "busy" refusal, no RunHealthCheck call at all.
func TestFixHealthKeyRefusedWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "a", ModName: "A", Status: "missing"}}}
	model.action.running = true

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFixHealthKeyInertWhileAnotherModalPending mirrors
// TestFullCheckKeyInertWhileAnotherModalPending: a DIFFERENT already-pending
// confirmation modal is left completely undisturbed by 'F'.
func TestFixHealthKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "a", ModName: "A", Status: "missing"}}}
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFixHealthKeyOtherScreensNoop proves 'F' is inert on every screen other
// than Health.
func TestFixHealthKeyOtherScreensNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenInstalledMods, ScreenSearch, ScreenProfiles, ScreenSources, ScreenConflicts} {
		rec := &recordingActions{}
		model := modelWithActions(t, rec)
		model.screen = screen

		updated, cmd := model.Update(keyRunes("F"))
		model = updated.(Model)
		require.Nil(t, cmd, "screen %v", screen)
		require.Nil(t, model.action.pending, "screen %v", screen)
		require.Empty(t, rec.RunHealthCheckCalls, "screen %v", screen)
	}
}

// TestFixHealthKeyDeclinedWithPushedContentOnHealth mirrors
// TestFullCheckKeyDeclinedWithPushedContentOnHealth: 'F' while ScreenHealth
// has pushed context content must not open the fix confirmation.
func TestFixHealthKeyDeclinedWithPushedContentOnHealth(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenConflicts

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake, ScreenConflicts)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.RunHealthCheckCalls)
	require.NotNil(t, model.contextContent, "the pushed content must remain")
}
