package serve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_HealthCard_ShowsAnOverriddenModFileAsTheProfilesChoice is #630 in
// the browser: a mod file the profile's overrides replace is deployed as a
// copy holding the override, and the Health card shows it - the file, an
// "Overridden" badge and the CLI's own words, "overridden by the profile" -
// as a fact rather than a problem: no warning heading, no Repair, and the
// mod's library row is not marked unhealthy for it.
func TestE2E_HealthCard_ShowsAnOverriddenModFileAsTheProfilesChoice(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	game.ModPath = filepath.Join(game.InstallPath, "mods")
	require.NoError(t, svc.SaveGame(ctx, game))

	mod := domain.Mod{ID: "tweaky", SourceID: "fake", Name: "Tweaky Mod", Version: "1.0", GameID: game.ID}
	seedInstalledMod(t, svc, game, mod, true, map[string][]byte{"Config/tweaky.ini": []byte("stock")})
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "tweaky", Version: "1.0"}))
	profile, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"mods/Config/tweaky.ini": []byte("tweaked")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(game.ModPath, "Config", "tweaky.ini"))
	require.NoError(t, err)
	require.Equal(t, "tweaked", string(got))

	baseURL := startE2EServer(t, svc)
	bctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: bctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	const row = `.card--health .card__row[data-status="deployed_overridden"]`
	var rowText, heading string
	var badges, repairs int
	var rowHealth *e2eRowHealth
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row, &rowText),
		textContent(`.card--health .card__title`, &heading),
		chromedp.Evaluate(`document.querySelectorAll('`+row+` [data-testid="finding-overridden"]').length`, &badges),
		chromedp.Evaluate(`document.querySelectorAll('`+row+` button').length`, &repairs),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(rowHealthJS("Tweaky Mod"), &rowHealth),
	)

	assert.Contains(t, rowText, "Tweaky Mod")
	assert.Contains(t, rowText, "Config/tweaky.ini")
	assert.Contains(t, rowText, "overridden by the profile")
	assert.Equal(t, 1, badges, "the row carries the Overridden badge")
	assert.Zero(t, repairs, "nothing to repair: the override is the profile's own choice")
	assert.NotContains(t, heading, "⚠", "an overridden file is no warning")
	if rowHealth != nil {
		assert.NotEqual(t, "issues", rowHealth.State, "the mod's row is not marked unhealthy for its override")
	}
	assert.Empty(t, f.BrowserErrors())
}
