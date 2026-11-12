package serve_test

// #606: a game whose Steam app id lmm's game catalog sets up with an adapter
// it does not use is offered that adapter on Mission Control - never
// switched to it. The button is the user choosing to.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestE2E_AdapterSuggestion_OfferedAndTakenOnMissionControl(t *testing.T) {
	f := newE2EFixture(t)
	f.Svc.RegisterAdapter(bethesda.New())
	f.Svc.SetCatalogAdapters(func(appID string) string {
		if appID == "489830" {
			return "bethesda"
		}
		return ""
	})
	f.Game.Launch = &domain.GameLaunch{SteamAppID: "489830"}
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	var note string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion"]') !== null`),
		textContent(`[data-testid="adapter-suggestion"]`, &note),
		clickWhenSettled(`[data-testid="adapter-suggestion"] [data-action="use-suggested-adapter"]`),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion"]') === null`),
	)
	assert.Contains(t, note, "bethesda")
	assert.Contains(t, note, "lmm never changes that by itself")
	assert.Contains(t, note, "Use the bethesda adapter")

	game, err := f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, "bethesda", game.Adapter, "the button wrote the game's adapter")
	assert.Empty(t, f.BrowserErrors())
}

// A game with nothing to suggest shows no note.
func TestE2E_AdapterSuggestion_AbsentWithoutACatalogAdapter(t *testing.T) {
	f := newE2EFixture(t)
	var present bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="adapter-suggestion"]') !== null`, &present),
	)
	assert.False(t, present)
	assert.Empty(t, f.BrowserErrors())
}
