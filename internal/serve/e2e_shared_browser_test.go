package serve_test

// #577: the suite shares one Chromium, and each test gets a tab in a fresh
// browser context of it. These pin what that sharing must not cost: a test
// sees nothing another left behind, and a browser that dies takes down only
// the test it died in.

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_SharedBrowserIsolatesEachTab opens two tabs on one server - one
// origin - and proves the second inherits neither the first's storage nor
// its error log.
func TestE2E_SharedBrowserIsolatesEachTab(t *testing.T) {
	f := newE2EFixture(t)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`localStorage.setItem("e2e-577", "left behind"); document.cookie = "e2e577=left; path=/"; setTimeout(() => { throw new Error("e2e-577 first tab") }); true`, nil),
		pollUntil(`localStorage.getItem("e2e-577") === "left behind"`),
	)
	require.Eventually(t, func() bool { return len(f.BrowserErrors()) > 0 }, 5*time.Second, 50*time.Millisecond,
		"the first tab's own error reaches its own log")

	second, secondErrors := newE2EBrowser(t)
	var stored, cookie string
	var hasStored bool
	runE2EActions(t, second, []chromedp.Action{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`localStorage.getItem("e2e-577") !== null`, &hasStored),
		chromedp.Evaluate(`localStorage.getItem("e2e-577") ?? ""`, &stored),
		chromedp.Evaluate(`document.cookie`, &cookie),
	})

	assert.False(t, hasStored, "a fresh browser context starts with empty storage, got %q", stored)
	assert.NotContains(t, cookie, "e2e577", "nor any cookie")
	assert.Empty(t, secondErrors(), "and none of the first tab's errors")
}

// TestE2E_SharedBrowserIsReplacedAfterACrash kills the shared browser out
// from under a test and proves the next tab opens anyway, on a new one.
func TestE2E_SharedBrowserIsReplacedAfterACrash(t *testing.T) {
	f := newE2EFixture(t)
	f.runInBrowser(t, chromedp.Navigate(f.HomePath()))

	browser := chromedp.FromContext(f.Ctx).Browser
	process := browser.Process()
	require.NotNil(t, process, "the shared browser is one this harness launched")
	require.NoError(t, process.Kill())
	select {
	case <-browser.LostConnection:
	case <-time.After(e2eTimeout):
		t.Fatal("the killed browser's DevTools connection never dropped")
	}

	next, nextErrors := newE2EBrowser(t)
	runE2EActions(t, next, []chromedp.Action{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
	})
	assert.NotSame(t, browser, chromedp.FromContext(next).Browser, "a new browser, not the dead one")
	assert.Empty(t, nextErrors())
}
