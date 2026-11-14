package serve_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

// TestServer_APIStatus_GameParam_CarriesProfileDrift is #641: the game
// status the SPA polls carries the active profile's drift summary - here a
// hand-edited version - and, once nothing differs, no drift member at all.
func TestServer_APIStatus_GameParam_CarriesProfileDrift(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	ctx := context.Background()
	seedInstalledMod(t, svc, game, domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha", Version: "1.0", GameID: game.ID}, true, map[string][]byte{"a.esp": []byte("a")})
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	get := func() (core.GameStatus, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/status?game="+game.ID, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var status core.GameStatus
		decodeStrict(t, rec.Body.Bytes(), &status)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		return status, raw
	}

	_, raw := get()
	assert.NotContains(t, raw, "profile_drift", "a profile that matches its installs has no drift member")

	require.NoError(t, pm.UpsertMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "a", Version: "2.0"}))
	status, _ := get()
	require.NotNil(t, status.ProfileDrift)
	assert.Equal(t, core.ProfileDrift{Profile: "default", Version: 1}, *status.ProfileDrift)
}
