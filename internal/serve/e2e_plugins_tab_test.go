package serve_test

// #617 in the browser: the reorder modal's Plugins tab on a Bethesda game.
// It shows the effective plugin order - flags, the masters a plugin needs
// (a missing one marked), what placed each plugin - and every change made
// there (a move, a switch off, a rule) is written to the profile and the
// game's Plugins.txt at once, through the same core edit `lmm plugins`
// runs. A game with no plugin list gets no tab.

import (
	"context"
	"encoding/binary"
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

// e2ePlugin is a minimal TES4 plugin header naming masters.
func e2ePlugin(flags uint32, masters ...string) []byte {
	var body []byte
	for _, m := range masters {
		data := append([]byte(m), 0)
		sub := make([]byte, 6, 6+len(data))
		copy(sub, "MAST")
		binary.LittleEndian.PutUint16(sub[4:], uint16(len(data)))
		body = append(body, append(sub, data...)...)
	}
	hdr := make([]byte, 24)
	copy(hdr, "TES4")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(body)))
	binary.LittleEndian.PutUint32(hdr[8:], flags)
	return append(hdr, body...)
}

// newE2EPluginsFixture is a Skyrim SE game on the bethesda adapter with
// three mods deployed: A.esp, B.esp, and C.esp, whose master Absent.esm is
// installed nowhere. It answers the fixture and the Plugins.txt path.
func newE2EPluginsFixture(t *testing.T) (e2eFixture, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	app.RegisterAdapters(svc)
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "bethesda"
	game.Catalog = "skyrim-se"
	prefix := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), nil, 0o644))
	game.Prefix = prefix
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "Skyrim.esm"), e2ePlugin(1), 0o644))
	require.NoError(t, svc.SaveGame(ctx, game))
	for _, m := range []struct{ id, name, file string }{
		{"a", "Mod A", "A.esp"}, {"b", "Mod B", "B.esp"}, {"c", "Mod C", "C.esp"},
	} {
		masters := []string{"Skyrim.esm"}
		if m.id == "c" {
			masters = append(masters, "Absent.esm")
		}
		mod := domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: game.ID}
		seedInstalledMod(t, svc, game, mod, true, map[string][]byte{m.file: e2ePlugin(0, masters...)})
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	list := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition", "Plugins.txt")
	data, err := os.ReadFile(list)
	require.NoError(t, err)
	require.Equal(t, "*A.esp\n*B.esp\n*C.esp\n", string(data))

	baseURL := startE2EServer(t, svc)
	bctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: bctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}, list
}

// openPluginsTab opens the reorder modal from the library and switches to
// its Plugins tab.
func openPluginsTab() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.library__toolbar [data-action="reorder"]`),
		chromedp.WaitVisible(`.reorder-tabs [data-tab="plugins"]`, chromedp.ByQuery),
		chromedp.Click(`.reorder-tabs [data-tab="plugins"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="plugins-list"]`, chromedp.ByQuery),
	}
}

// pluginNamesJS is the Plugins tab's rows, top to bottom.
const pluginNamesJS = `[...document.querySelectorAll('[data-testid="plugins-list"] [data-plugin]')].map((r) => r.dataset.plugin)`

func readPluginsTxt(t *testing.T, list string) string {
	t.Helper()
	data, err := os.ReadFile(list)
	require.NoError(t, err)
	return string(data)
}

func TestE2E_PluginsTab_ViewsTheEffectiveOrder(t *testing.T) {
	f, _ := newE2EPluginsFixture(t)

	var rows []string
	var skyrimPlacement, skyrimFlags, missing, findings string
	var skyrimControls int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openPluginsTab(),
		chromedp.Evaluate(pluginNamesJS, &rows),
		textContent(`[data-plugin="Skyrim.esm"] [data-testid="plugin-placement"]`, &skyrimPlacement),
		textContent(`[data-plugin="Skyrim.esm"] .plugins-row__name`, &skyrimFlags),
		chromedp.Evaluate(`document.querySelectorAll('[data-plugin="Skyrim.esm"] button').length`, &skyrimControls),
		textContent(`[data-plugin="C.esp"] [data-missing="true"]`, &missing),
		textContent(`[data-testid="plugins-findings"]`, &findings),
	)
	assert.Equal(t, []string{"Skyrim.esm", "A.esp", "B.esp", "C.esp"}, rows)
	assert.Equal(t, "game", skyrimPlacement)
	assert.Contains(t, skyrimFlags, "M", "the game's master carries its flag")
	assert.Equal(t, 0, skyrimControls, "the game's own plugin is not the profile's to move")
	assert.Equal(t, "Absent.esm (missing)", missing)
	assert.Contains(t, findings, "Error: C.esp needs the master(s) Absent.esm (not installed)")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_PluginsTab_MovesEnablesAndAddsARule(t *testing.T) {
	f, list := newE2EPluginsFixture(t)

	// Move B.esp up: the tab re-renders the order core answers, and the
	// game's list is written.
	var rows []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openPluginsTab(),
		chromedp.Click(`[data-plugin="B.esp"] [data-action="plugin-up"]`, chromedp.ByQuery),
		pollUntil(`JSON.stringify(`+pluginNamesJS+`) === JSON.stringify(["Skyrim.esm","B.esp","A.esp","C.esp"])`),
		chromedp.Evaluate(pluginNamesJS, &rows),
	)
	assert.Equal(t, []string{"Skyrim.esm", "B.esp", "A.esp", "C.esp"}, rows)
	assert.Equal(t, "*B.esp\n*A.esp\n*C.esp\n", readPluginsTxt(t, list))

	// Switch C.esp off (its master is missing): the finding goes, and the
	// list keeps it, inactive.
	var findings int
	f.runInBrowser(t,
		chromedp.Click(`[data-plugin="C.esp"] [data-action="plugin-toggle"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-plugin="C.esp"]').dataset.active === "false"`),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="plugins-findings"]').length`, &findings),
	)
	assert.Equal(t, 0, findings, "an inactive plugin's missing master is no finding")
	assert.Equal(t, "*B.esp\n*A.esp\nC.esp\n", readPluginsTxt(t, list))

	// A rule: B.esp loads after A.esp. The row says the rule placed it.
	var constraint, rule string
	f.runInBrowser(t,
		chromedp.SendKeys(`[data-testid="plugins-rule-form"] input[name="rule-plugin"]`, "B.esp", chromedp.ByQuery),
		chromedp.SendKeys(`[data-testid="plugins-rule-form"] input[name="rule-target"]`, "A.esp", chromedp.ByQuery),
		chromedp.Click(`[data-action="rule-add"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-plugin="B.esp"]').dataset.placement === "rule"`),
		chromedp.Evaluate(pluginNamesJS, &rows),
		textContent(`[data-plugin="B.esp"] [data-testid="plugin-constraints"]`, &constraint),
		textContent(`[data-testid="plugins-rules"] [data-rule="B.esp"]`, &rule),
	)
	assert.Equal(t, []string{"Skyrim.esm", "A.esp", "B.esp", "C.esp"}, rows)
	assert.Equal(t, "rule: after A.esp", constraint)
	assert.Contains(t, rule, "B.esp after A.esp")
	assert.Equal(t, "*A.esp\n*B.esp\nC.esp\n", readPluginsTxt(t, list))

	// Removing it puts the recorded order back.
	f.runInBrowser(t,
		chromedp.Click(`[data-rule="B.esp"] [data-action="rule-remove"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-rule="B.esp"]') === null`),
		pollUntil(`JSON.stringify(`+pluginNamesJS+`) === JSON.stringify(["Skyrim.esm","B.esp","A.esp","C.esp"])`),
	)
	assert.Equal(t, "*B.esp\n*A.esp\nC.esp\n", readPluginsTxt(t, list))

	profile, err := f.Svc.NewProfileManager().Get(context.Background(), f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []domain.PluginEntry{{Name: "B.esp", Enabled: true}, {Name: "A.esp", Enabled: true}, {Name: "C.esp", Enabled: false}}, profile.Plugins.Order)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ReorderModal_NoPluginsTabWithoutAPluginList(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var tabs int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.library__toolbar [data-action="reorder"]`),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
		// The plugin read has answered: no plugin list.
		pollUntil(`document.querySelector('#reorder-panel').dataset.plugins === "none"`),
		chromedp.Evaluate(`document.querySelectorAll('.reorder-tabs').length`, &tabs),
	)
	assert.Equal(t, 0, tabs)
	assert.Empty(t, f.BrowserErrors())
}
