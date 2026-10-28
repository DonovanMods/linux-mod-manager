package serve_test

// The browser scenarios for the toggle request ledger (issue 432,
// spa/app/toggleack.js): one scenario per transition that module's doc
// comment lists, driven through a real server and a real browser.
//
// e2e_toggle_ack_test.go covers what a click LOOKS like. This file covers
// how a request ENDS - including the endings the wire can take away: a
// start that never makes a job, a job that ends before its start is
// answered, a job that ends while the activity stream is down, a job the
// server forgets, and a stream the browser gives up on. Each of those used
// to leave a row saying "Disabling…" for the rest of the session.
//
// The proxy below (toggleWire) is how the wire misbehaves on cue. It extends
// e2e_toggle_ack_test.go's gated proxy with everything else these endings
// need, and every lever it has is deterministic: nothing here races a timer
// against the machine's speed except the browser's own EventSource
// reconnect, which the scenarios wait for rather than sleep through.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// toggleWire is a reverse proxy in front of a real server whose every
// misbehaviour is switched on and off by the scenario.
//
// Toggle starts (POST /api/v1/mods/{source}/{id}/{enable,disable}):
//   - the first `pass` go straight through; later ones are held until
//     releaseToggles, as in startE2EServerWithGatedToggle;
//   - a start for faultMod is refused (faultKind "csrf": the backend's own
//     403, reached with a wrong token) or dropped ("drop": the connection is
//     closed with no answer at all);
//   - while holdAnswers is set a start is FORWARDED at once but its answer
//     is held until releaseAnswers - the job runs and ends before the page
//     learns its id;
//   - stall makes a start go unanswered until releaseStalled, however long
//     the page waits and even once it has hung up: "answer" forwards the
//     start at once (the change happens, the answer never comes), "request"
//     forwards it only when released (the change happens late). Either way
//     the answer is then written to whatever is left of the connection, and
//     lateAnswers counts it. stallMod, when set, confines the stall to that
//     mod's start;
//
// GET /api/v1/mods answers a 500 while failMods is set, and GET
// /api/v1/status (scoped or not) while failStatus is set (issue 454);
// modsReads counts every library read that reached the wire.
//
// GET /api/v1/mods is held while holdMods is set: releaseNextMods answers
// the oldest read still held, releaseMods every read, held or to come.
// modsHeld counts the reads held, modsAnswered the held reads since
// answered. Chrome sends a second GET for a URL whose first is still
// unanswered only once that first one is (its HTTP cache's lock), so while
// one library read is held the next waits in the browser, not here.
// updatesReads counts GET /api/v1/updates, which a hydrate issues together
// with its library read and which nothing holds - the sign that a hydrate
// has got as far as its library read.
//
// The activity stream (GET /api/v1/events) is relayed frame by frame so a
// scenario can:
//   - withholdDone: drop every job_done frame - a job that never reaches a
//     terminal frame, as far as the page can tell;
//   - withholdStarted: drop every job_started frame too - with withholdDone,
//     a job whose whole life passes unheard;
//   - killStreams: hang up every open stream, as a restart or a lagging
//     watcher does; EventSource reconnects on its own;
//   - forget: answer a (re)connect with an empty snapshot and every
//     GET /api/v1/jobs/{id} with the server's own 404 - a server that no
//     longer knows the page's jobs;
//   - refuse: answer a (re)connect with a 502, which makes EventSource give
//     up for good (readyState CLOSED).
//
// connects counts every stream request, answered or refused.
type toggleWire struct {
	backend *url.URL
	proxy   *httputil.ReverseProxy
	client  *http.Client

	pass     int64
	gate     chan struct{}
	gateOnce sync.Once
	toggles  atomic.Int64

	faultMod  atomic.Value // string
	faultKind atomic.Value // string: "csrf" or "drop"

	holdAnswers atomic.Bool
	answers     chan struct{}
	answersOnce sync.Once

	stall       atomic.Value // string: "", "answer" or "request"
	stallMod    atomic.Value // string
	stalled     chan struct{}
	stalledOnce sync.Once
	lateAnswers atomic.Int64

	holdMods     atomic.Bool
	modsGate     chan struct{}
	modsOnce     sync.Once
	modsHeld     atomic.Int64
	modsAnswered atomic.Int64
	modsQueue    []chan struct{} // guarded by mu
	updatesReads atomic.Int64
	modsReads    atomic.Int64
	failMods     atomic.Bool
	failStatus   atomic.Bool

	withholdDone    atomic.Bool
	withholdStarted atomic.Bool
	forget          atomic.Bool
	refuse          atomic.Bool
	refused         atomic.Int64
	connects        atomic.Int64
	lookups         atomic.Int64

	mu      sync.Mutex
	streams []context.CancelFunc
}

func (w *toggleWire) releaseToggles() { w.gateOnce.Do(func() { close(w.gate) }) }
func (w *toggleWire) releaseAnswers() { w.answersOnce.Do(func() { close(w.answers) }) }
func (w *toggleWire) releaseMods()    { w.modsOnce.Do(func() { close(w.modsGate) }) }
func (w *toggleWire) releaseStalled() { w.stalledOnce.Do(func() { close(w.stalled) }) }

// releaseNextMods answers the oldest library read still held.
func (w *toggleWire) releaseNextMods(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	require.NotEmpty(t, w.modsQueue, "no library read is held")
	close(w.modsQueue[0])
	w.modsQueue = w.modsQueue[1:]
}

// killStreams hangs up every activity stream open right now.
func (w *toggleWire) killStreams() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, cancel := range w.streams {
		cancel()
	}
	w.streams = nil
}

func (w *toggleWire) trackStream(cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.streams = append(w.streams, cancel)
}

func (w *toggleWire) serveToggle(rw http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "enable" && action != "disable" {
		w.proxy.ServeHTTP(rw, r)
		return
	}
	if w.toggles.Add(1) > w.pass {
		select {
		case <-w.gate:
		case <-r.Context().Done():
			return
		}
	}
	if mod, _ := w.faultMod.Load().(string); mod != "" && mod == r.PathValue("id") {
		switch kind, _ := w.faultKind.Load().(string); kind {
		case "csrf":
			r.Header.Set("X-CSRF-Token", "not-the-shell-token")
		case "drop":
			if hj, ok := rw.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}
	}
	if mode, _ := w.stall.Load().(string); mode != "" {
		if mod, _ := w.stallMod.Load().(string); mod == "" || mod == r.PathValue("id") {
			w.serveStalled(rw, r, mode)
			return
		}
	}
	if !w.holdAnswers.Load() {
		w.proxy.ServeHTTP(rw, r)
		return
	}
	answer := httptest.NewRecorder()
	w.proxy.ServeHTTP(answer, r)
	select {
	case <-w.answers:
	case <-r.Context().Done():
		return
	}
	for name, values := range answer.Header() {
		rw.Header()[name] = values
	}
	rw.WriteHeader(answer.Code)
	_, _ = rw.Write(answer.Body.Bytes())
}

// serveStalled is the stall lever: the start reaches the backend on a
// context the page's hang-up cannot cancel, and its answer waits for
// releaseStalled.
func (w *toggleWire) serveStalled(rw http.ResponseWriter, r *http.Request, mode string) {
	detached := r.WithContext(context.WithoutCancel(r.Context()))
	answer := httptest.NewRecorder()
	if mode == "answer" {
		w.proxy.ServeHTTP(answer, detached)
	}
	<-w.stalled
	if mode == "request" {
		w.proxy.ServeHTTP(answer, detached)
	}
	w.lateAnswers.Add(1)
	for name, values := range answer.Header() {
		rw.Header()[name] = values
	}
	rw.WriteHeader(answer.Code)
	_, _ = rw.Write(answer.Body.Bytes())
}

// serveInjectedFailure answers the error envelope a failing handler would.
func serveInjectedFailure(rw http.ResponseWriter) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusInternalServerError)
	_, _ = rw.Write([]byte(`{"error":"injected failure"}`))
}

func (w *toggleWire) serveStatus(rw http.ResponseWriter, r *http.Request) {
	if w.failStatus.Load() {
		serveInjectedFailure(rw)
		return
	}
	w.proxy.ServeHTTP(rw, r)
}

func (w *toggleWire) serveMods(rw http.ResponseWriter, r *http.Request) {
	w.modsReads.Add(1)
	if w.failMods.Load() {
		serveInjectedFailure(rw)
		return
	}
	if w.holdMods.Load() {
		mine := make(chan struct{})
		w.mu.Lock()
		w.modsQueue = append(w.modsQueue, mine)
		w.mu.Unlock()
		w.modsHeld.Add(1)
		select {
		case <-w.modsGate:
		case <-mine:
		case <-r.Context().Done():
			return
		}
		defer w.modsAnswered.Add(1)
	}
	w.proxy.ServeHTTP(rw, r)
}

func (w *toggleWire) serveUpdates(rw http.ResponseWriter, r *http.Request) {
	w.updatesReads.Add(1)
	w.proxy.ServeHTTP(rw, r)
}

func (w *toggleWire) serveEvents(rw http.ResponseWriter, r *http.Request) {
	w.connects.Add(1)
	if w.refuse.Load() {
		w.refused.Add(1)
		http.Error(rw, "refused by the scenario", http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w.trackStream(cancel)

	if w.forget.Load() {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-cache")
		rw.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(rw, "event: snapshot\ndata: {\"jobs\":[]}\n\n")
		rw.(http.Flusher).Flush()
		<-ctx.Done()
		return
	}
	w.relayEvents(ctx, rw)
}

// relayEvents copies the backend's activity stream one frame at a time,
// leaving out the lifecycle frames the scenario is withholding.
func (w *toggleWire) relayEvents(ctx context.Context, rw http.ResponseWriter) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.backend.String()+"/api/v1/events", nil)
	if err != nil {
		return
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for name, values := range resp.Header {
		rw.Header()[name] = values
	}
	rw.WriteHeader(resp.StatusCode)
	flusher := rw.(http.Flusher)
	flusher.Flush()

	reader := bufio.NewReader(resp.Body)
	var frame strings.Builder
	event := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		frame.WriteString(line)
		if name, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(name)
		}
		if strings.TrimRight(line, "\r\n") != "" {
			continue
		}
		withheld := event == "job_done" && w.withholdDone.Load() ||
			event == "job_started" && w.withholdStarted.Load()
		if !withheld {
			if _, err := io.WriteString(rw, frame.String()); err != nil {
				return
			}
			flusher.Flush()
		}
		frame.Reset()
		event = ""
	}
}

func (w *toggleWire) serveJobLookup(rw http.ResponseWriter, r *http.Request) {
	w.lookups.Add(1)
	if !w.forget.Load() {
		w.proxy.ServeHTTP(rw, r)
		return
	}
	body, err := json.Marshal(map[string]string{"error": fmt.Sprintf("unknown job %q", r.PathValue("id"))})
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusNotFound)
	_, _ = rw.Write(body)
}

// runningJobs asks the BACKEND (not the proxy, which the scenario may be
// using to lie to the page) how many jobs are still running.
func (w *toggleWire) runningJobs(t *testing.T) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.backend.String()+"/api/v1/jobs", nil)
	require.NoError(t, err)
	resp, err := w.client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var index struct {
		Jobs []struct {
			State string `json:"state"`
		} `json:"jobs"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&index))
	running := 0
	for _, job := range index.Jobs {
		if job.State == "running" {
			running++
		}
	}
	return running
}

// newToggleWireFixture is the unseeded world behind a toggleWire: src is the
// game's source, and the first `pass` toggle starts go straight through.
func newToggleWireFixture(t *testing.T, src *fakeSource, pass int64) (e2eFixture, *toggleWire) {
	t.Helper()
	sandboxE2EEnv(t)
	svc, game := newFixtureServiceWithSource(t, src)
	backend := startE2EServer(t, svc)
	backendURL, err := url.Parse(backend)
	require.NoError(t, err)

	transport := e2eProxyTransport(t)
	w := &toggleWire{
		backend:  backendURL,
		client:   &http.Client{Transport: transport},
		pass:     pass,
		gate:     make(chan struct{}),
		answers:  make(chan struct{}),
		stalled:  make(chan struct{}),
		modsGate: make(chan struct{}),
		proxy: &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(backendURL)
				r.Out.Host = backendURL.Host
				if r.Out.Header.Get("Origin") != "" {
					r.Out.Header.Set("Origin", "http://"+backendURL.Host)
				}
			},
		},
	}
	w.faultMod.Store("")
	w.faultKind.Store("")
	w.stall.Store("")
	w.stallMod.Store("")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/mods/{source}/{id}/{action}", w.serveToggle)
	mux.HandleFunc("GET /api/v1/mods", w.serveMods)
	mux.HandleFunc("GET /api/v1/status", w.serveStatus)
	mux.HandleFunc("GET /api/v1/updates", w.serveUpdates)
	mux.HandleFunc("GET /api/v1/events", w.serveEvents)
	mux.HandleFunc("GET /api/v1/jobs/{id}", w.serveJobLookup)
	mux.Handle("/", w.proxy)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: mux}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()
	t.Cleanup(func() {
		w.releaseToggles()
		w.releaseAnswers()
		w.releaseMods()
		w.releaseStalled()
		w.killStreams()
		_ = server.Close()
		if err := <-served; err != nil && err != http.ErrServerClosed {
			t.Errorf("toggle wire: %v", err)
		}
	})

	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx:           ctx,
		BaseURL:       "http://" + ln.Addr().String(),
		Svc:           svc,
		Game:          game,
		Profile:       "default",
		BrowserErrors: browserErrors,
	}, w
}

// catalogSource is a fake source that can answer the slide-over's live
// reads for each named mod (id -> name).
func catalogSource(mods map[string]string) *fakeSource {
	src := newFakeSource("fake")
	for id, name := range mods {
		src.addMod(fakeSourceMod{Mod: domain.Mod{ID: id, SourceID: "fake", Name: name, Version: "1.0"}})
	}
	return src
}

// modEnabled reads a mod's enabled flag straight from the service.
func modEnabled(t *testing.T, f e2eFixture, id string) bool {
	t.Helper()
	m, err := f.Svc.GetInstalledMod(t.Context(), "fake", id, f.Game.ID, f.Profile)
	require.NoError(t, err)
	return m.Enabled
}

// awaitJobsOver waits until the mods named are in the state wanted AND the
// backend has no job left running - the point at which every job these
// scenarios start has written its terminal state.
func awaitJobsOver(t *testing.T, f e2eFixture, w *toggleWire, want map[string]bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		for id, enabled := range want {
			if modEnabled(t, f, id) != enabled {
				return false
			}
		}
		return w.runningJobs(t) == 0
	}, 15*time.Second, 20*time.Millisecond, "the server must have finished its jobs: %v", want)
}

// toastSaysJS is true once any toast's text contains text.
func toastSaysJS(text string) string {
	return fmt.Sprintf(`Array.from(document.querySelectorAll(".toast")).some((t) => t.textContent.includes(%q))`, text)
}

const noRowPendingJS = `document.querySelectorAll(".mod-row--pending").length === 0`

// runWithin is runInBrowser under a tighter budget, for a wait that a
// broken ledger would otherwise spend the harness's whole minute on.
func runWithin(t *testing.T, f e2eFixture, budget time.Duration, actions ...chromedp.Action) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.Ctx, budget)
	defer cancel()
	require.NoError(t, chromedp.Run(ctx, actions...))
}

// uncaughtErrors is BrowserErrors without the network log entries a
// scenario that breaks the wire on purpose produces by design (a refused
// start, a 404 lookup, a 502 stream).
func uncaughtErrors(f e2eFixture) []string {
	var out []string
	for _, e := range f.BrowserErrors() {
		if strings.HasPrefix(e, "uncaught") {
			out = append(out, e)
		}
	}
	return out
}

// TestE2E_LibraryBatch_ARowTheBatchOwesStaysPendingUntilItsOwnJob is the
// re-review's F1, as the contract now states it: a pending entry settles
// only on its OWN job's end. Beta already reads enabled when "Enable" is
// pressed for Alpha and Beta, and used to be released on the spot - live,
// while the batch still owed it a job. A click the user then made on it was
// overtaken by that job, and the row said "Disabling…" for the rest of the
// session over a mod the server had enabled.
func TestE2E_LibraryBatch_ARowTheBatchOwesStaysPendingUntilItsOwnJob(t *testing.T) {
	f, wire := newToggleWireFixture(t, catalogSource(map[string]string{"a": "Alpha Mod", "b": "Beta Mod"}), 0)
	seedToggleMod(t, f, "a", "Alpha Mod", false, map[string][]byte{"alpha.pak": []byte("alpha")})
	seedToggleMod(t, f, "b", "Beta Mod", true, map[string][]byte{"beta.pak": []byte("beta")})

	var beta e2eRowToggleState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
		chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-enable"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length >= 1`),
		settleEffects(),
		chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &beta),
	)
	awaitToggleRequests(t, &wire.toggles, 1)

	assert.True(t, beta.Pending,
		"Beta already reads enabled, but the batch still owes it a job - agreeing with the server is not settling (row: %+v)", beta)
	assert.True(t, beta.Checked, "it holds the value the batch asked for")
	assert.True(t, beta.Disabled, "and its box is not live while that job is owed")
	assert.Equal(t, "Enabling…", beta.Live)

	f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Beta Mod"), nil), settleEffects())
	assert.EqualValues(t, 1, wire.toggles.Load(), "a click on the frozen box sends nothing")

	// One live request per mod, whichever surface asks: Beta's slide-over
	// says what the row says, and offers nothing either.
	var panel e2eToggleButton
	f.runInBrowser(t,
		chromedp.Evaluate(clientNavigateJS(f.ContextPath()+"?mod=fake/b"), nil),
		pollUntil(`document.querySelector('.slide-over [data-action="toggle"]') !== null`),
		settleEffects(),
		chromedp.Evaluate(slideOverToggleJS, &panel),
		chromedp.Evaluate(`document.querySelector('.slide-over [data-action="toggle"]').click()`, nil),
		settleEffects(),
		chromedp.Evaluate(clientNavigateJS(f.ContextPath()), nil),
		waitGone(`.slide-over`),
	)
	assert.Equal(t, e2eToggleButton{Label: "Enabling…", Disabled: true}, panel,
		"the slide-over shows the batch's request for Beta rather than offering a second one")
	assert.EqualValues(t, 1, wire.toggles.Load(), "and its button sends nothing either")

	wire.releaseToggles()
	// Every wait below is on an event, never on the server looking idle:
	// Beta already reads enabled, so "no job running and both enabled" is
	// true in the gap after Alpha's job ends and before the batch sends
	// Beta's start - a gap a loaded machine stretches.
	awaitToggleRequests(t, &wire.toggles, 2)
	var settled e2eRowToggleState
	runWithin(t, f, 20*time.Second,
		// The page has heard Beta's job end...
		pollUntil(toastSaysJS("Enabled 2/2")),
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &settled),
	)
	// ...so the server has finished it, and the batch has nothing left to
	// send.
	awaitJobsOver(t, f, wire, map[string]bool{"a": true, "b": true})
	assert.EqualValues(t, 2, wire.toggles.Load(), "one job per mod: the batch's two, and nothing else")
	assert.True(t, settled.Checked, "the box agrees with the server")
	assert.False(t, settled.Disabled, "and is the user's again")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryRow_ASucceededToggleSettlesOnTheReadAfterIt is the other
// half of the same contract: once a request's own job has succeeded, the
// next document read decides what the box says - even when that read
// disagrees with what was asked, because something else changed the mod in
// between (another tab, the CLI). The request used to wait for the server
// to agree with it, which here it never does.
func TestE2E_LibraryRow_ASucceededToggleSettlesOnTheReadAfterIt(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
	seedDeployableMods(t, f.Svc, f.Game)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
	)
	// From here on the library's reads are held, so the one the disable's
	// own ending starts cannot land before the scenario is ready for it.
	wire.holdMods.Store(true)
	f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil))
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})
	require.Eventually(t, func() bool { return wire.modsHeld.Load() >= 1 },
		10*time.Second, 20*time.Millisecond, "the job's ending must re-read the library")

	var confirming e2eRowToggleState
	f.runInBrowser(t, settleEffects(), chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &confirming))
	assert.True(t, confirming.Pending,
		"until a read taken after the job's end has landed, the row keeps what was asked (row: %+v)", confirming)
	assert.False(t, confirming.Checked, "and does not flick back to the pre-job document")

	// Something other than this page enables Alpha again before that read
	// is answered.
	_, err := f.Svc.EnableMod(t.Context(), f.Game, f.Profile, "fake", "a")
	require.NoError(t, err)
	wire.holdMods.Store(false)
	wire.releaseMods()

	var settled e2eRowToggleState
	runWithin(t, f, 20*time.Second,
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &settled),
	)
	assert.True(t, settled.Checked, "the box shows what the read says, which is what is true")
	assert.False(t, settled.Disabled)
	assert.Empty(t, settled.Live)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryBatch_PendingRowsAreLeftOutOfANewBatch keeps the ledger at
// one live request per mod from the batch bar's side: a row whose own
// toggle is still in flight is not taken by a batch selected over it.
func TestE2E_LibraryBatch_PendingRowsAreLeftOutOfANewBatch(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 0)
	seedDeployableMods(t, f.Svc, f.Game)

	var disableLabel, disableTitle string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 1`),
		chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-action="batch-disable"]').title`, &disableTitle),
		chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
		settleEffects(),
		chromedp.Evaluate(`document.querySelector('[data-action="batch-disable"]').textContent.trim()`, &disableLabel),
	)
	awaitToggleRequests(t, &wire.toggles, 1)
	assert.Contains(t, disableTitle, "already",
		"with only the pending row selected, the refusal says why - and it is not Steam")
	assert.NotContains(t, disableTitle, "Steam")
	assert.Equal(t, "Disable (1)", disableLabel, "the batch takes Beta, not Alpha a second time")

	f.runInBrowser(t, chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery))
	awaitToggleRequests(t, &wire.toggles, 2)
	wire.releaseToggles()
	awaitJobsOver(t, f, wire, map[string]bool{"a": false, "b": false})
	runWithin(t, f, 20*time.Second, pollUntil(toastSaysJS("Disabled 1/1")), pollUntil(noRowPendingJS))
	assert.EqualValues(t, 2, wire.toggles.Load(), "Alpha's own request and Beta's, and no second one for Alpha")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ActivityGap_ABatchResumesOnceTheStreamReconnects is the
// re-review's F2: the activity stream drops while a batch's first job runs
// and ends. The reconnect's snapshot carries that job's end, and it used to
// be applied to the tray and nothing else - the batch waited for a job_done
// frame that had already gone by, never started Beta, and left both rows
// pending for good.
func TestE2E_ActivityGap_ABatchResumesOnceTheStreamReconnects(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 0)
	seedDeployableMods(t, f.Svc, f.Game)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
		chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 2`),
	)
	awaitToggleRequests(t, &wire.toggles, 1)

	// Alpha's job runs and ends while the page cannot hear it end...
	wire.withholdDone.Store(true)
	wire.releaseToggles()
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})
	// ...and then the stream drops. What reconnects hears it all again.
	wire.killStreams()
	wire.withholdDone.Store(false)

	var alpha, beta e2eRowToggleState
	runWithin(t, f, 30*time.Second,
		pollUntil(toastSaysJS("Disabled 2/2")),
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
		chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &beta),
	)
	awaitJobsOver(t, f, wire, map[string]bool{"a": false, "b": false})
	assert.EqualValues(t, 2, wire.toggles.Load(), "the batch resumed and started Beta")
	assert.False(t, alpha.Checked)
	assert.False(t, beta.Checked)
	assert.False(t, beta.Disabled)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ActivityGap_AToggleThatEndedDuringTheGapSettles is F2 for a
// single row: its job succeeds while the stream is down.
func TestE2E_ActivityGap_AToggleThatEndedDuringTheGapSettles(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
	seedDeployableMods(t, f.Svc, f.Game)

	wire.withholdDone.Store(true)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 1`),
	)
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})
	wire.killStreams()
	wire.withholdDone.Store(false)

	var alpha e2eRowToggleState
	runWithin(t, f, 30*time.Second,
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
	)
	assert.False(t, alpha.Checked, "a job that succeeded while the stream was down still settles the row")
	assert.False(t, alpha.Disabled)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ActivityGap_AJobTheServerForgotSettlesAsLost is the ending a
// bound request has when its job never reaches a terminal frame and the
// server, once the stream is back, no longer knows it at all - a restart,
// say. The row must not wait for a frame that cannot come: it settles as
// failed, says so, and shows what a fresh read says is true.
func TestE2E_ActivityGap_AJobTheServerForgotSettlesAsLost(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
		seedDeployableMods(t, f.Svc, f.Game)

		wire.withholdDone.Store(true)
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
			chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
			pollUntil(`document.querySelectorAll(".mod-row--pending").length === 1`),
		)
		awaitJobsOver(t, f, wire, map[string]bool{"a": false})
		wire.forget.Store(true)
		wire.killStreams()

		var alpha e2eRowToggleState
		var toast string
		runWithin(t, f, 30*time.Second,
			pollUntil(toastSaysJS("Lost track")),
			pollUntil(noRowPendingJS),
			chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
			chromedp.Evaluate(`Array.from(document.querySelectorAll(".toast")).map((t) => t.textContent).join(" | ")`, &toast),
		)
		assert.Positive(t, wire.lookups.Load(), "the page asked the server about the job it had lost sight of")
		assert.Contains(t, toast, "no longer knows", "the toast names what happened")
		assert.False(t, alpha.Checked,
			"the request is held until the fresh read lands, and that read says the disable did happen")
		assert.False(t, alpha.Disabled, "and the control is the user's again")
		assert.Empty(t, uncaughtErrors(f))
	})

	t.Run("batch", func(t *testing.T) {
		// Alpha's start goes through; Beta's is held until the scenario is
		// ready for it.
		f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1)
		seedDeployableMods(t, f.Svc, f.Game)

		wire.withholdDone.Store(true)
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
			chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
			chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
			chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
			chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
			pollUntil(`document.querySelectorAll(".mod-row--pending").length === 2`),
		)
		awaitJobsOver(t, f, wire, map[string]bool{"a": false})
		wire.forget.Store(true)
		wire.killStreams()

		// The lost job ends Alpha's wait, so the batch moves on to Beta.
		awaitToggleRequests(t, &wire.toggles, 2)
		runWithin(t, f, 30*time.Second, pollUntil(toastSaysJS("Lost track")))

		// The real stream comes back, and Beta's job runs on it.
		wire.forget.Store(false)
		wire.withholdDone.Store(false)
		wire.killStreams()
		wire.releaseToggles()
		awaitJobsOver(t, f, wire, map[string]bool{"a": false, "b": false})

		var alpha, beta e2eRowToggleState
		var toasts string
		runWithin(t, f, 30*time.Second,
			pollUntil(toastSaysJS("Disabled 1/2")),
			pollUntil(noRowPendingJS),
			chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
			chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &beta),
			chromedp.Evaluate(`Array.from(document.querySelectorAll(".toast")).map((t) => t.textContent).join(" | ")`, &toasts),
		)
		assert.EqualValues(t, 2, wire.toggles.Load())
		assert.Contains(t, toasts, "Outcome unknown: Alpha Mod",
			"the batch's tally does not call a job it lost sight of a failure")
		assert.False(t, alpha.Checked)
		assert.False(t, beta.Checked)
		assert.Empty(t, uncaughtErrors(f))
	})
}

// TestE2E_ActivityStream_IsReopenedAfterTheServerRefusesIt covers the one
// way EventSource stops on its own: a reconnect answered with anything but
// an event stream closes it for good. Nothing reopened it, so every job
// still running at that moment kept its row pending for the rest of the
// session. The page now opens a fresh stream itself, and the snapshot that
// brings back settles what ended in the meantime.
func TestE2E_ActivityStream_IsReopenedAfterTheServerRefusesIt(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
	seedDeployableMods(t, f.Svc, f.Game)

	wire.withholdDone.Store(true)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
		pollUntil(`document.querySelectorAll(".mod-row--pending").length === 1`),
	)
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})
	wire.refuse.Store(true)
	wire.killStreams()

	require.Eventually(t, func() bool { return wire.refused.Load() >= 2 },
		20*time.Second, 50*time.Millisecond,
		"the page keeps opening the stream after the browser has given up on it")
	wire.withholdDone.Store(false)
	wire.refuse.Store(false)

	var alpha e2eRowToggleState
	runWithin(t, f, 30*time.Second,
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
	)
	assert.False(t, alpha.Checked)
	assert.False(t, alpha.Disabled)
	assert.Empty(t, uncaughtErrors(f))
}

// TestE2E_LibraryRow_AJobThatEndsBeforeItsStartIsAnsweredStillSettles is
// the bind-after-end transition: the job runs and ends - its job_done frame
// is applied - before the page has read the answer that names it. Binding
// to a job that has already ended applies that ending at once.
func TestE2E_LibraryRow_AJobThatEndsBeforeItsStartIsAnsweredStillSettles(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mod         string
		enabled     bool
		cached      bool
		wantEnabled bool
	}{
		// Alpha is enabled and cached: the disable succeeds.
		{name: "succeeded", mod: "Alpha Mod", enabled: true, cached: true, wantEnabled: false},
		// Gamma is disabled with nothing cached: the enable fails.
		{name: "failed", mod: "Gamma Mod", enabled: false, cached: false, wantEnabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
			var files map[string][]byte
			if tc.cached {
				files = map[string][]byte{"mod.pak": []byte("mod")}
			}
			seedToggleMod(t, f, "m", tc.mod, tc.enabled, files)
			wire.holdAnswers.Store(true)

			f.runInBrowser(t,
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				chromedp.Evaluate(libraryToggleJS(tc.mod), nil),
				pollUntil(`document.querySelectorAll(".mod-row--pending").length === 1`),
			)
			awaitToggleRequests(t, &wire.toggles, 1)
			require.Eventually(t, func() bool { return wire.runningJobs(t) == 0 && modEnabled(t, f, "m") == tc.wantEnabled },
				15*time.Second, 20*time.Millisecond)

			var waiting e2eRowToggleState
			f.runInBrowser(t, settleEffects(), chromedp.Evaluate(libraryRowStateJS(tc.mod), &waiting))
			assert.True(t, waiting.Pending, "the start is not answered yet, so the request is still open (row: %+v)", waiting)

			wire.releaseAnswers()
			var settled e2eRowToggleState
			runWithin(t, f, 20*time.Second,
				pollUntil(noRowPendingJS),
				chromedp.Evaluate(libraryRowStateJS(tc.mod), &settled),
			)
			assert.Equal(t, tc.wantEnabled, settled.Checked, "the box shows the server's value")
			assert.False(t, settled.Disabled)
			assert.Empty(t, uncaughtErrors(f))
		})
	}
}

// TestE2E_ActivityGap_AJobKnownOnlyFromASnapshotEndsItsLateBinding is the
// same bind-after-end transition when the page heard nothing of the job at
// all: it started and ended while the stream was down, before the page had
// read the start that names it, so the only record of its end is the
// reconnect's snapshot - taken while nothing on the page knew the job
// existed, let alone waited on it. A binding (or a batch's wait) that
// consulted only the endings it had heard would wait for good.
func TestE2E_ActivityGap_AJobKnownOnlyFromASnapshotEndsItsLateBinding(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
			seedDeployableMods(t, f.Svc, f.Game)
			wire.holdAnswers.Store(true)

			start := []chromedp.Action{
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
			}
			if batch {
				start = append(start,
					chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
					chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
					chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
					chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
				)
			} else {
				start = append(start, chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil))
			}
			// From the page's first render on, the stream says nothing about
			// any job's life.
			f.runInBrowser(t, start[:3]...)
			wire.withholdStarted.Store(true)
			wire.withholdDone.Store(true)
			f.runInBrowser(t, start[3:]...)
			awaitToggleRequests(t, &wire.toggles, 1)
			awaitJobsOver(t, f, wire, map[string]bool{"a": false})

			// The stream comes back with the job's end in its snapshot, and only
			// then is the start answered.
			wire.withholdStarted.Store(false)
			wire.withholdDone.Store(false)
			wire.killStreams()
			require.Eventually(t, func() bool { return wire.connects.Load() >= 2 },
				15*time.Second, 20*time.Millisecond, "the page must reconnect")
			f.runInBrowser(t, settleEffects())
			wire.holdAnswers.Store(false)
			wire.releaseAnswers()

			want := map[string]bool{"a": false}
			if batch {
				want["b"] = false
			}
			awaitJobsOver(t, f, wire, want)
			wait := []chromedp.Action{pollUntil(noRowPendingJS)}
			if batch {
				wait = append(wait, pollUntil(toastSaysJS("Disabled 2/2")))
			}
			var alpha e2eRowToggleState
			wait = append(wait, chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha))
			runWithin(t, f, 20*time.Second, wait...)
			assert.False(t, alpha.Checked)
			assert.False(t, alpha.Disabled)
			assert.Empty(t, f.BrowserErrors())
		})
	}
}

// deadlineCompression is how much deadlineShim shrinks the page's deadlines
// by, so a scenario can wait one out in seconds.
const deadlineCompression = 40

// deadlineShim runs before the page's own scripts. It records every deadline
// the page sets (AbortSignal.timeout) and hands the browser one
// deadlineCompression times shorter - the page's own value is untouched,
// only how long the browser takes to reach it.
//
// The page sets two kinds, told apart by the module that asks: a request's
// (api.js), recorded in __requestDeadlines, and a toggle request's settle
// deadline (toggleack.js), recorded in __settleDeadlines. The second is
// compressed only when compressSettle is set, so a scenario that holds a
// settling read open on purpose can look behind it for as long as it needs.
// __settleDeadlinesPassed counts the settle deadlines that have passed; the
// shim hears each one before the page does, in the same event.
func deadlineShim(compressSettle bool) string {
	return fmt.Sprintf(`(() => {
	const timeout = AbortSignal.timeout.bind(AbortSignal);
	window.__requestDeadlines = [];
	window.__settleDeadlines = [];
	window.__settleDeadlinesPassed = 0;
	AbortSignal.timeout = (ms) => {
		const caller = (new Error().stack ?? "").split("\n")[2] ?? "";
		const settle = caller.includes("/toggleack.js");
		(settle ? window.__settleDeadlines : window.__requestDeadlines).push(ms);
		const signal = timeout(settle && !%t ? ms : ms / %d);
		if (settle) {
			signal.addEventListener("abort", () => { window.__settleDeadlinesPassed++; });
		}
		return signal;
	};
})()`, compressSettle, deadlineCompression)
}

// deadlineShimJS compresses request deadlines only.
var deadlineShimJS = deadlineShim(false)

// watchForPendingJS records, from the moment it runs, whether any row goes
// back to pending.
const watchForPendingJS = `(() => {
	window.__pendingAgain = false;
	new MutationObserver(() => {
		if (document.querySelector('.mod-row--pending, [data-testid="toggle-pending"]')) {
			window.__pendingAgain = true;
		}
	}).observe(document.body, {subtree: true, childList: true, attributes: true, attributeFilter: ["class"]});
})()`

// toastTextsJS reads every toast's text.
const toastTextsJS = `Array.from(document.querySelectorAll(".toast")).map((t) => t.textContent.trim())`

// TestE2E_LibraryToggle_AStartTheServerNeverAnswersSettlesAtTheDeadline is
// the requested -> unanswered -> removed path: the server takes the start
// and never answers it. Browsers put no deadline on a request of their own,
// so without one the row said "Disabling…" for as long as the connection
// stayed open. At the start's deadline the request is over: the row says
// the server did not answer and that the change may or may not have
// happened, keeps what was asked until a fresh read lands rather than
// guessing, and then shows that read. The answer, when it finally comes,
// changes nothing.
func TestE2E_LibraryToggle_AStartTheServerNeverAnswersSettlesAtTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stall string
		// applied is whether the change has happened by the time the
		// deadline passes.
		applied bool
	}{
		{name: "applied, never answered", stall: "answer", applied: true},
		{name: "not yet applied", stall: "request", applied: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
			seedDeployableMods(t, f.Svc, f.Game)
			wire.stall.Store(tc.stall)

			f.runInBrowser(t,
				chromedp.ActionFunc(func(ctx context.Context) error {
					_, err := page.AddScriptToEvaluateOnNewDocument(deadlineShimJS).Do(ctx)
					return err
				}),
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
			)
			// Every read from here on waits for the scenario, so the one the
			// deadline starts can be held open and looked behind.
			wire.holdMods.Store(true)

			clicked := time.Now()
			var during e2eRowToggleState
			f.runInBrowser(t,
				chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
				pollUntil(`document.querySelectorAll(".mod-row--pending").length === 1`),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &during),
			)
			awaitToggleRequests(t, &wire.toggles, 1)
			if tc.applied {
				awaitJobsOver(t, f, wire, map[string]bool{"a": false})
			}
			assert.True(t, during.Pending, "the unanswered start is pending while its deadline runs (row: %+v)", during)
			assert.True(t, during.Disabled)

			var asked []float64
			f.runInBrowser(t, chromedp.Evaluate(`window.__requestDeadlines`, &asked))
			require.Len(t, asked, 1, "the start, and only the start, carries a deadline")
			deadline := time.Duration(asked[0]) * time.Millisecond
			compressed := deadline / deadlineCompression
			t.Logf("the start's deadline is %v (%v in this scenario)", deadline, compressed)
			assert.GreaterOrEqual(t, deadline, 30*time.Second,
				"the deadline comfortably exceeds a start queued behind slow reads on a busy machine")

			var toasts []string
			var held e2eRowToggleState
			runWithin(t, f, compressed+15*time.Second,
				pollUntil(toastSaysJS("did not answer")),
				chromedp.Evaluate(toastTextsJS, &toasts),
				settleEffects(),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &held),
			)
			toldAt := time.Since(clicked)
			assert.GreaterOrEqual(t, toldAt, compressed, "the request is given its whole deadline")
			assert.Less(t, toldAt, compressed+5*time.Second, "and is over promptly once the deadline passes")
			assert.Contains(t, strings.Join(toasts, " | "), "may or may not have been applied")
			assert.True(t, held.Pending,
				"the deadline alone reverts nothing: the row keeps what was asked until a fresh read lands (row: %+v)", held)
			assert.False(t, held.Checked)

			wire.holdMods.Store(false)
			wire.releaseMods()
			var settled e2eRowToggleState
			runWithin(t, f, 15*time.Second,
				pollUntil(noRowPendingJS),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &settled),
				chromedp.Evaluate(watchForPendingJS, nil),
			)
			assert.Less(t, time.Since(clicked), compressed+10*time.Second, "the row leaves pending once that read lands")
			assert.Equal(t, !tc.applied, settled.Checked, "and shows what the server reports")
			assert.False(t, settled.Disabled, "with the control live again")

			// The answer finally comes - and, for the late start, the change
			// with it.
			wire.releaseStalled()
			require.Eventually(t, func() bool { return wire.lateAnswers.Load() == 1 },
				15*time.Second, 20*time.Millisecond)
			awaitJobsOver(t, f, wire, map[string]bool{"a": false})

			var after e2eRowToggleState
			var pendingAgain bool
			runWithin(t, f, 20*time.Second,
				// The row follows the documents the server sends, not the
				// answer: the late job's own end re-reads the library.
				pollUntil(`(() => {
					const row = document.querySelector('.mod-row[data-mod="fake:a"]');
					return row !== null && !row.querySelector("td.col--enabled input").checked;
				})()`),
				settleEffects(),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &after),
				chromedp.Evaluate(`window.__pendingAgain`, &pendingAgain),
				chromedp.Evaluate(toastTextsJS, &toasts),
			)
			assert.False(t, pendingAgain, "the late answer never brought the request back")
			assert.False(t, after.Pending)
			assert.False(t, after.Disabled)
			assert.EqualValues(t, 1, wire.toggles.Load(), "and nothing was sent again")
			unanswered := 0
			for _, toast := range toasts {
				if strings.Contains(toast, "did not answer") {
					unanswered++
				}
				assert.NotContains(t, toast, "disable failed", "the request is never also settled as a refused start")
			}
			assert.Equal(t, 1, unanswered, "the request was settled once: %q", toasts)
			assert.Empty(t, uncaughtErrors(f))
		})
	}
}

// recordRowStatesJS starts recording every state the row for modKey paints
// ("checked/pending", "unchecked/settled", ...), each once per change and
// timed from the moment it starts. A MutationObserver rather than a sampled
// frame: a render that settles a row changes its class and its box's
// disabled attribute, so no painted state, however brief, goes unrecorded -
// and a headless page that stops scheduling frames stops nothing here.
func recordRowStatesJS(modKey string) string {
	return fmt.Sprintf(`(() => {
	window.__rowStates = [];
	window.__recordingSince = performance.now();
	let last = "";
	const sample = () => {
		const row = document.querySelector('.mod-row[data-mod=%q]');
		if (!row) return;
		const box = row.querySelector("td.col--enabled input");
		const state = (box.checked ? "checked" : "unchecked") + "/" +
			(row.classList.contains("mod-row--pending") ? "pending" : "settled");
		if (state === last) return;
		last = state;
		window.__rowStates.push({state, at: performance.now() - window.__recordingSince});
	};
	sample();
	new MutationObserver(sample).observe(document.body,
		{subtree: true, childList: true, attributes: true, characterData: true});
})()`, modKey)
}

// e2eRowStateChange is one entry recordRowStatesJS records.
type e2eRowStateChange struct {
	State string  `json:"state"`
	At    float64 `json:"at"`
}

func rowStateNames(changes []e2eRowStateChange) []string {
	names := make([]string, 0, len(changes))
	for _, c := range changes {
		names = append(names, c.State)
	}
	return names
}

// awaitMods waits until the proxy has held n library reads and answered
// `answered` of them.
func awaitMods(t *testing.T, w *toggleWire, held, answered int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return w.modsHeld.Load() >= held && w.modsAnswered.Load() >= answered
	}, 15*time.Second, 20*time.Millisecond,
		"the library reads: want %d held and %d answered", held, answered)
}

// TestE2E_LibraryToggle_ASupersededRereadSettlesNothing is the final
// review's R1. Alpha's job ends, and the library read its ending starts is
// slow; Beta's job ends meanwhile and starts a newer read, which supersedes
// Alpha's - so Alpha's read resolves having written nothing. The request
// used to be removed right there, and Alpha's box painted the document from
// BEFORE its job, live, until Beta's read landed (about the gap between the
// two clicks, with a slow update check). A request settles only once a read
// taken after its job's end has actually been committed.
func TestE2E_LibraryToggle_ASupersededRereadSettlesNothing(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "rows"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			// Alpha's start goes through; Beta's waits until Alpha's read
			// is held, so Alpha's read is the older one whichever surface
			// starts Beta.
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1)
			seedDeployableMods(t, f.Svc, f.Game)

			f.runInBrowser(t,
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
			)
			if batch {
				f.runInBrowser(t,
					chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
					chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
					chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
				)
			}
			f.runInBrowser(t, chromedp.Evaluate(recordRowStatesJS("fake:a"), nil))
			require.EqualValues(t, 1, wire.updatesReads.Load(), "the page's first hydrate")
			wire.holdMods.Store(true)

			// Alpha's job ends, and the page has started the read its
			// ending asks for.
			if batch {
				f.runInBrowser(t, chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery))
			} else {
				f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil))
			}
			awaitJobsOver(t, f, wire, map[string]bool{"a": false})
			awaitMods(t, wire, 1, 0)
			wire.releaseToggles()

			// Beta's job ends too - clicked, or started by the batch - and the
			// read its ending asks for, issued later, is the newer one. It is
			// on its way: its update check is on the wire, and its library
			// read waits in the browser behind Alpha's.
			if !batch {
				f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Beta Mod"), nil))
			}
			awaitJobsOver(t, f, wire, map[string]bool{"a": false, "b": false})
			require.Eventually(t, func() bool { return wire.updatesReads.Load() >= 3 },
				15*time.Second, 20*time.Millisecond, "Beta's ending must start a newer read")

			// Alpha's own read is answered, and is already out of date. Beta's
			// library read reaches the server once it is.
			wire.releaseNextMods(t)
			awaitMods(t, wire, 2, 1)
			var superseded e2eRowToggleState
			f.runInBrowser(t, settleEffects(), chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &superseded))
			assert.True(t, superseded.Pending,
				"a superseded read settles nothing: the row keeps what was asked until the newer read lands (row: %+v)", superseded)
			assert.False(t, superseded.Checked, "and never shows the document from before its job")

			wire.releaseMods()
			var settled e2eRowToggleState
			var changes []e2eRowStateChange
			wait := []chromedp.Action{pollUntil(noRowPendingJS)}
			if batch {
				wait = append(wait, pollUntil(toastSaysJS("Disabled 2/2")))
			}
			wait = append(wait,
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &settled),
				chromedp.Evaluate(`window.__rowStates`, &changes),
			)
			runWithin(t, f, 20*time.Second, wait...)
			assert.False(t, settled.Checked, "the box shows the newer read, which has the disable")
			assert.False(t, settled.Disabled)
			assert.Equal(t, []string{"checked/settled", "unchecked/pending", "unchecked/settled"}, rowStateNames(changes),
				"from the click on, Alpha never painted its pre-job value")
			assert.Empty(t, f.BrowserErrors())
		})
	}
}

// TestE2E_LibraryToggle_ASettlingReadThatNeverLandsEndsAtItsDeadline is the
// final review's R2: the read that would settle a request is never
// answered - a hung server, whose reads hang with its writes. The request
// used to wait on that read for good, so a row said "Disabling…" long after
// its toast had claimed it showed what the server reports. The read now has
// a deadline: once it passes, the row shows the last value the server
// actually sent and a toast says the current state could not be read. When
// the read does land, the row follows it.
func TestE2E_LibraryToggle_ASettlingReadThatNeverLandsEndsAtItsDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		// stall is the toggleWire stall mode: "" lets the start through, so
		// the request settles through "confirming"; "answer" applies the
		// change and never answers the start, so it settles through
		// "unanswered".
		stall string
		// deadlines is how many compressed deadlines pass before the row
		// can leave pending: the settle deadline, after the start's own.
		deadlines int
	}{
		{name: "confirming", stall: "", deadlines: 1},
		{name: "unanswered", stall: "answer", deadlines: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
			seedDeployableMods(t, f.Svc, f.Game)
			wire.stall.Store(tc.stall)

			f.runInBrowser(t,
				chromedp.ActionFunc(func(ctx context.Context) error {
					_, err := page.AddScriptToEvaluateOnNewDocument(deadlineShim(true)).Do(ctx)
					return err
				}),
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
				chromedp.Evaluate(recordRowStatesJS("fake:a"), nil),
			)
			// Every library read from here on hangs until the very end.
			wire.holdMods.Store(true)

			f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil))
			awaitToggleRequests(t, &wire.toggles, 1)
			awaitJobsOver(t, f, wire, map[string]bool{"a": false})
			awaitMods(t, wire, 1, 0)

			var toasts []string
			var row e2eRowToggleState
			var changes []e2eRowStateChange
			var settleDeadlines []float64
			runWithin(t, f, 30*time.Second,
				pollUntil(toastSaysJS("could not be read")),
				pollUntil(noRowPendingJS),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &row),
				chromedp.Evaluate(toastTextsJS, &toasts),
				chromedp.Evaluate(`window.__rowStates`, &changes),
				chromedp.Evaluate(`window.__settleDeadlines`, &settleDeadlines),
			)
			assert.Zero(t, wire.modsAnswered.Load(), "no read was answered: the deadline, not a read, ended the wait")
			assert.False(t, row.Pending, "the request is over (row: %+v)", row)
			assert.True(t, row.Checked,
				"and the box shows the last value the server sent - the library read before the job")
			assert.False(t, row.Disabled, "with the control live again")
			assert.Empty(t, row.Live)

			require.NotEmpty(t, settleDeadlines, "the settling read has a deadline")
			deadline := time.Duration(settleDeadlines[0]) * time.Millisecond
			assert.GreaterOrEqual(t, deadline, 30*time.Second,
				"the deadline comfortably exceeds a read queued behind a slow update check")
			compressed := deadline / deadlineCompression
			require.Equal(t, []string{"checked/settled", "unchecked/pending", "checked/settled"}, rowStateNames(changes),
				"pending from the click to the deadline, then the last value the server sent")
			left := time.Duration(changes[2].At-changes[1].At) * time.Millisecond
			assert.GreaterOrEqual(t, left, time.Duration(tc.deadlines)*compressed,
				"the row is held for the whole of its deadline")
			assert.Less(t, left, time.Duration(tc.deadlines)*compressed+5*time.Second,
				"and released promptly once it passes")

			joined := strings.Join(toasts, " | ")
			assert.Contains(t, joined, "could not be read", "a toast says the current state is unknown")
			assert.Contains(t, joined, "Alpha Mod")
			assert.NotContains(t, joined, "now shows what the server reports",
				"no toast claims a read that never landed")
			if tc.stall != "" {
				assert.Contains(t, joined, "once it answers",
					"the start's own toast promises the server's value only when there is one")
			}

			// The server answers at last, and the row follows it.
			wire.releaseMods()
			var after e2eRowToggleState
			runWithin(t, f, 20*time.Second,
				pollUntil(`(() => {
					const row = document.querySelector('.mod-row[data-mod="fake:a"]');
					return row !== null && !row.querySelector("td.col--enabled input").checked;
				})()`),
				settleEffects(),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &after),
			)
			assert.False(t, after.Pending, "and is never pending again")
			assert.False(t, after.Disabled)
			wire.releaseStalled()
			assert.Empty(t, uncaughtErrors(f))
		})
	}
}

// TestE2E_LibraryToggle_ASettleDeadlineOffScreenEndsNothing: the settle
// deadline ends a hold only where its row can be seen. A request whose job
// ends while the user is on another page has nothing on screen to be wrong
// about, and a "could not be read" toast there would report a failure no
// read ever had - that page never reads the library. The hold waits, and
// the read that coming back starts is the one its deadline then bounds.
func TestE2E_LibraryToggle_ASettleDeadlineOffScreenEndsNothing(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 0)
	seedDeployableMods(t, f.Svc, f.Game)

	f.runInBrowser(t,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(deadlineShim(true)).Do(ctx)
			return err
		}),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(recordRowStatesJS("fake:a"), nil),
		// The click, then the Setup page - which reads no library - before
		// the job can end.
		chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
		chromedp.Evaluate(clientNavigateJS(f.SetupPath("")), nil),
		waitGone(`.library__table`),
	)
	awaitToggleRequests(t, &wire.toggles, 1)
	wire.holdMods.Store(true)
	wire.releaseToggles()
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})

	// The hold's deadline passes while the Setup page is on screen.
	var toasts []string
	runWithin(t, f, 20*time.Second,
		pollUntil(`window.__settleDeadlinesPassed >= 1`),
		settleEffects(),
		chromedp.Evaluate(toastTextsJS, &toasts),
	)
	assert.NotContains(t, strings.Join(toasts, " | "), "could not be read",
		"nothing on screen claims anything, so there is nothing to report")
	assert.Zero(t, wire.modsHeld.Load(), "and nothing read the library")

	// Back on the library: the row is still pending, its read begins, and
	// that read - which never lands - is what the deadline now bounds.
	var back, row e2eRowToggleState
	var changes []e2eRowStateChange
	var returnedAt float64
	var settleDeadlines []float64
	runWithin(t, f, 30*time.Second,
		chromedp.Evaluate(`window.__returnedAt = performance.now() - window.__recordingSince;`+
			clientNavigateJS(f.HomePath()), nil),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &back),
		pollUntil(toastSaysJS("could not be read")),
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &row),
		chromedp.Evaluate(`window.__rowStates`, &changes),
		chromedp.Evaluate(`window.__returnedAt`, &returnedAt),
		chromedp.Evaluate(`window.__settleDeadlines`, &settleDeadlines),
	)
	assert.True(t, back.Pending, "the row comes back still pending (row: %+v)", back)
	assert.Positive(t, wire.modsHeld.Load(), "coming back started a library read")
	assert.Zero(t, wire.modsAnswered.Load(), "which never landed")
	require.Equal(t, []string{"checked/settled", "unchecked/pending", "checked/settled"}, rowStateNames(changes),
		"pending from the click, off screen and back, until the deadline on screen")
	assert.True(t, row.Checked, "then the last value the server sent")
	assert.False(t, row.Disabled)

	require.GreaterOrEqual(t, len(settleDeadlines), 2, "coming back armed a new deadline")
	compressed := time.Duration(settleDeadlines[1]) * time.Millisecond / deadlineCompression
	held := time.Duration(changes[2].At-returnedAt) * time.Millisecond
	assert.GreaterOrEqual(t, held, compressed, "a whole deadline from the read coming back began")
	assert.Less(t, held, compressed+5*time.Second)

	wire.releaseMods()
	runWithin(t, f, 20*time.Second,
		pollUntil(`(() => {
			const row = document.querySelector('.mod-row[data-mod="fake:a"]');
			return row !== null && !row.querySelector("td.col--enabled input").checked;
		})()`),
	)
	assert.Empty(t, uncaughtErrors(f))
}

// TestE2E_LibraryBatch_AnUnansweredStartIsAnUnknownOutcome is the same
// deadline on a batch row: the batch does not wait on the unanswered start
// past its deadline, carries on with the next row, and tallies the
// unanswered one as an unknown outcome rather than a failure - the server
// may well have done it, and here it did.
func TestE2E_LibraryBatch_AnUnansweredStartIsAnUnknownOutcome(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
	seedDeployableMods(t, f.Svc, f.Game)
	wire.stall.Store("answer")
	wire.stallMod.Store("a")

	var alpha, beta e2eRowToggleState
	runWithin(t, f, 30*time.Second,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(deadlineShimJS).Do(ctx)
			return err
		}),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
		chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
		pollUntil(toastSaysJS("did not answer")),
		pollUntil(toastSaysJS("Disabled 1/2")),
		pollUntil(toastSaysJS("Outcome unknown: Alpha Mod")),
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
		chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &beta),
	)
	awaitJobsOver(t, f, wire, map[string]bool{"a": false, "b": false})
	assert.EqualValues(t, 2, wire.toggles.Load(), "the batch moved on to Beta")
	assert.False(t, alpha.Checked, "Alpha shows the read the deadline started, and the disable did happen")
	assert.False(t, alpha.Disabled)
	assert.False(t, beta.Checked)
	wire.releaseStalled()
	assert.Empty(t, uncaughtErrors(f))
}

// TestE2E_LibraryToggle_AStartThatMadeNoJobHandsTheControlBack is the
// requested-to-gone transition: the start itself fails, so no job id ever
// exists - the backend's own CSRF refusal, and a connection dropped with no
// answer at all - for a single row and for the first row of a batch, which
// must carry on with the next.
func TestE2E_LibraryToggle_AStartThatMadeNoJobHandsTheControlBack(t *testing.T) {
	for _, kind := range []string{"csrf", "drop"} {
		t.Run("single/"+kind, func(t *testing.T) {
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
			seedDeployableMods(t, f.Svc, f.Game)
			wire.faultKind.Store(kind)
			wire.faultMod.Store("a")

			var alpha e2eRowToggleState
			runWithin(t, f, 30*time.Second,
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil),
				pollUntil(`document.querySelectorAll(".toast").length > 0`),
				pollUntil(noRowPendingJS),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
			)
			assert.True(t, alpha.Checked, "back to the server's value")
			assert.False(t, alpha.Disabled, "and the control is live again")
			assert.True(t, modEnabled(t, f, "a"))
			assert.Empty(t, uncaughtErrors(f))
		})

		t.Run("batch/"+kind, func(t *testing.T) {
			f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
			seedDeployableMods(t, f.Svc, f.Game)
			wire.faultKind.Store(kind)
			wire.faultMod.Store("a")

			var alpha, beta e2eRowToggleState
			runWithin(t, f, 30*time.Second,
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
				pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
				chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
				chromedp.Evaluate(selectLibraryRowJS("Beta Mod"), nil),
				chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
				chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
				pollUntil(toastSaysJS("Disabled 1/2")),
				pollUntil(noRowPendingJS),
				chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &alpha),
				chromedp.Evaluate(libraryRowStateJS("Beta Mod"), &beta),
			)
			assert.True(t, alpha.Checked)
			assert.False(t, alpha.Disabled)
			assert.False(t, beta.Checked, "the batch carried on with Beta")
			assert.True(t, modEnabled(t, f, "a"))
			assert.False(t, modEnabled(t, f, "b"))
			assert.Empty(t, uncaughtErrors(f))
		})
	}
}

// TestE2E_Toggle_TwoRapidClicksSendOneRequest: a real double click - on the
// library row's box and on the slide-over's button - is one request.
func TestE2E_Toggle_TwoRapidClicksSendOneRequest(t *testing.T) {
	t.Run("row", func(t *testing.T) {
		f, wire := newToggleWireFixture(t, newFakeSource("fake"), 0)
		seedDeployableMods(t, f.Svc, f.Game)
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			chromedp.DoubleClick(`.mod-row[data-mod="fake:a"] td.col--enabled input`, chromedp.ByQuery),
			settleEffects(),
		)
		awaitToggleRequests(t, &wire.toggles, 1)
		f.runInBrowser(t, settleEffects())
		assert.EqualValues(t, 1, wire.toggles.Load())

		wire.releaseToggles()
		awaitJobsOver(t, f, wire, map[string]bool{"a": false})
		runWithin(t, f, 20*time.Second, pollUntil(noRowPendingJS))
		assert.Empty(t, f.BrowserErrors())
	})

	t.Run("slide-over", func(t *testing.T) {
		f, wire := newToggleWireFixture(t, catalogSource(map[string]string{"a": "Alpha Mod"}), 0)
		seedToggleMod(t, f, "a", "Alpha Mod", true, map[string][]byte{"alpha.pak": []byte("alpha")})
		f.runInBrowser(t,
			chromedp.Navigate(f.SlideOverPath("fake", "a")),
			chromedp.WaitVisible(`.slide-over [data-action="toggle"]`, chromedp.ByQuery),
			chromedp.DoubleClick(`.slide-over [data-action="toggle"]`, chromedp.ByQuery),
			settleEffects(),
		)
		awaitToggleRequests(t, &wire.toggles, 1)
		f.runInBrowser(t, settleEffects())
		assert.EqualValues(t, 1, wire.toggles.Load())

		wire.releaseToggles()
		awaitJobsOver(t, f, wire, map[string]bool{"a": false})
		assert.Empty(t, f.BrowserErrors())
	})
}

// slideOverOnJS is true once the slide-over is showing the mod named and its
// toggle button is rendered (not replaced by a job readout).
func slideOverOnJS(name string) string {
	return fmt.Sprintf(`document.querySelector('.slide-over')?.getAttribute("aria-label") === %q
		&& document.querySelector('.slide-over [data-action="toggle"]') !== null`, name+" details")
}

// TestE2E_SlideOver_SteppingAcrossThreeModsWithTwoTogglesPending: ←/→ over
// three mods, two of them with a request in flight, keeps every request on
// its own mod - and leaves the third one's button live throughout.
func TestE2E_SlideOver_SteppingAcrossThreeModsWithTwoTogglesPending(t *testing.T) {
	f, wire := newToggleWireFixture(t,
		catalogSource(map[string]string{"a": "Alpha Mod", "b": "Beta Mod", "c": "Gamma Mod"}), 0)
	seedToggleMod(t, f, "a", "Alpha Mod", true, map[string][]byte{"alpha.pak": []byte("alpha")})
	seedToggleMod(t, f, "b", "Beta Mod", false, map[string][]byte{"beta.pak": []byte("beta")})
	seedToggleMod(t, f, "c", "Gamma Mod", true, map[string][]byte{"gamma.pak": []byte("gamma")})

	click := chromedp.Evaluate(`document.querySelector('.slide-over [data-action="toggle"]').click()`, nil)
	step := func(dir, name string) chromedp.Action {
		return chromedp.Tasks{
			chromedp.Evaluate(fmt.Sprintf(`document.querySelector('.slide-over [aria-label=%q]').click()`, dir), nil),
			pollUntil(slideOverOnJS(name)),
			settleEffects(),
		}
	}
	read := func(out *e2eToggleButton) chromedp.Action { return chromedp.Evaluate(slideOverToggleJS, out) }

	var gamma1, beta2, alpha2, gamma2 e2eToggleButton
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		pollUntil(slideOverOnJS("Alpha Mod")),
		click,
		pollUntil(`document.querySelector('.slide-over [data-action="toggle"]').textContent.includes("Disabling")`),
		step("Next mod", "Beta Mod"),
		click,
		pollUntil(`document.querySelector('.slide-over [data-action="toggle"]').textContent.includes("Enabling")`),
		step("Next mod", "Gamma Mod"), read(&gamma1),
		step("Previous mod", "Beta Mod"), read(&beta2),
		step("Previous mod", "Alpha Mod"), read(&alpha2),
		step("Next mod", "Beta Mod"),
		step("Next mod", "Gamma Mod"), read(&gamma2),
	)
	awaitToggleRequests(t, &wire.toggles, 2)
	assert.Equal(t, e2eToggleButton{Label: "Disabling…", Disabled: true}, alpha2)
	assert.Equal(t, e2eToggleButton{Label: "Enabling…", Disabled: true}, beta2)
	assert.Equal(t, e2eToggleButton{Label: "Disable", Disabled: false}, gamma1)
	assert.Equal(t, e2eToggleButton{Label: "Disable", Disabled: false}, gamma2)

	wire.releaseToggles()
	awaitJobsOver(t, f, wire, map[string]bool{"a": false, "b": true, "c": true})

	// Once both jobs are over and their outcomes have handed the buttons
	// back, each mod offers the opposite of what it now is.
	live := func(name, label string) chromedp.Action {
		return pollUntil(fmt.Sprintf(`(() => {
			const b = document.querySelector('.slide-over [data-action="toggle"]');
			return document.querySelector('.slide-over')?.getAttribute("aria-label") === %q
				&& b !== null && !b.disabled && b.textContent.trim() === %q;
		})()`, name+" details", label))
	}
	runWithin(t, f, 30*time.Second,
		live("Gamma Mod", "Disable"),
		chromedp.Evaluate(`document.querySelector('.slide-over [aria-label="Previous mod"]').click()`, nil),
		live("Beta Mod", "Disable"),
		chromedp.Evaluate(`document.querySelector('.slide-over [aria-label="Previous mod"]').click()`, nil),
		live("Alpha Mod", "Enable"),
	)
	assert.Empty(t, f.BrowserErrors())
}
