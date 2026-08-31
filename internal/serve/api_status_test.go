package serve_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_APIStatus_ReturnsExactStatusReport is /api/v1/status's
// headline RED test (docs/plans/2026-08-30-serve-impl.md Task 5): the body
// must decode into core.StatusReport with unknown members rejected AND
// byte-match core.EncodeJSON of the same live Status(ctx) call - the
// contract IS the test. Status is not game/profile-scoped (mirrors "/"'s
// own handler, which never calls resolveSelection either).
func TestServer_APIStatus_ReturnsExactStatusReport(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Header().Get("Cache-Control"))

	var report core.StatusReport
	decodeStrict(t, rec.Body.Bytes(), &report)

	want, err := svc.Status(context.Background())
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}
