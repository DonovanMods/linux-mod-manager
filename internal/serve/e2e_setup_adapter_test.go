package serve_test

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// namedE2EAdapter is an identity adapter registered under a real adapter's
// name: which adapter a game resolves to is decided by name and
// registration alone.
type namedE2EAdapter string

func (a namedE2EAdapter) ID() string    { return string(a) }
func (a namedE2EAdapter) Label() string { return string(a) }
func (namedE2EAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// TestE2E_SetupGames_ShowsTheEffectiveAdapter is issue 426 in the browser:
// a game that declares BepInEx and names no adapter resolves to bepinex,
// and the Games table's Adapter cell says so instead of "generic-files" -
// the configured key, which is empty, is not what the game uses.
func TestE2E_SetupGames_ShowsTheEffectiveAdapter(t *testing.T) {
	f := newE2EFixture(t)
	f.Svc.RegisterAdapter(namedE2EAdapter("bepinex"))
	f.Game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	const cell = `document.querySelector('[data-testid="setup-games"] tbody tr td:nth-child(4)')?.textContent.trim()`
	var adapterCell string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup"),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Click(`.setup-nav__tab[data-section="games"]`, chromedp.ByQuery),
		chromedp.Poll(cell+` !== undefined`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Evaluate(cell, &adapterCell),
	)
	assert.Equal(t, "bepinex", adapterCell)
	assert.Empty(t, f.BrowserErrors())
}
