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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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

// TestServer_APIStatus_GameParam_ReturnsGameStatus is the task-5 gate
// review's Minor 6 fix: an explicit ?game= must switch /api/v1/status to
// exactly the core.GameStatus document `lmm status --game <id> --json`
// emits - previously ?game was silently ignored and the aggregate
// StatusReport answered regardless.
func TestServer_APIStatus_GameParam_ReturnsGameStatus(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/status?game="+game.ID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var status core.GameStatus
	decodeStrict(t, rec.Body.Bytes(), &status)
	assert.Equal(t, game.ID, status.ID)

	want, err := svc.GameStatus(context.Background(), game)
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIStatus_UnknownGameParam_Renders404 proves an unresolvable
// ?game= answers the same {"error","details"} envelope every other scoped
// endpoint uses, with details listing the real, configured game(s).
func TestServer_APIStatus_UnknownGameParam_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/status?game=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)

	var envelope struct {
		Error   string `json:"error"`
		Details struct {
			Games []core.GameListEntry `json:"games"`
		} `json:"details"`
	}
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "nope")
	require.Len(t, envelope.Details.Games, 1)
	assert.Equal(t, "g1", envelope.Details.Games[0].ID)
}
