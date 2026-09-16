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
