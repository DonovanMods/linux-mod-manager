package serve_test

// #568: the dismissible "newer lmm" banner, in a real browser. The checker
// talks to an httptest stand-in for GitHub, never the real API.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// newE2EFixtureWithRelease serves a fixture whose release checker has
// already learned latest (current is 2.0.1); "" means no checker at all.
func newE2EFixtureWithRelease(t *testing.T, latest string) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)
	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))

	var rc *core.ReleaseChecker
	if latest != "" {
		gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":"v` + latest + `","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/v` + latest + `"}`))
		}))
		t.Cleanup(gh.Close)
		rc = core.NewReleaseChecker(core.ReleaseCheckOptions{
			ConfigDir: t.TempDir(),
			DataDir:   t.TempDir(),
			Version:   "2.0.1",
			APIBase:   gh.URL,
			Probe: &core.InstallProbe{
				Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
				LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
				Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
			},
		})
		_, err := rc.Check(t.Context(), true)
		require.NoError(t, err)
	}

	baseURL := startE2EServerWithReleases(t, svc, rc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}
}

func TestE2E_ReleaseBannerShowsAndDismissalPersistsPerVersion(t *testing.T) {
	f := newE2EFixtureWithRelease(t, "2.1.0")

	var text, href, rel, label string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="release-banner"]`, chromedp.ByQuery),
		textContent(`[data-testid="release-banner"]`, &text),
		chromedp.AttributeValue(`[data-testid="release-banner"] a`, "href", &href, nil, chromedp.ByQuery),
		chromedp.AttributeValue(`[data-testid="release-banner"] a`, "rel", &rel, nil, chromedp.ByQuery),
		chromedp.AttributeValue(`[data-action="dismiss-release-banner"]`, "aria-label", &label, nil, chromedp.ByQuery),
	)
	assert.Contains(t, text, "lmm 2.1.0 is available")
	assert.Contains(t, text, "you have 2.0.1")
	assert.Contains(t, text, "Release notes")
	assert.Equal(t, "https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0", href)
	assert.Equal(t, "noopener noreferrer", rel)
	assert.Equal(t, "Dismiss the lmm 2.1.0 notice", label)

	f.runInBrowser(t,
		chromedp.Click(`[data-action="dismiss-release-banner"]`, chromedp.ByQuery),
		waitGone(`[data-testid="release-banner"]`),
		// A reload keeps it hidden: the dismissal is remembered per version.
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`(async () => { await fetch("/api/v1/updates/lmm"); return true; })()`),
		// The banner's own fetch has long since landed; give its render a beat.
		chromedp.Sleep(300*time.Millisecond),
	)
	var stored string
	var present bool
	f.runInBrowser(t,
		chromedp.Evaluate(`localStorage.getItem("lmm.release-banner.dismissed")`, &stored),
		chromedp.Evaluate(`document.querySelector('[data-testid="release-banner"]') !== null`, &present),
	)
	assert.Equal(t, "2.1.0", stored)
	assert.False(t, present, "a dismissed version stays dismissed across a reload")

	// A newer latest brings it back.
	f.runInBrowser(t,
		chromedp.Evaluate(`localStorage.setItem("lmm.release-banner.dismissed", "2.0.9")`, nil),
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('[data-testid="release-banner"]') !== null`),
	)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ReleaseBannerAbsentWhenNothingNewer(t *testing.T) {
	for name, latest := range map[string]string{"up to date": "2.0.1", "no checker": ""} {
		t.Run(name, func(t *testing.T) {
			f := newE2EFixtureWithRelease(t, latest)
			var present bool
			f.runInBrowser(t,
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
				pollUntil(`(async () => { await fetch("/api/v1/updates/lmm"); return true; })()`),
				chromedp.Sleep(300*time.Millisecond),
				chromedp.Evaluate(`document.querySelector('[data-testid="release-banner"]') !== null`, &present),
			)
			assert.False(t, present)
			assert.Empty(t, f.BrowserErrors())
		})
	}
}
