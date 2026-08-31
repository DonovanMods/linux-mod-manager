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

// twinConflictFixture seeds two enabled mods that provide the same
// game-dir path, deployed for real - the fixture pages_health_test.go's
// own TestServer_Health_RendersConflictsAndVerifySummary uses, reused here
// so /api/v1/health and /api/v1/conflicts exercise real, non-empty
// documents rather than trivial zero-value ones.
func twinConflictFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)

	seedInstalledMod(t, svc, game, domain.Mod{ID: "x", SourceID: "fake", Name: "Mod X", Version: "1.0", GameID: game.ID}, true,
		map[string][]byte{"shared.esp": []byte("X-content")})
	seedInstalledMod(t, svc, game, domain.Mod{ID: "y", SourceID: "fake", Name: "Mod Y", Version: "1.0", GameID: game.ID}, true,
		map[string][]byte{"shared.esp": []byte("Y-content")})

	ctx := context.Background()
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "x", Version: "1.0"}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "y", Version: "1.0"}))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	return svc, game
}

// TestServer_APIHealth_ReturnsExactVerifyReport is /api/v1/health's
// headline RED test (docs/plans/2026-08-30-serve-impl.md Task 5, per the
// coordinator's ruling on the design doc's route list): the body must
// decode into the bare core.VerifyReport document with unknown members
// rejected AND byte-match core.EncodeJSON of the same live VerifyReport
// call - exact `lmm verify --json` parity, no serve-local composite
// merging in the conflicts data (that lives at /api/v1/conflicts instead).
func TestServer_APIHealth_ReturnsExactVerifyReport(t *testing.T) {
	svc, game := twinConflictFixture(t)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.VerifyReport
	decodeStrict(t, rec.Body.Bytes(), &report)

	want, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIHealth_NoGames_Renders404 mirrors the other scoped
// endpoints' unresolved-selection behaviour.
func TestServer_APIHealth_NoGames_Renders404(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}

// TestServer_APIConflicts_ReturnsExactConflictReport is
// /api/v1/conflicts's headline test - the additive route the coordinator's
// ruling added in place of folding conflicts into /api/v1/health: the body
// must decode into core.ConflictReport with unknown members rejected AND
// byte-match core.EncodeJSON of the same live GetProfileConflicts call,
// naming both mods sharing the seeded path.
func TestServer_APIConflicts_ReturnsExactConflictReport(t *testing.T) {
	svc, game := twinConflictFixture(t)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/conflicts", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.ConflictReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Len(t, report.Conflicts, 1)
	assert.Equal(t, "shared.esp", report.Conflicts[0].Path)

	ctx := context.Background()
	conflicts, err := svc.GetProfileConflicts(ctx, game, "default")
	require.NoError(t, err)
	want := &core.ConflictReport{GameID: game.ID, Profile: "default", Conflicts: conflicts}
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIConflicts_NoGames_Renders404 mirrors the other scoped
// endpoints' unresolved-selection behaviour.
func TestServer_APIConflicts_NoGames_Renders404(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/conflicts", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}
