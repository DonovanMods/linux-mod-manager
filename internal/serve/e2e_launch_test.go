package serve_test

// The Play button (#501): the top bar's, the drift modal its 409 opens, and
// the Health card's on a BepInEx "launch the game once" finding. Nothing
// here starts a process: every Service gets e2eLauncher's Launcher, whose
// Run records the command line it was handed.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eLauncher is a core.Launcher that launches nothing: Steam is "found",
// the session has a display and a user manager, and Run records each
// systemd-run command line.
type e2eLauncher struct {
	mu    sync.Mutex
	runs  [][]string
	appID string // the Steam app id found; "" finds none
	// hold, when set, keeps each Run from answering until it is closed: a
	// launch the browser can watch in flight.
	hold chan struct{}
}

func (f *e2eLauncher) install(svc *core.Service) {
	svc.SetLauncher(&core.Launcher{
		LookPath: func(file string) (string, error) {
			if file == "systemd-run" || file == "steam" {
				return "/usr/bin/" + file, nil
			}
			return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
		},
		Getenv: func(key string) string {
			if key == "DISPLAY" {
				return ":0"
			}
			return ""
		},
		Environ:    func() []string { return []string{"DISPLAY=:0"} },
		ManagerEnv: func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if f.hold != nil {
				<-f.hold
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.runs = append(f.runs, append([]string{name}, args...))
			return nil, nil
		},
		Detach: func([]string, []string, string) error {
			return errors.New("serve must never start a game detached")
		},
		SteamApp:   func(string) (core.SteamApp, bool) { return core.SteamApp{AppID: f.appID}, f.appID != "" },
		UnitSuffix: func() string { return "e2e00000" },
	})
}

func (f *e2eLauncher) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// steamURL is the argv's last member for e2eLauncher's app id.
func steamURL(appID string) string { return "steam://rungameid/" + appID }

// TestE2E_Play_DisabledWithItsReasonForAnUnconfiguredGame: a game lmm
// cannot launch keeps its Play button, aria-disabled and focusable, with
// the reason as its tooltip and its accessible description - and a click
// on it launches nothing.
func TestE2E_Play_DisabledWithItsReasonForAnUnconfiguredGame(t *testing.T) {
	f := newE2EFixture(t)
	l := &e2eLauncher{}
	l.install(f.Svc)

	var disabled, tooltip, description string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-action="play"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-action="play"]').getAttribute("aria-disabled") === "true"`),
		chromedp.AttributeValue(`[data-action="play"]`, "aria-disabled", &disabled, nil, chromedp.ByQuery),
		chromedp.AttributeValue(`[data-action="play"]`, "data-tooltip", &tooltip, nil, chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById(document.querySelector('[data-action="play"]').getAttribute("aria-describedby")).textContent`, &description),
		chromedp.Click(`[data-action="play"]`, chromedp.ByQuery),
	)

	assert.Equal(t, "true", disabled)
	assert.Contains(t, tooltip, "launch.command")
	assert.Equal(t, tooltip, description, "the tooltip is also the button's accessible description")
	assert.Empty(t, l.recorded(), "a disabled Play launches nothing")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Play_LaunchesAndSaysSo: a deployed profile launches on the first
// click, and the toast says Steam was asked.
func TestE2E_Play_LaunchesAndSaysSo(t *testing.T) {
	f := newE2EFixture(t)
	l := &e2eLauncher{appID: "892970"}
	l.install(f.Svc)

	var toast string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
		chromedp.Click(`[data-action="play"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		textContent(`.toast--success .toast__title`, &toast),
	)

	assert.Equal(t, "Launch requested: Fixture Game (via Steam)", toast)
	runs := l.recorded()
	require.Len(t, runs, 1)
	assert.Equal(t, steamURL("892970"), runs[0][len(runs[0])-1])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Play_NamesTheLaunchInFlight is issue 583's loading convention on
// the Play button: a launch still out past the show delay says what it is
// doing, and the button goes back to Play once it has answered.
func TestE2E_Play_NamesTheLaunchInFlight(t *testing.T) {
	f := newE2EFixture(t)
	l := &e2eLauncher{appID: "892970", hold: make(chan struct{})}
	release := sync.OnceFunc(func() { close(l.hold) })
	t.Cleanup(release)
	l.install(f.Svc)

	var busy, label string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
		chromedp.Click(`[data-action="play"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-action="play"]').textContent.trim() === "Starting Fixture Game…"`),
		chromedp.AttributeValue(`[data-action="play"]`, "aria-busy", &busy, nil, chromedp.ByQuery),
	)
	assert.Equal(t, "true", busy)

	release()
	f.runInBrowser(t,
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-action="play"]').textContent.trim() === "Play"`),
		textContent(`[data-action="play"]`, &label),
	)
	assert.Equal(t, "Play", label)
	assert.Len(t, l.recorded(), 1)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Play_DriftOffersDeployAndLaunch: Play on a profile with
// undeployed mods opens the drift modal instead of launching; Deploy &
// launch runs the deploy job and launches once it has succeeded - the
// deploy really happened, and the launch went through without force.
func TestE2E_Play_DriftOffersDeployAndLaunch(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	l := &e2eLauncher{appID: "892970"}
	l.install(f.Svc)

	var causes string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
		chromedp.Click(`[data-action="play"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="launch-drift"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="launch-drift"] .modal__body`, &causes),
	)
	assert.Contains(t, causes, "2 enabled mods aren't deployed.")
	assert.Empty(t, l.recorded(), "a drifted profile is not launched before the user chooses")

	var toast string
	f.runInBrowser(t,
		chromedp.Click(`[data-action="deploy-and-launch"]`, chromedp.ByQuery),
		waitGone(`.modal[data-kind="launch-drift"]`),
		pollUntil(`[...document.querySelectorAll(".toast__title")].some((el) => el.textContent.startsWith("Launch requested"))`),
		chromedp.Evaluate(`[...document.querySelectorAll(".toast__title")].find((el) => el.textContent.startsWith("Launch requested")).textContent`, &toast),
	)
	assert.Equal(t, "Launch requested: Fixture Game (via Steam)", toast)

	mods, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, f.Profile)
	require.NoError(t, err)
	for _, m := range mods {
		assert.True(t, m.Deployed, "the deploy ran before the launch: %s", m.ID)
	}
	runs := l.recorded()
	require.Len(t, runs, 1)
	assert.Equal(t, steamURL("892970"), runs[0][len(runs[0])-1])
	assert.Equal(t, 0, core.CountUndeployed(mods), "the launch found nothing to force past")
	// The 409 the modal answers is the one network error.
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/g1/launch")
}

// TestE2E_Play_DriftLaunchAnywaySendsForce: Launch anyway launches the
// drifted profile as it stands - with force, the only way the server
// launches past the drift - and deploys nothing.
func TestE2E_Play_DriftLaunchAnywaySendsForce(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	l := &e2eLauncher{appID: "892970"}
	l.install(f.Svc)

	var toastDetail string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
		chromedp.Click(`[data-action="play"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="launch-anyway"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="launch-anyway"]`, chromedp.ByQuery),
		waitGone(`.modal[data-kind="launch-drift"]`),
		chromedp.WaitVisible(`.toast--success .toast__detail`, chromedp.ByQuery),
		textContent(`.toast--success .toast__detail`, &toastDetail),
		// Focus went back to the control that opened the modal.
		pollUntil(`document.activeElement?.dataset.action === "play"`),
	)

	assert.Contains(t, toastDetail, "launching anyway", "the forced launch's warning reaches the toast")
	require.Len(t, l.recorded(), 1)
	mods, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, f.Profile)
	require.NoError(t, err)
	assert.Equal(t, 2, core.CountUndeployed(mods), "Launch anyway deploys nothing")
	// The 409 the modal answers is the one network error.
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/g1/launch")
}

// TestE2E_Play_DriftModalEscapeReturnsFocus: Escape closes the drift modal,
// launches nothing, and hands focus back to Play.
func TestE2E_Play_DriftModalEscapeReturnsFocus(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	l := &e2eLauncher{appID: "892970"}
	l.install(f.Svc)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
		chromedp.Focus(`[data-action="play"]`, chromedp.ByQuery),
		chromedp.KeyEvent("\r"),
		chromedp.WaitVisible(`.modal[data-kind="launch-drift"]`, chromedp.ByQuery),
		chromedp.KeyEvent("\x1b"),
		waitGone(`.modal[data-kind="launch-drift"]`),
		pollUntil(`document.activeElement?.dataset.action === "play"`),
	)
	assert.Empty(t, l.recorded())
	// The 409 the modal answers is the one network error.
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/g1/launch")
}

// TestE2E_Play_LoaderStaleFindingOffersPlay: the BepInEx "launch the game
// once to confirm" finding's remedy is launching, so its Health row offers
// Play - and it launches.
func TestE2E_Play_LoaderStaleFindingOffersPlay(t *testing.T) {
	f := newE2EFixture(t)
	f.Game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative}
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	write := func(rel string, at time.Time) {
		p := filepath.Join(f.Game.InstallPath, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(p, at, at))
	}
	// BepInEx ran an hour before the newest plugin was deployed.
	write(domain.BepInExPreloaderPath, time.Now().Add(-2*time.Hour))
	write("run_bepinex.sh", time.Now().Add(-2*time.Hour))
	write("libdoorstop.so", time.Now().Add(-2*time.Hour))
	write(domain.BepInExLogPath, time.Now().Add(-time.Hour))
	write("BepInEx/plugins/new.dll", time.Now())
	l := &e2eLauncher{appID: "892970"}
	l.install(f.Svc)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card__row[data-status="loader_stale_log"] [data-action="play-loader"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-action="play-loader"]').getAttribute("aria-disabled") === null`),
		chromedp.Click(`[data-action="play-loader"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
	)
	runs := l.recorded()
	require.Len(t, runs, 1)
	assert.Equal(t, steamURL("892970"), runs[0][len(runs[0])-1])
	assert.Empty(t, f.BrowserErrors())
}
