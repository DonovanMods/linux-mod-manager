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
	"testing"

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
