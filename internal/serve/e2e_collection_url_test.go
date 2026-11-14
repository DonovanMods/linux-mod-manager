package serve_test

// #622: importing an lmm collection from an https address in the browser -
// the profiles modal's URL field, the plan view saying where it was fetched
// from (without the query) and that its hooks are never imported, and the
// import recording the address. The address is an httptest TLS server the
// Service's download client trusts: no network.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eSharedCollection is a version-1 document with a hook, for game g1.
const e2eSharedCollection = `lmm_collection: 1
name: shared
title: Shared Setup
author: friend
game_id: g1
hooks:
  install:
    after_all: /usr/bin/true
mods: []
`

// newE2EFixtureWithACollectionURL is newE2EFixture with an https server
// serving e2eSharedCollection, which the Service's downloads trust.
func newE2EFixtureWithACollectionURL(t *testing.T) (e2eFixture, string) {
	t.Helper()
	sandboxE2EEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(e2eSharedCollection))
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

	baseURL := startE2EServer(t, svc)
	browser, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: browser, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors},
		srv.URL + "/shared.yaml?token=s3cret"
}

// openProfilesImport opens the profiles modal, settled.
func openProfilesImport() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="import-url"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profiles-row`, chromedp.ByQuery),
	}
}

func TestE2E_CollectionURL_ImportsWithoutHooksAndRecordsTheAddress(t *testing.T) {
	f, link := newE2EFixtureWithACollectionURL(t)

	var plan string
	var hookBoxes int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesImport(),
		chromedp.SetValue(`[data-testid="import-profile-name"]`, "from-a-friend", chromedp.ByQuery),
		chromedp.SetValue(`[data-testid="import-url"]`, link, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="import-url-submit"]`),
		chromedp.WaitVisible(`[data-testid="lmm-collection"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &plan),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="import-hooks"] input[type="checkbox"]').length`, &hookBoxes),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)

	assert.Contains(t, plan, "Shared Setup")
	assert.Contains(t, plan, "Fetched from "+strings.TrimSuffix(link, "token=s3cret")+"…", "the address, without its query")
	assert.NotContains(t, plan, "s3cret")
	assert.Contains(t, plan, "/usr/bin/true", "the hooks are listed")
	assert.Contains(t, plan, "Hooks are never imported from a URL.")
	assert.Zero(t, hookBoxes, "a URL's hooks have no checkbox")

	require.Eventually(t, func() bool {
		_, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "from-a-friend")
		return err == nil
	}, 15*time.Second, 50*time.Millisecond, "the import saves the profile")
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "from-a-friend")
	require.NoError(t, err)
	assert.Equal(t, link, p.Collection.Origin.ImportURL, "the profile records the address")
	assert.Equal(t, domain.GameHooksExplicit{}, p.HooksExplicit)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_CollectionURL_RefusesPlainHTTPInTheForm(t *testing.T) {
	f, _ := newE2EFixtureWithACollectionURL(t)
	var disabled bool
	var msg string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesImport(),
		chromedp.SendKeys(`[data-testid="import-url"]`, "http://example.invalid/c.lmmc", chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="import-url-error"]`, chromedp.ByQuery),
		textContent(`[data-testid="import-url-error"]`, &msg),
		chromedp.Evaluate(`document.querySelector('[data-testid="import-url-submit"]').disabled`, &disabled),
	)
	assert.Contains(t, msg, "https only")
	assert.True(t, disabled)
	assert.Empty(t, f.BrowserErrors())
}
