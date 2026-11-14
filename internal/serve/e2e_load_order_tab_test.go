package serve_test

// #620 in the browser. The reorder modal's load-order tab on a Cyberpunk
// 2077 game: it shows the effective archive order - what placed each
// archive, the mod that ships it - and a move made there is written to the
// profile and to archive/pc/mod/modlist.txt at once, through the same core
// edit `lmm load-order` runs; a reset (previewed first) hands the list
// back. And a NexusMods collection's archive load order in the import plan.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newE2EArchivesFixture is a Cyberpunk 2077 game on the cyberpunk2077
// adapter with mod a shipping a.archive and b.archive and mod b shipping
// c.archive, deployed, and an archive the user installed by hand that
// modlist.txt names first. It answers the fixture and the list's path.
func newE2EArchivesFixture(t *testing.T) (e2eFixture, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	app.RegisterAdapters(svc)
	game.ModPath = game.InstallPath
	game.Adapter = "cyberpunk2077"
	require.NoError(t, svc.SaveGame(ctx, game))
	modDir := filepath.Join(game.InstallPath, "archive", "pc", "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "mine.archive"), []byte("x"), 0o644))
	list := filepath.Join(modDir, "modlist.txt")
	require.NoError(t, os.WriteFile(list, []byte("mine.archive\r\n"), 0o644))
	for _, m := range []struct {
		id, name string
		files    []string
	}{{"a", "Mod A", []string{"a.archive", "b.archive"}}, {"b", "Mod B", []string{"c.archive"}}} {
		files := map[string][]byte{}
		for _, f := range m.files {
			files["archive/pc/mod/"+f] = []byte(f)
		}
		mod := domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: game.ID}
		seedInstalledMod(t, svc, game, mod, true, files)
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, "mine.archive\r\n", readFileE2E(t, list), "a profile with no order leaves the list alone")

	baseURL := startE2EServer(t, svc)
	bctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: bctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}, list
}

func readFileE2E(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

const archiveTab = `.reorder-tabs [data-tab="lo-cyberpunk2077-archive"]`

// archiveNamesJS is the load-order tab's rows, top to bottom.
const archiveNamesJS = `[...document.querySelectorAll('[data-testid="load-order-list"] [data-file]')].map((r) => r.dataset.file)`

func TestE2E_LoadOrderTab_ViewsMovesAndResets(t *testing.T) {
	f, list := newE2EArchivesFixture(t)

	var rows []string
	var label, minePlacement, unrecorded string
	var mineControls int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.library__toolbar [data-action="reorder"]`),
		chromedp.WaitVisible(archiveTab, chromedp.ByQuery),
		textContent(archiveTab, &label),
		chromedp.Click(archiveTab, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="load-order-list"]`, chromedp.ByQuery),
		chromedp.Evaluate(archiveNamesJS, &rows),
		textContent(`[data-file="mine.archive"] [data-testid="load-order-placement"]`, &minePlacement),
		chromedp.Evaluate(`document.querySelectorAll('[data-file="mine.archive"] button').length`, &mineControls),
		textContent(`[data-testid="load-order-unrecorded"]`, &unrecorded),
	)
	assert.Equal(t, "Archives", label)
	// The profile orders nothing: the order the game uses now, the list's
	// own archive first.
	assert.Equal(t, []string{"mine.archive", "a.archive", "b.archive", "c.archive"}, rows)
	assert.Equal(t, "external", minePlacement)
	assert.Equal(t, 0, mineControls, "an archive lmm did not deploy is not the profile's to move")
	assert.Contains(t, unrecorded, "The profile orders nothing yet")

	// Move c.archive up: the tab re-renders the order core answers, the
	// profile records it, and modlist.txt is written.
	f.runInBrowser(t,
		chromedp.Click(`[data-file="c.archive"] [data-action="load-order-up"]`, chromedp.ByQuery),
		pollUntil(`JSON.stringify(`+archiveNamesJS+`) === JSON.stringify(["mine.archive","a.archive","c.archive","b.archive"])`),
		chromedp.Evaluate(archiveNamesJS, &rows),
	)
	assert.Equal(t, "mine.archive\r\na.archive\r\nc.archive\r\nb.archive\r\n", readFileE2E(t, list))
	profile, err := f.Svc.NewProfileManager().Get(context.Background(), f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.FileLoadOrders{"cyberpunk2077-archive": {"a.archive", "c.archive", "b.archive"}}, profile.LoadOrders)

	// To the top, then a reset: previewed first, then the user's own list
	// comes back.
	var plan string
	f.runInBrowser(t,
		chromedp.Click(`[data-file="b.archive"] [data-action="load-order-first"]`, chromedp.ByQuery),
		pollUntil(`(`+archiveNamesJS+`)[1] === "b.archive"`),
		clickWhenSettled(`[data-action="load-order-reset"]`),
		chromedp.WaitVisible(`[data-testid="load-order-reset-plan"]`, chromedp.ByQuery),
		textContent(`[data-testid="load-order-reset-plan"]`, &plan),
		clickWhenSettled(`[data-action="load-order-reset-apply"]`),
		pollUntil(`document.querySelector('[data-testid="load-order-unrecorded"]') !== null`),
	)
	assert.Contains(t, plan, "the game's own order applies again")
	assert.Equal(t, "mine.archive\r\n", readFileE2E(t, list), "the reset hands the user's list back")
	assert.Empty(t, f.BrowserErrors())
}

const e2eNexusArchiveRecipe = `{"Name": "Night City", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"}],
	"FileLoadOrders": [{"ID": "cyberpunk2077-archive", "Entries": 2, "Files": [
		{"Name": "! ten.archive", "Enabled": true, "ModHint": "Ten 10 1.0 2026-11-01"},
		{"Name": "other.archive", "Enabled": true, "ModHint": "Other 99 1.0"}]}]}`

func TestE2E_NexusCollection_PlanShowsTheArchiveOrder(t *testing.T) {
	f := newE2EFixture(t)
	app.RegisterAdapters(f.Svc)
	f.Svc.RegisterSource(e2eNexusSource{newFakeSource("nexusmods")})
	f.Game.SourceIDs["nexusmods"] = "cyberpunk2077"
	f.Game.Adapter = "cyberpunk2077"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "archives", e2eNexusArchiveRecipe)

	var heading, applied, files string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("archives"),
		chromedp.WaitVisible(`[data-testid="collection-load-order"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-load-order"] .plan__heading`, &heading),
		textContent(`[data-testid="collection-load-order-applied"]`, &applied),
		clickWhenSettled(`[data-testid="collection-load-order"] summary`),
		chromedp.WaitVisible(`[data-testid="collection-load-order-files"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-load-order-files"]`, &files),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Equal(t, "Archives load order (2)", heading)
	assert.Contains(t, applied, "written to the game's list at every deploy")
	assert.Contains(t, files, "1. ! ten.archive (Ten)", "an entry the recipe ties to a member mod names it")
	assert.Contains(t, files, "2. other.archive")
	assert.NotContains(t, files, "other.archive (")

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Night City")
		return err == nil && len(p.LoadOrders["cyberpunk2077-archive"]) == 2
	}, 15*time.Second, 50*time.Millisecond, "the import saves the load order on the profile")
	assert.Empty(t, f.BrowserErrors())
}
