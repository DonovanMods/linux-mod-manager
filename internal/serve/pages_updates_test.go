package serve_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_Updates_RendersAvailableUpdate is /updates's headline RED test
// (docs/plans/2026-08-30-serve-impl.md Task 4, #74): an installed mod whose
// source reports a newer version must render its current/new versions, a
// per-row selection checkbox, the CSRF token, and the batch-apply form's
// target route Task 9 (#322) will wire.
func TestServer_Updates_RendersAvailableUpdate(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "2.0"}})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID}, true, nil)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/updates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Better Boots")
	assert.Contains(t, body, "1.0")
	assert.Contains(t, body, "2.0")
	assert.Contains(t, body, `value="fake:1"`)
	assert.Contains(t, body, `action="/updates/apply"`)
	assert.Contains(t, body, "coming in this release")
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_Updates_UpToDate_RendersEmptyState covers the CSS/JS-absent,
// no-data case: nothing to update must still render a normal 200 page.
func TestServer_Updates_UpToDate_RendersEmptyState(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID}, true, nil)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/updates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No updates available")
}
