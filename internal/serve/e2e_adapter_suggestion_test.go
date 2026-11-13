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
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestE2E_AdapterSuggestion_OfferedAndTakenOnMissionControl(t *testing.T) {
	f := newE2EFixture(t)
	f.Svc.RegisterAdapter(bethesda.New())
	f.Svc.SetKnownGames(func() []core.KnownGame {
		return []core.KnownGame{{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"}}
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

// #606 review fix 1: on a game with mods already installed, the button asks
// first - core's sentence says they keep the layout they were installed
// with - and Cancel changes nothing; only "Switch anyway" writes the
// adapter.
func TestE2E_AdapterSuggestion_ConfirmsWhenModsAreInstalled(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)
	f.Svc.RegisterAdapter(bethesda.New())
	f.Svc.SetKnownGames(func() []core.KnownGame {
		return []core.KnownGame{{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"}}
	})
	f.Game.Launch = &domain.GameLaunch{SteamAppID: "489830"}
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	var warning string
	var confirmGoneAfterCancel bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion"]') !== null`),
		clickWhenSettled(`[data-testid="adapter-suggestion"] [data-action="use-suggested-adapter"]`),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion-confirm"]') !== null`),
		textContent(`[data-testid="adapter-suggestion-confirm"]`, &warning),
		clickWhenSettled(`[data-action="cancel-suggested-adapter"]`),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion-confirm"]') === null`),
		chromedp.Evaluate(`document.querySelector('[data-action="use-suggested-adapter"]') !== null`, &confirmGoneAfterCancel),
	)
	assert.Contains(t, warning, "3 mod(s) already installed for this game keep the layout they were installed with")
	assert.Contains(t, warning, "Switch anyway")
	assert.True(t, confirmGoneAfterCancel, "Cancel puts the offer back")
	game, err := f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Empty(t, game.Adapter, "Cancel wrote nothing")

	f.runInBrowser(t,
		clickWhenSettled(`[data-testid="adapter-suggestion"] [data-action="use-suggested-adapter"]`),
		pollUntil(`document.querySelector('[data-action="confirm-suggested-adapter"]') !== null`),
		clickWhenSettled(`[data-action="confirm-suggested-adapter"]`),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion"]') === null`),
	)
	game, err = f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, "bethesda", game.Adapter, "Switch anyway wrote the adapter")
	assert.Empty(t, f.BrowserErrors())
}

// #606 re-review item 2: the confirmation states what is installed NOW, not
// what was installed when the page loaded. A mod installed after the page
// rendered (here, behind its back) still makes the button ask, and the
// sentence counts it.
func TestE2E_AdapterSuggestion_ConfirmationIsCurrent(t *testing.T) {
	f := newE2EFixture(t)
	f.Svc.RegisterAdapter(bethesda.New())
	f.Svc.SetKnownGames(func() []core.KnownGame {
		return []core.KnownGame{{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"}}
	})
	f.Game.Launch = &domain.GameLaunch{SteamAppID: "489830"}
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	var warning string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion"]') !== null`),
	)
	// Installed after the page loaded its status, which said "no mods".
	seedInstalledMod(t, f.Svc, f.Game, domain.Mod{ID: "late", SourceID: "fake", Name: "Late Mod", Version: "1.0", GameID: f.Game.ID}, true, nil)

	f.runInBrowser(t,
		clickWhenSettled(`[data-testid="adapter-suggestion"] [data-action="use-suggested-adapter"]`),
		pollUntil(`document.querySelector('[data-testid="adapter-suggestion-confirm"]') !== null`),
		textContent(`[data-testid="adapter-suggestion-confirm"]`, &warning),
	)
	assert.Contains(t, warning, "1 mod(s) already installed for this game keep the layout they were installed with")
	game, err := f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Empty(t, game.Adapter, "nothing switched without the confirmation")
	assert.Empty(t, f.BrowserErrors())
}
