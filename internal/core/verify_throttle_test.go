package core_test

// #671: the owner's verify of a 353-mod NexusMods collection turned every
// throttled GetModFiles into a "could not check version" row with a raw
// Cloudflare JSON dump, because nothing retried the 429. These pin verify's
// half: a source throttled once and then answering reports the mod checked
// (and the wait as a notice, in the sentence both frontends show); a source
// still throttled after its retries says so readably; and a mod the
// profile's collection locks is not sent to its source at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// cloudflare1015Body is the 429 body api.nexusmods.com's edge answers a
// burst with: the wait is only in the JSON.
const cloudflare1015Body = `{"title":"Error 1015: You are being rate limited","status":429,"retryable":true,"retry_after":30,"zone":"api.nexusmods.com","cloudflare_error":true}`

// httpFilesSource is a mock source whose GetModFiles asks an HTTP server
// through the shared retrying transport (#671) - the path a real source's
// file list takes - with the transport's waits recorded, not slept.
type httpFilesSource struct {
	*mockSource
	baseURL string
	client  *http.Client
	mu      sync.Mutex
	waits   []time.Duration
}

func newHTTPFilesSource(t *testing.T, id string, handler http.HandlerFunc) *httpFilesSource {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := &httpFilesSource{mockSource: newMockSource(id), baseURL: srv.URL}
	policy := httpclient.RetryPolicy{
		Service: "NexusMods", MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: 30 * time.Second,
		MaxRetryAfter: 2 * time.Minute, MaxHold: time.Hour, WaitOutHold: 2 * time.Minute,
		BodyHint: httpclient.CloudflareRetryAfter,
	}
	s.client = &http.Client{Transport: &httpclient.RetryTransport{
		Base:   http.DefaultTransport,
		Policy: policy,
		Holds:  httpclient.NewMemoryHolds("NexusMods", nil, 3, 5*time.Minute, time.Hour),
		Sleep: func(ctx context.Context, d time.Duration) error {
			s.mu.Lock()
			s.waits = append(s.waits, d)
			s.mu.Unlock()
			return ctx.Err()
		},
	}}
	return s
}

func (s *httpFilesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/files/"+mod.ID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getting mod files: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var files []domain.DownloadableFile
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}
	return files, nil
}

// newVerifyThrottleGame is a game with one profile, "default".
func newVerifyThrottleGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err := svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	return svc, game
}

// versionFindings is result's per-mod (not per-file) rows by mod ID.
func versionFindings(result *core.VerifyResult) map[string]core.VerifyFinding {
	out := map[string]core.VerifyFinding{}
	for _, f := range result.Findings {
		if f.FileID == "" && f.ModID != "" {
			out[f.ModID] = f
		}
	}
	return out
}

func TestVerify_ASourceThrottledOnceThenAnsweringLeavesNoCouldNotCheckRow(t *testing.T) {
	svc, game := newVerifyThrottleGame(t)
	ctx := context.Background()
	var mu sync.Mutex
	calls := 0
	src := newHTTPFilesSource(t, "nexus", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(cloudflare1015Body))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"f1","version":"1.0"}]`))
	})
	svc.RegisterSource(src)
	seedVerifyMod(t, svc, game, "nexus", "m1", "Throttled Mod", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum(ctx, "nexus", "m1", game.ID, "default", "f1", "cs"))

	var mu2 sync.Mutex
	var warnings []core.WarningEvent
	noticed := core.WithSourceNotices(ctx, func(e core.Event) {
		if w, ok := e.(core.WarningEvent); ok {
			mu2.Lock()
			warnings = append(warnings, w)
			mu2.Unlock()
		}
	})
	result, err := svc.VerifyForTest(noticed, game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	for _, f := range result.Findings {
		assert.NotContains(t, f.Note, "could not check version", "the retry got the answer: %+v", f)
	}
	assert.Zero(t, result.Warnings)
	assert.Equal(t, 2, calls, "throttled once, then answered")
	require.Len(t, src.waits, 1)
	assert.GreaterOrEqual(t, src.waits[0], 30*time.Second)

	require.Len(t, warnings, 1, "the wait is said while it is waited")
	assert.Equal(t, core.SourceRetrying, warnings[0].Phase)
	// The wait actually slept: Cloudflare's 30s, plus at most a second of
	// jitter.
	assert.Regexp(t, `^Rate limited by NexusMods; retrying in 3[01]s \(attempt 2 of 3\)\.$`, warnings[0].Message)
}

func TestVerify_AThrottleThatOutlastsTheRetriesReadsAsARateLimit(t *testing.T) {
	svc, game := newVerifyThrottleGame(t)
	ctx := context.Background()
	until := time.Now().Add(10 * time.Minute)
	svc.RegisterSource(&scriptedVersionSource{
		mockSource: newMockSource("vsrc"),
		errByModID: map[string]error{
			"limited": fmt.Errorf("getting mod files: %w",
				&httpclient.StatusError{Service: "NexusMods", Status: http.StatusTooManyRequests, RetryIn: 30 * time.Second, Attempts: 3}),
			"held": fmt.Errorf("getting mod files: %w",
				&source.RetryLaterError{Source: "NexusMods", Until: until, Reason: "rate limited by NexusMods (HTTP 429), which asked lmm to wait 10m0s"}),
			"down": fmt.Errorf("getting mod files: %w",
				&httpclient.StatusError{Service: "NexusMods", Status: http.StatusBadGateway, Attempts: 3}),
		},
	})
	for _, id := range []string{"limited", "held", "down"} {
		seedVerifyMod(t, svc, game, "vsrc", id, id, "1.0", []string{"f1"}, true)
		require.NoError(t, svc.SaveFileChecksum(ctx, "vsrc", id, game.ID, "default", "f1", "cs-"+id))
	}

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	byMod := versionFindings(result)

	limited := byMod["limited"]
	assert.Equal(t, "skipped", limited.Status)
	assert.Equal(t, "could not check version: getting mod files: NexusMods rate limit: try again in 30s (still limited after 3 attempts)", limited.Note)
	assert.NotContains(t, limited.Note, "{", "no raw JSON dump")
	assert.Equal(t, "NexusMods rate limit: try again in 30s", limited.FixableReason)

	held := byMod["held"]
	assert.Equal(t, "skipped", held.Status)
	assert.Equal(t, "lmm is not asking NexusMods again until "+until.Local().Format("15:04:05"), held.FixableReason,
		"a hold may be a throttle's, a quota's or the breaker's: worded as lmm's own decision")

	assert.Empty(t, byMod["down"].FixableReason, "a server failure is not a rate limit")
}

// countingVersionSource records which mods verify asked about.
type countingVersionSource struct {
	*mockSource
	mu    sync.Mutex
	asked []string
}

func (s *countingVersionSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, mod.ID)
	return []domain.DownloadableFile{{ID: "f1", Version: "1.0"}}, nil
}

func TestVerify_ACollectionLockedModIsNotSentToItsSource(t *testing.T) {
	svc, game := newVerifyThrottleGame(t)
	ctx := context.Background()
	src := &countingVersionSource{mockSource: newMockSource("csrc")}
	svc.RegisterSource(src)
	for _, id := range []string{"coll1", "coll2", "handlocked", "free"} {
		seedVerifyMod(t, svc, game, "csrc", id, strings.ToUpper(id), "1.0", []string{"f1"}, true)
		require.NoError(t, svc.SaveFileChecksum(ctx, "csrc", id, game.ID, "default", "f1", "cs-"+id))
	}
	prof, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 3, Title: "Night City"}
	prof.Mods = nil
	for _, id := range []string{"coll1", "coll2", "handlocked", "free"} {
		ref := domain.ModReference{SourceID: "csrc", ModID: id, Version: "1.0", FileIDs: []string{"f1"}}
		switch id {
		case "coll1", "coll2":
			ref.Locked, ref.FromCollection = true, true
		case "handlocked":
			ref.Locked = true
		}
		prof.Mods = append(prof.Mods, ref)
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"handlocked", "free"}, src.asked,
		"the collection pins its mods' files with their hashes, and moves them itself: nothing to ask the source")
	assert.Equal(t, 2, result.CollectionLocked, "and the skip is counted, so a frontend can say so")
}

// TestVerify_ACollectionLockedModStillGetsItsFileFindingsAndRepair (#671
// review): leaving a collection's mods out of the version check leaves
// nothing else out. Their files are still checked against the recorded
// hashes - a missing one is a finding - and --fix still repairs it.
func TestVerify_ACollectionLockedModStillGetsItsFileFindingsAndRepair(t *testing.T) {
	svc, game, src := setupLockedMissingFileFixture(t, "1.0") // cache entry gone; the source still serves 1.0
	ctx := context.Background()
	pm := svc.NewProfileManager()
	prof, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	for i := range prof.Mods {
		if prof.Mods[i].ModID == "locked-mod" {
			prof.Mods[i].FromCollection = true
		}
	}
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 3, Title: "Night City"}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.CollectionLocked, "its version is left to the collection")
	f := findingFor(t, result, "locked-mod")
	assert.Equal(t, "missing", f.Status, "but its missing file is still a finding")
	assert.True(t, f.Fixable)
	assert.Zero(t, src.DownloadCount())

	result, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull, Fix: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, src.DownloadCount(), "and --fix fetches the pinned file again")
	assert.True(t, svc.GetGameCache(game).Exists(cache.Key{GameID: game.ID, SourceID: "lsrc", ModID: "locked-mod", Version: "1.0"}))
	assert.Equal(t, "ok", findingFor(t, result, "locked-mod").Status)
}
