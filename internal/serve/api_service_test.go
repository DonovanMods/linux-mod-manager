package serve_test

// #649: GET /api/v1/service is `lmm service status --json`'s document for
// the web UI's Setup > Updates: which lmm binary lmm-serve.service runs,
// beside the one this server runs, and whether they differ.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServiceStatus answers Status with a fixed report or error.
type fakeServiceStatus struct {
	report core.ServiceReport
	err    error
}

func (f fakeServiceStatus) Status(context.Context) (core.ServiceReport, error) {
	return f.report, f.err
}

// mismatchReport is a unit left on a previous build's binary.
var mismatchReport = core.ServiceReport{
	Units: []core.UnitStatus{{
		Name: core.ServeUnitName, Installed: true, Origin: core.UnitOriginUser,
		Path:    "/home/u/.config/systemd/user/lmm-serve.service",
		Enabled: true, Active: true, EnabledState: "enabled", ActiveState: "active",
		Binary:         &core.LmmBinary{Path: "/home/u/go/1.27.3/bin/lmm", Version: "2.2.0"},
		BinaryMismatch: true,
	}},
	Running: &core.LmmBinary{Path: "/home/u/go/1.27.4/bin/lmm", Version: "2.3.0"},
}

func getService(t *testing.T, status serve.ServiceStatuser) *httptest.ResponseRecorder {
	t.Helper()
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ServiceStatus: status})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/service", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAPIService_ReportsTheUnitsBinary(t *testing.T) {
	rec := getService(t, fakeServiceStatus{report: mismatchReport})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got core.ServiceReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got, json.RejectUnknownMembers(true)))
	assert.Equal(t, mismatchReport, got)
}

func TestAPIService_NoSystemdIs422(t *testing.T) {
	rec := getService(t, fakeServiceStatus{err: &core.SystemdUnavailableError{Reason: "systemctl is not installed"}})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "systemctl is not installed")
}

func TestAPIService_FailureIs500(t *testing.T) {
	rec := getService(t, fakeServiceStatus{err: errors.New("boom")})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// A server built without one - every other test - asks no user manager
// and has nothing to report: 204, which the page shows as no row.
func TestAPIService_NoneConfiguredIs204(t *testing.T) {
	rec := getService(t, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

// countingServiceStatus counts its Status calls and, when gate is set,
// holds each until gate closes.
type countingServiceStatus struct {
	calls atomic.Int32
	gate  chan struct{}
}

func (c *countingServiceStatus) Status(ctx context.Context) (core.ServiceReport, error) {
	c.calls.Add(1)
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return core.ServiceReport{}, ctx.Err()
		}
	}
	return mismatchReport, nil
}

// Each status read runs several systemctl calls and up to two --version
// execs, so the answer is shared (#649 review): requests close together
// read one report, and requests at once wait on the one read under way.
func TestAPIService_SharesOneReadBetweenRequests(t *testing.T) {
	status := &countingServiceStatus{}
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ServiceStatus: status})
	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/service", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, get())
	require.Equal(t, http.StatusOK, get())
	assert.Equal(t, int32(1), status.calls.Load(), "the second request reads the cached report")

	held := &countingServiceStatus{gate: make(chan struct{})}
	srv = serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ServiceStatus: held})
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/service", nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			codes[i] = rec.Code
		})
	}
	require.Eventually(t, func() bool { return held.calls.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // the other requests join the read under way
	close(held.gate)
	wg.Wait()
	assert.Equal(t, int32(1), held.calls.Load(), "one read for requests at once")
	for _, c := range codes {
		assert.Equal(t, http.StatusOK, c)
	}
}

// A failed read is not kept: the next request asks again.
func TestAPIService_DoesNotCacheAFailure(t *testing.T) {
	status := &failingThenOK{}
	srv := serve.New(t.Context(), newFixtureService(t), slog.New(slog.DiscardHandler),
		serve.Options{Addr: testAddr, ServiceStatus: status})
	for _, want := range []int{http.StatusInternalServerError, http.StatusOK} {
		req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/service", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		assert.Equal(t, want, rec.Code)
	}
}

// failingThenOK fails its first Status call and answers every later one.
type failingThenOK struct{ calls atomic.Int32 }

func (f *failingThenOK) Status(context.Context) (core.ServiceReport, error) {
	if f.calls.Add(1) == 1 {
		return core.ServiceReport{}, errors.New("boom")
	}
	return mismatchReport, nil
}
