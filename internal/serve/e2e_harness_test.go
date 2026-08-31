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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
)

// e2eTimeout bounds one chromedp.Run. Generous, because a cold browser
// start is the slowest thing in this package by an order of magnitude, and
// a timeout here should mean "the page is broken", never "the machine is
// busy".
const e2eTimeout = 30 * time.Second

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

// sandboxE2EEnv points HOME and every XDG_* path at throwaway directories,
// so nothing a test drives - the Service, or the browser the harness
// launches - can reach the developer's real config, data or cache. The
// package-internal tests have their own copy (jobs_internal_test.go's
// sandboxEnv); package serve_test cannot see it.
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

// newE2EFixture seeds a Service, serves it on a real loopback listener, and
// opens a browser against it. Every resource is released through t.Cleanup.
func newE2EFixture(t *testing.T) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           ctx,
		BaseURL:       startE2EServer(t, svc),
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
	f := newE2EFixture(t)

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

	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           ctx,
		BaseURL:       startE2EServer(t, svc),
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

	ctx, browserErrors := newE2EBrowser(t)
	return e2eMultiGameFixture{
		Ctx:           ctx,
		BaseURL:       startE2EServer(t, svc),
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
func startE2EServer(t *testing.T, svc *core.Service) string {
	t.Helper()

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler),
		serve.Options{Addr: "127.0.0.1:0", ShutdownGrace: 5 * time.Second})
	addr, err := srv.Listen()
	require.NoError(t, err)

	serveCtx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			require.NoError(t, err)
		case <-time.After(15 * time.Second):
			t.Error("the E2E server did not shut down")
		}
	})

	return "http://" + addr.String()
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

	ctx, browserErrors := newE2EBrowser(t)
	baseURL, setFailing := startE2EServerWithFailingPath(t, svc, failPath)
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

	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.ExecPath(binary))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(t.Context(), opts...)
	t.Cleanup(cancelAlloc)

	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)

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
