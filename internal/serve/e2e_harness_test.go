package serve_test

// The browser end-to-end harness (docs/plans/2026-08-31-webui-impl.md
// §Global Constraints: "chromedp E2E in Go (test-only dep; t.Skip when no
// Chrome/Chromium on PATH - the 7z pattern) for SPA flows").
//
// Why a real browser at all, when every endpoint already has an httptest
// suite. Because there is no bundler by design, and nothing else in this
// repo executes the application: a broken import path, a Content-Security-
// Policy that refuses the shell's own inline script, a token set that
// resolves to nothing - all three are a blank page in a browser and a green
// suite everywhere else. This harness is the only thing that runs the code
// the way a user does.
//
// chromedp is a TEST-ONLY dependency: nothing outside a _test.go file
// imports it, so it never reaches the shipped binary, and internal/serve's
// import-boundary ratchet (boundary_test.go, which lists the package's
// non-test imports) is unaffected. No browser is downloaded - the harness
// drives one already installed, and skips when there is none, exactly as
// the .7z/.rar extraction tests skip without the system 7z.
//
// This file is the reusable half; e2e_test.go holds the scenarios, and
// later units add their own beside them.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// e2eShutdownGrace is how long a browser fixture's server waits for
// in-flight requests when the test tears it down. Slightly under
// production's own default (10s): the SPA holds a session-long EventSource
// on GET /api/v1/events (activity.js), and Shutdown has to outlast it
// actually closing.
//
// It used to be 20s, to absorb a browser re-establishing that EventSource
// once or twice while it was torn down - each reconnect making the
// connection active again just as Shutdown was about to finish. That
// symptom is I1's finding: newE2EBrowser's chromedp.Cancel call, meant to
// wait for the whole browser PROCESS to exit before this grace window even
// starts, was silently falling back to a non-waiting cancel on every run
// (a context-derivation bug, fixed at newE2EBrowser). With the real wait
// restored, the reconnect this grace existed to absorb no longer happens:
// 5s held green over 3 consecutive full `-race` E2E runs (unit3 fix wave,
// #329) with room to spare (~37-38s total each), so it came back down.
//
// It is a TEST allowance and nothing else: Shutdown still returns the
// instant the connection really closes (microseconds, in the ordinary
// case), so this costs no wall clock on a healthy teardown, and the
// server's real shutdown semantics are pinned by their own tests
// (TestServer_ServeCancelsJobsOnceTheGraceExpires and friends), not here.
const e2eShutdownGrace = 5 * time.Second

// e2eShutdownCleanupGuard bounds how long a fixture's own t.Cleanup waits
// for Serve to return, once cancel() has told it to. It MUST exceed
// e2eShutdownGrace: Serve itself can legitimately take up to that long
// (a genuine grace exhaustion), and a guard shorter than the thing it is
// guarding fires first - reporting "the E2E server did not shut down" and
// never reaching the require.NoError(t, err) on Serve's own real error,
// which is the diagnostic this exists to surface. The +5s is slack for the
// goroutine scheduling and channel send between Serve returning and the
// cleanup's select observing it, not a second grace period.
const e2eShutdownCleanupGuard = e2eShutdownGrace + 5*time.Second

// TestE2EShutdownCleanupGuardExceedsGrace pins M1's invariant directly,
// without needing an actual 20+-second shutdown to observe it: a guard that
// does not outlast the grace it is guarding fires first on a genuine grace
// exhaustion, misreporting it as "the E2E server did not shut down" instead
// of reaching Serve's own real error. Before the fix this was a bare
// `15 * time.Second` literal, unrelated to e2eShutdownGrace's 20s - this
// test is what would have caught that drift.
func TestE2EShutdownCleanupGuardExceedsGrace(t *testing.T) {
	require.Greater(t, e2eShutdownCleanupGuard, e2eShutdownGrace)
}

// e2eTimeout bounds one chromedp.Run. Generous, because a cold browser
// start is the slowest thing in this package by an order of magnitude, and
// a timeout here should mean "the page is broken", never "the machine is
// busy". Raised from 30s (N-12, epic re-review = I-1 partial): I-1's own
// chromedp.Poll rewrite removed the sleep-shaped wait it was filed against,
// but the suite-wide ceiling itself was untouched, so a busy CI box could
// still time a browser step out on nothing more than contention. This is
// the ceiling only - no per-test sleeps.
const e2eTimeout = 60 * time.Second

// e2eLmmVersion is the fixed Options.Version every E2E-driven server
// carries, so N-5's own scenario (the shortcuts help names the running
// version) has something real to assert against.
const e2eLmmVersion = "2.0.0 (e2e-test)"

// chromeCandidates are the browser binaries the harness probes, in
// preference order. Named here rather than left to chromedp's own search so
// the skip message can say exactly what was looked for.
var chromeCandidates = []string{
	"chromium",
	"chromium-browser",
	"google-chrome",
	"google-chrome-stable",
	"chrome",
	"headless-shell",
}

// chromeBinary returns the first candidate on PATH, or skips the test. The
// skip is deliberate and matches internal/core's 7z-backed tests: a machine
// without a browser can still run the whole suite, it just cannot prove the
// browser half.
func chromeBinary(t *testing.T) string {
	t.Helper()
	for _, name := range chromeCandidates {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skipf("no Chrome/Chromium on PATH (probed: %s) - skipping the browser E2E",
		strings.Join(chromeCandidates, ", "))
	return ""
}

// sandboxE2EEnv points HOME and the three XDG_* paths lmm reads at
// throwaway directories, so nothing a test drives - the Service, or the
// browser the harness launches - can reach the developer's real config,
// data or cache. The package-internal tests have their own copy
// (jobs_internal_test.go's sandboxEnv); package serve_test cannot see it.
//
// THREE, not "every XDG_*" (M7, unit 8 gate review): lmm's non-test code
// reads no XDG_STATE_HOME and no XDG_RUNTIME_DIR, so this list is the
// complete set that could leak - the sandbox was never short, only the
// comment (and one report's wording) was.
func sandboxE2EEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
}

// e2eFixture is one browser test's world: a real listening server over a
// seeded Service, a browser driving it, and the browser's own error log.
type e2eFixture struct {
	// Ctx is the chromedp browser context. Wrap it with a timeout per Run.
	Ctx context.Context
	// BaseURL is the running server's origin, e.g. "http://127.0.0.1:41234".
	BaseURL string
	// Svc is the Service backing BaseURL - exposed so a scenario can seed
	// further state (installed mods, deploys) before it starts driving the
	// browser, the same Service every /api/v1 request the browser makes
	// reads from.
	Svc *core.Service
	// Game is the seeded game the SPA routes are scoped to.
	Game *domain.Game
	// Profile is the seeded game's profile name.
	Profile string
	// BrowserErrors returns every error the BROWSER reported so far -
	// uncaught JavaScript exceptions and error-level log entries, which is
	// where a CSP refusal surfaces. A page that renders but logs a refusal
	// is still broken, and this is the only place that can see it.
	BrowserErrors func() []string
}

// HomePath is the Mission Control route for the seeded context.
func (f e2eFixture) HomePath() string {
	return f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile
}

// SlideOverPath is the Mission Control route with sourceID/modID annotated
// as the ?mod= slide-over - a deep link into it, exactly as a bookmark or
// the tray's ?job= would carry (router.js).
func (f e2eFixture) SlideOverPath(sourceID, modID string) string {
	return f.HomePath() + "?mod=" + sourceID + "/" + modID
}

// ModPagePath is the full mod page route for sourceID/modID.
func (f e2eFixture) ModPagePath(sourceID, modID string) string {
	return f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile + "/mod/" + sourceID + "/" + modID
}

// newE2EFixture seeds a Service, serves it on a real loopback listener, and
// opens a browser against it. Every resource is released through t.Cleanup.
func newE2EFixture(t *testing.T) e2eFixture {
	t.Helper()
	return newE2EFixtureFromSource(t, newFakeSource("fake"))
}

// newE2EFixtureFromSource is newE2EFixture over a caller-supplied source,
// for a scenario that needs specific catalog entries - issue 330's slide-
// over/full-mod-page scenarios, whose ModDetail/versions reads are LIVE
// source calls (unlike everything Mission Control itself reads): a mod
// seedInstalledMod puts in the DB/cache with no matching src.addMod is
// installed but unreachable at the source, which those two reads report as
// a genuine (if gracefully handled) failure - and the resulting network
// log entry fails the plain assert.Empty(t, f.BrowserErrors()) every
// happy-path scenario in this suite uses, same as any other unexpected
// network error would.
func newE2EFixtureFromSource(t *testing.T, src *fakeSource) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	svc, game := newFixtureServiceWithSource(t, src)
	// The server is started BEFORE the browser deliberately: see
	// startE2EServer's doc comment on cleanup order.
	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           ctx,
		BaseURL:       baseURL,
		Svc:           svc,
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}
}

// newE2EFixtureWithLibrarySample is newE2EFixture plus three installed mods
// with distinguishable names and enabled states - enough to prove the
// library's filter and sort controls actually narrow/reorder the DOM,
// without needing the fuller health/conflict machinery
// newE2EFixtureWithAttention sets up. Seeded with no cache files (nil), so
// none of the three trips a verify finding of its own.
func newE2EFixtureWithLibrarySample(t *testing.T) e2eFixture {
	t.Helper()

	// Registered in the source's own catalog too (not just the DB/cache
	// seedInstalledMod writes), so a row's slide-over - issue 330 - can
	// resolve its live ModDetail/versions reads instead of 404ing
	// (newE2EFixtureFromSource's own doc comment explains why that matters
	// for this suite's plain assert.Empty(t, f.BrowserErrors()) bar).
	src := newFakeSource("fake")
	for _, mod := range []struct{ id, name string }{
		{"z", "Zebra Mod"}, {"a", "Alpha Mod"}, {"m", "Middle Mod"},
	} {
		src.addMod(fakeSourceMod{Mod: domain.Mod{
			ID: mod.id, SourceID: "fake", Name: mod.name, Version: "1.0", Author: "Ada Lovelace",
		}})
	}
	f := newE2EFixtureFromSource(t, src)

	// Every one carries the same author, so an assertion about the wide
	// columns' CONTENT (TestE2E_WideColumnsCarryTheDocumentsOwnData) does
	// not also depend on which of the three the library happens to render
	// first - which the filter and sort scenarios deliberately vary.
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "z", SourceID: "fake", Name: "Zebra Mod", Version: "1.0", Author: "Ada Lovelace", GameID: f.Game.ID}, true, nil)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", Author: "Ada Lovelace", GameID: f.Game.ID}, true, nil)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "m", SourceID: "fake", Name: "Middle Mod", Version: "1.0", Author: "Ada Lovelace", GameID: f.Game.ID}, false, nil)

	return f
}

// newE2EFixtureWithAttention seeds a state where all three attention cards
// have something to say: Better Boots is installed recording version 1.0
// while its matched file now reports 2.0 (Updates, and - since the
// recorded/effective versions disagree - Health too), and Mod X/Mod Y both
// provide the same deployed path (Conflicts). It is
// api_health_test.go's TestServer_APIHealth_MatchesCLIVerifyTier fixture and
// its twinConflictFixture, combined into one browser scenario, so the
// library and the cards both have real, non-trivial documents to render.
func newE2EFixtureWithAttention(t *testing.T) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "2.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake", "boots", "1.0", "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))

	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "x", SourceID: "fake", Name: "Mod X", Version: "1.0", GameID: game.ID}, true,
		map[string][]byte{"shared.esp": []byte("X-content")})
	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "y", SourceID: "fake", Name: "Mod Y", Version: "1.0", GameID: game.ID}, true,
		map[string][]byte{"shared.esp": []byte("Y-content")})

	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(t.Context(), game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "x", Version: "1.0"}))
	require.NoError(t, pm.AddMod(t.Context(), game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "y", Version: "1.0"}))
	_, err := svc.DeployProfile(t.Context(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           ctx,
		BaseURL:       baseURL,
		Svc:           svc,
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}
}

// e2eMultiGameFixture is a browser test's world with more than one
// configured game and no default among them - the state where "/" has a
// real choice to render rather than something to auto-redirect through
// (docs/plans/2026-08-31-serve-spa-design.md §Information architecture: "/
// -> game chooser (or redirect to the single/default game)" - this is the
// chooser's own branch).
type e2eMultiGameFixture struct {
	Ctx           context.Context
	BaseURL       string
	GameA, GameB  *domain.Game
	BrowserErrors func() []string
}

func newE2EMultiGameFixture(t *testing.T) e2eMultiGameFixture {
	t.Helper()
	sandboxE2EEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameA := &domain.Game{ID: "game-a", Name: "Game Alpha", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	gameB := &domain.Game{ID: "game-b", Name: "Game Beta", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(t.Context(), gameA))
	require.NoError(t, svc.SaveGame(t.Context(), gameB))
	_, err = svc.NewProfileManager().Create(t.Context(), gameA.ID, "default")
	require.NoError(t, err)
	_, err = svc.NewProfileManager().Create(t.Context(), gameB.ID, "default")
	require.NoError(t, err)

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eMultiGameFixture{
		Ctx:           ctx,
		BaseURL:       baseURL,
		GameA:         gameA,
		GameB:         gameB,
		BrowserErrors: browserErrors,
	}
}

func (f e2eMultiGameFixture) runInBrowser(t *testing.T, actions ...chromedp.Action) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.Ctx, e2eTimeout)
	defer cancel()
	require.NoError(t, chromedp.Run(ctx, actions...))
}

// startE2EServer binds a loopback listener, serves svc on it, and returns
// the origin. Serve is stopped and its error checked at test end - a
// browser test that left the server running would leak a goroutine into
// every test after it.
//
// CLEANUP ORDER. Every fixture calls this BEFORE newE2EBrowser, and must
// keep doing so. t.Cleanup runs last-registered-first, so starting the
// server first means the BROWSER is torn down first - which is the order
// that matters, because a page still open is a page still holding requests
// against this server. The SPA keeps a session-long EventSource on
// GET /api/v1/events (activity.js), and http.Server.Shutdown waits for
// active requests: shut the server down first and that stream is still
// live, so the wait runs out and Serve returns "shutting down: context
// deadline exceeded". That was a real, intermittent failure across the
// proxy-backed fixtures before this ordering was made explicit.
func startE2EServer(t *testing.T, svc *core.Service) string {
	t.Helper()

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler),
		serve.Options{Addr: "127.0.0.1:0", ShutdownGrace: e2eShutdownGrace, Version: e2eLmmVersion})
	addr, err := srv.Listen()
	require.NoError(t, err)

	// WithoutCancel, then our own cancel: t.Context() is cancelled when the
	// test FUNCTION returns, which is BEFORE any t.Cleanup runs. Deriving the
	// serve context from it directly therefore starts the graceful shutdown
	// while the browser is still open and still holding its session-long
	// EventSource on GET /api/v1/events - so Shutdown waits out its whole
	// grace and Serve returns "shutting down: context deadline exceeded".
	// That was an intermittent failure across the browser fixtures; the
	// server must be stopped by the cleanup below, in order, not by the test
	// function's own return.
	serveCtx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			require.NoError(t, err)
		case <-time.After(e2eShutdownCleanupGuard):
			t.Error("the E2E server did not shut down")
		}
	})

	return "http://" + addr.String()
}

// e2eProxyTransport returns a private http.Transport for one fixture's
// reverse proxy, whose idle connections are closed by a cleanup registered
// HERE - which is to say after startE2EServer's own shutdown cleanup and
// therefore, cleanups being LIFO, before it.
//
// This is the fix for the Unit 5 flake
// (TestE2E_OverlappingInstallAndToggleBothTrackCorrectly failing ~1 in 10
// under -race with "shutting down: context deadline exceeded"). The cause,
// found from a goroutine dump at the moment of failure: a reverse proxy
// with no Transport of its own uses http.DefaultTransport, a process-wide
// pool that outlives the proxy server closing. When two requests are
// genuinely in flight at once - which is the entire point of the
// overlapping-jobs fixture - the Transport dials a second connection to the
// backend, and the loser of that race is parked in the idle pool having
// never written a request. On the BACKEND that connection is StateNew, and
// net/http's Shutdown deliberately refuses to treat a StateNew connection
// as idle until it has sat there for five seconds (go issue 22682). The
// fixture's ShutdownGrace is five seconds, so Shutdown spun out its whole
// grace waiting for a connection nothing was ever going to send a request
// on, and Serve returned the deadline error.
//
// Closing the fixture's OWN idle connections at teardown removes that
// connection before the backend is asked to shut down, which makes the
// teardown deterministic rather than a race against that five-second
// timer. A private Transport (rather than DefaultTransport.CloseIdle
// Connections()) keeps one fixture's teardown from disturbing another's
// in-flight connections when tests run in parallel.
func e2eProxyTransport(t *testing.T) *http.Transport {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}

// startE2EServerWithFailingPath is startE2EServer plus a reverse proxy in
// front of the real server that can answer one exact request path with a
// 500 JSON error envelope instead of forwarding it - simulating an upstream
// failure (offline, rate limit, source outage) for exactly one of Mission
// Control's supplementary reads (the I3 scenarios), without touching
// production code or the other three, which still reach the real server.
// The returned setFailing toggles the fault on and off, so a scenario can
// also prove the retry affordance recovers once the fault clears.
//
// The rewrite sets the forwarded request's Host to the backend's own bound
// address: hostCheck (middleware.go) pins the allow-list to whatever
// Listen() actually bound, and the browser's Host header names the PROXY's
// address instead, which the backend would otherwise reject.
func startE2EServerWithFailingPath(t *testing.T, svc *core.Service, failPath string) (baseURL string, setFailing func(bool)) {
	t.Helper()
	backend := startE2EServer(t, svc)

	backendURL, err := url.Parse(backend)
	require.NoError(t, err)

	proxy := &httputil.ReverseProxy{
		Transport: e2eProxyTransport(t),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(backendURL)
			r.Out.Host = backendURL.Host
		},
	}

	var failing atomic.Bool
	failing.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc(failPath, func(w http.ResponseWriter, r *http.Request) {
		if !failing.Load() {
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"simulated upstream failure"}`))
	})
	mux.Handle("/", proxy)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyServer := &http.Server{Handler: mux}
	served := make(chan error, 1)
	go func() { served <- proxyServer.Serve(ln) }()
	t.Cleanup(func() {
		_ = proxyServer.Close()
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("proxy server: %v", err)
		}
	})

	return "http://" + ln.Addr().String(), failing.Store
}

// newE2EFixtureWithFailingPath is newE2EFixture, plus one seeded mod (so the
// three OTHER supplementary reads have something non-trivial to answer),
// served through startE2EServerWithFailingPath's proxy instead of directly.
func newE2EFixtureWithFailingPath(t *testing.T, failPath string) (e2eFixture, func(bool)) {
	t.Helper()
	sandboxE2EEnv(t)

	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: game.ID}, true, nil)

	baseURL, setFailing := startE2EServerWithFailingPath(t, svc, failPath)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           ctx,
		BaseURL:       baseURL,
		Svc:           svc,
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}, setFailing
}

// newE2EBrowser starts a headless browser and returns its chromedp context
// plus an accessor for everything the browser has complained about.
//
// The Log domain is enabled explicitly: a Content-Security-Policy refusal
// is a `log.entryAdded` entry with source "security", NOT a
// `Runtime.consoleAPICalled`, so without it the one browser-only failure
// this harness exists to catch would be invisible to it.
func newE2EBrowser(t *testing.T) (context.Context, func() []string) {
	t.Helper()
	binary := chromeBinary(t)

	// A window the product actually supports (issue 334). Headless Chrome
	// defaults to 800x600, which is BELOW this UI's own declared baseline -
	// "desktop only, >= 1080p" (docs/plans/2026-08-31-serve-spa-design.md
	// §Settled decisions) - so the suite was judging a layout the design
	// explicitly does not cover: at 800px the top bar cannot fit its nine
	// controls on one row, and the wrap that keeps them on screen puts the
	// activity bell at the left, where its right-anchored tray hangs off
	// the side. 1280 is comfortably inside the supported range and still
	// below the 1440px step at which the library reveals its extra
	// columns, so every existing column assertion is unchanged.
	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.ExecPath(binary), chromedp.WindowSize(1280, 900))
	// WithoutCancel, same reasoning as startE2EServer:331. t.Context() is
	// cancelled when the test FUNCTION returns, which is BEFORE t.Cleanup
	// runs - so deriving the allocator from it directly cancels ctx (below)
	// before the cleanup's chromedp.Cancel(ctx) call ever runs. chromedp
	// v0.16.0's Cancel takes its graceful path only when ctx is still live;
	// on an already-cancelled ctx its first act (closing the browser)
	// returns context.Canceled immediately, BEFORE the wait for the process
	// to actually exit that is the entire reason to call Cancel instead of
	// the bare context cancel - silently falling back to the non-waiting
	// path it was meant to replace.
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.WithoutCancel(t.Context()), opts...)
	t.Cleanup(cancelAlloc)

	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(func() {
		// chromedp.Cancel, not the bare context cancel: it closes the
		// browser AND WAITS for the process to exit. Neither cancelCtx nor
		// cancelAlloc waits (both return in microseconds), which leaves
		// Chrome alive while the cleanups below shut the server down - and
		// a live Chrome re-establishes the SPA's session-long EventSource
		// (activity.js) every few seconds, so http.Server.Shutdown keeps
		// finding an active request and eventually runs out its grace. That
		// was the cause of an intermittent "shutting down: context deadline
		// exceeded" across the browser fixtures.
		if err := chromedp.Cancel(ctx); err != nil {
			cancelCtx()
		}
	})

	var (
		mu     sync.Mutex
		errors []string
	)
	chromedp.ListenTarget(ctx, func(ev any) {
		var message string
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			message = "uncaught: " + e.ExceptionDetails.Error()
		case *log.EventEntryAdded:
			if e.Entry.Level != log.LevelError || ignorableBrowserError(e.Entry) {
				return
			}
			message = fmt.Sprintf("%s: %s (%s)", e.Entry.Source, e.Entry.Text, e.Entry.URL)
		default:
			return
		}
		mu.Lock()
		errors = append(errors, message)
		mu.Unlock()
	})

	// The FIRST Run is what allocates the browser, and chromedp ties the
	// browser's lifetime to the context that Run was given - so this one
	// must be the long-lived ctx, never a timeout-wrapped child. Wrapping
	// it kills the browser the instant the wrapper's cancel fires, which
	// shows up as "context canceled" on the NEXT Run, several frames away
	// from the cause. Per-action timeouts start from runInBrowser, once the
	// browser exists.
	require.NoError(t, chromedp.Run(ctx, log.Enable()), "starting %s", binary)

	return ctx, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(errors)
	}
}

// ignorableBrowserError filters the one error every Chrome logs against a
// server that serves no favicon, which this one deliberately does not (a
// favicon is polish, not foundation - see the task-2 report's carry-ins).
// Nothing else is filtered: the point of collecting these is that an error
// nobody looks at is the same as no error at all.
func ignorableBrowserError(entry *log.Entry) bool {
	return strings.HasSuffix(entry.URL, "/favicon.ico")
}

// runInBrowser runs actions against f's browser under the harness timeout.
func (f e2eFixture) runInBrowser(t *testing.T, actions ...chromedp.Action) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.Ctx, e2eTimeout)
	defer cancel()
	require.NoError(t, chromedp.Run(ctx, actions...))
}

// textContent reads an element's raw textContent with its whitespace
// collapsed, as a chromedp action.
//
// Raw rather than chromedp.Text's innerText deliberately: this UI's section
// headers are uppercased by CSS (the Launcher style's "confident
// uppercase-tracked headers"), and innerText returns what was PAINTED, so
// an assertion about what the application says would move every time a
// style did. Collapsed rather than exact, because htm templates put a
// component's text on its own indented line.
func textContent(sel string, out *string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var raw string
		if err := chromedp.TextContent(sel, &raw, chromedp.ByQuery).Do(ctx); err != nil {
			return err
		}
		*out = strings.Join(strings.Fields(raw), " ")
		return nil
	})
}

// newE2EFixtureWithDeployableMods seeds two enabled mods whose files are
// already in the cache and which have never been deployed - the state a
// real deploy acts on, and the one the top bar's undeployed indicator
// counts. Everything the deploy needs is local, so no source round trip
// (and no network) is involved in applying it.
func newE2EFixtureWithDeployableMods(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	seedDeployableMods(t, f.Svc, f.Game)
	return f
}

// newE2EFixtureWithSlowDeploy is newE2EFixtureWithDeployableMods plus an
// install.after_each hook that sleeps, which is what gives the browser a
// deterministic window in which a deploy job is genuinely IN FLIGHT.
//
// Without it every scenario about a running job would be a race against a
// deploy of two local files, which finishes in milliseconds: "open the tray
// during a job" would pass or fail on machine speed. A hook is the honest
// lever for this - it is a real, supported way for a deploy to take time -
// and it runs AFTER the first mod's DeployDeployed event, so the job is not
// merely running but running WITH PROGRESS already reported.
func newE2EFixtureWithSlowDeploy(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)

	script := filepath.Join(t.TempDir(), "slow-after-each")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\n"), 0o755))
	f.Game.Hooks.Install.AfterEach = script
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	seedDeployableMods(t, f.Svc, f.Game)
	return f
}

// newE2EFixtureWithQueuedToggle is newE2EFixtureWithSlowDeploy plus a third,
// disabled mod ("Gamma Mod") that is never added to the profile - it exists
// only so a concurrently-started enable job (startEnableFromAnotherClient)
// has something valid to act on, entirely independent of what the slow
// deploy is doing to Alpha/Beta.
//
// The slow deploy's AfterEach hook holds core's one mutation slot for the
// whole time it sleeps, so an enable job started while it runs sits blocked
// in beginOp: registered, state "running", zero events, no frame - exactly
// jobStateLabel's "queued" heuristic (progress.js) - for a window a browser
// can actually be driven through, rather than a race against a toggle that
// would otherwise complete in microseconds.
func newE2EFixtureWithQueuedToggle(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithSlowDeploy(t)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "c", SourceID: "fake", Name: "Gamma Mod", Version: "1.0", GameID: f.Game.ID},
		false, nil)
	return f
}

// seedDeployableMods installs two enabled mods with one cached file each
// AND puts both in the profile's load order.
//
// The load order is not decoration here: PlanDeploy's full-profile branch
// reads GetInstalledModsInProfileOrder, which deliberately omits a mod that
// is installed but absent from the profile (unlike the library's own
// ListMods, which lists it first). A fixture that skipped AddMod would
// therefore plan a deploy of nothing at all while the library rendered two
// rows - which is exactly what the first run of these scenarios showed.
func seedDeployableMods(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", Author: "Ada Lovelace", GameID: game.ID},
		true, map[string][]byte{"alpha.pak": []byte("alpha")})
	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", Author: "Ada Lovelace", GameID: game.ID},
		true, map[string][]byte{"beta.pak": []byte("beta")})

	pm := svc.NewProfileManager()
	for _, id := range []string{"a", "b"} {
		require.NoError(t, pm.AddMod(t.Context(), game.ID, "default",
			domain.ModReference{SourceID: "fake", ModID: id, Version: "1.0"}))
	}
}

// newE2EFixtureWithSlowUninstallAndFourMods is m6's own fixture (unit 6
// gate review): four enabled, cached mods - wider than seedDeployableMods'
// usual two, since the sequencing property this exists to pin needs a
// window a concurrent poller can actually sample more than once or twice
// in - plus an uninstall.before_each hook that sleeps briefly per mod, the
// same deterministic lever newE2EFixtureWithSlowDeploy uses for install.
func newE2EFixtureWithSlowUninstallAndFourMods(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)

	script := filepath.Join(t.TempDir(), "slow-uninstall-before-each")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.2\n"), 0o755))
	f.Game.Hooks.Uninstall.BeforeEach = script
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	pm := f.Svc.NewProfileManager()
	for _, m := range []struct{ id, name string }{
		{"a", "Alpha Mod"}, {"b", "Beta Mod"}, {"c", "Gamma Mod"}, {"d", "Delta Mod"},
	} {
		seedInstalledMod(t, f.Svc, f.Game,
			domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: f.Game.ID},
			true, map[string][]byte{m.id + ".pak": []byte(m.id)})
		require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default",
			domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}

	return f
}

// csrfMetaPattern lifts the shell's CSRF token out of the served document -
// the same place the SPA reads it from (spa/index.html's meta tag).
var csrfMetaPattern = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

// postAsAnotherClient returns a POST closure that goes through the real
// security middleware - the shell's own CSRF token as X-CSRF-Token, and an
// Origin naming the server itself - so a scenario can drive /api/v1 the way
// a SECOND client would (another browser tab, or a script) rather than
// through a privileged back door. Shared by startDeployFromAnotherClient and
// startEnableFromAnotherClient.
func postAsAnotherClient(t *testing.T, f e2eFixture) func(path, payload string) map[string]any {
	t.Helper()

	shell, err := http.Get(f.BaseURL + "/")
	require.NoError(t, err)
	defer func() { require.NoError(t, shell.Body.Close()) }()
	body, err := io.ReadAll(shell.Body)
	require.NoError(t, err)
	match := csrfMetaPattern.FindSubmatch(body)
	require.Len(t, match, 2, "the shell must carry a CSRF token")
	token := string(match[1])

	return func(path, payload string) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.BaseURL+path, strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", token)
		req.Header.Set("Origin", f.BaseURL)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Less(t, resp.StatusCode, 300, "POST %s: %s", path, raw)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(raw, &decoded))
		return decoded
	}
}

// startDeployFromAnotherClient runs a deploy through /api/v1 the way a
// SECOND client would - another browser tab, or a script - without the page
// under test having clicked anything.
//
// It is how a scenario produces a job with no origin on screen, which is
// the exact condition the design's toast rule turns on ("job completion/
// failure when its origin isn't on-screen"). Faking it by navigating away
// mid-job would be a race against a deploy that takes milliseconds; this is
// not a race at all, because no control in the page ever claimed this job.
func startDeployFromAnotherClient(t *testing.T, f e2eFixture) string {
	t.Helper()
	post := postAsAnotherClient(t, f)

	plan := post("/api/v1/plans/deploy?game="+url.QueryEscape(f.Game.ID)+"&profile="+url.QueryEscape(f.Profile), "{}")
	planID, ok := plan["plan_id"].(string)
	require.True(t, ok, "the plan response must carry a plan_id: %v", plan)

	job := post("/api/v1/jobs", `{"plan_id":"`+planID+`"}`)
	jobID, ok := job["job_id"].(string)
	require.True(t, ok, "the job response must carry a job_id: %v", job)
	return jobID
}

// startEnableFromAnotherClient starts an enable job through /api/v1's one
// sanctioned plan-free mutation path (kind_toggle.go), the way a second
// client would. EnableMod takes no core.EventSink (kind_toggle.go's own doc
// comment), so a job it starts NEVER gets a progress frame for its whole
// life - which is what makes it the deterministic way to hold a job in
// jobStateLabel's "queued" state (progress.js) for as long as core's single
// mutation slot (beginOp) is held by something else.
func startEnableFromAnotherClient(t *testing.T, f e2eFixture, sourceID, modID string) string {
	t.Helper()
	post := postAsAnotherClient(t, f)

	path := "/api/v1/mods/" + url.PathEscape(sourceID) + "/" + url.PathEscape(modID) + "/enable" +
		"?game=" + url.QueryEscape(f.Game.ID) + "&profile=" + url.QueryEscape(f.Profile)
	job := post(path, "")
	jobID, ok := job["job_id"].(string)
	require.True(t, ok, "the job response must carry a job_id: %v", job)
	return jobID
}

// newE2EFixtureWithFailingDeploy seeds a deployable profile whose
// install.before_all hook exits non-zero, which is a real, supported way
// for a deploy to fail: without --force, core refuses to continue past a
// failed before_all (internal/core/deploy.go), so ApplyDeploy returns an
// error and the job lands in state failed with that error's envelope.
//
// It is how the browser gets a genuinely failed job to render without
// faking a registry state that could never occur.
func newE2EFixtureWithFailingDeploy(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)

	script := filepath.Join(t.TempDir(), "failing-before-all")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\necho 'the mod directory is not writable' >&2\nexit 1\n"), 0o755))
	f.Game.Hooks.Install.BeforeAll = script
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	seedDeployableMods(t, f.Svc, f.Game)
	return f
}

// --- issue 330 (Unit 4): drill-in fixtures - the slide-over, the full mod
// page, and the mod mutations both surfaces wire. ---

// newE2EFixtureWithDrillInMods seeds two installed, catalog-registered mods
// (registration matters: the slide-over's changelog preview and the full
// mod page's own reads are LIVE source calls, unlike everything Mission
// Control itself reads - newE2EFixtureFromSource's own doc comment) with
// distinguishable names for the arrow-step scenarios, one carrying a real
// changelog and a two-entry version list.
func newE2EFixtureWithDrillInMods(t *testing.T) e2eFixture {
	t.Helper()

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", Author: "Ada Lovelace", Summary: "A tidy little mod."},
		Files: []domain.DownloadableFile{
			{ID: "f1", Version: "1.0"},
			{ID: "f2", Version: "2.0"},
		},
		Changelog: "Fixed a crash on load.",
	})
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", Author: "Ada Lovelace"},
	})
	f := newE2EFixtureFromSource(t, src)

	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", Author: "Ada Lovelace", Summary: "A tidy little mod.", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", Author: "Ada Lovelace", GameID: f.Game.ID},
		true, nil)

	// Both in the profile's own load order too - SetModLock/ClearModLock
	// (ProfileManager.SetModLock) look the ref up there, not in the DB row
	// seedInstalledMod alone writes (the same "both halves" pairing
	// seedDeployableMods' own doc comment explains).
	pm := f.Svc.NewProfileManager()
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "b", Version: "1.0"}))

	return f
}

// newE2EFixtureWithDrillInModsAndALockedMod is newE2EFixtureWithDrillInMods
// with "Alpha Mod" (fake/a) locked at its installed version - N-1's fixture,
// epic-rereview.md's finding that a re-link plan carrying a refusal still
// offers a live Confirm. The lock is set through ProfileManager.SetModLock,
// the same call `lmm mod lock` and the row menu's own "Lock" action make,
// so the ref this seeds is indistinguishable from one a user locked by hand.
func newE2EFixtureWithDrillInModsAndALockedMod(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithDrillInMods(t)
	require.NoError(t, f.Svc.NewProfileManager().SetModLock(t.Context(), f.Game.ID, "default", "fake", "a", "1.0"))
	return f
}

// newE2EFixtureWithRollbackReadyMod seeds one catalog-registered, deployed
// mod already advanced to a second version - PreviousVersion set, both
// versions' cache entries present - the precondition ApplyRollback's own
// guards check. Built the same way api_flow_rollback_internal_test.go's
// rollbackReadyFlowFixtureServer is: entirely from public Service calls,
// since core_test's own seedRollbackReadyMod helper is unreachable from
// here (its GetInstallerForTest/ApplyModUpdateForTest exports are
// core_test-only).
func newE2EFixtureWithRollbackReadyMod(t *testing.T) e2eFixture {
	t.Helper()

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "2.0"}})
	f := newE2EFixtureFromSource(t, src)

	require.NoError(t, f.Svc.GetGameCache(f.Game).Store(f.Game.ID, "fake", "a", "1.0", "alpha.esp", []byte("old content")))
	require.NoError(t, f.Svc.GetGameCache(f.Game).Store(f.Game.ID, "fake", "a", "2.0", "alpha.esp", []byte("new content")))
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:             domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "2.0", GameID: f.Game.ID},
		ProfileName:     "default",
		UpdatePolicy:    domain.UpdateNotify,
		Enabled:         true,
		LinkMethod:      domain.LinkSymlink,
		PreviousVersion: "1.0",
	}))
	require.NoError(t, f.Svc.NewProfileManager().UpsertMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "2.0"}))
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	return f
}

// newE2EFixtureWithThreeVersionsAndACheckedUpdate is C1's own repro: one
// installed mod at 1.0 whose source reports three per-file versions
// (1.0/2.0/3.0, AvailableModVersions) while the catalog's own Mod.Version is
// 3.0 - the ONE version fakeSource.CheckUpdates (and so CheckGameUpdates)
// will ever find, since it compares the catalog's Mod.Version against the
// installed row, never a per-file version. The versions table therefore has
// a row (2.0) core cannot actually reach at all, sitting between the
// installed row and the one row core WOULD update to (3.0).
func newE2EFixtureWithThreeVersionsAndACheckedUpdate(t *testing.T) e2eFixture {
	t.Helper()

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "3.0"},
		Files: []domain.DownloadableFile{
			{ID: "f1", Version: "1.0"},
			{ID: "f2", Version: "2.0"},
			{ID: "f3", Version: "3.0"},
		},
	})
	f := newE2EFixtureFromSource(t, src)

	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))

	return f
}

// newE2EFixtureWithDrillInModsAndSlowDeploy is newE2EFixtureWithDrillInMods
// plus an install.after_each hook that sleeps - the same lever
// newE2EFixtureWithSlowDeploy uses, applied to the drill-in catalog/fixture
// pair, so a deploy started against it (startDeployFromAnotherClient) holds
// core's one mutation slot long enough for a per-mod job started from the
// slide-over to sit genuinely "running" (queued in beginOp) for a window a
// browser can be driven through - M3's own row-level live-line scenario
// needs exactly that, or it is a race against a toggle that finishes in
// microseconds.
func newE2EFixtureWithDrillInModsAndSlowDeploy(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithDrillInMods(t)

	script := filepath.Join(t.TempDir(), "slow-after-each")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\n"), 0o755))
	f.Game.Hooks.Install.AfterEach = script
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	return f
}

// --- issue 331 (Unit 5): search and install - the omnibar, the dedicated
// search page, and installing (with a version pick and a conflict round
// trip) through the confirm-plan framework's install renderer. ---

// e2eSearchSourceMod is one catalog entry e2eSearchSource offers: the mod,
// its downloadable files, and the archive member each file's real zip
// carries - mirroring install_fixture_internal_test.go's installSource,
// ported here because that fixture lives in package serve (internal test),
// unreachable from this package's own external e2e_test.go.
type e2eSearchSourceMod struct {
	mod     domain.Mod
	files   []domain.DownloadableFile
	members map[string]string // file ID -> the single path inside its zip
}

// e2eSearchSource is a searchable, REALLY-downloading source.ModSource: a
// real httptest download server (so an install actually lands bytes on
// disk, not just a DB row), plus Search, since #331's scenarios reach these
// mods through the omnibar/search page - unlike installSource's ID-only
// fixture, which never needed to be found by a query.
type e2eSearchSource struct {
	id     string
	server *httptest.Server
	mods   map[string]*e2eSearchSourceMod
	// updatable turns CheckUpdates from a no-op into the catalog-versus-
	// installed comparison every real source makes (I1, unit 8 gate review's
	// locked-skip scenario). Off by default: every scenario written before
	// it asserts on a Mission Control with NO Updates card, and a source
	// that suddenly reported updates would change what those screens show.
	updatable   bool
	urlRequests atomic.Int64
}

func newE2ESearchSource(t *testing.T, id string) *e2eSearchSource {
	t.Helper()
	s := &e2eSearchSource{id: id, mods: map[string]*e2eSearchSourceMod{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modID, fileID, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		entry, ok := s.mods[modID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		member, ok := entry.members[fileID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(e2eZipWith(member, "payload for "+modID+"/"+fileID))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *e2eSearchSource) addMod(mod e2eSearchSourceMod) { s.mods[mod.mod.ID] = &mod }

// e2eZipWith returns a zip archive holding exactly one member at the given
// path - the smallest real archive the extractor will accept.
func e2eZipWith(member, content string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(path.Clean(member))
	if err != nil {
		panic(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func (s *e2eSearchSource) ID() string      { return s.id }
func (s *e2eSearchSource) Name() string    { return "E2E Search Source" }
func (s *e2eSearchSource) AuthURL() string { return "" }

func (s *e2eSearchSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (s *e2eSearchSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	var mods []domain.Mod
	needle := strings.ToLower(q.Query)
	for _, entry := range s.mods {
		if needle == "" || strings.Contains(strings.ToLower(entry.mod.Name), needle) {
			mods = append(mods, entry.mod)
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].ID < mods[j].ID })
	return source.SearchResult{Mods: mods, TotalCount: len(mods)}, nil
}

func (s *e2eSearchSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	entry, ok := s.mods[modID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	mod := entry.mod
	return &mod, nil
}

func (s *e2eSearchSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *e2eSearchSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	entry, ok := s.mods[mod.ID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	return append([]domain.DownloadableFile(nil), entry.files...), nil
}

// GetDownloadURL is the counted call: downloadModToCache asks for a URL only
// when it has decided to actually fetch the file, so this counter is the
// cache-warm oracle the conflict-overwrite scenario asserts on.
func (s *e2eSearchSource) GetDownloadURL(_ context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.urlRequests.Add(1)
	return s.server.URL + "/" + mod.ID + "/" + fileID, nil
}

func (s *e2eSearchSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	if !s.updatable {
		return nil, nil
	}
	var updates []domain.Update
	for _, im := range installed {
		entry, ok := s.mods[im.ID]
		if !ok || entry.mod.Version == im.Version {
			continue
		}
		updates = append(updates, domain.Update{InstalledMod: im, NewVersion: entry.mod.Version})
	}
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].InstalledMod.ID < updates[j].InstalledMod.ID
	})
	return updates, nil
}

func (s *e2eSearchSource) downloadCount() int { return int(s.urlRequests.Load()) }

var _ source.ModSource = (*e2eSearchSource)(nil)

// e2eFailingSearchSource is a second, registered source whose Search always
// fails - #331's "a failing source's warning row" scenario. Nothing else on
// it is ever called: an aggregate search skips straight to Search per
// source, so every other method panics if reached, catching a fixture bug
// (a real call here would mean the scenario is testing the wrong thing).
type e2eFailingSearchSource struct{ id string }

func (s *e2eFailingSearchSource) ID() string      { return s.id }
func (s *e2eFailingSearchSource) Name() string    { return "Flaky Source" }
func (s *e2eFailingSearchSource) AuthURL() string { return "" }
func (s *e2eFailingSearchSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *e2eFailingSearchSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, errors.New("upstream unavailable")
}
func (s *e2eFailingSearchSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	panic("e2eFailingSearchSource.GetMod: unreachable by a search-only scenario")
}
func (s *e2eFailingSearchSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	panic("e2eFailingSearchSource.GetDependencies: unreachable by a search-only scenario")
}
func (s *e2eFailingSearchSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	panic("e2eFailingSearchSource.GetModFiles: unreachable by a search-only scenario")
}
func (s *e2eFailingSearchSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	panic("e2eFailingSearchSource.GetDownloadURL: unreachable by a search-only scenario")
}
func (s *e2eFailingSearchSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	panic("e2eFailingSearchSource.CheckUpdates: unreachable by a search-only scenario")
}

var _ source.ModSource = (*e2eFailingSearchSource)(nil)

// e2eSearchInstallModID/e2eSearchConflictModID/e2eSearchDeployedFile name
// the search-fixture's own catalog, mirroring install_fixture_internal_
// test.go's installModID/conflictModID/installModFile constants.
const (
	e2eSearchInstallModID   = "boots"
	e2eSearchConflictModID  = "clash"
	e2eSearchMultiFileModID = "multi"
	e2eSearchDeployedFile   = "Mods/alpha.pak"
)

// e2eSearchFixture is newE2EFixture's own shape plus the two extra sources
// #331's scenarios need: Src (searchable, really downloads) and Failing
// (registered on the same game, always fails Search).
type e2eSearchFixture struct {
	e2eFixture
	Src     *e2eSearchSource
	Failing *e2eFailingSearchSource
}

// newE2EFixtureWithSearchableMods seeds: one ALREADY-INSTALLED, deployed mod
// ("Alpha Mod", owning e2eSearchDeployedFile - what the conflict scenario
// collides with) plus three SEARCHABLE, not-yet-installed catalog mods -
// "Better Boots" (two files/versions, #225's version-pick scenario),
// "Clashing Mod" (one file whose archive member is the ALREADY-DEPLOYED
// path, the conflict-round-trip scenario), and "Multi Edition Mod" (TWO
// files sharing the SAME version - plan_install.js's own file sub-picker,
// which only renders once the chosen version itself resolves to more than
// one file) - across TWO registered sources, the second of which always
// fails Search (the warning-row scenario).
func newE2EFixtureWithSearchableMods(t *testing.T) e2eSearchFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := newE2ESearchSource(t, "fake")
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: e2eSearchInstallModID, SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "f2", Name: "Main 2.0", FileName: "boots-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 128},
			{ID: "f1", Name: "Main 1.0", FileName: "boots-1.0.zip", Version: "1.0", Category: "MAIN", Size: 96},
		},
		members: map[string]string{"f1": "Mods/boots.pak", "f2": "Mods/boots.pak"},
	})
	src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: e2eSearchConflictModID, SourceID: "fake", Name: "Clashing Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "c1", Name: "Main", FileName: "clash.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"c1": e2eSearchDeployedFile},
	})
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: e2eSearchMultiFileModID, SourceID: "fake", Name: "Multi Edition Mod", Version: "1.0"},
		files: []domain.DownloadableFile{
			{ID: "m1", Name: "Regular Edition", FileName: "multi-regular.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 48},
			{ID: "m2", Name: "Definitive Edition", FileName: "multi-definitive.zip", Version: "1.0", Category: "MAIN", Size: 48},
		},
		members: map[string]string{"m1": "Mods/multi-regular.pak", "m2": "Mods/multi-definitive.pak"},
	})

	failing := &e2eFailingSearchSource{id: "flaky"}

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)
	svc.RegisterSource(failing)

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{src.ID(): "", failing.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	// "other" is I5's own profile-switch scenario's target - an empty
	// sibling profile that never fanned anything out, so a stale
	// omnibarSearch/searchPage surviving the switch is unambiguous.
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "alpha", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: game.ID},
		true, map[string][]byte{e2eSearchDeployedFile: []byte("alpha content")})
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "alpha", Version: "1.0"}))
	_, err = svc.DeployProfile(t.Context(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eSearchFixture{
		e2eFixture: e2eFixture{
			Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
			BrowserErrors: browserErrors,
		},
		Src:     src,
		Failing: failing,
	}
}

// SearchPagePath is the dedicated search page's deep-link route.
func (f e2eSearchFixture) SearchPagePath(query string) string {
	return f.HomePath() + "/search?q=" + url.QueryEscape(query)
}

// e2eManyResultsPageSize mirrors main.js's own SEARCH_PAGE_SIZE - the
// pagination scenario needs strictly more catalog mods than one page holds
// to prove a Next page is real rather than a control that merely LOOKS
// clickable.
const e2eManyResultsPageSize = 20

// startE2EServerWithDelayedJobStart is startE2EServer plus a reverse proxy
// that sleeps for delay before forwarding every POST /api/v1/jobs OR
// POST .../enable|disable (the toggle's own plan-free start) - #331's
// bindingJob carry-in ("installs can now start from multiple search rows
// while a modal is open elsewhere - re-check the single-slot assumption
// ... make it a map keyed by origin, with a test proving the overlap
// case"): a deterministic window in which one origin's start call is
// genuinely still in flight (bindingJobs still holds its promise), so a
// SECOND origin's own start (issued through a completely different UI path
// - a toggle, which needs no modal at all) is a real overlap rather than a
// race against microsecond-fast local HTTP round trips that would pass or
// fail on machine speed alone.
//
// Delaying the TOGGLE's own start too, by its own shorter toggleDelay (unit
// 5 fix wave, Important 2), is what makes the overlap OBSERVABLE rather
// than merely real: without it the toggle's start resolves in microseconds,
// so by the time anything reads bindingJobs' size the toggle's own entry is
// already gone and the window where both starts are genuinely tracked at
// once has closed before a test could ever sample it. toggleDelay must stay
// SHORTER than delay: the scenario's own staleness conflict depends on the
// toggle's disable genuinely finishing (not just starting) before the
// install's own Apply runs, which only holds if the toggle, started AFTER
// the install, still resolves first.
func startE2EServerWithDelayedJobStart(t *testing.T, svc *core.Service, delay, toggleDelay time.Duration) string {
	t.Helper()
	backend := startE2EServer(t, svc)
	backendURL, err := url.Parse(backend)
	require.NoError(t, err)

	proxy := &httputil.ReverseProxy{
		Transport: e2eProxyTransport(t),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(backendURL)
			r.Out.Host = backendURL.Host
			// originCheck (middleware.go) rejects a state-changing request
			// whose Origin header names anything other than "http://" +
			// r.Host - it never sees this proxy exists, so the browser's
			// real Origin (the PROXY's own address, since that is where
			// the shell was served from) must be rewritten to match the
			// BACKEND's address the same way Host just was, or every POST
			// through this proxy is refused as cross-origin. startE2EServer
			// WithFailingPath's proxy gets away without this because it is
			// only ever driven with GET requests, which originCheck never
			// inspects (unsafeMethod) - this proxy is the first to carry a
			// real mutation, so it is the first to need the fix.
			if r.Out.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", "http://"+backendURL.Host)
			}
		},
	}

	sleepThenProxy := func(d time.Duration) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(d)
			proxy.ServeHTTP(w, r)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/jobs", sleepThenProxy(delay))
	mux.HandleFunc("POST /api/v1/mods/{source}/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "enable", "disable":
			sleepThenProxy(toggleDelay)(w, r)
		default:
			proxy.ServeHTTP(w, r)
		}
	})
	mux.Handle("/", proxy)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyServer := &http.Server{Handler: mux}
	served := make(chan error, 1)
	go func() { served <- proxyServer.Serve(ln) }()
	t.Cleanup(func() {
		_ = proxyServer.Close()
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("proxy server: %v", err)
		}
	})

	return "http://" + ln.Addr().String()
}

// newE2EFixtureWithSearchableModsAndDelayedJobStart is
// newE2EFixtureWithSearchableMods, routed through the job-start-delaying
// proxy above, plus one extra ALREADY-INSTALLED, ENABLED mod ("Gamma Mod") -
// the other half of the overlap scenario, reachable through the slide-over
// (no modal, no lock) while the install's own confirm modal sits "starting"
// for the whole delay window. toggleDelay is the SAME proxy's own shorter
// delay on the toggle's start (see startE2EServerWithDelayedJobStart) - both
// starts genuinely overlap, but the toggle still resolves (and finishes
// disabling Gamma) before the install's own Apply runs.
func newE2EFixtureWithSearchableModsAndDelayedJobStart(t *testing.T, delay, toggleDelay time.Duration) e2eSearchFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := newE2ESearchSource(t, "fake")
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: e2eSearchInstallModID, SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "f2", Name: "Main 2.0", FileName: "boots-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 128},
			{ID: "f1", Name: "Main 1.0", FileName: "boots-1.0.zip", Version: "1.0", Category: "MAIN", Size: 96},
		},
		members: map[string]string{"f1": "Mods/boots.pak", "f2": "Mods/boots.pak"},
	})
	// Registered in the catalog too (not just the DB row seedInstalledMod
	// writes below) - newE2EFixtureFromSource's own doc comment explains
	// why: the slide-over's changelog is a LIVE ModDetail read, which 404s
	// for an installed-but-unregistered mod and fails this scenario's own
	// assert.Empty(t, f.BrowserErrors()) bar over an unrelated fetch.
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "gamma", SourceID: "fake", Name: "Gamma Mod", Version: "1.0"},
	})

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{src.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "gamma", SourceID: "fake", Name: "Gamma Mod", Version: "1.0", GameID: game.ID}, true, nil)

	baseURL := startE2EServerWithDelayedJobStart(t, svc, delay, toggleDelay)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eSearchFixture{
		e2eFixture: e2eFixture{
			Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
			BrowserErrors: browserErrors,
		},
		Src: src,
	}
}

// newE2EFixtureWithManySearchResults seeds one source with
// e2eManyResultsPageSize+5 catalog mods, split evenly across two
// categories ("Armor"/"Weapons") - the search page's own pagination
// (Next/Prev) and category-filter scenarios. No downloads needed here (no
// install happens against this fixture), so a plain fakeSource-shaped
// catalog is enough - GetModFiles is never called.
func newE2EFixtureWithManySearchResults(t *testing.T) e2eFixture {
	t.Helper()

	src := newFakeSource("fake")
	for i := 1; i <= e2eManyResultsPageSize+5; i++ {
		category := "Armor"
		if i%2 == 0 {
			category = "Weapons"
		}
		src.addMod(fakeSourceMod{Mod: domain.Mod{
			ID: fmt.Sprintf("item%02d", i), SourceID: "fake",
			Name: fmt.Sprintf("Item %02d", i), Version: "1.0", Category: category,
			// Downloads is the SAME across every item: core's own
			// rankAggregate (service.go) re-ranks a name-matching aggregate
			// by Downloads descending, then Name ascending - a tie here (as
			// a real source's uniform catalog might legitimately have) lets
			// the Name tiebreak hold, which is what this fixture's
			// pagination test relies on for a stable, predictable order.
			// Summary still varies, for the detailed-row rendering assertion.
			Downloads: 100, Summary: fmt.Sprintf("Summary text for item %02d", i),
		}})
	}
	return newE2EFixtureFromSource(t, src)
}

// newE2EFixtureWithTwoWorkingSearchSources seeds TWO real (non-failing)
// sources on one game, both contributing hits to the same "gizmo" query -
// M4 (unit 5 fix wave): the search page's source filter (sourceIDs.length
// > 1) had no fixture where it was ever true, so it shipped untested end to
// end. newE2EFixtureWithSearchableMods's own second source ("flaky")
// always fails and so never lights it up. No downloads happen against this
// fixture - a plain catalog is enough.
func newE2EFixtureWithTwoWorkingSearchSources(t *testing.T) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src1 := newFakeSource("fake")
	src1.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Gizmo One", Version: "1.0"}})
	src1.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake", Name: "Gizmo Two", Version: "1.0"}})
	src1.addMod(fakeSourceMod{Mod: domain.Mod{ID: "3", SourceID: "fake", Name: "Gizmo Three", Version: "1.0"}})

	src2 := newFakeSource("fake2")
	src2.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake2", Name: "Gizmo Four", Version: "1.0"}})
	src2.addMod(fakeSourceMod{Mod: domain.Mod{ID: "2", SourceID: "fake2", Name: "Gizmo Five", Version: "1.0"}})

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src1)
	svc.RegisterSource(src2)

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{src1.ID(): "", src2.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
		BrowserErrors: browserErrors,
	}
}

// --- issue 332 (Unit 6): reorder/profiles/health-repair/updates-batch. ---

// newE2EFixtureWithReorderableConflict seeds two mods that both deploy the
// same path ("shared.esp") - a real load-order conflict the reorder
// modal's own preview can flip, deployed once up front so the "stale"
// comparison the preview renders starts honest. X is added to the profile
// BEFORE Y, so Y (the higher position) starts as the winner.
func newE2EFixtureWithReorderableConflict(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)

	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "x", SourceID: "fake", Name: "Mod X", Version: "1.0", GameID: f.Game.ID}, true,
		map[string][]byte{"shared.esp": []byte("X-content")})
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "y", SourceID: "fake", Name: "Mod Y", Version: "1.0", GameID: f.Game.ID}, true,
		map[string][]byte{"shared.esp": []byte("Y-content")})

	pm := f.Svc.NewProfileManager()
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "x", Version: "1.0"}))
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "y", Version: "1.0"}))
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	return f
}

// dragRowTo drags the reorder modal's row named fromText onto the row named
// toText, via PLAIN mouse events - the same events reordermodal.js's own
// mousedown/mouseenter/mouseup handlers are built on (its own header
// comment explains why: native HTML5 Drag and Drop's dragstart/dragover
// events are only ever raised by the browser's own gesture recognizer, and
// a synthetic mousedown/mousemove/mouseup sequence - what this dispatches -
// never triggers them). Three intermediate points, not just the two
// endpoints: a single mousemove straight to the target can land inside the
// target row without ever entering it from the handler's point of view in
// some layouts, where a midpoint first primes the same mouseenter chain a
// real drag produces.
func dragRowTo(fromText, toText string) chromedp.Action {
	return dragRowToChecking(fromText, toText, nil)
}

// dragRowToChecking is dragRowTo's own press/move/release sequence, split so
// a caller can run midDrag - itself ordinary chromedp actions against ctx -
// after the pointer has moved but BEFORE it releases (issue 338's drag-ghost
// scenario: "the ghost must be present while a drag is in progress"). nil
// behaves exactly like dragRowTo.
func dragRowToChecking(fromText, toText string, midDrag chromedp.Action) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		center := func(text string) (x, y float64, err error) {
			var box []float64
			js := fmt.Sprintf(`(() => {
				const row = Array.from(document.querySelectorAll(".reorder-row")).find((r) => r.textContent.includes(%q));
				const handle = row.querySelector(".reorder-row__handle");
				const r = handle.getBoundingClientRect();
				return [r.x + r.width / 2, r.y + r.height / 2];
			})()`, text)
			if err := chromedp.Evaluate(js, &box).Do(ctx); err != nil {
				return 0, 0, err
			}
			return box[0], box[1], nil
		}

		fx, fy, err := center(fromText)
		if err != nil {
			return err
		}
		tx, ty, err := center(toText)
		if err != nil {
			return err
		}
		mid := func(a, b float64) float64 { return a + (b-a)/2 }

		if err := input.DispatchMouseEvent(input.MousePressed, fx, fy).WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchMouseEvent(input.MouseMoved, mid(fx, tx), mid(fy, ty)).WithButton(input.Left).Do(ctx); err != nil {
			return err
		}
		if midDrag != nil {
			if err := midDrag.Do(ctx); err != nil {
				return err
			}
		}
		if err := input.DispatchMouseEvent(input.MouseMoved, tx, ty).WithButton(input.Left).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, tx, ty).WithButton(input.Left).WithClickCount(1).Do(ctx)
	})
}

// newE2EFixtureWithASwitchTarget seeds the world a profile SWITCH acts on:
// two profiles whose mod sets differ, with everything the switch needs
// already local so applying it makes no source call at all.
//
//	default (active)  Alpha Mod - enabled, cached, deployed
//	hardcore          Beta Mod  - installed, cached, DISABLED under default
//
// Switching default -> hardcore therefore plans exactly one disable (Alpha,
// enabled under default and absent from hardcore) and exactly one enable
// (Beta, installed and cached but disabled), and no install - which is what
// keeps the scenario a pure disk round trip: alpha.pak leaves the game
// directory, beta.pak arrives in it, and the game's default profile moves.
func newE2EFixtureWithASwitchTarget(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	pm := f.Svc.NewProfileManager()

	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "alpha", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.pak": []byte("alpha content")})
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "alpha", Version: "1.0"}))

	// Disabled, so the switch's own enable pass is what deploys it - not a
	// deploy that already happened before the browser was ever opened.
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "beta", SourceID: "fake", Name: "Beta Mod", Version: "1.0", GameID: f.Game.ID},
		false, map[string][]byte{"beta.pak": []byte("beta content")})

	_, err := pm.Create(t.Context(), f.Game.ID, "hardcore")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "hardcore",
		domain.ModReference{SourceID: "fake", ModID: "beta", Version: "1.0"}))
	// Marked explicitly, as `lmm game add` leaves a real game: without it
	// NO profile is the default, GameStatus reports is_default false for
	// both, and the picker would offer to switch to the profile core would
	// have switched FROM.
	require.NoError(t, pm.SetDefault(t.Context(), f.Game.ID, "default"))

	_, err = f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return f
}

// newE2EFixtureWithAnUnappliedProfile seeds the state `lmm profile apply`
// exists for: the profile LISTS a mod that is not installed at all.
//
// "Better Boots" is added to default's load order without ever being
// installed, so the profile's own mod count (2) runs ahead of the installed
// rows (1) - which is exactly what Mission Control's profile card reads to
// decide it has something to say. The mod comes from the search fixture's
// real downloading source, so applying the profile is a genuine
// download/extract/deploy rather than a plan that could never converge.
func newE2EFixtureWithAnUnappliedProfile(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: e2eSearchInstallModID}))
	return f
}

// waitGone waits until sel matches nothing, asked of the PAGE rather than
// of chromedp's own node tracking.
//
// chromedp.WaitNotPresent is the natural spelling and works for an element
// removed synchronously by the action that preceded it. It does NOT
// reliably notice a removal that happens LATER - the slide-over's, which
// issue 334 deliberately delays by one animation so its exit has somewhere
// to play (spa/app/motion.js). Reproduced: the panel is provably gone from
// the document (its own querySelector says so, and the URL has changed),
// while WaitNotPresent sits there until the harness's whole 30-second
// timeout expires.
//
// The polling INTERVAL is explicit for a second, independent reason:
// chromedp.Poll's default mode is requestAnimationFrame, and a headless
// page that has finished animating stops scheduling frames - so a poll
// armed while the panel was still playing its exit would evaluate a few
// times, see it still present, and then never run again once the page went
// idle, which is the very moment the answer changed. A timer keeps asking.
func waitGone(sel string) chromedp.Action {
	return chromedp.Poll(
		fmt.Sprintf("document.querySelector(%q) === null", sel), nil,
		chromedp.WithPollingInterval(50*time.Millisecond),
	)
}

// settleEffects gives Preact's hook effects time to run before the next
// action depends on one having been attached.
//
// Preact flushes effects after paint - requestAnimationFrame, with a
// ~100ms setTimeout fallback for a browser that is not painting, which is
// exactly what a headless one often is not. chromedp's own round trips are
// single-digit milliseconds, so an action issued straight after a
// WaitVisible routinely lands BEFORE the effect that installs the listener
// it depends on. That is not a hypothetical: the picker's Escape handler
// lives in such an effect, and without this the menu simply stayed open and
// the wait for it to close ran out the harness's whole 30-second timeout -
// deterministically under -race, intermittently without it.
//
// The same shape (and the same reasoning) as the `settle` closures the
// profiles-modal scenarios already declare inline; this is that pattern
// with one name and one explanation.
func settleEffects() chromedp.Action {
	return chromedp.Sleep(300 * time.Millisecond)
}

// focusableSelectorJS mirrors spa/app/focustrap.js's own FOCUSABLE list -
// the elements a browser hands focus to on Tab. Kept in sync by intent
// rather than by a ratchet: it is a browser fact, not an application one,
// and the two would only drift if the browser's own rules changed.
const focusableSelectorJS = "'" +
	`a[href]:not([tabindex="-1"]), ` +
	`button:not([disabled]):not([tabindex="-1"]), ` +
	`input:not([disabled]):not([tabindex="-1"]), ` +
	`select:not([disabled]):not([tabindex="-1"]), ` +
	`textarea:not([disabled]):not([tabindex="-1"]), ` +
	`[tabindex]:not([tabindex="-1"])` + "'"

// countFocusableJS counts the RENDERED focusable elements one Tab pass can
// reach - how many presses a full pass takes.
//
// Scoped to the OVERLAY when one is open, because that is what a focus trap
// makes true: with a modal or a slide-over on screen the reachable set is
// that panel's, and the page behind the scrim is deliberately out of reach
// (spa/app/focustrap.js). Counting the whole document there would assert
// the exact bug the trap exists to prevent.
const countFocusableJS = `(() => {
	const root = document.querySelector(".modal, .slide-over__panel") ?? document;
	return [...root.querySelectorAll(` + focusableSelectorJS + `)]
		.filter((el) => el.getClientRects().length > 0).length;
})()`

// activeElementJS describes whatever holds focus, or "" when nothing in the
// page does. The empty string is the whole point: focus landing on <body>
// is focus LOST - the keyboard user's cursor has fallen off the screen and
// their next Tab restarts from the top.
const activeElementJS = `(() => {
	const el = document.activeElement;
	if (!el || el === document.body || el === document.documentElement) return "";
	const label = (el.getAttribute("aria-label") || el.textContent || "").trim();
	return el.tagName.toLowerCase() + "|" + (el.className || "") + "|" + label.slice(0, 40);
})()`

// tabThrough presses Tab n times, recording what holds focus after each
// press. An entry is "" for a press that lost focus to the document.
func tabThrough(n int, out *[]string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for range n {
			if err := chromedp.KeyEvent(kb.Tab).Do(ctx); err != nil {
				return err
			}
			var seen string
			if err := chromedp.Evaluate(activeElementJS, &seen).Do(ctx); err != nil {
				return err
			}
			*out = append(*out, seen)
		}
		return nil
	})
}

// assertKeyboardTraversal walks every focusable control on the page
// currently loaded in f's browser and fails if focus is ever lost.
//
// One full pass, sized from the page itself: pressing Tab exactly as many
// times as there are focusable elements should visit each of them and come
// back round. A screen with an unreachable region fails by visiting fewer
// distinct elements than it has; a screen that drops focus fails on the ""
// entry.
func assertKeyboardTraversal(t *testing.T, f e2eFixture, screen string) {
	t.Helper()

	// The screen has to be SETTLED, not merely visible: a panel whose
	// content is still arriving (the slide-over fetches its changelog after
	// mounting) has fewer controls than the screen it is about to be, and
	// counting then would size the walk to a half-drawn page.
	var count int
	f.runInBrowser(t, settleEffects(), chromedp.Evaluate(countFocusableJS, &count))
	require.Greater(t, count, 3, "%s: too few focusable controls to be a real screen", screen)

	var visited []string
	f.runInBrowser(t, tabThrough(count, &visited))

	distinct := map[string]bool{}
	for i, seen := range visited {
		require.NotEmptyf(t, seen,
			"%s: focus was lost to the document on Tab press %d of %d - the previous stop was %q",
			screen, i+1, count, previousStop(visited, i))
		distinct[seen] = true
	}
	assert.GreaterOrEqualf(t, len(distinct), count-1,
		"%s: one pass visited only %d distinct controls out of %d focusable ones",
		screen, len(distinct), count)
}

func previousStop(visited []string, i int) string {
	if i == 0 {
		return "(the page's own starting focus)"
	}
	return visited[i-1]
}

// e2eLockedUpdateModID/e2eOpenUpdateModID name the two mods the locked-batch
// fixture below installs at 1.0 against a catalog sitting at 2.0. The ids
// are chosen so that "boots" sorts before "gloves": CheckUpdates reports in
// id order, PlanUpdateBatch keeps that order, and ApplyUpdateBatch applies
// in it - so the LOCKED row is the one the batch reaches first, and a tally
// that only ever noticed the last item would still be wrong.
const (
	e2eLockedUpdateModID = "boots"
	e2eOpenUpdateModID   = "gloves"
)

// newE2EFixtureWithALockedAndAnUnlockedUpdate seeds the exact batch the
// unit-8 gate review drove by hand: two installed mods with a real update
// waiting, one of them LOCKED in the profile (#97).
//
// Applying both is therefore one genuine update and one refusal, which is
// the outcome the whole #324 batch flow exists to report honestly - and the
// outcome the web UI used to render as a bare "Done". The updates are real:
// the source downloads over its own httptest server, so the applied row
// actually moves the deployed file to 2.0 and the locked row actually does
// not.
func newE2EFixtureWithALockedAndAnUnlockedUpdate(t *testing.T) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := newE2ESearchSource(t, "fake")
	src.updatable = true
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: e2eLockedUpdateModID, SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "boots-2.0", Name: "Main 2.0", FileName: "boots-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 128},
			{ID: "boots-1.0", Name: "Main 1.0", FileName: "boots-1.0.zip", Version: "1.0", Category: "MAIN", Size: 96},
		},
		members: map[string]string{"boots-1.0": "Mods/boots.pak", "boots-2.0": "Mods/boots.pak"},
	})
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: e2eOpenUpdateModID, SourceID: "fake", Name: "Great Gloves", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "gloves-2.0", Name: "Main 2.0", FileName: "gloves-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 128},
			{ID: "gloves-1.0", Name: "Main 1.0", FileName: "gloves-1.0.zip", Version: "1.0", Category: "MAIN", Size: 96},
		},
		members: map[string]string{"gloves-1.0": "Mods/gloves.pak", "gloves-2.0": "Mods/gloves.pak"},
	})

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{src.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	pm := svc.NewProfileManager()
	for _, seed := range []struct {
		id, name string
		locked   bool
	}{
		{e2eLockedUpdateModID, "Better Boots", true},
		{e2eOpenUpdateModID, "Great Gloves", false},
	} {
		fileID := seed.id + "-1.0"
		member := "Mods/" + seed.id + ".pak"
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, "fake", seed.id, "1.0",
			member, []byte("payload for "+seed.id+"/"+fileID)))
		require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
			Mod: domain.Mod{
				ID: seed.id, SourceID: "fake", Name: seed.name,
				Version: "1.0", GameID: game.ID,
			},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      []string{fileID},
		}))
		require.NoError(t, pm.AddMod(t.Context(), game.ID, "default", domain.ModReference{
			SourceID: "fake", ModID: seed.id, Version: "1.0",
			FileIDs: []string{fileID}, Locked: seed.locked,
		}))
	}
	_, err = svc.DeployProfile(t.Context(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
		BrowserErrors: browserErrors,
	}
}

// startE2EProxyDelayingProfileReads fronts an already-running backend with a
// reverse proxy that sleeps for delay before forwarding any GET whose
// `profile` query parameter names profile, and forwards everything else
// untouched.
//
// It exists for exactly one thing: making C-1's hydrate() race
// DETERMINISTIC. A profile switch leaves two hydrations in flight at once -
// onJobDone's (read under the OLD profile, main.js) and the route change's
// (the new one) - and the store belongs to whichever settles last. On a
// loopback server both settle in microseconds, so which one wins is decided
// by the scheduler; the epic live review measured the stale one winning
// ~10% of the time. Slowing only the OLD profile's reads inverts that into
// a certainty: the stale hydration is now guaranteed to land last, so a
// scenario that asserts the NEW profile's documents are on screen fails
// every single run without the fence and passes every single run with it.
//
// GET only. The switch's own plan/apply are POSTs and must not be delayed:
// the scenario needs the JOB to finish promptly and the READS to lag.
func startE2EProxyDelayingProfileReads(t *testing.T, backend, profile string, delay time.Duration) string {
	t.Helper()

	return startE2EDelayingProxy(t, backend, delay, func(r *http.Request) bool {
		return r.Method == http.MethodGet && r.URL.Query().Get("profile") == profile
	})
}

// startE2EDelayingProxy is the shape both delaying proxies share: a reverse
// proxy in front of an already-running backend that sleeps for delay before
// forwarding any request shouldDelay says yes to, and forwards everything
// else untouched.
//
// The Host and Origin rewrites are not optional. The server's own Host
// allow-list (middleware.go's DNS-rebinding guard) and originCheck both
// compare against the address the request claims, which is this proxy's
// once a browser is talking to it - so a plain NewSingleHostReverseProxy
// gets every request refused and the SPA never loads at all.
func startE2EDelayingProxy(t *testing.T, backend string, delay time.Duration, shouldDelay func(*http.Request) bool) string {
	t.Helper()

	backendURL, err := url.Parse(backend)
	require.NoError(t, err)

	proxy := &httputil.ReverseProxy{
		Transport: e2eProxyTransport(t),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(backendURL)
			r.Out.Host = backendURL.Host
			// Same reason startE2EServerWithDelayedJobStart rewrites it:
			// originCheck (middleware.go) compares Origin against r.Host,
			// which is the BACKEND's address once this proxy has rewritten
			// it, so the browser's own Origin (this proxy) has to move too
			// or every mutation through here is refused as cross-origin.
			if r.Out.Header.Get("Origin") != "" {
				r.Out.Header.Set("Origin", "http://"+backendURL.Host)
			}
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if shouldDelay(r) {
			time.Sleep(delay)
		}
		proxy.ServeHTTP(w, r)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyServer := &http.Server{Handler: mux}
	served := make(chan error, 1)
	go func() { served <- proxyServer.Serve(ln) }()
	t.Cleanup(func() {
		_ = proxyServer.Close()
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("proxy server: %v", err)
		}
	})

	return "http://" + ln.Addr().String()
}

// startE2EProxyDelayingTheNthModFilesRead fronts an already-running backend
// with a reverse proxy that sleeps for delay before forwarding the nth GET
// of a mod-files read (/api/v1/mods/{source}/{id}/files) SCOPED TO profile,
// and forwards everything else untouched.
//
// Scoped to one profile because the scenario it serves needs the OTHER
// profile's read of the same mod to run unblocked, and because two
// hydrations of the same mod page under the same profile issue the
// IDENTICAL URL - which Chrome will not run concurrently at all (its HTTP
// cache takes a single-writer lock on an in-flight cacheable GET and queues
// the duplicate behind it), so no proxy delay could order them.
//
// Its subject is MIN-2 of the closing wave's gate review, and it is the
// same trick startE2EProxyDelayingProfileReads plays for C-1: two
// hydrations of the SAME mod page settle in microseconds on a loopback
// server, so which one lands last is the scheduler's choice. Delaying
// exactly one of them makes the out-of-order landing a certainty instead.
//
// The mod-files read specifically, because it is hydrateModPage's PRIMARY
// read - the one whose write resets detail, versions and updates to null.
func startE2EProxyDelayingTheNthModFilesRead(t *testing.T, backend, profile string, n int, delay time.Duration) string {
	t.Helper()

	var seen atomic.Int64
	return startE2EDelayingProxy(t, backend, delay, func(r *http.Request) bool {
		if r.Method != http.MethodGet ||
			!strings.HasPrefix(r.URL.Path, "/api/v1/mods/") ||
			!strings.HasSuffix(r.URL.Path, "/files") ||
			r.URL.Query().Get("profile") != profile {
			return false
		}
		return int(seen.Add(1)) == n
	})
}

// newE2EFixtureWithASwitchTargetAndSlowStaleReads is
// newE2EFixtureWithASwitchTarget served through the profile-read-delaying
// proxy above, pointed at the profile the scenario switches AWAY from.
func newE2EFixtureWithASwitchTargetAndSlowStaleReads(t *testing.T, delay time.Duration) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithASwitchTarget(t)
	f.BaseURL = startE2EProxyDelayingProfileReads(t, f.BaseURL, f.Profile, delay)
	return f
}

// newE2EFixtureWithAnUnlistedInstall seeds the state `lmm profile sync`
// exists for, which is the MIRROR of newE2EFixtureWithAnUnappliedProfile's:
// a mod installed and enabled in the database that the profile's own load
// order does not list.
//
// Alpha Mod is in both; Beta Mod is installed only. So the profile's own
// mod count (1) runs BEHIND the installed rows (2), which is what Mission
// Control's profile card reads to decide it has a sync to offer.
func newE2EFixtureWithAnUnlistedInstall(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)

	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.pak": []byte("alpha content")})
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"beta.pak": []byte("beta content")})

	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))
	return f
}

// compileE2ESource is the browser harness's fakeSource plus
// source.MergeCompiler, so a fixture game can be DeployCompile.
//
// Pak conversion is only meaningful for a game whose deploy mode compiles a
// merged artifact AND whose mod has a retained file the compiler classifies
// as convertible - core resolves the game's ONE compile-capable source to
// decide, so a game mapping none has nothing to convert and
// core.ModListing's tri-state convert_paks stays null. It is the same
// shaped stand-in api_mod_convert_internal_test.go builds one package
// boundary away, for the same reason: nothing here compiles or reads a real
// pak, which is internal/source and internal/core's ground to cover.
type compileE2ESource struct{ *fakeSource }

func (*compileE2ESource) ValidateSource(string) error { return nil }
func (*compileE2ESource) MergeCompile(context.Context, string, []source.MergeSource, string) ([]string, []source.MergeFailure, error) {
	return nil, nil, nil
}
func (*compileE2ESource) ResolveBaseArtifact(*domain.Game) (string, error) { return "", nil }
func (*compileE2ESource) FingerprintBase(string) (string, error)           { return "", nil }
func (*compileE2ESource) IsNativeMergeSource(name string) bool             { return name == "exmodz" }
func (*compileE2ESource) IsConvertibleArtifact(name string) bool           { return name == "pak" }
func (*compileE2ESource) ClassifyMergeSource(id string) (string, bool) {
	if id == "pak" {
		return "pak", true
	}
	return "exmodz", false
}
func (*compileE2ESource) MergedArtifactName() string            { return "zzz_LMM_Merged_P.pak" }
func (*compileE2ESource) MergedArtifactLabel() string           { return "Merged Pak" }
func (*compileE2ESource) RestoredArtifactName(id string) string { return id + "_P.pak" }

var _ source.MergeCompiler = (*compileE2ESource)(nil)

// newE2EFixtureWithAConvertibleMod is the world `lmm mod convert` acts on: a
// DeployCompile game whose one installed mod carries a pak-kind retained
// file, which is the only state in which core.ModListing's convert_paks is
// non-null and the SPA offers the toggle at all.
func newE2EFixtureWithAConvertibleMod(t *testing.T) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &compileE2ESource{fakeSource: newFakeSource("fake")}
	svc.RegisterSource(src)

	ctx := t.Context()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Compile Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		DeployMode:  domain.DeployCompile,
		ConvertPaks: true,
		SourceIDs:   map[string]string{"fake": ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: "fake", Name: "Convertible Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		ConvertPaks:  true,
		FileIDs:      []string{"pak"},
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "m1", Version: "1.0"}))

	baseURL := startE2EServer(t, svc)
	browserCtx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           browserCtx,
		BaseURL:       baseURL,
		Svc:           svc,
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}
}

// newE2EFixtureWithATagCapableSource is the world `lmm search --tag` acts
// on: a game whose one source is registered under the id the SPA's own
// TAG_CAPABLE_SOURCES list names (searchpage.js), holding one tagged mod
// and one untagged one so the filter has something to actually narrow.
//
// The source is this package's fakeSource under the nexusmods id, not a
// real client: nothing here talks to NexusMods, and the property under test
// is that the FILTER reaches the source at all - which fakeSource's own tag
// support (fakeModHasEveryTag) answers exactly as well.
func newE2EFixtureWithATagCapableSource(t *testing.T) e2eFixture {
	t.Helper()
	src := newFakeSource("nexusmods")
	src.addMod(fakeSourceMod{
		Mod:  domain.Mod{ID: "armoured", SourceID: "nexusmods", Name: "Armoured Mod", Version: "1.0"},
		Tags: []string{"armour"},
	})
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{ID: "plain", SourceID: "nexusmods", Name: "Plain Mod", Version: "1.0"},
	})
	return newE2EFixtureFromSource(t, src)
}
