package serve_test

// #568: GET /api/v1/updates/lmm answers core.ReleaseStatus from the
// checker's cache, and a running server refreshes that cache itself -
// which is the only way a server built WITHOUT a checker (every other test
// in this package) is guaranteed never to reach GitHub.

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReleaseFixture(t *testing.T) (*core.ReleaseChecker, *atomic.Int32) {
	t.Helper()
	return newReleaseFixtureWith(t, &core.InstallProbe{
		Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	})
}

func newReleaseFixtureWith(t *testing.T, probe *core.InstallProbe) (*core.ReleaseChecker, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v2.1.0","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0"}`))
	}))
	t.Cleanup(gh.Close)
	rc := core.NewReleaseChecker(core.ReleaseCheckOptions{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		Version:   "2.0.1",
		APIBase:   gh.URL,
		Probe:     probe,
	})
	return rc, &hits
}

func getReleaseStatus(t *testing.T, h http.Handler) core.ReleaseStatus {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/updates/lmm", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var st core.ReleaseStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st, json.RejectUnknownMembers(true)))
	return st
}

// TestAPIReleaseStatus_AnswersFromTheCacheWithoutARequest: the endpoint is
// a read of what the checker knows, never a request of its own.
func TestAPIReleaseStatus_AnswersFromTheCacheWithoutARequest(t *testing.T) {
	rc, hits := newReleaseFixture(t)
	_, err := rc.Check(t.Context(), true)
	require.NoError(t, err)
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ReleaseChecker: rc})

	st := getReleaseStatus(t, srv.Handler())
	assert.Equal(t, "2.1.0", st.Latest)
	assert.True(t, st.UpdateAvailable)
	assert.True(t, st.CheckEnabled)
	assert.Equal(t, int32(1), hits.Load(), "only the test's own forced check asked")
}

// TestAPIReleaseStatus_NoCheckerIsAnOffCheck: a server without a checker
// reports the check off rather than failing, so the SPA shows no banner.
func TestAPIReleaseStatus_NoCheckerIsAnOffCheck(t *testing.T) {
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, Version: "2.0.1"})
	st := getReleaseStatus(t, srv.Handler())
	assert.Equal(t, core.ReleaseStatus{Current: "2.0.1"}, st)
}

// TestServer_ServeRefreshesTheReleaseCache: a running server checks on
// start (within the checker's own 24-hour rule), so a web-only user hears
// about a release without ever running `lmm version --check`.
func TestServer_ServeRefreshesTheReleaseCache(t *testing.T) {
	rc, hits := newReleaseFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	srv := serve.New(ctx, newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: "127.0.0.1:0", ReleaseChecker: rc})
	_, err := srv.Listen()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	require.Eventually(t, func() bool { return rc.Status(t.Context()).Latest == "2.1.0" },
		5*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	assert.Equal(t, int32(1), hits.Load())
}

// slowPackageManager is a probe whose owner queries take delay, then say
// pacman owns the binary - or give up when their context ends first.
func slowPackageManager(delay time.Duration) *core.InstallProbe {
	return &core.InstallProbe{
		Executable: func() (string, error) { return "/usr/bin/lmm", nil },
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run: func(ctx context.Context, name string, _ ...string) ([]byte, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			if name == "pacman" {
				return []byte("lmm\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

// TestAPIReleaseStatus_DoesNotWaitOnAPackageManager: a package database
// that is slow to answer cannot hold the endpoint - it answers with the
// release page (self-managed) until detection is done.
func TestAPIReleaseStatus_DoesNotWaitOnAPackageManager(t *testing.T) {
	rc, _ := newReleaseFixtureWith(t, slowPackageManager(30*time.Second))
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ReleaseChecker: rc})
	start := time.Now()
	st := getReleaseStatus(t, srv.Handler())
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.Equal(t, core.InstallMethod{Method: core.InstallSelfManaged}, st.Install)
}

// TestServer_ServeDetectsTheInstallMethodInTheBackground: a running server
// detects how lmm was installed at start, so the endpoint names the
// upgrade command once detection is done, without a request waiting on it.
func TestServer_ServeDetectsTheInstallMethodInTheBackground(t *testing.T) {
	rc, _ := newReleaseFixtureWith(t, slowPackageManager(400*time.Millisecond))
	ctx, cancel := context.WithCancel(t.Context())
	srv := serve.New(ctx, newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: "127.0.0.1:0", ReleaseChecker: rc})
	_, err := srv.Listen()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	// Each read waits no longer than a request does, which is never long
	// enough for this probe: only the server's own detection can finish.
	require.Eventually(t, func() bool {
		rctx, rcancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer rcancel()
		return rc.Status(rctx).Install.Manager == "pacman"
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

// lockedBuffer is a bytes.Buffer safe to write from the server's goroutine
// while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServer_ServeSaysItChecksTheFirstTime: a web-only user is told, once
// per data dir, that lmm asks GitHub and how to stop it - the same notice
// the CLI prints - before the server's first check.
func TestServer_ServeSaysItChecksTheFirstTime(t *testing.T) {
	rc, hits := newReleaseFixture(t)
	run := func() string {
		var out lockedBuffer
		ctx, cancel := context.WithCancel(t.Context())
		srv := serve.New(ctx, newFixtureService(t), slog.New(slog.DiscardHandler),
			serve.Options{Addr: "127.0.0.1:0", ReleaseChecker: rc, Notices: &out})
		_, err := srv.Listen()
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- srv.Serve(ctx) }()
		require.Eventually(t, func() bool { return rc.Status(t.Context()).Latest == "2.1.0" },
			5*time.Second, 10*time.Millisecond)
		cancel()
		require.NoError(t, <-done)
		return out.String()
	}

	assert.Equal(t, rc.FirstRunNotice()+"\n", run())
	assert.False(t, rc.FirstRunNoticeDue())
	assert.Equal(t, int32(1), hits.Load())
	assert.Empty(t, run(), "once per data dir")
}

// replacedBinaryProbe is a probe whose executable is a real temp file
// that `<exe> --version` reports as 2.2.0, and a function that replaces
// the file - what `lmm self-update` does under a running `lmm serve`.
func replacedBinaryProbe(t *testing.T) (*core.InstallProbe, func()) {
	t.Helper()
	exe := t.TempDir() + "/lmm"
	require.NoError(t, os.WriteFile(exe, []byte("2.0.1"), 0o755))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(exe, old, old))
	probe := &core.InstallProbe{
		Executable: func() (string, error) { return exe, nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == exe && len(args) == 1 && args[0] == "--version" {
				return []byte("lmm version 2.2.0\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}
	replace := func() { require.NoError(t, os.WriteFile(exe, []byte("2.2.0"), 0o755)) }
	return probe, replace
}

// TestAPIReleaseStatus_ReportsANewerInstalledBinary (#569): once the
// binary a hand-started server runs from is replaced on disk by a newer
// lmm, the status says so, so the banner can ask for a restart.
func TestAPIReleaseStatus_ReportsANewerInstalledBinary(t *testing.T) {
	probe, replace := replacedBinaryProbe(t)
	rc, _ := newReleaseFixtureWith(t, probe)
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ReleaseChecker: rc})

	assert.Empty(t, getReleaseStatus(t, srv.Handler()).Installed, "the binary is the one the server started from")
	replace()
	assert.Equal(t, "2.2.0", getReleaseStatus(t, srv.Handler()).Installed)
}
