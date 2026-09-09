package steamworkshop

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	rt.sleep = func(d time.Duration) { waits = append(waits, d) }
	return rt, &waits
}

func post(t *testing.T, rt *retryTransport, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("itemcount=1"))
	require.NoError(t, err)
	return rt.RoundTrip(req)
}

func TestRetryTransport_RetriesA429AndHonoursRetryAfter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// The retried request must carry the same body: a rewind that
			// dropped it would silently ask Valve for nothing.
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		body := make([]byte, 32)
		n, _ := r.Body.Read(body)
		assert.Equal(t, "itemcount=1", string(body[:n]), "the retry replays the original form body")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rt, waits := newTestTransport(t, time.Now)
	resp, err := post(t, rt, srv.URL)
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
	_, err := post(t, rt, srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMetadataUnavailable)
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
	resp, err := post(t, rt, srv.URL)
	require.NoError(t, err, "a 403 is an answer, handed straight back to the caller")
	_ = resp.Body.Close()
	assert.Equal(t, 1, calls, "repeating a request lmm got wrong would only be rude")
	assert.Empty(t, *waits)
}

func TestRetryTransport_CircuitBreakerOpensAfterThreeFailedRequests(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	rt, _ := newTestTransport(t, func() time.Time { return clock })

	for range breakerThreshold {
		_, err := post(t, rt, srv.URL)
		require.Error(t, err)
	}
	failedCalls := calls

	// Breaker open: the next request never reaches the network.
	_, err := post(t, rt, srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMetadataUnavailable)
	assert.Equal(t, failedCalls, calls, "a tripped breaker makes no request at all")

	// It reopens after the cooldown.
	clock = clock.Add(breakerCooldown + time.Second)
	_, err = post(t, rt, srv.URL)
	require.Error(t, err)
	assert.Greater(t, calls, failedCalls, "past the cooldown the API is probed again")
}

func TestRetryTransport_SuccessClearsTheFailureStreak(t *testing.T) {
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rt, _ := newTestTransport(t, time.Now)
	fail = true
	for range breakerThreshold - 1 {
		_, err := post(t, rt, srv.URL)
		require.Error(t, err)
	}
	fail = false
	resp, err := post(t, rt, srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()

	fail = true
	// The streak restarted, so this failure alone must not trip the breaker.
	_, err = post(t, rt, srv.URL)
	require.Error(t, err)
	_, open := rt.breakerOpen()
	assert.False(t, open, "the breaker counts CONSECUTIVE failures")
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

func TestRewind_RefusesARequestThatCannotBeReplayed(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid", strings.NewReader("x"))
	require.NoError(t, err)
	req.GetBody = nil
	_, err = rewind(req, 2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMetadataUnavailable))
}
