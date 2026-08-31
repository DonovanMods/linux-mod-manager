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

// TestServer_Health_RendersConflictsAndVerifySummary is /health's headline
// RED test (docs/plans/2026-08-30-serve-impl.md Task 4): two enabled mods
// that provide the same game-dir path, deployed for real, must render as a
// conflict row naming both mods, alongside a real (non-error) verify
// summary. Task 9 (#322) wired POST /health/fix and made its form
// conditional, so a report with nothing repairable - like this one - offers
// no repair action at all.
func TestServer_Health_RendersConflictsAndVerifySummary(t *testing.T) {
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

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "shared.esp")
	assert.Contains(t, body, "Mod X")
	assert.Contains(t, body, "Mod Y")
	assert.NotContains(t, body, `action="/health/fix"`, "nothing here is repairable, so no repair is offered")
	assert.NotContains(t, body, "coming in this release")
	// The repair form was this page's only form, so its CSRF token is
	// asserted where the form actually renders
	// (TestServer_Health_OffersRepairOnlyWhenSomethingIsFixable).
}

// TestServer_Health_NoGames_RendersEmptyState covers the CSS/JS-absent,
// no-data case.
func TestServer_Health_NoGames_RendersEmptyState(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No games configured")
}
