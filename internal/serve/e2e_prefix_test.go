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

// TestE2E_HealthCard_NamesAPrefixFile is #636 in the browser: on Baldur's
// Gate 3 a .pak mod is deployed into the Mods folder in the game's
// Wine/Proton prefix, and when the user replaces it the Health card's row
// names it by its root - "<prefix>/AppData/Local/.../Mods/MyMod.pak", with
// a "Prefix" badge read from the finding's root field - so it cannot be
// read as a file in the game's Data folder.
func TestE2E_HealthCard_NamesAPrefixFile(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	app.RegisterAdapters(svc)
	prefix := filepath.Join(t.TempDir(), "pfx")
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "baldurs-gate-3"
	game.Prefix, game.PrefixUser = prefix, "steamuser"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	game, err := svc.GetGame(game.ID)
	require.NoError(t, err)

	mod := domain.Mod{ID: "pak", SourceID: "fake", Name: "Camera Mod", Version: "1.0", GameID: game.ID}
	seedInstalledMod(t, svc, game, mod, true, map[string][]byte{"CameraMod/CameraMod.pak": []byte("the mod's pak")})
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "pak", Version: "1.0"}))
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	pak := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios", "Baldur's Gate 3", "Mods", "CameraMod.pak")
	require.FileExists(t, pak)
	require.NoError(t, os.Remove(pak))
	require.NoError(t, os.WriteFile(pak, []byte("mine"), 0o644))

	baseURL := startE2EServer(t, svc)
	bctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: bctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	const row = `.card--health .card__row[data-status="deployed_modified"]`
	var rowText string
	var prefixBadges, rootBadges int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row, &rowText),
		chromedp.Evaluate(`document.querySelectorAll('`+row+` [data-testid="finding-prefix"]').length`, &prefixBadges),
		chromedp.Evaluate(`document.querySelectorAll('`+row+` [data-testid="finding-game-root"]').length`, &rootBadges),
	)

	assert.Contains(t, rowText, "Camera Mod")
	assert.Contains(t, rowText, "<prefix>/AppData/Local/Larian Studios/Baldur's Gate 3/Mods/CameraMod.pak")
	assert.Contains(t, rowText, "Prefix")
	assert.Equal(t, 1, prefixBadges, "the row carries the Prefix badge")
	assert.Zero(t, rootBadges, "and not the Game root one")
	assert.Empty(t, f.BrowserErrors())
}
