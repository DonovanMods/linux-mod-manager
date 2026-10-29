package serve_test

// #568: GET /api/v1/updates/lmm answers core.ReleaseStatus from the
// checker's cache, and a running server refreshes that cache itself -
// which is the only way a server built WITHOUT a checker (every other test
// in this package) is guaranteed never to reach GitHub.

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
		Probe: &core.InstallProbe{
			Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
			LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
			Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		},
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
