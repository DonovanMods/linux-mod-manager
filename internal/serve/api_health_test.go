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
// The "want" call uses VerifyFull, matching handleAPIHealth's own tier
// (task-5 gate review Important 1) - twinConflictFixture's mods carry no
// FileIDs, so the version pass is a no-op either way and this test alone
// cannot distinguish the two tiers; TestServer_APIHealth_MatchesCLIVerifyTier
// below is the one that does.
func TestServer_APIHealth_ReturnsExactVerifyReport(t *testing.T) {
	svc, game := twinConflictFixture(t)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.VerifyReport
	decodeStrict(t, rec.Body.Bytes(), &report)

	want, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIHealth_MatchesCLIVerifyTier is the task-5 gate review's
// Important 1 RED test: it reproduces the review's live repro directly
// (API "issues": 0 vs `lmm verify --json`'s "issues": 1, a version_mismatch
// the offline VerifyLocal tier cannot see). A source-backed mod is
// installed recording version 1.0 while its matched file now reports 2.0 -
// exactly the BetterBoots 1.0-recorded/2.0-effective scenario from the
// review. Before the fix (handleAPIHealth pinned to VerifyLocal) this is
// RED: the API answers 0 issues while the CLI's VerifyFull tier would
// report 1. After the fix both agree.
func TestServer_APIHealth_MatchesCLIVerifyTier(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "2.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake", "boots", "1.0", "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
		},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var report core.VerifyReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	require.Equal(t, 1, report.Result.Issues, "API must report the same issue count `lmm verify --json` (VerifyFull) reports on this state")

	var mismatch *core.VerifyFinding
	for i := range report.Result.Findings {
		if report.Result.Findings[i].Status == "version_mismatch" {
			mismatch = &report.Result.Findings[i]
		}
	}
	require.NotNil(t, mismatch, "expected a version_mismatch finding - the CLI's version pass would find it too")
	assert.Equal(t, "1.0", mismatch.Recorded)
	assert.Equal(t, "2.0", mismatch.Effective)

	want, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_HealthSurfaces_APIAndCLIAgreeOnCounts is Important 2 (epic live
// review): on the exact same state as TestServer_APIHealth_MatchesCLIVerifyTier
// (a version_mismatch a VerifyLocal tier cannot see), GET /api/v1/health and
// a direct core.VerifyReport(VerifyFull) call - the CLI's own tier - must
// report the SAME issue count. Before the C1 fix this was a THREE-leg test
// whose first leg was the /health PAGE, pinned to VerifyLocal: the page said
// "0 issue(s)" while the API and the CLI-equivalent call both said "1". The
// page went with the server-rendered layer
// (docs/plans/2026-08-31-serve-spa-design.md); the third leg it stood for -
// that the REPAIR's own tier agrees too, which is the plan/apply mismatch
// kind_verify_fix.go's doc comment warns can resurrect the corruption - is
// carried by TestFlowVerifyFixPlan_TierMatchesTheAPIAndTheCLI
// (c1_verify_fix_tier_internal_test.go), which can reach the CSRF token a
// POST needs.
func TestServer_HealthSurfaces_APIAndCLIAgreeOnCounts(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "2.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake", "boots", "1.0", "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
		},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	apiReq := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/health", nil)
	apiRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(apiRec, apiReq)
	require.Equal(t, http.StatusOK, apiRec.Code)
	var apiReport core.VerifyReport
	decodeStrict(t, apiRec.Body.Bytes(), &apiReport)

	cliEquivalent, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	require.Equal(t, 1, apiReport.Result.Issues, "the API must see the version_mismatch, not report a clean sheet")
	assert.Equal(t, apiReport.Result.Issues, cliEquivalent.Result.Issues, "API and the CLI's own tier must agree")
}

// TestServer_APIHealth_NoGames_Renders404 mirrors the other scoped
// endpoints' unresolved-selection behaviour.
func TestServer_APIHealth_NoGames_Renders404(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
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
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/conflicts", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}
