package serve_test

// #586: Setup > Updates and the "lmm updated itself" banner, in a real
// browser. The checker talks to an httptest stand-in for GitHub, and the
// automatic update on record is written the way a finished `lmm
// self-update --auto` leaves it - nothing is downloaded or replaced.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// e2eSelfManagedProbe owns nothing: a self-managed lmm.
func e2eSelfManagedProbe() *core.InstallProbe {
	return &core.InstallProbe{
		Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	}
}

// newE2EFixtureForUpdates serves a fixture whose checker has learned latest
// (current is 2.0.1) and reads the service's own config.yaml, as
// production's does. config is written to that config.yaml and autoState
// to the checker's auto-update.json first, when not empty.
func newE2EFixtureForUpdates(t *testing.T, latest string, probe *core.InstallProbe, config, autoState string) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)
	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	if config != "" {
		require.NoError(t, os.WriteFile(filepath.Join(svc.ConfigDir(), "config.yaml"), []byte(config), 0o644))
	}
	dataDir := t.TempDir()
	if autoState != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, "auto-update.json"), []byte(autoState), 0o644))
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v` + latest + `","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/v` + latest + `"}`))
	}))
	t.Cleanup(gh.Close)
	rc := core.NewReleaseChecker(core.ReleaseCheckOptions{
		ConfigDir: svc.ConfigDir(),
		DataDir:   dataDir,
		Version:   "2.0.1",
		APIBase:   gh.URL,
		Probe:     probe,
	})
	_, err := rc.Check(t.Context(), true)
	require.NoError(t, err)

	baseURL := startE2EServerWithReleases(t, svc, rc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}
}

const updatesSwitch = `[data-action="toggle-auto-update"]`

func TestE2E_SetupUpdatesShowsTheFactsAndTheSwitchWritesAutoUpdate(t *testing.T) {
	f := newE2EFixtureForUpdates(t, "2.1.0", e2eSelfManagedProbe(), "", "")

	var current, latest, install, check, reason string
	var checked, disabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates"][data-applicable="true"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-current"]`, &current),
		textContent(`[data-testid="setup-updates-latest"]`, &latest),
		textContent(`[data-testid="setup-updates-install"]`, &install),
		textContent(`[data-testid="setup-updates-check"]`, &check),
		textContent(`[data-testid="setup-updates-reason"]`, &reason),
		chromedp.Evaluate(`document.querySelector('`+updatesSwitch+`').checked`, &checked),
		chromedp.Evaluate(`document.querySelector('`+updatesSwitch+`').disabled`, &disabled),
	)
	assert.Contains(t, current, "2.0.1")
	assert.Contains(t, latest, "2.1.0")
	assert.Contains(t, install, "Self-managed")
	assert.Contains(t, check, "On")
	assert.True(t, checked, "on by default")
	assert.False(t, disabled)
	assert.Contains(t, reason, "installs it by itself")

	f.runInBrowser(t,
		chromedp.Click(updatesSwitch, chromedp.ByQuery),
		pollUntil(`(() => { const el = document.querySelector('`+updatesSwitch+`'); return el && !el.checked && !el.disabled; })()`),
		textContent(`[data-testid="setup-updates-reason"]`, &reason),
	)
	assert.Contains(t, reason, "Off")
	data, err := os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_update: false")

	// A reload reads the written setting back.
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates"][data-applicable="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+updatesSwitch+`').checked`, &checked),
	)
	assert.False(t, checked)

	f.runInBrowser(t,
		chromedp.Click(updatesSwitch, chromedp.ByQuery),
		pollUntil(`(() => { const el = document.querySelector('`+updatesSwitch+`'); return el && el.checked && !el.disabled; })()`),
	)
	data, err = os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_update: true")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_SetupUpdatesExplainsAPackageInstall(t *testing.T) {
	f := newE2EFixtureForUpdates(t, "2.1.0", &core.InstallProbe{
		Executable: func() (string, error) { return "/usr/bin/lmm", nil },
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "pacman" {
				return []byte("lmm\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}, "", "")

	var install, reason string
	var disabled, checked bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		// Detection runs in the background as the server starts; a page
		// that loaded before it finished shows self-managed until reloaded.
		pollUntil(`(async () => {
			const st = await (await fetch("/api/v1/updates/lmm")).json();
			return st.install.method === "package";
		})()`),
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates"][data-applicable="false"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-install"]`, &install),
		textContent(`[data-testid="setup-updates-reason"]`, &reason),
		chromedp.Evaluate(`document.querySelector('`+updatesSwitch+`').disabled`, &disabled),
		chromedp.Evaluate(`document.querySelector('`+updatesSwitch+`').checked`, &checked),
	)
	assert.Contains(t, install, "pacman")
	assert.Contains(t, reason, "package manager updates it")
	assert.Contains(t, reason, "sudo pacman -Syu")
	assert.True(t, disabled, "a package's lmm is never updated by lmm, so there is nothing to switch")
	assert.False(t, checked)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_SetupUpdatesSaysTheCheckIsOff(t *testing.T) {
	f := newE2EFixtureForUpdates(t, "2.1.0", e2eSelfManagedProbe(), "update_check: false\n", "")
	var check, reason string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates"][data-applicable="true"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-check"]`, &check),
		textContent(`[data-testid="setup-updates-reason"]`, &reason),
	)
	assert.Contains(t, check, "Off")
	assert.Contains(t, reason, "release check is off")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ReleaseBannerAnnouncesAnAutomaticUpdateOnce(t *testing.T) {
	state := `{"last":{"from":"2.0.0","to":"2.0.1","at":"2026-10-30T03:00:00Z","first":true},"updates":1}`
	f := newE2EFixtureForUpdates(t, "2.0.1", e2eSelfManagedProbe(), "", state)

	var text string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="release-banner"][data-state="auto-updated"]`, chromedp.ByQuery),
		textContent(`[data-testid="release-banner"]`, &text),
	)
	assert.Contains(t, text, "lmm updated itself to v2.0.1.")
	assert.Contains(t, text, "To turn this off: auto_update: false in config.yaml, or Setup > Updates.")

	// Told once for every frontend, not once per browser: the banner tells
	// the server it showed it, so a browser with none of this one's storage
	// - and the CLI (TestAPIAutoUpdateNotice_MarksItToldForEveryFrontend) -
	// does not show it again, dismissed or not.
	var present bool
	f.runInBrowser(t,
		pollUntil(`performance.getEntriesByType("resource")
			.some((e) => new URL(e.name).pathname === "/api/v1/updates/lmm/notice")`),
		chromedp.Evaluate(`localStorage.clear()`, nil),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		releaseReadLanded(),
		chromedp.Evaluate(`document.querySelector('[data-testid="release-banner"]') !== null`, &present),
	)
	assert.False(t, present, "told once")

	var last string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates-last"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-last"]`, &last),
	)
	assert.Contains(t, last, "2.0.0 to 2.0.1")
	assert.Empty(t, f.BrowserErrors())
}
