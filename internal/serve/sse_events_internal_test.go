package serve

// Internal (package serve) tests for GET /api/v1/jobs/{id}/events - the SSE
// endpoint itself (docs/plans/2026-08-30-serve-design.md §"Jobs and SSE":
// "subscribers joining late replay the buffer first"). The three things
// that actually matter here, and that a status-code test would not catch:
// the replay/live seam loses nothing and repeats nothing, a client that
// goes away takes its subscription with it, and a flush really reaches the
// wire through the whole middleware chain.

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sseFrame is one parsed SSE frame: an event frame carries Event and Data,
// a heartbeat carries only Comment.
type sseFrame struct {
	Event   string
	Data    string
	Comment string
}

// parseSSE splits an SSE body into frames. It is deliberately strict -
// every line must be one of the three forms the stream writes - so a
// malformed frame fails the test rather than being skipped.
func parseSSE(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if block == "" {
			continue
		}
		var frame sseFrame
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.Data = strings.TrimPrefix(line, "data: ")
			case strings.HasPrefix(line, ": "):
				frame.Comment = strings.TrimPrefix(line, ": ")
			default:
				t.Fatalf("unparsable SSE line %q in block %q", line, block)
			}
		}
		frames = append(frames, frame)
	}
	return frames
}

// indexEvent is a numbered core event: the sequence number rides in Detail
// so a test can assert the exact order it came back in.
func indexEvent(i int) core.StepEvent {
	return core.StepEvent{
		Scope:  core.Scope{Op: core.OpDeploy, Index: i, Total: 4},
		Phase:  core.DeployDeployed,
		Detail: strconv.Itoa(i),
	}
}

// eventSequence extracts the indexEvent sequence numbers from a stream's
// event frames, ignoring heartbeats and the terminal frame.
func eventSequence(t *testing.T, frames []sseFrame) []int {
	t.Helper()
	var seq []int
	for _, frame := range frames {
		if frame.Event == "" || frame.Event == sseDoneEvent {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
			Data struct {
				Detail string `json:"detail"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &envelope))
		assert.Equal(t, "step", envelope.Type)
		n, err := strconv.Atoi(envelope.Data.Detail)
		require.NoError(t, err)
		seq = append(seq, n)
	}
	return seq
}

// emitOnFirstWrite is a ResponseWriter that runs a hook the first time the
// handler writes to it. It is how a subscriber is made to arrive DURING an
// emission deterministically: the hook fires after the first replayed frame
// has been written, so the event it emits is racing the handler's own
// transition from replay to live - exactly the seam that must neither drop
// nor duplicate.
type emitOnFirstWrite struct {
	*httptest.ResponseRecorder
	once sync.Once
	hook func()
}

func (w *emitOnFirstWrite) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	w.once.Do(w.hook)
	return n, err
}

// TestSSEEvents_ReplayThenLiveHasNoGapAndNoDuplicate is the hard one. The
// job emits 1..3, a subscriber joins, and event 4 is emitted from inside
// the handler's first Write - i.e. after the replay has started but before
// the live loop is running. The stream must deliver 1,2,3,4 once each, in
// order.
func TestSSEEvents_ReplayThenLiveHasNoGapAndNoDuplicate(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	sinkReady := make(chan core.EventSink, 1)
	release := make(chan struct{})
	id, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		sink(indexEvent(2))
		sink(indexEvent(3))
		sinkReady <- sink
		<-release
		return &core.DeployResult{Deployed: 1}, nil
	})
	require.NoError(t, err)

	var sink core.EventSink
	select {
	case sink = <-sinkReady:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the job to emit its first events")
	}

	rec := &emitOnFirstWrite{ResponseRecorder: httptest.NewRecorder()}
	rec.hook = func() {
		sink(indexEvent(4))
		close(release)
	}
	s.Handler().ServeHTTP(rec, apiRequest(s, http.MethodGet, "/api/v1/jobs/"+string(id)+"/events", ""))

	require.Equal(t, http.StatusOK, rec.Code)
	frames := parseSSE(t, rec.Body.String())
	assert.Equal(t, []int{1, 2, 3, 4}, eventSequence(t, frames),
		"the replay/live seam must lose nothing and repeat nothing")

	last := frames[len(frames)-1]
	assert.Equal(t, sseDoneEvent, last.Event, "the stream must end with the terminal frame")
	var final jobStatus
	require.NoError(t, json.Unmarshal([]byte(last.Data), &final))
	assert.Equal(t, jobSucceeded, final.State)
	assert.Equal(t, 4, final.EventCount)
}

// TestSSEEvents_FinishedJobReplaysAndEndsImmediately covers the late
// arrival: everything the ring still holds, then the terminal frame.
func TestSSEEvents_FinishedJobReplaysAndEndsImmediately(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	id, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		sink(indexEvent(2))
		return &core.DeployResult{Deployed: 1}, nil
	})
	require.NoError(t, err)
	j, ok := s.jobs.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(id)+"/events", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, sseContentType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	frames := parseSSE(t, rec.Body.String())
	assert.Equal(t, []int{1, 2}, eventSequence(t, frames))
	assert.Equal(t, sseDoneEvent, frames[len(frames)-1].Event)
}

// TestSSEEvents_UnknownJob_404Envelope keeps the failure JSON: the stream
// only takes over the response once there is a job to stream.
func TestSSEEvents_UnknownJob_404Envelope(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/nope/events", "")

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
}

// TestSSEEvents_HeartbeatOnTheInjectedClock proves an idle stream writes
// comment frames, on the clock seam rather than a real 15-second wait.
func TestSSEEvents_HeartbeatOnTheInjectedClock(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	ticks := make(chan time.Time)
	s.heartbeat = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }

	release := make(chan struct{})
	id, err := s.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-release
		return &core.DeployResult{}, nil
	})
	require.NoError(t, err)

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp := getStream(t, srv, "/api/v1/jobs/"+string(id)+"/events")
	t.Cleanup(func() { _ = resp.Body.Close() })
	reader := bufio.NewReader(resp.Body)

	ticks <- time.Now()
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, ": heartbeat\n", line, "an idle stream's only traffic is the comment heartbeat")

	close(release)
}

// TestSSEEvents_FlushReachesTheWireThroughTheMiddlewareChain is the live-TCP
// probe: over a real connection, through securityHeaders -> hostCheck ->
// requestLogging -> the handler, the first frame must arrive while the job
// is STILL RUNNING. Without statusRecorder.Unwrap (task-3 review Important
// 2) http.ResponseController could not find a Flusher and the whole stream
// would sit in the buffer until the handler returned - a bug no
// ResponseRecorder test can see, because a recorder has no wire.
func TestSSEEvents_FlushReachesTheWireThroughTheMiddlewareChain(t *testing.T) {
	s, _ := newLiveFixtureServer(t)

	emitted := make(chan struct{})
	release := make(chan struct{})
	id, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		close(emitted)
		<-release
		return &core.DeployResult{Deployed: 1}, nil
	})
	require.NoError(t, err)
	waitFor(t, emitted, "the job's first event")

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	resp := getStream(t, srv, "/api/v1/jobs/"+string(id)+"/events")
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, sseContentType, resp.Header.Get("Content-Type"))

	frame := readFrame(t, bufio.NewReader(resp.Body))
	assert.Equal(t, "event: step", frame[0])

	j, ok := s.jobs.job(id)
	require.True(t, ok)
	assert.Equal(t, jobRunning, j.status().State, "the frame arrived before the job finished, which is the point")
	close(release)
}

// TestSSEEvents_ClientDropReleasesTheSubscription is the leak test: a
// browser that closes the tab must not leave a subscriber attached to a
// long-running job, holding a channel the job's emit still writes to.
func TestSSEEvents_ClientDropReleasesTheSubscription(t *testing.T) {
	s, _ := newLiveFixtureServer(t)

	emitted := make(chan struct{})
	release := make(chan struct{})
	id, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		close(emitted)
		<-release
		return &core.DeployResult{}, nil
	})
	require.NoError(t, err)
	waitFor(t, emitted, "the job's first event")
	j, ok := s.jobs.job(id)
	require.True(t, ok)

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/jobs/"+string(id)+"/events", nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	readFrame(t, bufio.NewReader(resp.Body))
	require.Equal(t, 1, j.subscriberCount(), "the open stream is the job's one subscriber")

	cancel()
	_ = resp.Body.Close()

	requireEventually(t, func() bool { return j.subscriberCount() == 0 },
		"the dropped client's subscription to be released")
	close(release)
}

// getStream issues a streaming GET against srv, with the Host the wildcard
// bind admits.
func getStream(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp
}

// readFrame reads one complete SSE frame (up to and including its blank
// line) and returns its lines, failing the test if nothing arrives - a
// stream that buffers instead of flushing hangs here, which is exactly what
// the live-TCP probe is looking for.
func readFrame(t *testing.T, reader *bufio.Reader) []string {
	t.Helper()
	done := make(chan []string, 1)
	go func() {
		var lines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- lines
				return
			}
			if line == "\n" {
				done <- lines
				return
			}
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}
	}()
	select {
	case lines := <-done:
		require.NotEmpty(t, lines, "stream produced an empty frame")
		return lines
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an SSE frame: the response is buffering rather than flushing")
		return nil
	}
}

// requireEventually polls cond until it holds, failing after a generous
// timeout. Written as a poll loop rather than assert.Eventually for the
// reason jobs_internal_test.go records: Eventually runs its condition on
// goroutines of its own.
func requireEventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSSEEvents_DrainingServerClosesTheStream covers the shutdown hazard
// Task 6's handoff flagged: an SSE response is an ACTIVE request, and
// http.Server.Shutdown waits for active requests. A stream that only ended
// when its job ended would hold the whole grace window open. It must close
// itself as soon as the server starts draining, while its job keeps running.
func TestSSEEvents_DrainingServerClosesTheStream(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	serveCtx, cancelServe := context.WithCancel(t.Context())
	s := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: "127.0.0.1:0", ShutdownGrace: 5 * time.Second})

	addr, err := s.Listen()
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- s.Serve(serveCtx) }()

	emitted := make(chan struct{})
	release := make(chan struct{})
	id, err := s.jobs.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		sink(indexEvent(1))
		close(emitted)
		<-release
		return &core.DeployResult{}, nil
	})
	require.NoError(t, err)
	waitFor(t, emitted, "the job's first event")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/api/v1/jobs/%s/events", addr.String(), id), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	reader := bufio.NewReader(resp.Body)
	readFrame(t, reader)

	cancelServe()

	closed := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(reader)
		closed <- readErr
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream did not close when the server began draining")
	}

	j, ok := s.jobs.job(id)
	require.True(t, ok)
	assert.Equal(t, jobRunning, j.status().State, "draining must close the stream, not the job")

	close(release)
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its job finished")
	}
}
