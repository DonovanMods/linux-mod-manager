package serve_test

// The browser scenarios for the enable/disable click acknowledgment (#432).
//
// This is a claim about what a BROWSER computes and nothing one layer down
// can stand in for it: it is about what is on screen DURING a request that
// has not come back yet - a state no document, plan or job summary ever
// carries.
//
// The harness (e2e_harness_test.go) is shared with the rest of the suite;
// what is new here is the GATED proxy below, which holds the toggle's own
// POST open until the test lets it go. Every other in-flight scenario in
// this suite buys its window with a sleeping hook or a fixed proxy delay,
// which is fine for "a job is running" but not for this one: the window
// #432 is about opens at the click and closes when the POST returns, so a
// test that merely raced a timer against it would be asserting on the
// machine's speed.

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// startE2EServerWithGatedToggle is startE2EServer behind a reverse proxy
// that holds POST /api/v1/mods/{source}/{id}/{enable,disable} open until the
// returned release is called - the deterministic version of
// startE2EServerWithDelayedJobStart's fixed sleep.
//
// The first `pass` toggle requests go straight through, which is how a
// scenario lets an earlier toggle run to its end (a failure, say) and then
// holds the NEXT one open. toggles counts every toggle request the proxy has
// seen, held or not, so a scenario can prove the click it is asserting about
// really did reach the wire.
//
// The Origin rewrite is the same one that proxy documents: originCheck
// (middleware.go) refuses a state-changing request whose Origin names
// anything but the backend's own address, and the browser's Origin is this
// proxy's.
//
// release is registered as a cleanup as well as returned. A test that fails
// its assertions and returns early must not leave the proxy sitting on a
// request the backend's own graceful shutdown would then wait out.
func startE2EServerWithGatedToggle(t *testing.T, svc *core.Service, pass int64) (baseURL string, release func(), toggles *atomic.Int64) {
	t.Helper()

	backend := startE2EServer(t, svc)
	backendURL, err := url.Parse(backend)
	require.NoError(t, err)

	proxy := &httputil.ReverseProxy{
		Transport: e2eProxyTransport(t),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(backendURL)
			r.Out.Host = backendURL.Host
			if r.Out.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", "http://"+backendURL.Host)
			}
		},
	}

	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)

	toggles = new(atomic.Int64)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/mods/{source}/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "enable", "disable":
			if toggles.Add(1) > pass {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	})
	mux.Handle("/", proxy)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyServer := &http.Server{Handler: mux}
	served := make(chan error, 1)
	go func() { served <- proxyServer.Serve(ln) }()
	t.Cleanup(func() {
		_ = proxyServer.Close()
		if err := <-served; err != nil && err != http.ErrServerClosed {
			t.Errorf("proxy server: %v", err)
		}
	})

	return "http://" + ln.Addr().String(), release, toggles
}

// newE2EFixtureWithAGatedToggle is newE2EFixtureWithDeployableMods behind
// that gate: two enabled, cached mods (Alpha/Beta) whose enable/disable
// requests do not reach the server until the test says so.
func newE2EFixtureWithAGatedToggle(t *testing.T) (e2eFixture, func()) {
	t.Helper()
	f, release, _ := newE2EGatedToggleFixture(t, newFakeSource("fake"), 0)
	seedDeployableMods(t, f.Svc, f.Game)
	return f, release
}

// newE2EGatedToggleFixture is the unseeded world behind that gate, letting
// the first `pass` toggle requests through. src is registered as the game's
// source, so a scenario that opens the slide-over or the full mod page can
// give its mods a catalog entry for those surfaces' live reads.
func newE2EGatedToggleFixture(t *testing.T, src *fakeSource, pass int64) (e2eFixture, func(), *atomic.Int64) {
	t.Helper()
	sandboxE2EEnv(t)

	svc, game := newFixtureServiceWithSource(t, src)
	baseURL, release, toggles := startE2EServerWithGatedToggle(t, svc, pass)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{
		Ctx:           ctx,
		BaseURL:       baseURL,
		Svc:           svc,
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}
	return f, release, toggles
}

// seedToggleMod installs one mod into the default profile, enabled or not.
// A mod seeded with no files has nothing in the cache, which is what makes
// its ENABLE fail for real: core.EnableMod deploys from the cache and
// refuses a mod it cannot find there. That is the failing toggle #432's
// retry scenarios need, reached through the engine rather than faked.
func seedToggleMod(t *testing.T, f e2eFixture, id, name string, enabled bool, files map[string][]byte) {
	t.Helper()
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: id, SourceID: "fake", Name: name, Version: "1.0", GameID: f.Game.ID},
		enabled, files)
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: id, Version: "1.0"}))
}

// awaitToggleRequests waits until the proxy has seen n toggle requests - the
// proof that a click reached the wire, which a held request otherwise gives
// no sign of.
func awaitToggleRequests(t *testing.T, toggles *atomic.Int64, n int64) {
	t.Helper()
	require.Eventually(t, func() bool { return toggles.Load() >= n },
		10*time.Second, 20*time.Millisecond,
		"the click must have sent toggle request %d", n)
}

// libraryToggleJS clicks the enabled checkbox of the library row whose text
// contains name - the same shape the row-menu scenarios use.
func libraryToggleJS(name string) string {
	return fmt.Sprintf(`Array.from(document.querySelectorAll(".mod-row"))
		.find((r) => r.textContent.includes(%q))
		.querySelector("td.col--enabled input").click();`, name)
}

// libraryRowStateJS reads everything one row's enabled cell can say about
// itself in a single evaluation, so the assertions below describe ONE
// instant rather than several.
func libraryRowStateJS(name string) string {
	return fmt.Sprintf(`(() => {
		const row = Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes(%q));
		if (!row) return null;
		const box = row.querySelector("td.col--enabled input");
		const live = row.querySelector('[data-testid="toggle-pending"]');
		return {
			checked: box.checked,
			disabled: box.disabled,
			busy: box.getAttribute("aria-busy") === "true",
			pending: row.classList.contains("mod-row--pending"),
			spinner: row.querySelector(".toggle-pending") !== null,
			live: live ? live.textContent.trim() : "",
		};
	})()`, name)
}

// e2eRowToggleState is libraryRowStateJS's shape.
type e2eRowToggleState struct {
	Checked  bool   `json:"checked"`
	Disabled bool   `json:"disabled"`
	Busy     bool   `json:"busy"`
	Pending  bool   `json:"pending"`
	Spinner  bool   `json:"spinner"`
	Live     string `json:"live"`
}

// TestE2E_LibraryRow_ToggleAcknowledgesTheClickImmediately is issue 432.
//
// The owner's report: "there is a long delay between the user clicking a
// checkbox and the screen visually showing the change. This leads the user
// to clicking multiple times thinking the change hasn't been accepted."
// The library row rendered `checked=${row.enabled}` - the SERVER's value -
// and merely disabled the box, so for the whole length of a real deploy the
// row looked exactly as it had before the click, and the repeated clicks the
// user then made were swallowed by the very `disabled` that was the only
// visible change.
//
// The window this asserts inside is the one that could not be seen at all
// before: the toggle's own POST is held open by the fixture's proxy, so
// there is no job id, no summary, no progress frame and no re-hydrate -
// nothing from the server whatsoever. Everything the row shows here it shows
// because the CLICK said so.
func TestE2E_LibraryRow_ToggleAcknowledgesTheClickImmediately(t *testing.T) {
	f, release := newE2EFixtureWithAGatedToggle(t)

	var pending e2eRowToggleState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
		// The acknowledgment, waited for rather than slept against: the
		// POST behind it cannot come back until release() below.
		chromedp.WaitVisible(`.mod-row--pending [data-testid="toggle-pending"]`, chromedp.ByQuery),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &pending),
	)

	assert.False(t, pending.Checked,
		"the box must move to the state the click ASKED for, not sit on the server's")
	assert.True(t, pending.Pending, "and the row must mark itself as not-yet-settled")
	assert.True(t, pending.Busy, "aria-busy is how that reaches a screen reader on the control itself")
	assert.True(t, pending.Spinner, "with a visible affordance beside the box")
	assert.Equal(t, "Disabling…", pending.Live,
		"and the row must say what it is doing, in the same words the running job will use")
	assert.True(t, pending.Disabled,
		"a second, contrary job over the same mod is still not offered - but the frozen box now holds the REQUESTED value")

	// Nothing has actually happened yet: the request the click made is still
	// sitting in the proxy. This is the whole of the defect - the UI was
	// telling the truth about the server and saying nothing about the user.
	alpha, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
	require.NoError(t, err)
	assert.True(t, alpha.Enabled,
		"the acknowledgment is on screen BEFORE the server has been asked, let alone answered")

	release()

	require.Eventually(t, func() bool {
		m, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		return err == nil && !m.Enabled
	}, 10*time.Second, 20*time.Millisecond, "the toggle must still actually disable the mod")

	// And the hand-off is seamless: once the real answer lands, the row drops
	// the pending marks and keeps the same value it has been showing all
	// along. A box that flicked back to "enabled" on the way would be the
	// same defect wearing different clothes.
	var settled e2eRowToggleState
	f.runInBrowser(t,
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 0`),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &settled),
	)
	assert.False(t, settled.Checked)
	assert.False(t, settled.Disabled, "the control is the user's again")
	assert.False(t, settled.Busy)
	assert.Empty(t, settled.Live)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryBatch_ToggleAcknowledgesEveryRowAtOnce is the batch half of
// issue 432. main.js#startBatchToggle runs one job per mod strictly one at a
// time (the wire's own sequencing caveat), so on a long selection the last
// row used to sit visually untouched for the entire run - the same silence
// the single row had, multiplied by the size of the selection.
func TestE2E_LibraryBatch_ToggleAcknowledgesEveryRowAtOnce(t *testing.T) {
	f, release := newE2EFixtureWithAGatedToggle(t)

	selectRowJS := func(name string) string {
		return fmt.Sprintf(`Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes(%q))
			.querySelector("td.col--select input").click();`, name)
	}

	var alpha, beta e2eRowToggleState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(selectRowJS("Alpha Mod"), nil),
		chromedp.Evaluate(selectRowJS("Beta Mod"), nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
		// BOTH rows, while the FIRST one's own POST is still held open.
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 2`),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
		chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &beta),
	)

	assert.False(t, alpha.Checked)
	assert.False(t, beta.Checked,
		"the second row of a sequenced batch acknowledges on the CLICK, not when its own job finally gets its turn")
	assert.Equal(t, "Disabling…", beta.Live)

	release()

	require.Eventually(t, func() bool {
		for _, id := range []string{"a", "b"} {
			m, err := f.Svc.GetInstalledMod(t.Context(), "fake", id, f.Game.ID, "default")
			if err != nil || m.Enabled {
				return false
			}
		}
		return true
	}, 15*time.Second, 20*time.Millisecond, "the batch must still actually disable both mods")

	f.runInBrowser(t, pollUntil(`document.querySelectorAll(".mod-row--pending").length === 0`))
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryRow_RetryAfterAFailedToggleIsAcknowledged is the retry half
// of issue 432, and the path where the defect mattered most: a user whose
// toggle has just failed is the one most likely to click again.
//
// The acknowledgment used to settle a request against whatever job its
// control's origin last named. That origin is stable across the toggle's
// direction on purpose, and a FAILED job's binding is never released, so the
// second click was settled on the spot by the first click's failure: the box
// did not move, nothing was disabled and the row said nothing for the whole
// second request - the very silence #432 was filed about.
func TestE2E_LibraryRow_RetryAfterAFailedToggleIsAcknowledged(t *testing.T) {
	// The first toggle request goes through, so its job can fail; the retry is
	// held open.
	f, release, toggles := newE2EGatedToggleFixture(t, newFakeSource("fake"), 1)
	seedToggleMod(t, f, "c", "Gamma Mod", false, nil)

	var afterFailure e2eRowToggleState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(libraryToggleJS("Gamma Mod"), nil),
		// Nothing is cached, so the enable fails, the row goes back to what is
		// true and the failure lands as a toast - the row's bare checkbox has
		// no inline surface of its own.
		pollUntil(`document.querySelectorAll(".toast").length > 0`),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 0`),
		chromedp.Evaluate(libraryRowStateJS("Gamma Mod"), &afterFailure),
	)
	require.False(t, afterFailure.Checked, "a failed enable puts the box back")
	require.False(t, afterFailure.Disabled, "and hands the control back")

	var retry e2eRowToggleState
	f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Gamma Mod"), nil))
	awaitToggleRequests(t, toggles, 2)
	f.runInBrowser(t,
		// Long enough for any effect that WOULD settle the request to have
		// run: the retry's own request is held, so nothing from the server
		// can legitimately settle it inside this window.
		settleEffects(),
		chromedp.Evaluate(libraryRowStateJS("Gamma Mod"), &retry),
	)
	assert.True(t, retry.Pending,
		"the retry is acknowledged like any other click, not settled by the previous job's failure (row: %+v)", retry)
	assert.True(t, retry.Checked, "the box holds the value the retry asked for")
	assert.True(t, retry.Disabled, "and a contrary request is still not offered")
	assert.Equal(t, "Enabling…", retry.Live)

	// The retry fails as well - still nothing to deploy - and it is THAT
	// job, the retry's own, that settles it.
	release()
	var settled e2eRowToggleState
	f.runInBrowser(t,
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 0`),
		chromedp.Evaluate(libraryRowStateJS("Gamma Mod"), &settled),
	)
	assert.False(t, settled.Checked, "the retry's own failure puts the box back")
	assert.False(t, settled.Disabled)
	assert.Empty(t, settled.Live)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryBatch_RetryAfterAFailedBatchIsAcknowledged is the same
// defect on the batch bar, where it was deterministic for every row after
// the first: a sequenced batch binds each row's job only when that row's turn
// comes, so for the whole length of the batch the only binding a waiting row
// had was the one its PREVIOUS, failed job left behind.
func TestE2E_LibraryBatch_RetryAfterAFailedBatchIsAcknowledged(t *testing.T) {
	// The first batch's two requests go through and both fail; the second
	// batch's first request is held, so its second never starts at all.
	f, release, toggles := newE2EGatedToggleFixture(t, newFakeSource("fake"), 2)
	seedToggleMod(t, f, "c", "Gamma Mod", false, nil)
	seedToggleMod(t, f, "d", "Delta Mod", false, nil)

	batchEnableJS := `(() => {
		for (const row of document.querySelectorAll(".mod-row")) {
			const box = row.querySelector("td.col--select input");
			if (!box.checked) box.click();
		}
	})()`

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(batchEnableJS, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-enable"]`, chromedp.ByQuery),
		// The end-of-batch toast is the batch's own "all done".
		pollUntil(`Array.from(document.querySelectorAll(".toast")).some((t) => t.textContent.includes("Enabled 0/2"))`),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 0`),
	)

	f.runInBrowser(t,
		chromedp.Evaluate(batchEnableJS, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-enable"]`, chromedp.ByQuery),
	)
	awaitToggleRequests(t, toggles, 3)

	var gamma, delta e2eRowToggleState
	f.runInBrowser(t,
		settleEffects(),
		chromedp.Evaluate(libraryRowStateJS("Gamma Mod"), &gamma),
		chromedp.Evaluate(libraryRowStateJS("Delta Mod"), &delta),
	)
	for name, row := range map[string]e2eRowToggleState{"Gamma Mod": gamma, "Delta Mod": delta} {
		assert.True(t, row.Pending, "%s: the re-run batch acknowledges every row (row: %+v)", name, row)
		assert.True(t, row.Checked, "%s: holding the value the batch asked for", name)
		assert.Equal(t, "Enabling…", row.Live, name)
	}

	release()
	f.runInBrowser(t,
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 0`),
	)
	assert.Empty(t, f.BrowserErrors())
}

// slideOverToggleJS reads the slide-over's Enable/Disable button.
const slideOverToggleJS = `(() => {
	const b = document.querySelector('.slide-over [data-action="toggle"]');
	return b ? {label: b.textContent.trim(), disabled: b.disabled} : null;
})()`

// e2eToggleButton is slideOverToggleJS's (and modPageToggleJS's) shape.
type e2eToggleButton struct {
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// newE2EFixtureForStepping is the two-mod world the step-away scenarios use:
// Alpha ENABLED and Beta DISABLED, both cached and both in the catalog (the
// slide-over and the full mod page make live reads for a mod). The pairing is
// deliberate - disabling Alpha asks for exactly the value Beta already
// reads, which is what exposed a control that answered for the wrong mod.
func newE2EFixtureForStepping(t *testing.T) (e2eFixture, func()) {
	t.Helper()
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0"}})
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0"}})
	f, release, _ := newE2EGatedToggleFixture(t, src, 0)
	seedToggleMod(t, f, "a", "Alpha Mod", true, map[string][]byte{"alpha.pak": []byte("alpha")})
	seedToggleMod(t, f, "b", "Beta Mod", false, map[string][]byte{"beta.pak": []byte("beta")})
	return f, release
}

// TestE2E_SlideOver_SteppingAwayKeepsTheInFlightToggle is issue 432 on the
// slide-over's ←/→. The panel is not re-mounted by a step - the same
// component renders the next mod - so what it remembers about Alpha's
// request survives the step, and the question is only whether it keeps
// asking about ALPHA. It asked about whichever mod was on screen, so a step
// onto a mod that already read "disabled" settled Alpha's disable, and
// stepping back offered a live Disable over a job still in flight.
func TestE2E_SlideOver_SteppingAwayKeepsTheInFlightToggle(t *testing.T) {
	f, release := newE2EFixtureForStepping(t)

	var inFlight, back e2eToggleButton
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		chromedp.WaitVisible(`.slide-over [data-action="toggle"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.slide-over [data-action="toggle"]').click()`, nil),
		pollUntil(`document.querySelector('.slide-over [data-action="toggle"]').textContent.includes("Disabling")`),
		chromedp.Evaluate(slideOverToggleJS, &inFlight),
		chromedp.Evaluate(`document.querySelector('.slide-over [aria-label="Next mod"]').click()`, nil),
		pollUntil(`document.querySelector('.slide-over').getAttribute("aria-label") === "Beta Mod details"`),
		settleEffects(),
		chromedp.Evaluate(`document.querySelector('.slide-over [aria-label="Previous mod"]').click()`, nil),
		pollUntil(`document.querySelector('.slide-over').getAttribute("aria-label") === "Alpha Mod details"`),
		settleEffects(),
		chromedp.Evaluate(slideOverToggleJS, &back),
	)

	require.Equal(t, "Disabling…", inFlight.Label, "the click is acknowledged on Alpha")
	assert.Equal(t, "Disabling…", back.Label,
		"stepping away and back does not forget Alpha's request (button: %+v)", back)
	assert.True(t, back.Disabled, "and a second, contrary request is still not offered")

	release()
	require.Eventually(t, func() bool {
		m, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		return err == nil && !m.Enabled
	}, 10*time.Second, 20*time.Millisecond, "the request still disables Alpha")
	f.runInBrowser(t, waitGone(`.slide-over [data-action="toggle"][disabled]`))
	assert.Empty(t, f.BrowserErrors())
}

// modPageToggleJS reads the full mod page's Enable/Disable button, once the
// page on screen is the one for the mod named.
func modPageToggleJS(name string) string {
	return fmt.Sprintf(`(() => {
		const title = document.querySelector(".mod-page__title");
		const b = document.querySelector('.mod-page [data-action="toggle"]');
		if (!title || title.textContent.trim() !== %q || !b) return null;
		return {label: b.textContent.trim(), disabled: b.disabled};
	})()`, name)
}

// clientNavigateJS moves the SPA to path the way router.js#navigate does -
// pushState plus a popstate - so the page's components are re-rendered
// rather than re-mounted by a full load.
func clientNavigateJS(path string) string {
	return fmt.Sprintf(`history.pushState(null, "", %q); window.dispatchEvent(new PopStateEvent("popstate"));`, path)
}

// TestE2E_FullModPage_NavigatingAwayKeepsTheInFlightToggle is the same
// defect on the full mod page, which is also re-rendered rather than
// re-mounted when one mod page leads to another.
func TestE2E_FullModPage_NavigatingAwayKeepsTheInFlightToggle(t *testing.T) {
	f, release := newE2EFixtureForStepping(t)

	var inFlight, back e2eToggleButton
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		pollUntil(modPageToggleJS("Alpha Mod")+` !== null`),
		chromedp.Evaluate(`document.querySelector('.mod-page [data-action="toggle"]').click()`, nil),
		pollUntil(`document.querySelector('.mod-page [data-action="toggle"]').textContent.includes("Disabling")`),
		chromedp.Evaluate(modPageToggleJS("Alpha Mod"), &inFlight),
		chromedp.Evaluate(clientNavigateJS(f.ModPagePath("fake", "b")), nil),
		pollUntil(modPageToggleJS("Beta Mod")+` !== null`),
		settleEffects(),
		chromedp.Evaluate(clientNavigateJS(f.ModPagePath("fake", "a")), nil),
		pollUntil(modPageToggleJS("Alpha Mod")+` !== null`),
		settleEffects(),
		chromedp.Evaluate(modPageToggleJS("Alpha Mod"), &back),
	)

	require.Equal(t, "Disabling…", inFlight.Label, "the click is acknowledged on Alpha's page")
	assert.Equal(t, "Disabling…", back.Label,
		"visiting another mod's page and coming back does not forget Alpha's request (button: %+v)", back)
	assert.True(t, back.Disabled)

	release()
	f.runInBrowser(t, pollUntil(`(() => {
		const b = document.querySelector('.mod-page [data-action="toggle"]');
		return b !== null && b.textContent.trim() === "Enable" && !b.disabled;
	})()`))
	assert.Empty(t, f.BrowserErrors())
}

// pendingRowContrastJS measures, in the browser, the lowest text contrast on
// the pending library row and the contrast of its spinner ring - composited
// the way the browser paints them, opacity included.
//
// contrast_test.go certifies token PAIRS and so cannot see a rule that
// re-composites a certified pair at partial opacity; this can. Disabled form
// controls are skipped, as WCAG 1.4.3 exempts them.
const pendingRowContrastJS = `(() => {
	const row = document.querySelector(".mod-row--pending");
	if (!row) return null;
	const parse = (c) => {
		const m = /rgba?\(([^)]+)\)/.exec(c);
		if (!m) return null;
		const p = m[1].split(/[\s,\/]+/).filter(Boolean).map(Number);
		return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1];
	};
	const opacity = (el) => {
		let o = 1;
		for (let e = el; e; e = e.parentElement) o *= Number(getComputedStyle(e).opacity);
		return o;
	};
	const background = (el) => {
		for (let e = el; e; e = e.parentElement) {
			const c = parse(getComputedStyle(e).backgroundColor);
			if (c && c[3] > 0) return c;
		}
		return [255, 255, 255, 1];
	};
	const lum = (c) => {
		const ch = c.slice(0, 3).map((v) => {
			v /= 255;
			return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
		});
		return 0.2126 * ch[0] + 0.7152 * ch[1] + 0.0722 * ch[2];
	};
	const ratio = (fg, bg, o) => {
		const mixed = fg.slice(0, 3).map((v, i) => v * o + bg[i] * (1 - o));
		const [a, b] = [lum(mixed), lum(bg)].sort((x, y) => y - x);
		return (a + 0.05) / (b + 0.05);
	};
	let text = Infinity, worst = "";
	for (const el of row.querySelectorAll("*")) {
		if (el.getClientRects().length === 0) continue;
		if (el.closest("button:disabled, input:disabled")) continue;
		const own = Array.from(el.childNodes).some((n) => n.nodeType === 3 && n.textContent.trim() !== "");
		if (!own) continue;
		const r = ratio(parse(getComputedStyle(el).color), background(el), opacity(el));
		if (r < text) { text = r; worst = el.className + ": " + el.textContent.trim().slice(0, 30); }
	}
	const ring = row.querySelector(".toggle-pending");
	const indicator = ring
		? ratio(parse(getComputedStyle(ring).borderLeftColor), background(ring), opacity(ring))
		: 0;
	return {text, worst, indicator};
})()`

// e2ePendingContrast is pendingRowContrastJS's shape.
type e2ePendingContrast struct {
	Text      float64 `json:"text"`
	Worst     string  `json:"worst"`
	Indicator float64 `json:"indicator"`
}

// TestE2E_LibraryRow_PendingRowKeepsItsContrast is issue 432's a11y half.
// The pending row was marked by dimming it to 75% opacity, which took every
// text token on it below WCAG AA in both themes - the "Enabling…" line
// itself, the one piece of text that exists to say the click landed, went
// from 5.55:1 to 3.39:1 - while the token ratchet, which measures pairs,
// stayed green.
func TestE2E_LibraryRow_PendingRowKeepsItsContrast(t *testing.T) {
	f, release := newE2EFixtureWithAGatedToggle(t)
	defer release()

	var light, dark e2ePendingContrast
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
		chromedp.WaitVisible(`.mod-row--pending [data-testid="toggle-pending"]`, chromedp.ByQuery),
		chromedp.Evaluate(pendingRowContrastJS, &light),
		chromedp.Evaluate(`document.documentElement.setAttribute("data-theme", "dark")`, nil),
		chromedp.Evaluate(pendingRowContrastJS, &dark),
	)

	for name, got := range map[string]e2ePendingContrast{"light": light, "dark": dark} {
		assert.GreaterOrEqualf(t, got.Text, 4.5,
			"%s: every piece of text on a pending row stays at WCAG AA (worst: %s)", name, got.Worst)
		assert.GreaterOrEqualf(t, got.Indicator, 3.0,
			"%s: the spinner ring is a non-text indicator and holds SC 1.4.11's 3:1", name)
	}
	assert.Empty(t, f.BrowserErrors())
}
