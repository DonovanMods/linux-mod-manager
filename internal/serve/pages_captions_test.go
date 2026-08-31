package serve_test

// Task 10 a11y pass (docs/plans/unit6-carry-list.md): mods.gohtml, job.gohtml,
// confirm.gohtml and result.gohtml already gave their tables an sr-only
// <caption> naming what the table lists, for a screen-reader user who
// navigates table-by-table rather than heading-by-heading. Task 4's other
// list pages (search/updates/profiles/health) didn't get the same
// treatment - these tests pin the fix, one small new test per page, none
// touching an existing assertion.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_Search_ResultsTable_HasCaption(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0"}})
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/search?q=boots", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `<caption class="sr-only">`)
}

func TestServer_Updates_Table_HasCaption(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{Mod: domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "2.0"}})
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID}, true, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/updates", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `<caption class="sr-only">`)
}

func TestServer_Profiles_Table_HasCaption(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "hardcore")
	require.NoError(t, err)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `<caption class="sr-only">`)
}

func TestServer_Health_Tables_HaveCaptions(t *testing.T) {
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
	// This fixture has no checksum mismatches, so the verify-findings table
	// itself doesn't render (a "no issues found" line takes its place) -
	// only the conflicts table does, and it must be captioned.
	assert.Contains(t, body, `<caption class="sr-only">File conflicts`)
	assert.Equal(t, 1, strings.Count(body, `<caption class="sr-only">`))
}
