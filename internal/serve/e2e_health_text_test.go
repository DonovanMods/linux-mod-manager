package serve_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthRowClippingJS reports every piece of Health card row text a reader
// cannot see in full without hovering: an element whose content overflows
// its box, or that is cut with an ellipsis.
const healthRowClippingJS = `[...document.querySelectorAll(".card--health .card__row > span")]
	.filter((el) => el.scrollWidth > el.clientWidth + 1 || getComputedStyle(el).textOverflow === "ellipsis" && el.scrollWidth > el.clientWidth)
	.map((el) => el.textContent.trim())`

// TestE2E_HealthCard_FindingsReadInFullWithoutCLIText is issue 585: a
// Health card row is the finding, readable in full - its subject and its
// own words wrap rather than being cut to one line with the rest in a
// tooltip - and never the CLI's "Not fixable: ... --fix ..." verdict,
// which a person in a browser can do nothing with.
func TestE2E_HealthCard_FindingsReadInFullWithoutCLIText(t *testing.T) {
	const longName = "An Extraordinarily Long Mod Name That Keeps Going Well Past Any Card Width Anyone Would Choose For A Health Row"
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "long", SourceID: "fake", Name: longName, Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "2.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "fake", "long", "1.0", "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "long", SourceID: "fake", Name: longName, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	// Locked, so the version_mismatch is NOT fixable: the row that used to
	// carry the "Not fixable" sentence.
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "long", Version: "1.0", FileIDs: []string{"f1"}, Locked: true}))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	var rowsText string
	var clipped []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health .card__row`, chromedp.ByQuery),
		textContent(`.card--health .card__list`, &rowsText),
		chromedp.Evaluate(healthRowClippingJS, &clipped),
	)

	assert.Contains(t, rowsText, longName)
	assert.Contains(t, rowsText, "version mismatch (recorded 1.0, source reports 2.0)")
	assert.NotContains(t, rowsText, "Not fixable")
	assert.NotContains(t, rowsText, "--fix")
	assert.Empty(t, clipped, "every row's text is readable without hovering")
	assert.Empty(t, f.BrowserErrors())
}
