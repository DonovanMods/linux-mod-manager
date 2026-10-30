package serve_test

// Issue 583: long operations must say what is running, and how far along it
// is when that is knowable, instead of leaving a page that looks frozen.
// Mission Control renders the moment its fast reads land, each slow read
// names itself in place ("Checking 3 mods for updates (1/3)…"), a game or
// profile switch never shows the previous context's documents, a fast
// operation flashes nothing, and a job's control names it from its first
// frame.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedUpdateSource is a fakeSource whose update check can be held: armed,
// the next check stops before its first per-mod tick and again after it,
// each until the test releases it. Unarmed it answers at once - so a
// scenario loads its starting page at full speed and holds only the check
// it is about.
type gatedUpdateSource struct {
	*fakeSource

	mu   sync.Mutex
	held *updateCheckHold
}

// updateCheckHold is one armed check's two gates.
type updateCheckHold struct {
	first, rest         chan struct{}
	firstOnce, restOnce sync.Once
}

func (h *updateCheckHold) releaseFirst() { h.firstOnce.Do(func() { close(h.first) }) }
func (h *updateCheckHold) releaseRest()  { h.restOnce.Do(func() { close(h.rest) }) }

// arm holds the next update check, released through the returned hold.
// Both gates open at test end, so a held request never outlives the test.
func (s *gatedUpdateSource) arm(t *testing.T) *updateCheckHold {
	t.Helper()
	h := &updateCheckHold{first: make(chan struct{}), rest: make(chan struct{})}
	s.mu.Lock()
	s.held = h
	s.mu.Unlock()
	t.Cleanup(func() { h.releaseFirst(); h.releaseRest() })
	return h
}

// CheckUpdatesWithProgress implements source.UpdateProgressReporter, the
// per-mod reporter shape a real one-request-per-mod source has.
func (s *gatedUpdateSource) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report source.UpdateProgressFunc) ([]domain.Update, error) {
	s.mu.Lock()
	h := s.held
	s.held = nil
	s.mu.Unlock()

	wait := func(gate chan struct{}) error {
		if h == nil {
			return nil
		}
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := wait(h.firstGate()); err != nil {
		return nil, err
	}
	for i, mod := range installed {
		report(i+1, len(installed), mod.Name)
		if i == 0 {
			if err := wait(h.restGate()); err != nil {
				return nil, err
			}
		}
	}
	return s.CheckUpdates(ctx, installed)
}

func (h *updateCheckHold) firstGate() chan struct{} {
	if h == nil {
		return nil
	}
	return h.first
}

func (h *updateCheckHold) restGate() chan struct{} {
	if h == nil {
		return nil
	}
	return h.rest
}

// loadingFixture is a browser world with two games and, in the first, two
// profiles - each with its own installed mods, so a switch's target is
// told apart from what was on screen by name.
type loadingFixture struct {
	e2eFixture
	Src    *gatedUpdateSource
	Second *domain.Game
}

// newE2ELoadingFixture: "Fixture Game" (g1) has profile "default" with
// Alpha/Beta/Gamma - Alpha with an update upstream - and profile "other"
// with Delta/Epsilon; "Second Game" (g2) has Zeta. One gated source serves
// all of them.
func newE2ELoadingFixture(t *testing.T) loadingFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := &gatedUpdateSource{fakeSource: newFakeSource("fake")}
	for _, m := range []struct{ id, name, version string }{
		{"alpha", "Alpha Mod", "2.0"}, {"beta", "Beta Mod", "1.0"}, {"gamma", "Gamma Mod", "1.0"},
		{"delta", "Delta Mod", "1.0"}, {"epsilon", "Epsilon Mod", "1.0"}, {"zeta", "Zeta Mod", "1.0"},
	} {
		src.addMod(fakeSourceMod{Mod: domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: m.version}})
	}
	svc, game := newFixtureServiceWithSource(t, src)

	second := &domain.Game{
		ID: "g2", Name: "Second Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{"fake": ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), second))
	pm := svc.NewProfileManager()
	_, err := pm.Create(t.Context(), second.ID, "default")
	require.NoError(t, err)
	_, err = pm.Create(t.Context(), game.ID, "other")
	require.NoError(t, err)

	install := func(g *domain.Game, profile, id, name string) {
		require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: id, SourceID: "fake", Name: name, Version: "1.0", GameID: g.ID},
			ProfileName:  profile,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		}))
	}
	install(game, "default", "alpha", "Alpha Mod")
	install(game, "default", "beta", "Beta Mod")
	install(game, "default", "gamma", "Gamma Mod")
	install(game, "other", "delta", "Delta Mod")
	install(game, "other", "epsilon", "Epsilon Mod")
	install(second, "default", "zeta", "Zeta Mod")

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return loadingFixture{
		e2eFixture: e2eFixture{
			Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
			BrowserErrors: browserErrors,
		},
		Src:    src,
		Second: second,
	}
}

// pendingReadSel is the in-place loading row for one of Mission Control's
// reads (missioncontrol.js's PendingReads).
func pendingReadSel(read string) string {
	return fmt.Sprintf(`[data-pending-read=%q]`, read)
}

// pendingTextIncludes waits until read's loading row says want.
func pendingTextIncludes(read, want string) chromedp.Action {
	return pollUntil(fmt.Sprintf(
		`(document.querySelector(%q)?.textContent ?? "").includes(%q)`, pendingReadSel(read), want))
}

// recordLoadingAppearancesJS starts recording, from the moment it runs,
// every loading state that becomes visible - a pending read row, or the
// library's own loading line - with its text and when it appeared
// (performance.now()). window.__loadMark is the moment a scenario's action
// was taken, set by markJS. An observer rather than a poll: a flash is one
// frame, which a poll samples past.
const recordLoadingAppearancesJS = `(() => {
	window.__loadingSeen = [];
	window.__loadMark = null;
	const seen = new Set();
	const sample = () => {
		for (const el of document.querySelectorAll('[data-pending-read], [data-testid="library-loading"]')) {
			const text = el.textContent.trim();
			const key = (el.dataset.pendingRead ?? "library") + ":" + text;
			if (seen.has(key)) continue;
			seen.add(key);
			window.__loadingSeen.push({read: el.dataset.pendingRead ?? "library", text, at: performance.now()});
		}
	};
	sample();
	new MutationObserver(sample).observe(document.body,
		{subtree: true, childList: true, characterData: true, attributes: true});
})()`

const markJS = `window.__loadMark = performance.now()`

type loadingAppearance struct {
	Read string  `json:"read"`
	Text string  `json:"text"`
	At   float64 `json:"at"`
}

// hydrated is Mission Control's "every read has settled" marker.
const hydrated = `.mission-control[data-hydrated="true"]`

// TestE2E_Loading_MissionControlRendersBeforeASlowUpdateCheck is case (a):
// the library is on screen while the update check is still out, and the
// check names itself - how many mods, then how far it has got - instead of
// the page waiting silently on its slowest read.
func TestE2E_Loading_MissionControlRendersBeforeASlowUpdateCheck(t *testing.T) {
	f := newE2ELoadingFixture(t)
	hold := f.Src.arm(t)

	var hydratedWhileHeld, busy, announced string
	var rows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`tr.mod-row`, chromedp.ByQuery),
		pendingTextIncludes("updates", "Checking 3 mods for updates"),
		chromedp.Evaluate(`document.querySelectorAll("tr.mod-row").length`, &rows),
		chromedp.AttributeValue(`.mission-control`, "data-hydrated", &hydratedWhileHeld, nil, chromedp.ByQuery),
		chromedp.AttributeValue(`.pending-reads`, "aria-busy", &busy, nil, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { hold.releaseFirst(); return nil }),
		pendingTextIncludes("updates", "(1/3)"),
		chromedp.ActionFunc(func(context.Context) error { hold.releaseRest(); return nil }),
		waitGone(pendingReadSel("updates")),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		pollUntil(`(document.querySelector('[data-testid="load-announcer"]')?.textContent ?? "").includes("Update check finished")`),
		textContent(`[data-testid="load-announcer"]`, &announced),
	)

	assert.Equal(t, 3, rows, "the library rendered while the check was still out")
	assert.Equal(t, "false", hydratedWhileHeld, "the page is not settled while a read is out")
	assert.Equal(t, "true", busy, "the loading region says it is busy")
	assert.Contains(t, announced, "Update check finished")
	assert.Empty(t, f.BrowserErrors())
}

// switchAndWatch runs a context switch (pick) with the update check held,
// recording every loading state that appears, and returns them alongside
// the library's rows while the check was out.
func switchAndWatch(t *testing.T, f loadingFixture, pick []chromedp.Action, wantPending string) ([]loadingAppearance, []string, float64) {
	t.Helper()
	var hold *updateCheckHold
	var seen []loadingAppearance
	var names []string
	var mark float64
	actions := []chromedp.Action{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { hold = f.Src.arm(t); return nil }),
		chromedp.Evaluate(recordLoadingAppearancesJS, nil),
	}
	actions = append(actions, pick...)
	actions = append(actions,
		pendingTextIncludes("updates", wantPending),
		chromedp.Evaluate(`[...document.querySelectorAll("tr.mod-row")].map((el) => el.textContent.trim())`, &names),
		chromedp.ActionFunc(func(context.Context) error { hold.releaseFirst(); hold.releaseRest(); return nil }),
		waitGone(pendingReadSel("updates")),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		chromedp.Evaluate(`window.__loadingSeen`, &seen),
		chromedp.Evaluate(`window.__loadMark`, &mark),
	)
	f.runInBrowser(t, actions...)
	return seen, names, mark
}

// firstAppearance is when read's loading state first appeared, or -1.
func firstAppearance(seen []loadingAppearance, read string) float64 {
	for _, s := range seen {
		if s.Read == read {
			return s.At
		}
	}
	return -1
}

// TestE2E_Loading_GameSwitchNamesWhatIsLoading is case (b) for a game: the
// switch shows what it is loading within the delay window, never the
// previous game's mods under the new game's name, and clears once done.
func TestE2E_Loading_GameSwitchNamesWhatIsLoading(t *testing.T) {
	f := newE2ELoadingFixture(t)

	seen, names, mark := switchAndWatch(t, f, []chromedp.Action{
		chromedp.Click(`[data-picker="game"]`, chromedp.ByQuery),
		chromedp.Evaluate(markJS, nil),
		chromedp.Click(`//ul[contains(@class,"game-picker__menu")]//button[normalize-space()="Second Game"]`, chromedp.BySearch),
	}, "Checking 1 mod for updates")

	assertNoStaleRows(t, names, "Zeta Mod", "Alpha Mod", "Beta Mod", "Gamma Mod")
	assertShownWithinTheWindow(t, seen, mark)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Loading_ProfileSwitchNamesWhatIsLoading is case (b) for a
// profile: the same, within one game.
func TestE2E_Loading_ProfileSwitchNamesWhatIsLoading(t *testing.T) {
	f := newE2ELoadingFixture(t)

	seen, names, mark := switchAndWatch(t, f, []chromedp.Action{
		chromedp.Click(`[data-picker="profile"]`, chromedp.ByQuery),
		chromedp.Evaluate(markJS, nil),
		chromedp.Click(`li[data-profile="other"] .picker__item`, chromedp.ByQuery),
	}, "Checking 2 mods for updates")

	assertNoStaleRows(t, names, "Delta Mod", "Alpha Mod", "Beta Mod", "Gamma Mod")
	assertShownWithinTheWindow(t, seen, mark)
	assert.Empty(t, f.BrowserErrors())
}

// assertNoStaleRows: while the switch's check was out the library already
// showed the target's mods, and none of the context switched away from.
func assertNoStaleRows(t *testing.T, names []string, want string, stale ...string) {
	t.Helper()
	joined := fmt.Sprint(names)
	assert.Contains(t, joined, want, "the target context's library is on screen while the check runs")
	for _, s := range stale {
		assert.NotContains(t, joined, s, "the previous context's mods must not linger under the new one")
	}
}

// assertShownWithinTheWindow: the update check's loading row appeared after
// the show delay (it does not flash) and well inside the ~200 ms the issue
// asks for plus a test machine's slack.
func assertShownWithinTheWindow(t *testing.T, seen []loadingAppearance, mark float64) {
	t.Helper()
	at := firstAppearance(seen, "updates")
	require.GreaterOrEqual(t, at, 0.0, "the update check's loading row appeared (seen: %+v)", seen)
	delay := at - mark
	assert.GreaterOrEqual(t, delay, 100.0, "the loading row waits out the show delay rather than flashing")
	assert.Less(t, delay, 1500.0, "the loading row appears promptly")
}

// TestE2E_Loading_ASwitchDropsTheLeftContextsLoadingLine: switching away
// while the update check is out takes that check's line with it at once,
// and the check's later progress frames - it is still running server-side -
// never reach the context switched to. The target's status read is held
// so the window between the switch and the new context's own loads (which
// used to overwrite the stale line, and forever never did when that status
// read failed) stays open for as long as the scenario needs it.
func TestE2E_Loading_ASwitchDropsTheLeftContextsLoadingLine(t *testing.T) {
	f := newE2ELoadingFixture(t)
	statusGate := make(chan struct{})
	var openOnce sync.Once
	openStatus := func() { openOnce.Do(func() { close(statusGate) }) }
	t.Cleanup(openStatus)
	f.BaseURL = startE2EDelayingProxy(t, f.BaseURL, 0, func(r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/status" && r.URL.Query().Get("profile") == "other" {
			select {
			case <-statusGate:
			case <-r.Context().Done():
			}
		}
		return false
	})
	hold := f.Src.arm(t)

	const oldUpdatesDone = `performance.getEntriesByType("resource").some((e) =>
		e.name.includes("/api/v1/updates") && e.name.includes("profile=default") && e.name.includes("progress="))`
	// Every text the update check's line paints from the switch on - not
	// deduplicated against what it said before, because the stale line's
	// failure mode is coming BACK, unchanged, once the remounted view's show
	// delay has passed.
	const recordUpdatesLineJS = `(() => {
		window.__updatesLine = [];
		let last = null;
		const sample = () => {
			const text = document.querySelector('[data-pending-read="updates"]')?.textContent.trim() ?? null;
			if (text === last) return;
			last = text;
			if (text !== null) window.__updatesLine.push(text);
		};
		new MutationObserver(sample).observe(document.body,
			{subtree: true, childList: true, characterData: true, attributes: true});
	})()`
	var painted, names []string
	var lineWhileHeld string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`tr.mod-row`, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { hold.releaseFirst(); return nil }),
		pendingTextIncludes("updates", "(1/3)"),
		chromedp.Click(`[data-picker="profile"]`, chromedp.ByQuery),
		chromedp.Click(`li[data-profile="other"] .picker__item`, chromedp.ByQuery),
		pollUntil(`location.pathname.endsWith("/other")`),
		chromedp.Evaluate(recordUpdatesLineJS, nil),
		// The new context's status read is held, so nothing of it has
		// loaded: anything the line paints now is the left context's.
		// Long enough to outlast the show delay the remounted view would
		// wait before painting it again.
		chromedp.Sleep(600*time.Millisecond),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q)?.textContent ?? ""`, pendingReadSel("updates")), &lineWhileHeld),
		// Now let the left context's check run on: its remaining frames
		// and its answer arrive while the new context is still waiting.
		chromedp.ActionFunc(func(context.Context) error { hold.releaseRest(); return nil }),
		pollUntil(oldUpdatesDone),
		// Its frames precede its response on the server but travel on the
		// activity stream, so give that stream a moment to deliver them.
		chromedp.Sleep(300*time.Millisecond),
		chromedp.ActionFunc(func(context.Context) error { openStatus(); return nil }),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll("tr.mod-row")].map((el) => el.textContent.trim())`, &names),
		chromedp.Evaluate(`window.__updatesLine`, &painted),
	)

	assert.Empty(t, lineWhileHeld, "the left context's update-check line goes with the switch")
	assert.Contains(t, fmt.Sprint(names), "Delta Mod")
	for _, text := range painted {
		assert.False(t, strings.Contains(text, "/3)") || strings.Contains(text, "3 mods"),
			"the left context's check must not paint into the new one (painted: %q)", painted)
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Loading_AFastSwitchFlashesNothing is case (c): when every read
// answers inside the show delay, no loading state is ever painted.
func TestE2E_Loading_AFastSwitchFlashesNothing(t *testing.T) {
	f := newE2ELoadingFixture(t)

	var seen []loadingAppearance
	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		chromedp.Evaluate(recordLoadingAppearancesJS, nil),
		chromedp.Click(`[data-picker="profile"]`, chromedp.ByQuery),
		chromedp.Click(`li[data-profile="other"] .picker__item`, chromedp.ByQuery),
		pollUntil(`(document.querySelector(".mission-control h1")?.textContent ?? "").includes("other")`),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll("tr.mod-row")].map((el) => el.textContent)`, &names),
		chromedp.Evaluate(`window.__loadingSeen`, &seen),
	)

	assert.Contains(t, fmt.Sprint(names), "Delta Mod")
	assert.Empty(t, seen, "a switch whose reads all answer inside the delay paints no loading state")
	assert.Empty(t, f.BrowserErrors())
}

// recordRunningLabelsJS records every distinct label a RUNNING readout
// inside container paints, in order, from the moment it runs.
func recordRunningLabelsJS(container string) string {
	return fmt.Sprintf(`(() => {
	window.__runningLabels = [];
	let last = null;
	const sample = () => {
		const el = document.querySelector(%q + ' .job-progress:is([data-state="running"], [data-state="queued"]) .job-progress__text');
		if (!el) return;
		const text = el.textContent.trim();
		if (text === last) return;
		last = text;
		window.__runningLabels.push(text);
	};
	sample();
	new MutationObserver(sample).observe(document.body,
		{subtree: true, childList: true, attributes: true, characterData: true});
})()`, container)
}

// TestE2E_Loading_AJobNamesItselfFromItsFirstFrame is case (d): the control
// an update was started from becomes that job's readout, and from its very
// first paint it says what is running - "Starting…" or the job's own name -
// never an anonymous "Working…", then the phase and position core reports.
func TestE2E_Loading_AJobNamesItselfFromItsFirstFrame(t *testing.T) {
	f := newE2EFixtureWithSlowDeploy(t)

	var labels []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(hydrated, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(recordRunningLabelsJS(".app-bar"), nil),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		pollUntil(`window.__runningLabels.some((l) => l.includes("of 2"))`),
		chromedp.Evaluate(`window.__runningLabels`, &labels),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	require.NotEmpty(t, labels)
	assert.NotEmpty(t, labels[0], "the first paint says something (all labels: %q)", labels)
	assert.NotContains(t, labels, "Working…", "a running readout never goes anonymous")
	assert.Empty(t, f.BrowserErrors())
}

var _ source.UpdateProgressReporter = (*gatedUpdateSource)(nil)
