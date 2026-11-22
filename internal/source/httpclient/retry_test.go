package httpclient

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// fakeClock is a settable clock the retry engine and its holds read, and
// which a recorded sleep advances: a test asserts the waits without
// spending them (#671).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testPolicy is a policy shaped like NexusMods' (#671), small enough to
// read in a test.
func testPolicy() RetryPolicy {
	return RetryPolicy{
		Service:       "Example",
		MaxAttempts:   3,
		BaseBackoff:   time.Second,
		MaxBackoff:    30 * time.Second,
		MaxRetryAfter: 2 * time.Minute,
		MaxHold:       24 * time.Hour,
		WaitOutHold:   2 * time.Minute,
		BodyHint:      CloudflareRetryAfter,
	}
}

// harness is a RetryTransport over srv whose sleeps are recorded (and
// advance the fake clock) and whose notices are collected.
type harness struct {
	rt      *RetryTransport
	clock   *fakeClock
	holds   *MemoryHolds
	waits   []time.Duration
	notices []source.Notice
}

func newHarness(t *testing.T, policy RetryPolicy) *harness {
	t.Helper()
	h := &harness{clock: newFakeClock()}
	h.holds = NewMemoryHolds(policy.Service, h.clock.Now, 3, 5*time.Minute, policy.MaxHold)
	h.rt = &RetryTransport{
		Base:   http.DefaultTransport,
		Policy: policy,
		Holds:  h.holds,
		Now:    h.clock.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.waits = append(h.waits, d)
			h.clock.Advance(d)
			return ctx.Err()
		},
	}
	return h
}

func (h *harness) do(t *testing.T, method, url, body string) (*http.Response, error) {
	t.Helper()
	ctx := source.WithNotices(context.Background(), func(n source.Notice) { h.notices = append(h.notices, n) })
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	require.NoError(t, err)
	resp, err := h.rt.RoundTrip(req)
	if resp != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	return resp, err
}

// sequence answers each request with the next of answers, the last one
// repeating, and counts the requests.
func sequence(t *testing.T, answers ...func(http.ResponseWriter, *http.Request)) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := min(calls, len(answers)-1)
		calls++
		mu.Unlock()
		answers[i](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }

func status(code int, header map[string]string, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// cloudflare1015 is the body NexusMods' edge answered the owner's verify
// with (#671): no Retry-After header, the wait only in the JSON.
const cloudflare1015 = `{"type":"https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-1xxx-errors/error-1015/","title":"Error 1015: You are being rate limited","status":429,"detail":"You are being rate limited","retryable":true,"retry_after":30,"zone":"api.nexusmods.com","cloudflare_error":true}`

func TestRetryTransport_A429WithRetryAfterIsWaitedOutAndRetried(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Retry-After": "12"}, ""), ok)
	h := newHarness(t, testPolicy())

	resp, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, *calls)
	require.Len(t, h.waits, 1)
	assert.GreaterOrEqual(t, h.waits[0], 12*time.Second, "a server's own wait is a floor")
	assert.LessOrEqual(t, h.waits[0], 13*time.Second, "the jitter adds at most a tenth, at most a second")

	require.Len(t, h.notices, 1, "the wait is said before it is slept")
	n := h.notices[0]
	assert.Equal(t, source.NoticeRetry, n.Kind)
	assert.Equal(t, "Example", n.Source)
	assert.Equal(t, source.RetryRateLimited, n.Reason)
	assert.Equal(t, http.StatusTooManyRequests, n.Status)
	assert.Equal(t, 2, n.Attempt)
	assert.Equal(t, 3, n.MaxAttempts)
	assert.Equal(t, h.waits[0], n.Wait)
}

func TestRetryTransport_A429WithOnlyTheCloudflareBodyHonoursItsRetryAfter(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Content-Type": "application/problem+json"}, cloudflare1015), ok)
	h := newHarness(t, testPolicy())

	resp, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, *calls)
	require.Len(t, h.waits, 1)
	assert.GreaterOrEqual(t, h.waits[0], 30*time.Second, "the body's retry_after, with no header")
	assert.LessOrEqual(t, h.waits[0], 31*time.Second)
	require.Len(t, h.notices, 1)
	assert.Equal(t, source.RetryRateLimited, h.notices[0].Reason)
}

func TestRetryTransport_A429WithNoHintBacksOffExponentially(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, nil, "slow down"), status(http.StatusTooManyRequests, nil, ""), ok)
	h := newHarness(t, testPolicy())

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err)
	assert.Equal(t, 3, *calls)
	require.Len(t, h.waits, 2)
	assert.GreaterOrEqual(t, h.waits[0], 500*time.Millisecond)
	assert.LessOrEqual(t, h.waits[0], time.Second)
	assert.GreaterOrEqual(t, h.waits[1], time.Second)
	assert.LessOrEqual(t, h.waits[1], 2*time.Second)
}

func TestRetryTransport_A5xxIsRetriedAndThenGivenUp(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusServiceUnavailable, nil, "down"))
	h := newHarness(t, testPolicy())

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.Error(t, err)
	assert.Equal(t, 3, *calls)
	assert.Len(t, h.waits, 2)
	require.Len(t, h.notices, 2)
	for _, n := range h.notices {
		assert.Equal(t, source.RetryServerError, n.Reason)
		assert.Equal(t, http.StatusServiceUnavailable, n.Status)
	}
	assert.Equal(t, "Example answered HTTP 503 (gave up after 3 attempts)", err.Error())
	var se *StatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, http.StatusServiceUnavailable, se.Status)
}

func TestRetryTransport_AThrottleThatOutlastsTheAttemptsReadsAsARateLimit(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, nil, cloudflare1015))
	h := newHarness(t, testPolicy())

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.Error(t, err)
	assert.Equal(t, 3, *calls)
	assert.Equal(t, "Example rate limit: try again in 30s (still limited after 3 attempts)", err.Error())
	assert.NotContains(t, err.Error(), "cloudflare", "no raw JSON dump")
	var se *StatusError
	require.ErrorAs(t, err, &se)
	assert.True(t, se.RateLimited())
	assert.Equal(t, 30*time.Second, se.RetryIn)

	// Out of attempts is not out of obligation: the host's wait stands for
	// the next request, which waits it out (it is short) before asking.
	holds := h.holds.Holds()
	require.Len(t, holds, 1)
	assert.WithinRange(t, holds[0].Until, h.clock.Now().Add(30*time.Second), h.clock.Now().Add(31*time.Second),
		"held for the named 30s, rounded up to a whole second")
}

func TestRetryTransport_AWaitPastTheBudgetIsHeldNotSlept(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Retry-After": "600"}, ""))
	h := newHarness(t, testPolicy())

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.Error(t, err)
	assert.Equal(t, 1, *calls)
	assert.Empty(t, h.waits, "ten minutes is refused, not slept through")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, "Example", later.Source)
	assert.Equal(t, h.clock.Now().Add(10*time.Minute), later.Until)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
	require.Len(t, h.notices, 1)
	assert.Equal(t, source.NoticeSuspended, h.notices[0].Kind)

	// The next request is refused without reaching the host.
	h.notices = nil
	_, err = h.do(t, http.MethodGet, srv.URL, "")
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 1, *calls, "a held host is not asked")
	require.Len(t, h.notices, 1)
	assert.Equal(t, source.NoticeSuspended, h.notices[0].Kind)
	assert.Equal(t, later.Until, h.notices[0].Until)

	// Once the hold has passed, the host is asked again.
	h.clock.Advance(10*time.Minute + time.Second)
	_, _ = h.do(t, http.MethodGet, srv.URL, "")
	assert.Equal(t, 2, *calls)
}

func TestRetryTransport_AShortHoldIsWaitedOutRatherThanRefused(t *testing.T) {
	srv, calls := sequence(t, ok)
	h := newHarness(t, testPolicy())
	h.holds.Named(nil, h.clock.Now().Add(30*time.Second), "rate limited")

	h.notices = nil
	resp, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, *calls)
	require.Len(t, h.waits, 1)
	assert.Equal(t, 30*time.Second, h.waits[0], "waited until the hold ends, then asked")
	require.Len(t, h.notices, 1)
	// Said as the hold it is, not as a retry of some status: lmm does not
	// know here whether a 429, a 503 or a spent quota set it.
	assert.Equal(t, source.NoticeSuspended, h.notices[0].Kind)
	assert.Equal(t, h.clock.Now(), h.notices[0].Until, "until the moment it was waited to")
}

// pastHold is a keeper whose one hold has already ended - the moment
// between a hold's end and its keeper noticing.
type pastHold struct {
	*MemoryHolds
	until time.Time
}

func (p pastHold) Active(*http.Request) (source.Hold, bool) {
	return source.Hold{Source: "Example", Until: p.until, Reason: "over"}, true
}

func TestRetryTransport_AHoldThatHasEndedSendsTheRequest(t *testing.T) {
	srv, calls := sequence(t, ok)
	h := newHarness(t, testPolicy())
	h.rt.Holds = pastHold{MemoryHolds: h.holds, until: h.clock.Now().Add(-time.Second)}

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err, "an ended hold refuses nothing")
	assert.Equal(t, 1, *calls)
	assert.Empty(t, h.waits)
	assert.Empty(t, h.notices)
}

func TestRetryTransport_AWaitedOutHoldCountsAgainstTheRequestsBudget(t *testing.T) {
	// 90s waited out, then a host asking 60s more: 150s is past the
	// two-minute budget, so the 60s is held, not slept.
	srv, calls := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Retry-After": "60"}, ""))
	h := newHarness(t, testPolicy())
	h.holds.Named(nil, h.clock.Now().Add(90*time.Second), "a throttle a moment ago")

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 1, *calls)
	assert.Equal(t, []time.Duration{90 * time.Second}, h.waits, "only the hold was waited")
}

// quotaKeeper is a HoldKeeper keeping quota holds apart (QuotaKeeper).
type quotaKeeper struct {
	*MemoryHolds
	quota *MemoryHolds
}

func (q quotaKeeper) Active(req *http.Request) (source.Hold, bool) {
	host, hostOK := q.MemoryHolds.Active(req)
	quota, quotaOK := q.quota.Active(req)
	if !hostOK || (quotaOK && quota.Until.After(host.Until)) {
		return quota, quotaOK
	}
	return host, true
}

func (q quotaKeeper) Quota(req *http.Request, until time.Time, why string) source.Hold {
	return q.quota.Named(req, until, why)
}

func TestRetryTransport_A429WithRetryAfterAndASpentQuotaWaitsForTheLater(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}, ""))
	h := newHarness(t, testPolicy())
	keeper := quotaKeeper{MemoryHolds: h.holds, quota: NewMemoryHolds("Example", h.clock.Now, 0, time.Minute, time.Hour)}
	h.rt.Holds = keeper
	h.rt.Policy.Observe = func(_ *http.Response, now time.Time) (time.Time, string) {
		return now.Add(40 * time.Minute), "quota used up"
	}

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later, "the quota's forty minutes, not the header's thirty seconds")
	assert.Equal(t, 1, *calls)
	assert.Empty(t, h.waits)
	assert.Equal(t, h.clock.Now().Add(40*time.Minute), later.Until)
	assert.Equal(t, "quota used up", later.Reason)
	require.Len(t, keeper.quota.Holds(), 1, "kept as the credential's quota hold")
	host := h.holds.Holds()
	require.Len(t, host, 1, "and the host's own thirty seconds as the host's")
	assert.Equal(t, h.clock.Now().Add(30*time.Second), host[0].Until)
}

func TestRetryTransport_ANamedWaitIsBoundedAcrossTheWholeRequest(t *testing.T) {
	// Two 90s waits would be three minutes of one request: the second is
	// past the two-minute budget, so it is held instead.
	srv, calls := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Retry-After": "90"}, ""))
	h := newHarness(t, testPolicy())

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 2, *calls)
	require.Len(t, h.waits, 1)
}

func TestRetryTransport_APostBodyIsReplayedOnTheRetry(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv, _ := sequence(t,
		func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
		},
		func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			ok(w, r)
		})
	h := newHarness(t, testPolicy())

	_, err := h.do(t, http.MethodPost, srv.URL, `{"query":"q"}`)
	require.NoError(t, err)
	assert.Equal(t, []string{`{"query":"q"}`, `{"query":"q"}`}, bodies, "the GraphQL body is sent again, whole")
}

func TestRetryTransport_AnOrdinary4xxIsAnAnswer(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusNotFound, nil, "nope"))
	h := newHarness(t, testPolicy())

	resp, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, 1, *calls)
	assert.Empty(t, h.waits)
}

func TestRetryTransport_TheBreakerTripsAfterConsecutiveFailuresAndASuccessClearsIt(t *testing.T) {
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		ok(w, nil)
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, testPolicy())

	for range 2 {
		_, err := h.do(t, http.MethodGet, srv.URL, "")
		require.Error(t, err)
	}
	fail = false
	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err)
	fail = true
	_, err = h.do(t, http.MethodGet, srv.URL, "")
	require.Error(t, err)
	assert.Empty(t, h.holds.Holds(), "the streak restarted at the success")

	for range 2 {
		_, err = h.do(t, http.MethodGet, srv.URL, "")
		require.Error(t, err)
	}
	holds := h.holds.Holds()
	require.Len(t, holds, 1, "three in a row trip it")
	assert.Contains(t, holds[0].Reason, "suspended after 3 failed requests in a row")
}

func TestRetryTransport_CancellationDuringAWaitEndsTheRequest(t *testing.T) {
	srv, _ := sequence(t, status(http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}, ""))
	h := newHarness(t, testPolicy())
	h.rt.Sleep = SleepContext

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = h.rt.RoundTrip(req)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Empty(t, h.holds.Holds(), "giving up is not the host failing")
}

func TestRetryTransport_ObserveHoldsTheHostAfterASuccess(t *testing.T) {
	srv, calls := sequence(t, ok)
	policy := testPolicy()
	h := newHarness(t, policy)
	h.rt.Policy.Observe = func(_ *http.Response, now time.Time) (time.Time, string) {
		return now.Add(time.Hour), "the quota is used up"
	}

	resp, err := h.do(t, http.MethodGet, srv.URL, "")
	require.NoError(t, err, "the answer that said so is still an answer")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	_, err = h.do(t, http.MethodGet, srv.URL, "")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, "the quota is used up", later.Reason)
	assert.Equal(t, 1, *calls)
}

func TestRetryTransport_HooksShapeTheErrors(t *testing.T) {
	sentinel := errors.New("metadata unavailable")
	srv, _ := sequence(t, status(http.StatusBadGateway, nil, ""))
	policy := testPolicy()
	policy.Exhausted = func(last error, attempts int) error { return errors.Join(sentinel, last) }
	h := newHarness(t, policy)

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	require.ErrorIs(t, err, sentinel)
}

func TestBackoff_IsBoundedAndJittered(t *testing.T) {
	p := testPolicy()
	for attempt := 1; attempt <= 6; attempt++ {
		nominal := min(p.BaseBackoff<<(attempt-1), p.MaxBackoff)
		for range 20 {
			d := p.Backoff(attempt, 0)
			assert.GreaterOrEqual(t, d, nominal/2)
			assert.LessOrEqual(t, d, nominal)
		}
	}
	for range 20 {
		d := p.Backoff(1, 45*time.Second)
		assert.GreaterOrEqual(t, d, 45*time.Second, "a hint is a floor")
		assert.LessOrEqual(t, d, 46*time.Second)
	}
}

func TestCloudflareRetryAfter_ReadsTheProblemBody(t *testing.T) {
	assert.Equal(t, 30*time.Second, CloudflareRetryAfter([]byte(cloudflare1015)))
	assert.Zero(t, CloudflareRetryAfter([]byte(`not json`)))
	assert.Zero(t, CloudflareRetryAfter([]byte(`{"retry_after":-5}`)))
	assert.Zero(t, CloudflareRetryAfter(nil))
	assert.Equal(t, 1500*time.Millisecond, CloudflareRetryAfter([]byte(`{"retry_after":1.5}`)))
}

func TestMemoryHolds_ANamedHoldIsNotLiftedByASuccess(t *testing.T) {
	clock := newFakeClock()
	holds := NewMemoryHolds("Example", clock.Now, 3, 5*time.Minute, time.Hour)
	holds.Named(nil, clock.Now().Add(10*time.Minute), "asked to wait")
	holds.Succeeded(nil)
	_, held := holds.Active(nil)
	assert.True(t, held, "an answer already in flight says nothing about the time the host asked for")
	holds.Named(nil, clock.Now().Add(time.Minute), "a shorter one")
	h, _ := holds.Active(nil)
	assert.Equal(t, clock.Now().Add(10*time.Minute), h.Until, "a hold is never shortened")
	holds.Named(nil, clock.Now().Add(48*time.Hour), "absurd")
	h, _ = holds.Active(nil)
	assert.Equal(t, clock.Now().Add(time.Hour), h.Until, "capped at maxHold")
}

func TestRetryTransport_ObserveIsTheHintForAThrottleThatNamesNoOther(t *testing.T) {
	srv, calls := sequence(t, status(http.StatusTooManyRequests, nil, `{"message":"Rate limit exceeded"}`))
	h := newHarness(t, testPolicy())
	h.rt.Policy.Observe = func(_ *http.Response, now time.Time) (time.Time, string) {
		return now.Add(40 * time.Minute), "quota used up"
	}

	_, err := h.do(t, http.MethodGet, srv.URL, "")
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later, "forty minutes is past the budget: held, not slept")
	assert.Equal(t, 1, *calls)
	assert.Empty(t, h.waits)
	assert.Equal(t, h.clock.Now().Add(40*time.Minute), later.Until)
}
