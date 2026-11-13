package serve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_HealthCard_NamesAGameRootFile is #629 in the browser: on a
// Bethesda game a mod's ENB file is deployed in the game folder itself,
// beside the executable, and when the user replaces it the Health card's
// row names it by its root - "<game root>/enbseries/effect.fx", with a
// "Game root" badge - so it cannot be read as the Data file of that name.
func TestE2E_HealthCard_NamesAGameRootFile(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	app.RegisterAdapters(svc)
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "bethesda"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))

	mod := domain.Mod{ID: "enb", SourceID: "fake", Name: "Shiny ENB", Version: "1.0", GameID: game.ID}
	seedInstalledMod(t, svc, game, mod, true, map[string][]byte{
		"enbseries/effect.fx": []byte("shader"),
		"textures/sky.dds":    []byte("sky"),
	})
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "enb", Version: "1.0"}))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	shader := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	require.NoError(t, os.Remove(shader))
	require.NoError(t, os.WriteFile(shader, []byte("mine"), 0o644))

	baseURL := startE2EServer(t, svc)
	bctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: bctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	const row = `.card--health .card__row[data-status="deployed_modified"]`
	var rowText string
	var badges int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row, &rowText),
		chromedp.Evaluate(`document.querySelectorAll('`+row+` [data-testid="finding-game-root"]').length`, &badges),
	)

	assert.Contains(t, rowText, "Shiny ENB")
	assert.Contains(t, rowText, "<game root>/enbseries/effect.fx")
	assert.Contains(t, rowText, "Game root")
	assert.Equal(t, 1, badges, "the row carries the Game root badge")
	assert.Empty(t, f.BrowserErrors())
}
