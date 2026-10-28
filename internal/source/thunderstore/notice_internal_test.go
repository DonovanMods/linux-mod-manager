package thunderstore

// #436 at the level a user meets it: a cold build, a throttled cold build
// and a stalled one, each driven through the Source's own entry points
// with the two clocks in the transport replaced - the retry sleep and the
// stall timer - so nothing here waits for real.
//
// Package-INTERNAL because those two clocks are not options: production
// never wants to change them, and widening Options for a test would hand
// every caller a way to disable the stall guard.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const noticeCommunity = "lethal-company"

// handTimer is a stall window the test fires itself.
type handTimer struct {
	mu     sync.Mutex
	d      time.Duration
	f      func()
	armed  bool
	resets chan struct{}
}

func (h *handTimer) Reset(d time.Duration) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	was := h.armed
	h.d, h.armed = d, true
	select {
	case h.resets <- struct{}{}:
	default:
	}
	return was
}

func (h *handTimer) Stop() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	was := h.armed
	h.armed = false
	return was
}

func (h *handTimer) fire() {
	h.mu.Lock()
	armed, f := h.armed, h.f
	h.armed = false
	h.mu.Unlock()
	if armed {
		f()
	}
}

// handTimers records every window the transport opens.
type handTimers struct {
	mu     sync.Mutex
	timers []*handTimer
	opened chan *handTimer
}

func newHandTimers() *handTimers { return &handTimers{opened: make(chan *handTimer, 16)} }

func (h *handTimers) afterFunc(d time.Duration, f func()) httpclient.Timer {
	t := &handTimer{d: d, f: f, armed: true, resets: make(chan struct{}, 256)}
	h.mu.Lock()
	h.timers = append(h.timers, t)
	h.mu.Unlock()
	h.opened <- t
	return t
}

// noticeSource builds a Source against handler whose retry sleeps are
// recorded rather than slept and whose stall windows are fired by hand.
func noticeSource(t *testing.T, handler http.Handler) (*Source, *[]time.Duration, *handTimers, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cacheDir := t.TempDir()
	src := New(Options{CacheDir: cacheDir, BaseURL: srv.URL})
	var mu sync.Mutex
	var waits []time.Duration
	src.client.retry.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	timers := newHandTimers()
	src.client.idle.AfterFunc = timers.afterFunc
	return src, &waits, timers, cacheDir
}

// noticeRecorder collects notices; safe for the transport's goroutines.
type noticeRecorder struct {
	mu  sync.Mutex
	got []source.Notice
}

func (r *noticeRecorder) ctx(parent context.Context) context.Context {
	return source.WithNotices(parent, func(n source.Notice) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got = append(r.got, n)
	})
}

func (r *noticeRecorder) kinds() []source.NoticeKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]source.NoticeKind, len(r.got))
	for i, n := range r.got {
		out[i] = n.Kind
	}
	return out
}

func (r *noticeRecorder) all() []source.Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]source.Notice(nil), r.got...)
}

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "community_small.json"))
	require.NoError(t, err)
	return data
}

func serveFixture(t *testing.T) http.HandlerFunc {
	body := fixtureBytes(t)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		_, _ = w.Write(body)
	}
}

// TestSearch_AColdBuildAnnouncesItself: the one-time build a search does
// for itself says so, with the community, and says what it produced - to
// any caller, which is how `lmm import`'s scan-mode matching gets the same
// readout `lmm search` has (T1 review #8) without a line of its own. A warm
// search says nothing.
func TestSearch_AColdBuildAnnouncesItself(t *testing.T) {
	src, _, _, _ := noticeSource(t, serveFixture(t))
	rec := &noticeRecorder{}

	_, err := src.Search(rec.ctx(t.Context()), source.SearchQuery{GameID: noticeCommunity, Query: "ship"})
	require.NoError(t, err)

	got := rec.all()
	require.Len(t, got, 2)
	assert.Equal(t, source.NoticeIndexBuilding, got[0].Kind)
	assert.Equal(t, noticeCommunity, got[0].GameID)
	assert.Equal(t, "Thunderstore", got[0].Source)
	assert.Equal(t, source.NoticeIndexBuilt, got[1].Kind)
	assert.Positive(t, got[1].Packages)

	warm := &noticeRecorder{}
	_, err = src.Search(warm.ctx(t.Context()), source.SearchQuery{GameID: noticeCommunity, Query: "ship"})
	require.NoError(t, err)
	assert.Empty(t, warm.kinds(), "a warm index has nothing to announce")
}

// TestRefreshIndex_AnExplicitRefreshReportsOnlyThroughItsProgress: a caller
// that asked for the build hands in its own progress function, and the
// same build must not ALSO be announced to the context's observer - that
// is two lines for one event.
func TestRefreshIndex_AnExplicitRefreshReportsOnlyThroughItsProgress(t *testing.T) {
	src, _, _, _ := noticeSource(t, serveFixture(t))
	rec := &noticeRecorder{}
	var ticks []string

	_, err := src.RefreshIndex(rec.ctx(t.Context()), noticeCommunity, true, func(phase, _ string, _ int64) {
		ticks = append(ticks, phase)
	})
	require.NoError(t, err)
	assert.NotEmpty(t, ticks)
	assert.Empty(t, rec.kinds())
}

// TestSearch_AThrottledColdBuildReportsTheWait is #436's own scenario: the
// first search hits the throttle, and what the user is told is the wait,
// before the index arrives.
func TestSearch_AThrottledColdBuildReportsTheWait(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	body := fixtureBytes(t)
	src, waits, _, _ := noticeSource(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(body)
	}))
	rec := &noticeRecorder{}

	res, err := src.Search(rec.ctx(t.Context()), source.SearchQuery{GameID: noticeCommunity})
	require.NoError(t, err)
	assert.Positive(t, res.TotalCount)

	assert.Equal(t, []source.NoticeKind{source.NoticeIndexBuilding, source.NoticeRetry, source.NoticeIndexBuilt}, rec.kinds())
	retry := rec.all()[1]
	assert.Equal(t, source.RetryRateLimited, retry.Reason)
	assert.Equal(t, 2, retry.Attempt)
	assert.Equal(t, maxAttempts, retry.MaxAttempts)
	require.Len(t, *waits, 1)
	assert.Equal(t, (*waits)[0], retry.Wait)
	assert.GreaterOrEqual(t, retry.Wait, 12*time.Second)
}

// TestRefreshIndex_AStalledBodyFailsFastAndKeepsTheOldIndex is #436's
// second ask. A refresh whose body stops mid-document fails once the stall
// window passes - not after fetchTimeout - with the stall in its error
// chain; it leaves no staging file; and the index that was already there is
// still the one served, reported alongside the failure as the stale-copy
// contract says.
func TestRefreshIndex_AStalledBodyFailsFastAndKeepsTheOldIndex(t *testing.T) {
	body := fixtureBytes(t)
	var mu sync.Mutex
	stall := false
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	src, _, timers, cacheDir := noticeSource(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stalling := stall
		mu.Unlock()
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		if !stalling {
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Last-Modified", "Thu, 11 Sep 2026 12:00:00 GMT")
		_, _ = w.Write(body[:len(body)/3])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))

	before, err := src.RefreshIndex(t.Context(), noticeCommunity, true, nil)
	require.NoError(t, err)
	require.True(t, before.Present)
	drain(timers.opened)

	mu.Lock()
	stall = true
	mu.Unlock()

	type outcome struct {
		status source.IndexStatus
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		st, err := src.RefreshIndex(t.Context(), noticeCommunity, true, nil)
		done <- outcome{st, err}
	}()

	window := <-timers.opened
	window.mu.Lock()
	armedFor := window.d
	window.mu.Unlock()
	assert.Equal(t, stallTimeout, armedFor)
	assert.Less(t, stallTimeout*10, fetchTimeout, "a stall is noticed in a small fraction of the request ceiling")
	<-window.resets // the headers arrived
	<-window.resets // and the first third of the body did
	window.fire()

	got := <-done
	require.Error(t, got.err)
	assert.ErrorIs(t, got.err, ErrIndexUnavailable)
	assert.ErrorIs(t, got.err, httpclient.ErrStalled)
	assert.Contains(t, got.err.Error(), "no data received for 30s")
	assert.True(t, got.status.Present, "the index already on disk is still served")
	assert.Equal(t, before.Packages, got.status.Packages)

	entries, err := os.ReadDir(filepath.Join(cacheDir, rootDirName, noticeCommunity))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".packages-") || strings.HasPrefix(e.Name(), ".index-"),
			"a stalled build leaves no staging file: %s", e.Name())
	}
}

// TestSearch_ASuspendedHostIsRefusedWithWhenItReopens: once the breaker is
// open, a search is refused at once, and both the notice and the error say
// when lmm will ask again.
func TestSearch_ASuspendedHostIsRefusedWithWhenItReopens(t *testing.T) {
	src, _, _, _ := noticeSource(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	for range breakerThreshold {
		_, err := src.Search(t.Context(), source.SearchQuery{GameID: noticeCommunity})
		require.Error(t, err)
	}

	rec := &noticeRecorder{}
	_, err := src.Search(rec.ctx(t.Context()), source.SearchQuery{GameID: noticeCommunity})
	require.Error(t, err)
	var later *source.RetryLaterError
	require.True(t, errors.As(err, &later), "the refusal carries its deadline: %v", err)
	assert.False(t, later.Until.IsZero())
	assert.ErrorIs(t, err, ErrIndexUnavailable)
	assert.Contains(t, rec.kinds(), source.NoticeSuspended)
}

func drain(ch chan *handTimer) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
