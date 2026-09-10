package thunderstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTransport builds a retryTransport whose waits are recorded rather
// than slept, so the backoff policy is asserted without a test that takes
// seconds to run.
func newTestTransport(t *testing.T, now func() time.Time) (*retryTransport, *[]time.Duration) {
	t.Helper()
	var waits []time.Duration
	rt := newRetryTransport(http.DefaultTransport, now)
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
	// Jittered over the lower half of the server's own 2s hint.
	assert.GreaterOrEqual(t, (*waits)[0], time.Second)
	assert.LessOrEqual(t, (*waits)[0], 2*time.Second)
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

	// Breaker open: the next request never reaches the network.
	_, err := get(t, rt, srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexUnavailable)
	assert.Equal(t, failedCalls, calls, "a tripped breaker makes no request at all")

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
	_, open := rt.breakerOpen()
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

	rt := newRetryTransport(http.DefaultTransport, time.Now)
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
	assert.LessOrEqual(t, backoffFor(1, time.Hour), maxBackoff, "an absurd Retry-After is capped")
}

func TestRetryAfter_ReadsOnlyTheDelaySecondsForm(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"Wed, 10 Sep 2026 12:00:00 GMT", 0},
		{"soon", 0},
	}
	for _, tt := range tests {
		resp := &http.Response{Header: http.Header{}}
		if tt.header != "" {
			resp.Header.Set("Retry-After", tt.header)
		}
		assert.Equal(t, tt.want, retryAfter(resp), "Retry-After: %q", tt.header)
	}
}
