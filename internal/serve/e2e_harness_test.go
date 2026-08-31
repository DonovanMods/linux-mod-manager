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
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync"
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
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}
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
