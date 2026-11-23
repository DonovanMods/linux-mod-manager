package serve_test

// Issue 674 in the browser and over /api/v1: a collection imported from an
// https address is checked with a conditional GET, and the Updates card's
// collection line says a newer version is out, as a NexusMods
// collection's does (#673).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newURLRevisionService is a game whose active profile, "shared", was
// imported from an https address that answers with an ETag per version
// and a 304 for the current one - and that now serves version 2 - served
// at the returned base URL. It returns how many conditional requests the
// address saw.
func newURLRevisionService(t *testing.T) (*core.Service, *domain.Game, string, *atomic.Int32) {
	t.Helper()
	sandboxE2EEnv(t)
	var version atomic.Value
	version.Store("1")
	var conditional atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := version.Load().(string)
		etag := `"v` + v + `"`
		w.Header().Set("ETag", etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			conditional.Add(1)
			if inm == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		doc := e2eUpdateV1
		if v == "2" {
			doc = e2eUpdateV2
		}
		_, _ = io.WriteString(w, doc)
	}))
	t.Cleanup(srv.Close)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler), DownloadClient: srv.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := newFakeSource("fake")
	svc.RegisterSource(src)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{src.ID(): ""}}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	plan, err := svc.PlanCollectionImportURL(ctx, core.CollectionURLImportRequest{URL: srv.URL + "/shared.yaml", GameID: game.ID})
	require.NoError(t, err)
	_, err = svc.ApplyCollectionImport(ctx, game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, "shared"))
	version.Store("2")
	return svc, game, startE2EServer(t, svc), &conditional
}

// newE2EURLRevisionFixture is newURLRevisionService with a browser.
func newE2EURLRevisionFixture(t *testing.T) (e2eFixture, *atomic.Int32) {
	t.Helper()
	svc, game, baseURL, conditional := newURLRevisionService(t)
	browser, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: browser, BaseURL: baseURL, Svc: svc, Game: game, Profile: "shared", BrowserErrors: browserErrors}, conditional
}

func TestE2E_UpdatesCard_AURLCollectionsNewVersion(t *testing.T) {
	f, conditional := newE2EURLRevisionFixture(t)

	var note string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-collection-locked"][data-state="available"]`, chromedp.ByQuery),
		textContent(`.card--updates [data-testid="updates-collection-locked"]`, &note),
	)
	assert.Contains(t, note, "Collection Shared Setup: revision 2 available (installed 1).")
	assert.Contains(t, note, "Update collection…")
	assert.Positive(t, conditional.Load(), "asked with the recorded ETag")
	assert.Empty(t, f.BrowserErrors())
}

func TestAPIUpdates_AURLCollectionsRevision(t *testing.T) {
	_, _, baseURL, conditional := newURLRevisionService(t)

	resp, err := http.Get(baseURL + "/api/v1/updates?game=g1&profile=shared")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var report core.UpdateCheckReport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&report))
	assert.Equal(t, &core.CollectionRevisionStatus{
		Name: "Shared Setup", Origin: core.CollectionOriginURL, Installed: 1, Latest: 2,
		Available: true, Checked: true, Sync: true,
	}, report.CollectionRevision)
	assert.Equal(t, int32(1), conditional.Load(), "one conditional request")
}
