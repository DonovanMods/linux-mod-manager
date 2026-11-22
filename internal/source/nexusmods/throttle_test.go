package nexusmods

// #671: the owner's verify of a 353-mod collection hit Cloudflare's error
// 1015 on api.nexusmods.com, and every throttled request became a raw JSON
// dump in a "could not check version" row. These pin the NexusMods client
// on the shared retrying transport: REST and GraphQL alike are retried, the
// wait Cloudflare names only in its body is honoured, the wait is said as a
// notice, the quota headers hold the host, and what is left after the
// retries reads as a rate limit.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// cloudflare1015 is the 429 body the owner's verify got (#671), whole.
const cloudflare1015 = `{"type":"https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-1xxx-errors/error-1015/","title":"Error 1015: You are being rate limited","status":429,"detail":"You are being rate limited","instance":"8f1a2b3c4d5e6f70","retryable":true,"retry_after":30,"owner_action_required":false,"ray_id":"8f1a2b3c4d5e6f70","timestamp":"2026-11-22T12:00:00Z","zone":"api.nexusmods.com","cloudflare_error":true,"error_code":1015,"error_name":"rate_limited","error_category":"rate_limit"}`

// throttleClock is the fake clock a throttled client's retries read and
// sleep on: a sleep advances it, so nothing in these tests waits.
type throttleClock struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
}

func (c *throttleClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *throttleClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.t = c.t.Add(d)
	return ctx.Err()
}

// newThrottleSource is a NexusMods source over srv, REST and GraphQL alike,
// whose retries run on a fake clock.
func newThrottleSource(t *testing.T, srv *httptest.Server) (*NexusMods, *throttleClock) {
	t.Helper()
	clock := &throttleClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	nm := New(srv.Client(), "test-key")
	nm.client.SetBaseURL(srv.URL)
	nm.client.graphqlURL = srv.URL + "/v2/graphql"
	nm.client.setRetryClock(clock.now, clock.sleep)
	return nm, clock
}

// collectNotices returns a ctx whose source notices are appended to the
// returned slice.
func collectNotices() (context.Context, *[]source.Notice) {
	var mu sync.Mutex
	var got []source.Notice
	ctx := source.WithNotices(context.Background(), func(n source.Notice) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, n)
	})
	return ctx, &got
}

const filesJSON = `{"files":[{"file_id":7,"name":"Main","version":"1.0","is_primary":true,"file_name":"main.zip"}],"file_updates":[]}`

func TestNexusThrottle_ARESTRequestThrottledOnceSucceedsAfterTheNamedWait(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(cloudflare1015))
			return
		}
		_, _ = w.Write([]byte(filesJSON))
	}))
	defer srv.Close()
	nm, clock := newThrottleSource(t, srv)
	ctx, notices := collectNotices()

	files, err := nm.GetModFiles(ctx, &domain.Mod{ID: "1", GameID: "cyberpunk2077"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, 2, calls)
	require.Len(t, clock.waits, 1)
	assert.GreaterOrEqual(t, clock.waits[0], 30*time.Second, "Cloudflare's retry_after, from the body")

	require.Len(t, *notices, 1)
	n := (*notices)[0]
	assert.Equal(t, source.NoticeRetry, n.Kind)
	assert.Equal(t, "NexusMods", n.Source)
	assert.Equal(t, source.RetryRateLimited, n.Reason)
	assert.Equal(t, clock.waits[0], n.Wait)
}

func TestNexusThrottle_AGraphQLRequestIsRetriedToo(t *testing.T) {
	var calls int
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"mods":{"nodes":[{"modId":5,"name":"Five"}]}}}`))
	}))
	defer srv.Close()
	nm, clock := newThrottleSource(t, srv)
	ctx, notices := collectNotices()

	got, err := nm.Search(ctx, source.SearchQuery{GameID: "cyberpunk2077", Query: "five"})
	require.NoError(t, err)
	require.Len(t, got.Mods, 1)
	assert.Equal(t, 2, calls)
	require.Len(t, bodies, 2)
	assert.Equal(t, bodies[0], bodies[1], "the query is sent again, whole")
	assert.Len(t, clock.waits, 1)
	require.Len(t, *notices, 1)
	assert.Equal(t, source.RetryServerError, (*notices)[0].Reason)
}

func TestNexusThrottle_AThrottleThatOutlastsTheRetriesReadsAsARateLimit(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(cloudflare1015))
	}))
	defer srv.Close()
	nm, _ := newThrottleSource(t, srv)

	_, err := nm.GetModFiles(context.Background(), &domain.Mod{ID: "1", GameID: "cyberpunk2077"})
	require.Error(t, err)
	assert.Equal(t, 3, calls)
	assert.Equal(t, "getting mod files: NexusMods rate limit: try again in 30s (still limited after 3 attempts)", err.Error(),
		"one 'getting mod files', no request path and no raw JSON")
	var se *httpclient.StatusError
	require.ErrorAs(t, err, &se)
	assert.True(t, se.RateLimited())
	assert.False(t, errors.Is(err, domain.ErrModNotFound))
}

func TestNexusThrottle_AUsedUpQuotaHoldsTheHostUntilItsReset(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("X-RL-Daily-Remaining", "0")
		w.Header().Set("X-RL-Hourly-Remaining", "0")
		w.Header().Set("X-RL-Hourly-Reset", "2026-11-22T12:40:00+00:00")
		_, _ = w.Write([]byte(filesJSON))
	}))
	defer srv.Close()
	nm, _ := newThrottleSource(t, srv)
	ctx, notices := collectNotices()

	_, err := nm.GetModFiles(ctx, &domain.Mod{ID: "1", GameID: "cyberpunk2077"})
	require.NoError(t, err, "the answer that spent the quota is still an answer")

	_, err = nm.GetModFiles(ctx, &domain.Mod{ID: "2", GameID: "cyberpunk2077"})
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, 1, calls, "nothing is sent while the quota is spent")
	assert.Equal(t, time.Date(2026, 11, 22, 12, 40, 0, 0, time.UTC), later.Until.UTC())
	assert.Contains(t, later.Reason, "quota")
	require.NotEmpty(t, *notices)
	assert.Equal(t, source.NoticeSuspended, (*notices)[len(*notices)-1].Kind)

	holds := nm.Holds(context.Background())
	require.Len(t, holds, 1, "the hold is reported without asking the host")
	assert.Equal(t, "NexusMods", holds[0].Source)
}

func TestNexusThrottle_AQuotaWithBudgetLeftHoldsNothing(t *testing.T) {
	for _, h := range []map[string]string{
		{"X-RL-Daily-Remaining": "0", "X-RL-Hourly-Remaining": "12"},
		{"X-RL-Daily-Remaining": "900", "X-RL-Hourly-Remaining": "0"},
		{},
	} {
		until, _ := quotaHold(&http.Response{Header: headerOf(h)}, time.Now())
		assert.True(t, until.IsZero(), "%v", h)
	}
}

func TestNexusThrottle_AQuotaResetThatCannotBeReadWaitsForTheNextHour(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 17, 3, 0, time.UTC)
	resp := &http.Response{Header: headerOf(map[string]string{
		"X-RL-Daily-Remaining": "0", "X-RL-Hourly-Remaining": "0", "X-RL-Hourly-Reset": "soon",
	})}
	until, why := quotaHold(resp, now)
	assert.Equal(t, time.Date(2026, 11, 22, 13, 0, 0, 0, time.UTC), until, "the hourly budget comes back on the hour")
	assert.NotEmpty(t, why)
}

func headerOf(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

// setRetryClock puts the client's retries, and the holds they keep, on the
// given clock and sleep.
func (c *Client) setRetryClock(now func() time.Time, sleep func(context.Context, time.Duration) error) {
	c.holds = httpclient.NewMemoryHolds(serviceName, now, breakerThreshold, breakerCooldown, maxHold)
	c.retry.Holds = c.holds
	c.retry.Now = now
	c.retry.Sleep = sleep
}
