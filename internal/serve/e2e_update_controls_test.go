package serve_test

// The browser scenarios for the update controls (#417).
//
// The owner's hand-test note was not that updating was impossible - it was
// that nothing read as THE update action: the Updates card needed a
// selection first, the batch bar only exists once rows are ticked, and the
// per-mod one was a click deep inside the ⋯ menu. So these scenarios are
// about what is REACHABLE from the screen as it first renders, which is a
// question about the rendered page and nothing else.

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// startE2EServerCountingUpdateRefreshes is startE2EServer behind a proxy
// that counts the update reads asking for a REFRESH - GET /api/v1/updates
// with ?refresh=1, the parameter that bypasses a source's own metadata
// cache (api.go#handleAPIUpdates).
//
// Counted rather than merely observed because the distinction is the whole
// point of the control: an ordinary hydrate reads the same endpoint without
// it, so "the page fetched updates" proves nothing about the button.
func startE2EServerCountingUpdateRefreshes(t *testing.T, svc *core.Service) (baseURL string, refreshes *atomic.Int64) {
	t.Helper()

	backend := startE2EServer(t, svc)
	backendURL, err := url.Parse(backend)
	require.NoError(t, err)

	var count atomic.Int64
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/updates", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("refresh") == "1" {
			count.Add(1)
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

	return "http://" + ln.Addr().String(), &count
}

// TestE2E_UpdateControlsAreVisibleWithoutASelection is issue 417's headline:
// the library header carries both halves - an explicit check, and an
// "Update all" that states how many mods it is about to touch - and neither
// needs a row ticked first.
func TestE2E_UpdateControlsAreVisibleWithoutASelection(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var header struct {
		Check    string `json:"check"`
		All      string `json:"all"`
		Disabled bool   `json:"disabled"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__toolbar [data-action="update-all"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library__toolbar [data-action="update-all"]').textContent.includes("1")`),
		chromedp.Evaluate(`({
			check: document.querySelector('.library__toolbar [data-action="check-updates"]').textContent.trim(),
			all: document.querySelector('.library__toolbar [data-action="update-all"]').textContent.trim(),
			disabled: document.querySelector('.library__toolbar [data-action="update-all"]').disabled,
		})`, &header),
	)

	assert.Equal(t, "Check for updates", header.Check)
	assert.Equal(t, "Update all (1)", header.All,
		"the count is on the control, so the size of the batch is known before the confirm modal states it")
	assert.False(t, header.Disabled)

	// And it plans exactly that batch.
	var title string
	f.runInBrowser(t,
		chromedp.Click(`.library__toolbar [data-action="update-all"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="updates"] .modal__title`, &title),
	)
	assert.Equal(t, "Update 1 mod", title)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryRow_UpdateIsVisibleRatherThanInTheMenu is the per-mod half.
// The ⋯ menu's own Update is gone: two controls doing one thing on the same
// row is how the original became invisible.
func TestE2E_LibraryRow_UpdateIsVisibleRatherThanInTheMenu(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var rowButton, ariaLabel string
	var menuItems []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.mod-row [data-action="row-update"]') !== null`),
		chromedp.Evaluate(`document.querySelector('.mod-row [data-action="row-update"]').textContent.trim()`, &rowButton),
		chromedp.Evaluate(`document.querySelector('.mod-row [data-action="row-update"]').getAttribute("aria-label")`, &ariaLabel),
		chromedp.Evaluate(openRowMenuJS("Better Boots"), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".row-menu__item")).map((b) => b.textContent.trim())`, &menuItems),
	)

	assert.Equal(t, "Update", rowButton)
	assert.Contains(t, ariaLabel, "Better Boots",
		"the accessible name names the mod, since the visible word cannot")
	assert.NotContains(t, menuItems, "Update",
		"the row's Update lives in the open now, not behind ⋯")
	assert.Contains(t, menuItems, "Uninstall", "the rest of the menu is untouched")

	// The visible button opens the same single-mod plan the menu used to.
	var title string
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.Escape), // close the menu
		chromedp.Click(`.mod-row [data-action="row-update"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="updates"] .modal__title`, &title),
	)
	assert.Equal(t, "Update Better Boots", title)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CheckForUpdatesAsksTheSourceAgain pins the one thing the control
// does that the page's own hydrate does not: ?refresh=1, which bypasses a
// source's metadata cache. Without it the button would be a re-read of an
// answer the source is holding onto - which, for the Steam Workshop source's
// hours-long cache of Valve's keyless replies, is exactly nothing.
func TestE2E_CheckForUpdatesAsksTheSourceAgain(t *testing.T) {
	f := newE2EFixtureWithAttention(t)
	// Re-point the fixture at a proxy that can see the difference. The
	// browser has not navigated yet, so nothing has been fetched from the
	// direct address.
	baseURL, refreshes := startE2EServerCountingUpdateRefreshes(t, f.Svc)
	f.BaseURL = baseURL

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__toolbar [data-action="check-updates"]`, chromedp.ByQuery),
	)
	require.Zero(t, refreshes.Load(),
		"an ordinary hydrate reads the same endpoint WITHOUT the refresh - the memo is there to be used")

	f.runInBrowser(t,
		chromedp.Click(`.library__toolbar [data-action="check-updates"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library__toolbar [data-action="check-updates"]').disabled === false`),
	)
	assert.Equal(t, int64(1), refreshes.Load(),
		"pressing it asks the source again, which is the whole of what it adds")
	assert.Empty(t, f.BrowserErrors())
}
