package serve_test

// Issue 671's web half: a source waiting out a rate limit under the Health
// card's verify or the update check - plain reads, not jobs - is said on
// the read's own loading line, and a collection profile's Health card says
// that the collection's own mods were left to it rather than checked
// against their source.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// TestE2E_AReadsLoadingLineSaysWhatItIsWaitingOn: a read_progress frame
// carrying a notice (internal/serve/read_progress.go) adds it to the
// read's line, and the next frame, carrying none, takes it away.
func TestE2E_AReadsLoadingLineSaysWhatItIsWaitingOn(t *testing.T) {
	f := newE2EFixture(t)

	var got map[string]string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { pendingReadLabel } = await import("/static/app/loading.js");
			const notice = "Rate limited by NexusMods; retrying in 30s (attempt 2 of 3).";
			return {
				waiting: pendingReadLabel("health", { progress: { index: 17, total: 353, notice } }),
				moved: pendingReadLabel("health", { progress: { index: 18, total: 353 } }),
				updates: pendingReadLabel("updates", { progress: { index: 4, total: 353, notice } }),
			};
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	assert.Equal(t, "Verifying 353 mods (17/353)… Rate limited by NexusMods; retrying in 30s (attempt 2 of 3).", got["waiting"])
	assert.Equal(t, "Verifying 353 mods (18/353)…", got["moved"], "the next tick ends it")
	assert.Equal(t, "Checking 353 mods for updates (4/353)… Rate limited by NexusMods; retrying in 30s (attempt 2 of 3).", got["updates"])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_HealthCard_SaysTheCollectionsModsWereLeftToIt: a mod the profile's
// collection locks is not sent to its source by the full tier (#671), and
// the Health card's coverage line says so rather than letting a clean card
// read as "every mod was checked".
func TestE2E_HealthCard_SaysTheCollectionsModsWereLeftToIt(t *testing.T) {
	sandboxE2EEnv(t)
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "coll", SourceID: "fake", Name: "Collection Mod", Version: "1.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "1.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "fake", ModID: "coll", Version: "1.0"}, "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "coll", SourceID: "fake", Name: "Collection Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "coll", Version: "1.0", FileIDs: []string{"f1"}, Locked: true, FromCollection: true}))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	var coverage string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="health-coverage"]`, chromedp.ByQuery),
		textContent(`[data-testid="health-coverage"]`, &coverage),
	)

	assert.Contains(t, coverage, "1 left to the collection, not checked against their source")
	assert.Empty(t, f.BrowserErrors())
}
