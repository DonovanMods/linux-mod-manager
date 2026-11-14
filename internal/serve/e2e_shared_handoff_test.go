package serve_test

// #633 in the browser: Alpha Mod and Beta Mod both ship shared.pak, and
// Beta, later in load order, has it deployed. Disabling Beta from its full
// mod page hands the path to Alpha in the same job, and the page's readout
// says so where the job finished - the warning the CLI prints - and holds
// it there past the success release, as a switch's notice is held.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestE2E_SharedHandOff_DisablingTheWinnerSaysWhoHasThePath(t *testing.T) {
	f := newE2EFixtureFromSource(t, catalogSource(map[string]string{"a": "Alpha Mod", "b": "Beta Mod"}))
	for _, m := range []struct{ id, name string }{{"a", "Alpha Mod"}, {"b", "Beta Mod"}} {
		seedInstalledMod(t, f.Svc, f.Game,
			domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: f.Game.ID},
			true, map[string][]byte{"shared.pak": []byte(m.name), m.id + ".pak": []byte(m.id)})
		require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, f.Profile,
			domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, f.Profile, core.DeployOptions{}, nil)
	require.NoError(t, err)
	shared := filepath.Join(f.Game.ModPath, "shared.pak")
	got, err := os.ReadFile(shared)
	require.NoError(t, err)
	require.Equal(t, "Beta Mod", string(got))

	const warning = `.mod-page [data-testid="job-result-warning"]`
	var text string
	f.runInBrowser(t,
		scaleUITimers(),
		releaseSucceededReadouts(),
		chromedp.Navigate(f.ModPagePath("fake", "b")),
		pollUntil(modPageButtonJS+` === "Disable"`),
		clickWhenSettled(`.mod-page [data-action="toggle"]`),
		pollUntil(`document.querySelector('`+warning+`') !== null`),
		chromedp.Text(warning, &text, chromedp.ByQuery),
	)
	assert.Contains(t, text, "shared.pak: Alpha Mod's file is deployed there now in place of Beta Mod's")
	holdsFor(t, f.Ctx, readoutReleaseWait, "a disable that handed a shared path on",
		`document.querySelector('`+warning+`') !== null`)

	got, err = os.ReadFile(shared)
	require.NoError(t, err)
	assert.Equal(t, "Alpha Mod", string(got), "the next mod in load order has the path, with no deploy")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
