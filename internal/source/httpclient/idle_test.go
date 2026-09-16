package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manualTimer is a stall timer the test fires by hand. It records how
// often the transport re-armed it, which is the observable form of "bytes
// moved, so the window starts again".
type manualTimer struct {
	mu      sync.Mutex
	d       time.Duration
	f       func()
	armed   bool
	resets  int
	resetCh chan struct{}
}

func (m *manualTimer) Reset(d time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := m.armed
	m.d, m.armed = d, true
	m.resets++
	select {
	case m.resetCh <- struct{}{}:
	default:
	}
	return was
}

func (m *manualTimer) Stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := m.armed
	m.armed = false
	return was
}

// fire runs the timer's function as the runtime would once the window
// expired - only if it is still armed, exactly as a stopped time.Timer
// never fires.
func (m *manualTimer) fire() {
	m.mu.Lock()
	armed, f := m.armed, m.f
	m.armed = false
	m.mu.Unlock()
	if armed {
		f()
	}
}

// waitReset waits for the transport to re-arm timer, and fails - rather
// than hanging until the test binary's timeout - if it never does (T3
// review P4 B12).
func waitReset(t *testing.T, timer *manualTimer) {
	t.Helper()
	select {
	case <-timer.resetCh:
	case <-time.After(5 * time.Second):
		t.Fatal("the stall window was not re-armed: bytes arrived and the guard did not notice")
	}
}

// manualTimers hands out one manualTimer per request and remembers each.
type manualTimers struct {
	mu     sync.Mutex
	timers []*manualTimer
}

func (m *manualTimers) afterFunc(d time.Duration, f func()) httpclient.Timer {
	t := &manualTimer{d: d, f: f, armed: true, resetCh: make(chan struct{}, 64)}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timers = append(m.timers, t)
	return t
}

func (m *manualTimers) last(t *testing.T) *manualTimer {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.timers, "the transport armed no timer")
	return m.timers[len(m.timers)-1]
}

// TestIdleTimeout_AStoppedBodyFailsWithAStallError is #436's headline: a
// body that stops arriving fails once the idle window passes, with an
// error that says it STALLED - not after the ten-minute request ceiling,
// and not as a bare "context canceled" a user would read as their own
// Ctrl-C.
func TestIdleTimeout_AStoppedBodyFailsWithAStallError(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select { // then nothing: the stall
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	timers := &manualTimers{}
	client := &http.Client{Transport: &httpclient.IdleTimeout{
		Base: http.DefaultTransport, Timeout: 30 * time.Second, AfterFunc: timers.afterFunc,
	}}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	buf := make([]byte, 7)
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	assert.Equal(t, "partial", string(buf))

	timer := timers.last(t)
	assert.Equal(t, 30*time.Second, timer.d, "the window is the configured timeout")
	timer.fire()

	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	var stall *httpclient.StallError
	require.ErrorAs(t, err, &stall, "a stalled body names the stall")
	assert.Equal(t, 30*time.Second, stall.Timeout)
	assert.ErrorIs(t, err, httpclient.ErrStalled)
	assert.Contains(t, err.Error(), "no data received for 30s")
	assert.Equal(t, 1, strings.Count(err.Error(), "stalled"), "the stall is named once: %v", err)
}

// TestIdleTimeout_AMovingBodyIsNeverCutOff is the other half: every read
// that delivers bytes re-arms the window, so a slow download that keeps
// moving finishes however long it takes in total.
func TestIdleTimeout_AMovingBodyIsNeverCutOff(t *testing.T) {
	chunks := make(chan string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for c := range chunks {
			_, _ = w.Write([]byte(c))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	timers := &manualTimers{}
	client := &http.Client{Transport: &httpclient.IdleTimeout{
		Base: http.DefaultTransport, Timeout: 30 * time.Second, AfterFunc: timers.afterFunc,
	}}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	timer := timers.last(t)
	waitReset(t, timer) // the headers arriving re-armed it once already

	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(resp.Body)
		done <- b
	}()
	for _, c := range []string{"a", "b", "c", "d"} {
		chunks <- c
		waitReset(t, timer) // the read that delivered c re-armed the window
	}
	close(chunks)

	assert.Equal(t, "abcd", string(<-done))
	timer.mu.Lock()
	defer timer.mu.Unlock()
	assert.GreaterOrEqual(t, timer.resets, 4, "each delivered chunk re-armed the window")
}

// TestIdleTimeout_AServerThatNeverAnswersStalls: the window covers the wait
// for the response headers too, so a server that accepts the connection
// and then says nothing is a stall rather than a hang.
func TestIdleTimeout_AServerThatNeverAnswersStalls(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()

	timers := &manualTimers{}
	client := &http.Client{Transport: &httpclient.IdleTimeout{
		Base: http.DefaultTransport, Timeout: 20 * time.Second, AfterFunc: timers.afterFunc,
	}}

	errc := make(chan error, 1)
	go func() {
		resp, err := client.Get(srv.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
		errc <- err
	}()
	<-entered
	timers.last(t).fire()

	err := <-errc
	require.Error(t, err)
	assert.ErrorIs(t, err, httpclient.ErrStalled)
}

// TestIdleTimeout_ACompletedBodyReleasesItsTimer: nothing is left armed once
// the caller has read and closed the response - a finished request must
// never be "cancelled" after the fact.
func TestIdleTimeout_ACompletedBodyReleasesItsTimer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("whole"))
	}))
	defer srv.Close()

	timers := &manualTimers{}
	client := &http.Client{Transport: &httpclient.IdleTimeout{
		Base: http.DefaultTransport, Timeout: time.Second, AfterFunc: timers.afterFunc,
	}}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "whole", string(body))

	timer := timers.last(t)
	timer.mu.Lock()
	armed := timer.armed
	timer.mu.Unlock()
	assert.False(t, armed, "closing the body stops the window")
}

// TestIdleTimeout_ACallerCancellationIsNotAStall keeps the two apart: the
// caller giving up is the caller's context error, never ErrStalled.
func TestIdleTimeout_ACallerCancellationIsNotAStall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	timers := &manualTimers{}
	client := &http.Client{Transport: &httpclient.IdleTimeout{
		Base: http.DefaultTransport, Timeout: time.Minute, AfterFunc: timers.afterFunc,
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	cancel()
	_, err = io.ReadAll(resp.Body)
	require.Error(t, err)
	assert.False(t, errors.Is(err, httpclient.ErrStalled), "a cancellation is not a stall: %v", err)
}

// TestIdleTimeout_ZeroTimeoutIsAPassThrough: an unset timeout adds nothing,
// so a caller that has not opted in keeps its transport's own behaviour.
func TestIdleTimeout_ZeroTimeoutIsAPassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	timers := &manualTimers{}
	client := &http.Client{Transport: &httpclient.IdleTimeout{Base: http.DefaultTransport, AfterFunc: timers.afterFunc}}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	timers.mu.Lock()
	defer timers.mu.Unlock()
	assert.Empty(t, timers.timers, "no window is armed without a timeout")
}

// servers is every transport a stall test runs over. thunderstore.io
// answers over HTTP/2, and Go's HTTP/2 transport reports a cancelled
// request as a BARE context.Canceled where HTTP/1 reports the cancellation
// cause (T3 review F1) - so a stall that reads correctly over one can read
// as the user's own Ctrl-C over the other, and every classification test
// has to hold on both.
var servers = []struct {
	name  string
	start func(http.Handler) *httptest.Server
}{
	{"http1", httptest.NewServer},
	{"http2", func(h http.Handler) *httptest.Server {
		srv := httptest.NewUnstartedServer(h)
		srv.EnableHTTP2 = true
		srv.StartTLS()
		return srv
	}},
}

// assertStallNotCancel is the classification a stalled transfer must have
// wherever it surfaces: the stall, once, and nothing a caller would read as
// its own cancellation.
func assertStallNotCancel(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, httpclient.ErrStalled)
	assert.False(t, errors.Is(err, context.Canceled), "a stall is not a cancellation: %v", err)
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "a stall is not a deadline: %v", err)
	assert.NotContains(t, err.Error(), "context canceled", "the stall's own cancellation is not named: %v", err)
	assert.Equal(t, 1, strings.Count(err.Error(), "stalled"), "the stall is named once: %v", err)
}

// TestIdleTimeout_AStallIsNeverACancellation runs the header stall and the
// body stall over both protocols (T3 review F1).
func TestIdleTimeout_AStallIsNeverACancellation(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name+"/headers", func(t *testing.T) {
			entered := make(chan struct{})
			srv := s.start(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
			}))
			defer srv.Close()

			timers := &manualTimers{}
			client := &http.Client{Transport: &httpclient.IdleTimeout{
				Base: srv.Client().Transport, Timeout: 30 * time.Second, AfterFunc: timers.afterFunc,
			}}
			errc := make(chan error, 1)
			go func() {
				resp, err := client.Get(srv.URL)
				if resp != nil {
					_ = resp.Body.Close()
				}
				errc <- err
			}()
			<-entered
			timers.last(t).fire()
			assertStallNotCancel(t, <-errc)
		})
		t.Run(s.name+"/body", func(t *testing.T) {
			srv := s.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("partial"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()

			timers := &manualTimers{}
			client := &http.Client{Transport: &httpclient.IdleTimeout{
				Base: srv.Client().Transport, Timeout: 30 * time.Second, AfterFunc: timers.afterFunc,
			}}
			resp, err := client.Get(srv.URL)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			buf := make([]byte, 7)
			_, err = io.ReadFull(resp.Body, buf)
			require.NoError(t, err)

			timers.last(t).fire()
			_, err = io.ReadAll(resp.Body)
			assertStallNotCancel(t, err)
		})
	}
}

// TestIdleTimeout_ACancellationBeforeTheHeadersIsNotAStall is the caller's
// Ctrl-C landing while the request still waits for its headers - the one
// moment the guard's own window is also armed (T3 review P4 B13) - over
// both protocols.
func TestIdleTimeout_ACancellationBeforeTheHeadersIsNotAStall(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			entered := make(chan struct{})
			srv := s.start(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
			}))
			defer srv.Close()

			timers := &manualTimers{}
			client := &http.Client{Transport: &httpclient.IdleTimeout{
				Base: srv.Client().Transport, Timeout: time.Minute, AfterFunc: timers.afterFunc,
			}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			require.NoError(t, err)
			errc := make(chan error, 1)
			go func() {
				resp, err := client.Do(req)
				if resp != nil {
					_ = resp.Body.Close()
				}
				errc <- err
			}()
			<-entered
			cancel()

			err = <-errc
			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled, "the caller's cancellation stays the caller's")
			assert.False(t, errors.Is(err, httpclient.ErrStalled), "a cancellation is not a stall: %v", err)
		})
	}
}

// TestIdleTimeout_AGapUnderTheWindowCompletesOverHTTP2: a body that pauses
// for less than the window, again and again, finishes - over the protocol
// the real host speaks, with a real timer.
func TestIdleTimeout_AGapUnderTheWindowCompletesOverHTTP2(t *testing.T) {
	const gap, window = 100 * time.Millisecond, 2 * time.Second
	srv := servers[1].start(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := range 5 {
			_, _ = w.Write([]byte{'a' + byte(i)})
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &httpclient.IdleTimeout{Base: srv.Client().Transport, Timeout: window}}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "abcde", string(body))
}
