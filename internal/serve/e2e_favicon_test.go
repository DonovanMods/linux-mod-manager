package serve_test

// The browser half of the tab icon (issue 435).
//
// The httptest suite (favicon_internal_test.go) pins the route, the content
// type and the shell's <link>. What only a browser can answer is whether the
// Content-Security-Policy actually admits the thing: a refused sub-resource
// is a console entry and a green Go suite, which is the exact failure mode
// this harness exists for.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_FaviconLoadsUnderTheContentSecurityPolicy(t *testing.T) {
	f := newE2EFixture(t)

	var icon struct {
		Href        string `json:"href"`
		Type        string `json:"type"`
		Status      int    `json:"status"`
		ContentType string `json:"contentType"`
		HasSVG      bool   `json:"hasSvg"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		// Fetched from the PAGE, so the policy that governs the page is the
		// one that decides - exactly as it does for the browser chrome's own
		// request for the same URL.
		chromedp.Evaluate(`(async () => {
			const link = document.querySelector('link[rel="icon"]');
			const res = await fetch(link.href);
			return {
				href: new URL(link.href).pathname,
				type: link.getAttribute("type"),
				status: res.status,
				contentType: res.headers.get("content-type"),
				hasSvg: (await res.text()).includes("<svg"),
			};
		})()`, &icon, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Equal(t, "/static/favicon.svg", icon.Href)
	assert.Equal(t, "image/svg+xml", icon.Type)
	assert.Equal(t, 200, icon.Status)
	assert.Contains(t, icon.ContentType, "image/svg+xml")
	assert.True(t, icon.HasSVG, "and what came back really is the mark")
	assert.Empty(t, f.BrowserErrors(),
		"a policy that refused the icon would say so here and nowhere else")
}
