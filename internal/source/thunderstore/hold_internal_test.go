package thunderstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A hold is persisted beside the indexes (T3 review F3), and scoped to the
// host or to one community (F9). Each Source built here over the same cache
// directory stands in for a separate lmm process: they share nothing but
// the disk.

// holdClock is a settable clock shared by every "process" in a test.
type holdClock struct {
	mu sync.Mutex
	t  time.Time
}

func newHoldClock() *holdClock {
	return &holdClock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *holdClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *holdClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// holdProcess is one lmm process over cacheDir: its retries are not slept.
func holdProcess(t *testing.T, cacheDir, baseURL string, clock *holdClock) *Source {
	t.Helper()
	src := New(Options{CacheDir: cacheDir, BaseURL: baseURL, Now: clock.now})
	src.client.retry.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return src
}

// countingHandler counts requests per community and answers each with
// answer(community).
type countingHandler struct {
	mu     sync.Mutex
	calls  map[string]int
	answer func(w http.ResponseWriter, community string)
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	community := communityOfPath(r.URL.Path)
	h.mu.Lock()
	if h.calls == nil {
		h.calls = map[string]int{}
	}
	h.calls[community]++
	h.mu.Unlock()
	h.answer(w, community)
}

func (h *countingHandler) count(community string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[community]
}

func search(src *Source, ctx context.Context, community string) error {
	_, err := src.Search(ctx, source.SearchQuery{GameID: community, Query: "ship"})
	return err
}

// TestHold_AServerNamedWaitOutlivesTheProcess is F3: a host that asked for
// ten minutes is not asked again by the NEXT lmm process either, and that
// process says until when.
func TestHold_AServerNamedWaitOutlivesTheProcess(t *testing.T) {
	h := &countingHandler{answer: func(w http.ResponseWriter, _ string) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	cacheDir, clock := t.TempDir(), newHoldClock()

	err := search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity)
	var first *source.RetryLaterError
	require.ErrorAs(t, err, &first)
	assert.Equal(t, 1, h.count(noticeCommunity))

	clock.advance(10 * time.Second)
	rec := &noticeRecorder{}
	err = search(holdProcess(t, cacheDir, srv.URL, clock), rec.ctx(t.Context()), noticeCommunity)
	var second *source.RetryLaterError
	require.ErrorAs(t, err, &second, "a new process honours the hold")
	assert.Equal(t, 1, h.count(noticeCommunity), "and sends nothing")
	assert.Equal(t, first.Until, second.Until)
	assert.Equal(t, clock.now().Add(590*time.Second), second.Until)
	assert.Contains(t, second.Reason, "HTTP 429")
	notices := rec.all()
	require.Len(t, notices, 1)
	assert.Equal(t, source.NoticeSuspended, notices[0].Kind)
	assert.Equal(t, second.Until, notices[0].Until, "the notice shows the persisted wait")

	clock.advance(591 * time.Second)
	_ = search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity)
	assert.Equal(t, 2, h.count(noticeCommunity), "past the wait a new process asks again")
}

// TestHold_ABreakerStreakAddsUpAcrossProcesses: three processes that each
// fail once are three failures in a row, and the fourth does not ask.
func TestHold_ABreakerStreakAddsUpAcrossProcesses(t *testing.T) {
	h := &countingHandler{answer: func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusServiceUnavailable) }}
	srv := httptest.NewServer(h)
	defer srv.Close()
	cacheDir, clock := t.TempDir(), newHoldClock()

	for range breakerThreshold {
		require.Error(t, search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity))
		clock.advance(time.Second)
	}
	sent := h.count(noticeCommunity)
	require.Equal(t, breakerThreshold*maxAttempts, sent)

	err := search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Equal(t, sent, h.count(noticeCommunity), "a tripped breaker holds a new process too")
	assert.Empty(t, later.GameID, "a server error holds the host")
}

// TestHold_ConcurrentProcessesKeepEveryFailure: changes are serialised
// across processes, so none is lost to a read-modify-write race.
func TestHold_ConcurrentProcessesKeepEveryFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), rootDirName)
	clock := newHoldClock()
	const writers = 24
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			newHoldStore(root, clock.now).failed(noticeCommunity, "document is not a package list")
		})
	}
	wg.Wait()

	state := readHoldFile(filepath.Join(root, holdsFileName), clock.now())
	assert.Equal(t, writers, state.Communities[noticeCommunity].Failures)
	assert.True(t, state.Communities[noticeCommunity].activeAt(clock.now()))
}

// TestHold_ADamagedFileHoldsNothing: the file is advisory, and one lmm
// could not have written is not obeyed - a damaged file must not lock lmm
// out of Thunderstore.
func TestHold_ADamagedFileHoldsNothing(t *testing.T) {
	clock := newHoldClock()
	far := clock.now().Add(10 * 365 * 24 * time.Hour).Format(time.RFC3339)
	soon := clock.now().Add(time.Hour).Format(time.RFC3339)
	for name, content := range map[string]string{
		"garbage":        "{not json",
		"wrong schema":   `{"schema":99,"host":{"until":"` + soon + `","reason":"x"}}`,
		"a decade ahead": `{"schema":1,"host":{"until":"` + far + `","reason":"x"}}`,
		"bad community":  `{"schema":1,"communities":{"../etc":{"until":"` + soon + `","reason":"x"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), rootDirName)
			require.NoError(t, os.MkdirAll(root, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, holdsFileName), []byte(content), 0o600))
			holds := newHoldStore(root, clock.now)
			_, _, held := holds.active("")
			assert.False(t, held)
			assert.Empty(t, holds.list())
		})
	}

	t.Run("a hold within a day is obeyed", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), rootDirName)
		require.NoError(t, os.MkdirAll(root, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, holdsFileName),
			[]byte(`{"schema":1,"host":{"until":"`+soon+`","reason":"x","named":true}}`), 0o600))
		_, _, held := newHoldStore(root, clock.now).active("")
		assert.True(t, held)
	})
}

// TestHold_AHoldIsNeverSetPastADay: whatever a host names, lmm holds off at
// most maxHold (review F8).
func TestHold_AHoldIsNeverSetPastADay(t *testing.T) {
	clock := newHoldClock()
	holds := newHoldStore(filepath.Join(t.TempDir(), rootDirName), clock.now)
	got := holds.named("", clock.now().Add(300*24*time.Hour), "asked for a year")
	assert.Equal(t, clock.now().Add(maxHold), got.Until)
}

// TestHold_ASuccessDoesNotLiftAServerNamedWait is review F9's second half:
// an answer to a request already in flight when the host asked for ten
// minutes says nothing about those ten minutes. A breaker lmm tripped
// itself is lifted by one.
func TestHold_ASuccessDoesNotLiftAServerNamedWait(t *testing.T) {
	clock := newHoldClock()
	holds := newHoldStore(filepath.Join(t.TempDir(), rootDirName), clock.now)

	holds.named("", clock.now().Add(10*time.Minute), "asked for ten minutes")
	holds.succeeded("")
	_, _, held := holds.active("")
	assert.True(t, held, "a named wait stands")

	other := newHoldStore(filepath.Join(t.TempDir(), rootDirName), clock.now)
	for range breakerThreshold {
		other.failed("", "HTTP 503")
	}
	_, _, held = other.active("")
	require.True(t, held)
	other.succeeded("")
	_, _, held = other.active("")
	assert.False(t, held, "a breaker lmm tripped is lifted by a success")
}

// communityServer answers the good community with the fixture and the
// broken one with broken.
func communityServer(t *testing.T, broken func(w http.ResponseWriter)) *countingHandler {
	body := fixtureBytes(t)
	return &countingHandler{answer: func(w http.ResponseWriter, community string) {
		if community == "broken-community" {
			broken(w)
			return
		}
		_, _ = w.Write(body)
	}}
}

// TestHold_ACommunitysOwnFailureHoldsOnlyThatCommunity is F9: a 404, or a
// document that is not a package list, belongs to one community, and the
// next game's search still reaches the host.
func TestHold_ACommunitysOwnFailureHoldsOnlyThatCommunity(t *testing.T) {
	for name, broken := range map[string]func(http.ResponseWriter){
		"not found":        func(w http.ResponseWriter) { http.NotFound(w, nil) },
		"not a list":       func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"detail":"nope"}`)) },
		"does not parse":   func(w http.ResponseWriter) { _, _ = w.Write([]byte(`[{"name": oops}]`)) },
		"wrong field type": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`[{"name": 7}]`)) },
	} {
		t.Run(name, func(t *testing.T) {
			h := communityServer(t, broken)
			srv := httptest.NewServer(h)
			defer srv.Close()
			cacheDir, clock := t.TempDir(), newHoldClock()

			for range breakerThreshold {
				err := search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), "broken-community")
				require.Error(t, err)
				require.False(t, errors.As(err, new(*source.RetryLaterError)), "not held yet: %v", err)
			}
			sent := h.count("broken-community")

			rec := &noticeRecorder{}
			err := search(holdProcess(t, cacheDir, srv.URL, clock), rec.ctx(t.Context()), "broken-community")
			var later *source.RetryLaterError
			require.ErrorAs(t, err, &later)
			assert.Equal(t, "broken-community", later.GameID)
			assert.Equal(t, sent, h.count("broken-community"), "the held community is not asked")
			assert.Contains(t, err.Error(), "not asking Thunderstore about broken-community again until")
			require.Len(t, rec.all(), 1)
			assert.Equal(t, "broken-community", rec.all()[0].GameID)

			require.NoError(t, search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity),
				"another community is still asked")
			assert.Equal(t, 1, h.count(noticeCommunity))
		})
	}
}

// TestHold_AServerErrorHoldsTheHostAndSaysWhichIndexFailed is F9's other
// side: a 5xx is the shared host's, and the hold another game then meets
// names the index whose fetch tripped it.
func TestHold_AServerErrorHoldsTheHostAndSaysWhichIndexFailed(t *testing.T) {
	h := communityServer(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) })
	srv := httptest.NewServer(h)
	defer srv.Close()
	cacheDir, clock := t.TempDir(), newHoldClock()

	for range breakerThreshold {
		require.Error(t, search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), "broken-community"))
	}
	err := search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	assert.Empty(t, later.GameID)
	assert.Zero(t, h.count(noticeCommunity), "the host is held for every community")
	assert.Contains(t, later.Reason, "broken-community", "the hold names the index that tripped it")
	assert.Contains(t, later.Reason, "HTTP 503")
}

// TestHold_ATransferThatStopsIsNotTheCommunitys: a dropped or stalled body
// says nothing about the community's document, so it holds nothing there.
func TestHold_ATransferThatStopsIsNotTheCommunitys(t *testing.T) {
	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte(`[{"name":"ShipLoot","full_name":"tinyhoot-Ship`))
		// then the connection closes short of its declared length
	}))
	defer srv.Close()
	cacheDir, clock := t.TempDir(), newHoldClock()

	for range breakerThreshold + 1 {
		err := search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity)
		require.Error(t, err)
		require.False(t, errors.As(err, new(*source.RetryLaterError)), "a short body holds nothing: %v", err)
	}
	assert.Equal(t, int64(breakerThreshold+1), served.Load())
	state := readHoldFile(filepath.Join(cacheDir, rootDirName, holdsFileName), clock.now())
	assert.Zero(t, state.Communities[noticeCommunity].Failures)
}

// TestHold_ACommunitySuccessClearsItsStreak: failures count in a row.
func TestHold_ACommunitySuccessClearsItsStreak(t *testing.T) {
	var fail atomic.Bool
	body := fixtureBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.NotFound(w, nil)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	cacheDir, clock := t.TempDir(), newHoldClock()

	fail.Store(true)
	for range breakerThreshold - 1 {
		require.Error(t, search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity))
	}
	fail.Store(false)
	require.NoError(t, search(holdProcess(t, cacheDir, srv.URL, clock), t.Context(), noticeCommunity))

	state := readHoldFile(filepath.Join(cacheDir, rootDirName, holdsFileName), clock.now())
	assert.Zero(t, state.Communities[noticeCommunity].Failures, "a good answer ends the streak")
}

// TestHold_AListingNamesEveryHoldInForce is what the web UI's setup card
// reads (review F10).
func TestHold_AListingNamesEveryHoldInForce(t *testing.T) {
	clock := newHoldClock()
	src := New(Options{CacheDir: t.TempDir(), BaseURL: "http://127.0.0.1:9", Now: clock.now})
	src.holds.named("", clock.now().Add(90*time.Second), "rate limited")
	for range breakerThreshold {
		src.holds.failed("broken-community", "not found")
	}
	holds := src.Holds(t.Context())
	require.Len(t, holds, 2)
	assert.Equal(t, source.Hold{Source: "Thunderstore", Until: clock.now().Add(90 * time.Second), Reason: "rate limited"}, holds[0])
	assert.Equal(t, "broken-community", holds[1].GameID)
	assert.True(t, strings.HasPrefix(holds[1].Reason, fmt.Sprintf("suspended after %d failed requests in a row", breakerThreshold)), holds[1].Reason)

	clock.advance(breakerCooldown + time.Second)
	assert.Empty(t, src.Holds(t.Context()), "an expired hold is not listed")
}

// TestHold_AHeldColdBuildLeavesNothingOnDisk: a refusal sends nothing and
// writes nothing - it used to create the community's directory (and its
// lock file) on the way to refusing, which then listed as an index.
func TestHold_AHeldColdBuildLeavesNothingOnDisk(t *testing.T) {
	cacheDir, clock := t.TempDir(), newHoldClock()
	src := holdProcess(t, cacheDir, "http://127.0.0.1:9", clock)
	src.holds.named("", clock.now().Add(10*time.Minute), "rate limited")

	var later *source.RetryLaterError
	require.ErrorAs(t, search(src, t.Context(), noticeCommunity), &later)
	assert.NoDirExists(t, filepath.Join(cacheDir, rootDirName, noticeCommunity))
}
