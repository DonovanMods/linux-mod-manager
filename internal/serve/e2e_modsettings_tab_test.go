package serve_test

// #646 in the browser. The reorder modal's load-order tab on a Baldur's
// Gate 3 game: the .pak order as modsettings.lsx lists it - the game's own
// GustavX entry and a mod the user enabled shown as external and never
// movable, lmm's paks with their mods - and the tab says the list enables
// them. A move made there is written to the profile and to modsettings.lsx
// at once; a reset (previewed first) clears the recorded order and keeps
// the paks listed. Every pak is synthetic (lariantest).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian/lariantest"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	e2eUUIDA = "a0000000-0000-4000-8000-00000000000a"
	e2eUUIDB = "b0000000-0000-4000-8000-00000000000b"
)

// newE2EPaksFixture is a Baldur's Gate 3 game on the baldurs-gate-3 adapter
// with a Proton-shaped prefix, mod a shipping A.pak and mod b shipping
// B.pak, deployed onto a mod list that held GustavX and a user's own mod.
// It answers the fixture and the list's path.
func newE2EPaksFixture(t *testing.T) (e2eFixture, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	app.RegisterAdapters(svc)
	prefix := filepath.Join(t.TempDir(), "pfx")
	user := filepath.Join(prefix, "drive_c", "users", "steamuser")
	require.NoError(t, os.MkdirAll(filepath.Join(user, "AppData", "Local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "baldurs-gate-3"
	game.Prefix, game.PrefixUser = prefix, "steamuser"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	dir := filepath.Join(user, "AppData", "Local", "Larian Studios", "Baldur's Gate 3", "PlayerProfiles", "Public")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	list := filepath.Join(dir, "modsettings.lsx")
	require.NoError(t, os.WriteFile(list, lariantest.ModSettings(lariantest.GustavX,
		lariantest.Entry{Folder: "Mine", Name: "My own mod", UUID: "eeee0000-0000-4000-8000-00000000000e", Version64: "1"}), 0o644))
	for _, m := range []struct{ id, name, pak, uuid string }{{"a", "Mod A", "A.pak", e2eUUIDA}, {"b", "Mod B", "B.pak", e2eUUIDB}} {
		mod := domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: game.ID}
		seedInstalledMod(t, svc, game, mod, true, map[string][]byte{m.pak: lariantest.ModPak(strings.TrimSuffix(m.pak, ".pak"), m.name, m.uuid)})
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}
	got, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	_, err = svc.DeployProfile(ctx, got, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Contains(t, readFileE2E(t, list), e2eUUIDA, "the deploy enabled the paks")

	baseURL := startE2EServer(t, svc)
	bctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: bctx, BaseURL: baseURL, Svc: svc, Game: got, Profile: "default", BrowserErrors: browserErrors}, list
}

const paksTab = `.reorder-tabs [data-tab="lo-baldursgate3"]`

func TestE2E_LoadOrderTab_BG3PaksEnableAndMove(t *testing.T) {
	f, list := newE2EPaksFixture(t)

	var rows []string
	var label, intro, gustavPlacement, unrecorded string
	var gustavControls int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.library__toolbar [data-action="reorder"]`),
		chromedp.WaitVisible(paksTab, chromedp.ByQuery),
		textContent(paksTab, &label),
		chromedp.Click(paksTab, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="load-order-list"]`, chromedp.ByQuery),
		chromedp.Evaluate(archiveNamesJS, &rows),
		textContent(`[data-testid="load-order-tab"] > .plan__note`, &intro),
		textContent(`[data-file="GustavX"] [data-testid="load-order-placement"]`, &gustavPlacement),
		chromedp.Evaluate(`document.querySelectorAll('[data-file="GustavX"] button').length`, &gustavControls),
		textContent(`[data-testid="load-order-unrecorded"]`, &unrecorded),
	)
	assert.Equal(t, "Paks", label)
	assert.Equal(t, []string{"GustavX", "My own mod", "A.pak", "B.pak"}, rows)
	assert.Contains(t, intro, "modsettings.lsx enables them too")
	assert.Contains(t, intro, "Entries lmm did not add are never moved")
	assert.Equal(t, "external", gustavPlacement)
	assert.Equal(t, 0, gustavControls, "the game's own entry is not the profile's to move")
	assert.Contains(t, unrecorded, "lmm keeps its files in the order")

	// Move B.pak up: the profile records it, and modsettings.lsx is
	// written with B's module before A's.
	f.runInBrowser(t,
		clickWhenSettled(`[data-file="B.pak"] [data-action="load-order-up"]`),
		pollUntil(`JSON.stringify(`+archiveNamesJS+`) === JSON.stringify(["GustavX","My own mod","B.pak","A.pak"])`),
	)
	data := readFileE2E(t, list)
	assert.Less(t, strings.Index(data, e2eUUIDB), strings.Index(data, e2eUUIDA))
	profile, err := f.Svc.NewProfileManager().Get(context.Background(), f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.FileLoadOrders{"baldursgate3": {"B.pak", "A.pak"}}, profile.LoadOrders)

	// A reset, previewed first: the recorded order goes, the paks stay
	// listed in the order they have.
	var plan string
	f.runInBrowser(t,
		clickWhenSettled(`[data-action="load-order-reset"]`),
		chromedp.WaitVisible(`[data-testid="load-order-reset-plan"]`, chromedp.ByQuery),
		textContent(`[data-testid="load-order-reset-plan"]`, &plan),
		clickWhenSettled(`[data-action="load-order-reset-apply"]`),
		pollUntil(`document.querySelector('[data-testid="load-order-unrecorded"]') !== null`),
	)
	assert.Contains(t, plan, "lmm keeps listing its files in modsettings.lsx")
	data = readFileE2E(t, list)
	assert.Less(t, strings.Index(data, e2eUUIDB), strings.Index(data, e2eUUIDA), "the paks keep their order and stay listed")
	assert.Empty(t, f.BrowserErrors())
}

const e2eNexusPakRecipe = `{"Name": "Faerun", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"}],
	"LoadOrder": {"Entries": 2, "Files": [
		{"Name": "Ten.pak", "Enabled": true, "ModHint": "Ten-10-1-0"},
		{"Name": "Other.pak", "Enabled": true, "ModHint": "Other-99-1"}]}}`

// A NexusMods collection's top-level loadOrder on a Baldur's Gate 3 game
// (#646): the import plan shows the pak order and says lmm lists the paks
// in the game's mod list, and the import saves it on the profile.
func TestE2E_NexusCollection_PlanShowsThePakOrder(t *testing.T) {
	f := newE2EFixture(t)
	app.RegisterAdapters(f.Svc)
	f.Svc.RegisterSource(e2eNexusSource{newFakeSource("nexusmods")})
	prefix := filepath.Join(t.TempDir(), "pfx")
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
	f.Game.SourceIDs["nexusmods"] = "baldursgate3"
	f.Game.Adapter = "baldurs-gate-3"
	f.Game.Prefix, f.Game.PrefixUser = prefix, "steamuser"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "paks", e2eNexusPakRecipe)

	var heading, applied, files string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("paks"),
		chromedp.WaitVisible(`[data-testid="collection-load-order"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-load-order"] .plan__heading`, &heading),
		textContent(`[data-testid="collection-load-order-applied"]`, &applied),
		clickWhenSettled(`[data-testid="collection-load-order"] summary`),
		chromedp.WaitVisible(`[data-testid="collection-load-order-files"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-load-order-files"]`, &files),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Equal(t, "Paks load order (2)", heading)
	assert.Contains(t, applied, "lmm lists these files in the game's mod list")
	assert.Contains(t, files, "1. Ten.pak")
	assert.Contains(t, files, "2. Other.pak")

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Faerun")
		return err == nil && len(p.LoadOrders["baldursgate3"]) == 2
	}, 15*time.Second, 50*time.Millisecond, "the import saves the pak order on the profile")
	assert.Empty(t, f.BrowserErrors())
}

// With no modsettings.lsx (the game writes it on its first run, and lmm
// never makes one up), the Paks tab says what to do instead of "No files
// deployed." (#646 review).
func TestE2E_LoadOrderTab_BG3MissingListSaysRunTheGame(t *testing.T) {
	f, list := newE2EPaksFixture(t)
	require.NoError(t, os.Remove(list))

	var empty string
	var rows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.library__toolbar [data-action="reorder"]`),
		chromedp.WaitVisible(paksTab, chromedp.ByQuery),
		chromedp.Click(paksTab, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="load-order-empty"]`, chromedp.ByQuery),
		textContent(`[data-testid="load-order-empty"]`, &empty),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="load-order-list"] [data-file]').length`, &rows),
	)
	assert.Equal(t, "modsettings.lsx not found: run the game once, then deploy again.", strings.TrimSpace(empty))
	assert.NotContains(t, empty, "No files deployed")
	assert.Equal(t, 0, rows)
	assert.Empty(t, f.BrowserErrors())
}
