package thunderstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTransport builds a retryTransport whose waits are recorded rather
// than slept, so the backoff policy is asserted without a test that takes
// seconds to run.
func newTestTransport(t *testing.T, now func() time.Time) (*retryTransport, *[]time.Duration) {
	t.Helper()
	var waits []time.Duration
	rt := newRetryTransport(http.DefaultTransport, now, nil)
	rt.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return rt, &waits
}

func get(t *testing.T, rt *retryTransport, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	return rt.RoundTrip(req)
}

func TestRetryTransport_RetriesA429AndHonoursRetryAfter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	rt, waits := newTestTransport(t, time.Now)
	resp, err := get(t, rt, srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, 2, calls)
	require.Len(t, *waits, 1)
	// HONOURED: never shorter than the server asked for (#436 - the old
	// policy jittered DOWN from the hint, so a throttled client came back
	// early and was throttled again), with a little jitter on top.
	assert.GreaterOrEqual(t, (*waits)[0], 2*time.Second)
	assert.LessOrEqual(t, (*waits)[0], 2*time.Second+200*time.Millisecond)
}

// recordNotices returns a context whose notices are appended to the slice.
func recordNotices(t *testing.T) (context.Context, *[]source.Notice) {
	t.Helper()
	var got []source.Notice
	return source.WithNotices(t.Context(), func(n source.Notice) { got = append(got, n) }), &got
}

func getCtx(t *testing.T, ctx context.Context, rt *retryTransport, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	return rt.RoundTrip(req)
}

// TestRetryTransport_ReportsEachWaitAsANotice is #436's first ask: every
// throttled attempt and the wait before the next reaches whoever is
// listening, with the numbers the user is shown ("retrying in 12s (attempt
// 2 of 3)") - not only the eventual error.
func TestRetryTransport_ReportsEachWaitAsANotice(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	rt, waits := newTestTransport(t, time.Now)
	ctx, notices := recordNotices(t)
	resp, err := getCtx(t, ctx, rt, srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.Len(t, *notices, 2)
	for i, n := range *notices {
		assert.Equal(t, source.NoticeRetry, n.Kind)
		assert.Equal(t, "Thunderstore", n.Source)
		assert.Equal(t, source.RetryRateLimited, n.Reason)
		assert.Equal(t, http.StatusTooManyRequests, n.Status)
		assert.Equal(t, i+2, n.Attempt, "the attempt ABOUT to be made")
		assert.Equal(t, maxAttempts, n.MaxAttempts)
		assert.Equal(t, (*waits)[i], n.Wait, "the notice names the wait actually slept")
		assert.GreaterOrEqual(t, n.Wait, 12*time.Second)
	}
}

// TestRetryTransport_NoticesNameTheReason: a server error and a dropped
// connection are retried too, and say which they were.
func TestRetryTransport_NoticesNameTheReason(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		}))
		defer srv.Close()

		rt, _ := newTestTransport(t, time.Now)
		ctx, notices := recordNotices(t)
		resp, err := getCtx(t, ctx, rt, srv.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Len(t, *notices, 1)
		assert.Equal(t, source.RetryServerError, (*notices)[0].Reason)
		assert.Equal(t, http.StatusServiceUnavailable, (*notices)[0].Status)
	})

	t.Run("network error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing listens there now

		rt, _ := newTestTransport(t, time.Now)
		ctx, notices := recordNotices(t)
		_, err := getCtx(t, ctx, rt, url)
		require.Error(t, err)
		require.Len(t, *notices, maxAttempts-1)
		assert.Equal(t, source.RetryNetworkError, (*notices)[0].Reason)
		assert.Zero(t, (*notices)[0].Status)
		assert.Error(t, (*notices)[0].Err)
	})

	// T3 review F1: a stalled attempt reached the host and then heard
	// nothing, which is not "could not reach" it.
	t.Run("stalled", func(t *testing.T) {
		rt, _ := newTestTransport(t, time.Now)
		rt.base = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &httpclient.StallError{Timeout: stallTimeout}
		})
		ctx, notices := recordNotices(t)
		_, err := getCtx(t, ctx, rt, "http://127.0.0.1:9/")
		require.ErrorIs(t, err, httpclient.ErrStalled)
		require.Len(t, *notices, maxAttempts-1)
		assert.Equal(t, source.RetryStalled, (*notices)[0].Reason)
	})
}

// roundTripFunc is an http.RoundTripper made of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRetryTransport_ARetryAfterPastTheCeilingIsNotWaitedOut: a throttle
// that asks for longer than maxRetryAfter is not slept through - a search
// must not sit silent for five minutes - and is not re-asked before the
// time the server named, by this request or the next.
func TestRetryTransport_ARetryAfterPastTheCeilingIsNotWaitedOut(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	clock := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	rt, waits := newTestTransport(t, func() time.Time { return clock })
	ctx, notices := recordNotices(t)

	_, err := getCtx(t, ctx, rt, srv.URL)
	require.Error(t, err)
	assert.Empty(t, *waits, "a wait past the ceiling is refused, not slept")
	assert.Equal(t, 1, calls)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, clock.Add(300*time.Second), later.Until)
	assert.ErrorIs(t, err, ErrIndexUnavailable)
	assert.Contains(t, later.Reason, "5m0s")

	// The next request honours the same deadline without asking.
	_, err = getCtx(t, ctx, rt, srv.URL)
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 1, calls, "nothing is sent before the server's own deadline")
	require.NotEmpty(t, *notices)
	last := (*notices)[len(*notices)-1]
	assert.Equal(t, source.NoticeSuspended, last.Kind)
	assert.Equal(t, clock.Add(300*time.Second), last.Until)

	clock = clock.Add(301 * time.Second)
	_, _ = getCtx(t, ctx, rt, srv.URL)
	assert.Equal(t, 2, calls, "past the deadline the host is asked again")
}

// TestRetryTransport_AWaitNamedOnTheLastAttemptIsStillHonoured: running out
// of attempts does not make the server's Retry-After void - the next
// request, from this search or the next package read, waits for it too.
func TestRetryTransport_AWaitNamedOnTheLastAttemptIsStillHonoured(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	clock := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	rt, _ := newTestTransport(t, func() time.Time { return clock })
	_, err := get(t, rt, srv.URL)
	require.Error(t, err)
	assert.Equal(t, maxAttempts, calls)

	_, err = get(t, rt, srv.URL)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later, "the next request is held until the server's time")
	assert.Equal(t, clock.Add(20*time.Second), later.Until)
	assert.Equal(t, maxAttempts, calls, "nothing was sent")

	clock = clock.Add(21 * time.Second)
	_, _ = get(t, rt, srv.URL)
	assert.Greater(t, calls, maxAttempts)
}

// TestRetryTransport_TheOneMinuteWindowIsPerRequest is T3 review F8: two
// answers of Retry-After: 60 held a cold search silent for two minutes,
// because the minute lmm sits through applied to each wait. It bounds the
// request's total: the second such answer is a hold, not another minute.
func TestRetryTransport_TheOneMinuteWindowIsPerRequest(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	clock := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	rt, waits := newTestTransport(t, func() time.Time { return clock })
	_, err := get(t, rt, srv.URL)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 2, calls, "one wait of a minute, then the host is held")
	require.Len(t, *waits, 1, "a minute was waited once, not twice")
	assert.GreaterOrEqual(t, (*waits)[0], time.Minute)
	assert.Equal(t, clock.Add(time.Minute), later.Until)
	assert.Contains(t, later.Reason, "1m0s")
}

// TestRetryAfter_AnAbsurdValueIsClampedNotWrapped: a delay too large for a
// time.Duration must not overflow into a negative (ignored) wait - it is a
// very long wait, which the transport then refuses to sit through.
func TestRetryAfter_AnAbsurdValueIsClampedNotWrapped(t *testing.T) {
	// Exact values (T3 review P4 B6/B11): "positive and large" also held
	// for the wrapped value the clamp exists to prevent, and a 20-digit
	// value used to skip the clamp entirely and read as no hint (F8).
	now := time.Now()
	for _, v := range []string{"99999999999999", "99999999999999999999", "9223372036854775807", "Mon, 01 Jan 2300 00:00:00 GMT"} {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("Retry-After", v)
		assert.Equal(t, maxHold, retryAfter(resp, now), "Retry-After: %s", v)
	}
}

func TestRetryTransport_RetriesA5xxAndGivesUpAfterThreeAttempts(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	rt, waits := newTestTransport(t, time.Now)
	_, err := get(t, rt, srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexUnavailable)
	assert.Equal(t, maxAttempts, calls)
	assert.Len(t, *waits, maxAttempts-1)
}

func TestRetryTransport_DoesNotRetryAnOrdinary4xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	rt, waits := newTestTransport(t, time.Now)
	resp, err := get(t, rt, srv.URL)
	require.NoError(t, err, "a 403 is an answer, handed straight back to the caller")
	_ = resp.Body.Close()
	assert.Equal(t, 1, calls, "repeating a request lmm got wrong would only be rude")
	assert.Empty(t, *waits)
}

// TestRetryTransport_A304IsNotAFailure pins that the answer a conditional
// GET exists to get is not mistaken for something to retry.
func TestRetryTransport_A304IsNotAFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	rt, waits := newTestTransport(t, time.Now)
	resp, err := get(t, rt, srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)
	assert.Equal(t, 1, calls)
	assert.Empty(t, *waits)
}

func TestRetryTransport_CircuitBreakerOpensAfterThreeFailedRequests(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rt, _ := newTestTransport(t, func() time.Time { return clock })

	for range breakerThreshold {
		_, err := get(t, rt, srv.URL)
		require.Error(t, err)
	}
	failedCalls := calls

	// Breaker open: the next request never reaches the network, and says
	// until when - as data, and as a notice (#436).
	ctx, notices := recordNotices(t)
	_, err := getCtx(t, ctx, rt, srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexUnavailable)
	assert.Equal(t, failedCalls, calls, "a tripped breaker makes no request at all")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, clock.Add(breakerCooldown), later.Until)
	assert.Equal(t, "Thunderstore", later.Source)
	require.Len(t, *notices, 1)
	assert.Equal(t, source.NoticeSuspended, (*notices)[0].Kind)
	assert.Equal(t, clock.Add(breakerCooldown), (*notices)[0].Until)

	// It reopens after the cooldown.
	clock = clock.Add(breakerCooldown + time.Second)
	_, err = get(t, rt, srv.URL)
	require.Error(t, err)
	assert.Greater(t, calls, failedCalls, "past the cooldown the host is probed again")
}

func TestRetryTransport_SuccessClearsTheFailureStreak(t *testing.T) {
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	rt, _ := newTestTransport(t, time.Now)
	fail = true
	for range breakerThreshold - 1 {
		_, err := get(t, rt, srv.URL)
		require.Error(t, err)
	}
	fail = false
	resp, err := get(t, rt, srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()

	fail = true
	// The streak restarted, so this failure alone must not trip the breaker.
	_, err = get(t, rt, srv.URL)
	require.Error(t, err)
	_, _, open := rt.holds.active("")
	assert.False(t, open, "the breaker counts CONSECUTIVE failures")
}

// TestRetryTransport_CancellingDuringBackoffReturnsPromptly drives the REAL
// sleepCtx: with maxBackoff at 30s and two retries, a Ctrl-C during a cold
// index build could otherwise wait out a minute of sleeping before anyone
// noticed. The seam is precisely what a test could paper over, so this one
// does not use it.
func TestRetryTransport_CancellingDuringBackoffReturnsPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A Retry-After far larger than any test may wait for.
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	rt := newRetryTransport(http.DefaultTransport, time.Now, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err = rt.RoundTrip(req)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "the caller's cancellation, not an upstream failure")
	assert.Less(t, elapsed, 5*time.Second, "the 15s+ jittered backoff was abandoned, not slept")
}

func TestSleepCtx_ReturnsNilWhenTheWaitCompletes(t *testing.T) {
	require.NoError(t, sleepCtx(context.Background(), time.Millisecond))
}

func TestBackoffFor_IsBoundedAndJittered(t *testing.T) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		nominal := min(baseBackoff<<(attempt-1), maxBackoff)
		for range 20 {
			d := backoffFor(attempt, 0)
			assert.GreaterOrEqual(t, d, nominal/2)
			assert.LessOrEqual(t, d, nominal)
		}
	}
}

// TestBackoffFor_AServerHintIsAFloor: the wait a server names is the least
// lmm waits; the jitter only ever adds to it, and never more than a tenth
// (and never more than a second).
func TestBackoffFor_AServerHintIsAFloor(t *testing.T) {
	for _, hint := range []time.Duration{time.Second, 12 * time.Second, maxRetryAfter} {
		for range 20 {
			d := backoffFor(1, hint)
			assert.GreaterOrEqual(t, d, hint)
			assert.LessOrEqual(t, d, hint+min(hint/10, time.Second))
		}
	}
}

// TestRetryAfter_ReadsBothForms: RFC 9110 allows delay-seconds or an
// HTTP-date, and a server behind a CDN may send either.
func TestRetryAfter_ReadsBothForms(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"Wed, 10 Sep 2026 12:00:30 GMT", 30 * time.Second},
		{"Wed, 10 Sep 2026 11:59:00 GMT", 0}, // already past
		{"soon", 0},
	}
	for _, tt := range tests {
		resp := &http.Response{Header: http.Header{}}
		if tt.header != "" {
			resp.Header.Set("Retry-After", tt.header)
		}
		assert.Equal(t, tt.want, retryAfter(resp, now), "Retry-After: %q", tt.header)
	}
}

// TestRetryConstants_ArePinnedToTheDocumentedWindow keeps the policy honest
// about what it rests on (package doc): the only rate Thunderstore's own
// code sets is per MINUTE, so a server-named wait up to one minute is
// waited out, and anything longer is refused rather than slept.
func TestRetryConstants_ArePinnedToTheDocumentedWindow(t *testing.T) {
	assert.Equal(t, time.Minute, maxRetryAfter)
	assert.Less(t, maxBackoff, maxRetryAfter, "lmm's own backoff never outwaits a server's")
	worst := maxAttempts * stallTimeout
	for attempt := 1; attempt < maxAttempts; attempt++ {
		worst += maxRetryAfter + min(maxRetryAfter/10, time.Second)
	}
	assert.Less(t, worst, fetchTimeout, "the stall and retry bounds fail a dead host long before the request ceiling")
}
