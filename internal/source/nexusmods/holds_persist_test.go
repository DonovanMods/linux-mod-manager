package nexusmods

// #675: NexusMods' holds outlive the process. Each "process" here is a new
// source over the same data dir and the same fake clock - which is all a
// second `lmm` command shares with the first.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// persistServer counts what reaches it; spend makes its answers report a
// used-up quota, throttle makes them a ten-minute 429.
type persistServer struct {
	*httptest.Server
	mu       sync.Mutex
	calls    int
	spend    bool
	throttle bool
}

func newPersistServer(t *testing.T) *persistServer {
	t.Helper()
	s := &persistServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.calls++
		spend, throttle := s.spend, s.throttle
		s.mu.Unlock()
		if throttle {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if spend {
			w.Header().Set("X-RL-Daily-Remaining", "0")
			w.Header().Set("X-RL-Hourly-Remaining", "0")
			w.Header().Set("X-RL-Hourly-Reset", "2026-11-22T12:40:00+00:00")
		}
		_, _ = w.Write([]byte(filesJSON))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *persistServer) set(spend, throttle bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spend, s.throttle = spend, throttle
}

func (s *persistServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newProcess is one lmm process's NexusMods source over srv, keeping its
// holds under dataDir, on clock.
func newProcess(t *testing.T, srv *persistServer, dataDir, key string, clock *throttleClock) *NexusMods {
	t.Helper()
	nm := New(srv.Client(), key, WithDataDir(dataDir))
	nm.client.SetBaseURL(srv.URL)
	nm.client.graphqlURL = srv.URL + "/v2/graphql"
	nm.client.setRetryClock(clock.now, clock.sleep)
	return nm
}

func newPersistClock() *throttleClock {
	return &throttleClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
}

var persistMod = &domain.Mod{ID: "1", GameID: "cyberpunk2077"}

func TestNexusHolds_AThrottleHoldOneProcessRecordsHoldsTheNext(t *testing.T) {
	srv, dataDir, clock := newPersistServer(t), t.TempDir(), newPersistClock()
	srv.set(false, true)

	_, err := newProcess(t, srv, dataDir, "free-key", clock).GetModFiles(context.Background(), persistMod)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later)
	sent := srv.count()

	// The next command starts inside the hold: it says so up front, through
	// the #436 notice, and sends nothing.
	next := newProcess(t, srv, dataDir, "free-key", clock)
	holds := next.Holds(context.Background())
	require.Len(t, holds, 1, "a new process reports the hold before asking anything")
	assert.Equal(t, clock.now().Add(10*time.Minute), holds[0].Until)

	ctx, notices := collectNotices()
	_, err = next.GetModFiles(ctx, persistMod)
	require.ErrorAs(t, err, &later)
	assert.Equal(t, clock.now().Add(10*time.Minute), later.Until)
	assert.Equal(t, sent, srv.count(), "the held host is not asked by the next process")
	require.Len(t, *notices, 1)
	assert.Equal(t, source.NoticeSuspended, (*notices)[0].Kind)
	assert.Equal(t, "NexusMods", (*notices)[0].Source)
	assert.Equal(t, later.Until, (*notices)[0].Until)

	// A different key changes nothing: Cloudflare's wait is the address's.
	_, err = newProcess(t, srv, dataDir, "premium-key", clock).GetModFiles(context.Background(), persistMod)
	require.ErrorAs(t, err, &later)
	assert.Equal(t, sent, srv.count())
}

func TestNexusHolds_AShortHoldIsWaitedOutByTheNextProcess(t *testing.T) {
	srv, dataDir, clock := newPersistServer(t), t.TempDir(), newPersistClock()
	first := newProcess(t, srv, dataDir, "free-key", clock)
	first.client.holds.Named(nil, clock.now().Add(30*time.Second), "Cloudflare asked for thirty seconds")

	ctx, notices := collectNotices()
	_, err := newProcess(t, srv, dataDir, "free-key", clock).GetModFiles(ctx, persistMod)
	require.NoError(t, err, "a hold within the wait-out window is slept through, not refused")
	require.NotEmpty(t, *notices)
	assert.Equal(t, source.NoticeSuspended, (*notices)[0].Kind, "and the wait is said up front")
	assert.Contains(t, clock.waits, 30*time.Second)
}

func TestNexusHolds_TheQuotaHoldIsTheKeys(t *testing.T) {
	srv, dataDir, clock := newPersistServer(t), t.TempDir(), newPersistClock()
	srv.set(true, false)
	_, err := newProcess(t, srv, dataDir, "free-key", clock).GetModFiles(context.Background(), persistMod)
	require.NoError(t, err, "the answer that spent the quota is still an answer")
	srv.set(false, false)
	sent := srv.count()

	sameKey := newProcess(t, srv, dataDir, "free-key", clock)
	_, err = sameKey.GetModFiles(context.Background(), persistMod)
	var later *source.RetryLaterError
	require.ErrorAs(t, err, &later, "the same key in a new process is still spent")
	assert.Equal(t, time.Date(2026, 11, 22, 12, 40, 0, 0, time.UTC), later.Until.UTC())
	assert.Contains(t, later.Reason, "quota")
	assert.Equal(t, sent, srv.count())
	require.Len(t, sameKey.Holds(context.Background()), 1)

	otherKey := newProcess(t, srv, dataDir, "premium-key", clock)
	assert.Empty(t, otherKey.Holds(context.Background()), "another key's quota is not this one's")
	_, err = otherKey.GetModFiles(context.Background(), persistMod)
	require.NoError(t, err, "a process on another key is not held")

	// A key change in a running process lifts the hold for it (#671's
	// review fix) ...
	sameKey.SetAPIKey("premium-key")
	assert.Empty(t, sameKey.Holds(context.Background()))
	_, err = sameKey.GetModFiles(context.Background(), persistMod)
	require.NoError(t, err)

	// ... and re-attaching the same key, as every process does on start,
	// does not: the persisted hold is still the key's.
	again := newProcess(t, srv, dataDir, "free-key", clock)
	again.SetAPIKey("free-key")
	_, err = again.GetModFiles(context.Background(), persistMod)
	require.ErrorAs(t, err, &later)
}

func TestNexusHolds_TheKeyNeverReachesTheFile(t *testing.T) {
	srv, dataDir, clock := newPersistServer(t), t.TempDir(), newPersistClock()
	const key = "s3cr3t-nexus-api-key-abcdef0123456789"
	srv.set(true, false)
	nm := newProcess(t, srv, dataDir, key, clock)
	_, err := nm.GetModFiles(context.Background(), persistMod)
	require.NoError(t, err)
	srv.set(false, true)
	_, err = nm.GetModFiles(context.Background(), persistMod)
	require.Error(t, err)

	data, err := os.ReadFile(filepath.Join(dataDir, holdsFileName))
	require.NoError(t, err)
	assert.Contains(t, string(data), "quota", "the quota hold was written")
	assert.NotContains(t, string(data), key)
	for i := 0; i+8 <= len(key); i++ {
		assert.NotContains(t, string(data), key[i:i+8], "no fragment of the key is written")
	}
	info, err := os.Stat(filepath.Join(dataDir, holdsFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestNexusHolds_AnExpiredHoldIsIgnoredAndCleanedUp(t *testing.T) {
	srv, dataDir, clock := newPersistServer(t), t.TempDir(), newPersistClock()
	srv.set(false, true)
	_, err := newProcess(t, srv, dataDir, "free-key", clock).GetModFiles(context.Background(), persistMod)
	require.Error(t, err)
	require.FileExists(t, filepath.Join(dataDir, holdsFileName))

	srv.set(false, false)
	clock.mu.Lock()
	clock.t = clock.t.Add(10*time.Minute + time.Second)
	clock.mu.Unlock()
	next := newProcess(t, srv, dataDir, "free-key", clock)
	assert.Empty(t, next.Holds(context.Background()), "an expired hold is not reported")
	_, err = next.GetModFiles(context.Background(), persistMod)
	require.NoError(t, err, "nor obeyed")
	assert.NoFileExists(t, filepath.Join(dataDir, holdsFileName), "and the file of expired holds is cleaned up")
}

func TestNexusHolds_ACorruptFileNeverBlocksRequests(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":   "{not json",
		"truncated": `{"schema":1,"host":{"until":"2026-11-22T12:30:00Z"`,
		"far ahead": `{"schema":1,"host":{"until":"2036-11-22T12:00:00Z","reason":"x","named":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, dataDir, clock := newPersistServer(t), t.TempDir(), newPersistClock()
			require.NoError(t, os.WriteFile(filepath.Join(dataDir, holdsFileName), []byte(content), 0o600))
			nm := newProcess(t, srv, dataDir, "free-key", clock)
			assert.Empty(t, nm.Holds(context.Background()))
			_, err := nm.GetModFiles(context.Background(), persistMod)
			require.NoError(t, err)
			assert.Equal(t, 1, srv.count())
		})
	}
}

func TestNexusHolds_WithNoDataDirTheHoldsLiveInMemory(t *testing.T) {
	srv, clock := newPersistServer(t), newPersistClock()
	srv.set(false, true)
	nm := New(srv.Client(), "free-key")
	nm.client.SetBaseURL(srv.URL)
	nm.client.setRetryClock(clock.now, clock.sleep)
	_, err := nm.GetModFiles(context.Background(), persistMod)
	require.Error(t, err)
	assert.Len(t, nm.Holds(context.Background()), 1)
	assert.Empty(t, nm.client.holds.store.Path())
}

func TestNexusHolds_KeyFingerprintIsStableAndOneWay(t *testing.T) {
	a, b := keyFingerprint("free-key"), keyFingerprint("premium-key")
	assert.Equal(t, a, keyFingerprint("free-key"))
	assert.NotEqual(t, a, b)
	assert.Regexp(t, `^[0-9a-f]{32}$`, a)
	assert.False(t, strings.Contains(a, "free"))
	assert.Regexp(t, `^[0-9a-f]{32}$`, keyFingerprint(""), "no key has a scope of its own too")
}

// TestNexusHolds_AQuotaHoldIsTheKeyThatSpentIt (#675 review): the quota a
// response reports spent is the key the request was sent with, even when
// the key changes while it is in flight.
func TestNexusHolds_AQuotaHoldIsTheKeyThatSpentIt(t *testing.T) {
	dataDir, clock := t.TempDir(), newPersistClock()
	var nm *NexusMods
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { nm.SetAPIKey("premium-key") }) // the user re-keys mid-request
		w.Header().Set("X-RL-Daily-Remaining", "0")
		w.Header().Set("X-RL-Hourly-Remaining", "0")
		w.Header().Set("X-RL-Hourly-Reset", "2026-11-22T12:40:00+00:00")
		_, _ = w.Write([]byte(filesJSON))
	}))
	defer srv.Close()
	nm = New(srv.Client(), "free-key", WithDataDir(dataDir))
	nm.client.SetBaseURL(srv.URL)
	nm.client.setRetryClock(clock.now, clock.sleep)

	_, err := nm.GetModFiles(context.Background(), persistMod)
	require.NoError(t, err)

	host, scopes := nm.client.holds.store.Snapshot()
	assert.False(t, host.ActiveAt(clock.now()))
	require.Len(t, scopes, 1)
	assert.Contains(t, scopes, keyFingerprint("free-key"), "the hold is the key that spent the quota")
	assert.NotContains(t, scopes, keyFingerprint("premium-key"))
}
