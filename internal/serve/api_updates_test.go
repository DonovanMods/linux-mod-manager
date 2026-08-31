package serve_test

import (
	"context"
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

// TestServer_APIUpdates_ReturnsExactUpdateCheckReport is /api/v1/updates's
// headline RED test (docs/plans/2026-08-30-serve-impl.md Task 5): the body
// must decode into core.UpdateCheckReport with unknown members rejected AND
// byte-match core.EncodeJSON of the same document a bulk `lmm update --json`
// (no mod ID) would build - GameID/Profile/Updates/Skipped, ErrorMessage
// omitted on a clean check.
func TestServer_APIUpdates_ReturnsExactUpdateCheckReport(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "2.0"}})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID}, true, nil)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/updates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.UpdateCheckReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Len(t, report.Updates, 1)
	assert.Equal(t, "2.0", report.Updates[0].NewVersion)

	ctx := context.Background()
	installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	updates, err := svc.CheckGameUpdates(ctx, game, "default", installed, nil)
	require.NoError(t, err)
	want := &core.UpdateCheckReport{GameID: game.ID, Profile: "default", Updates: updates, Skipped: core.CountUpdateSkips(installed)}
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIUpdates_NoGames_Renders404 mirrors /api/v1/mods's own
// unresolved-selection test: an unready game/profile answers the envelope
// at 404, never a 200 empty document.
func TestServer_APIUpdates_NoGames_Renders404(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/updates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}

// TestServer_APIUpdates_InternalFailure_Renders500 is the task-5 gate
// review's Minor 2 fix: /api/v1/updates' own GetInstalledMods 500 branch
// (api.go's handleAPIUpdates) was untested - the package's only 500 case
// lived on /api/v1/mods. Same "closing the DB early forces the read to
// fail" pattern internal/core's own tests use.
func TestServer_APIUpdates_InternalFailure_Renders500(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.Close())

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/updates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}
