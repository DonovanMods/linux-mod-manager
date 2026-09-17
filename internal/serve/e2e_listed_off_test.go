package serve_test

// #440: a mod an imported profile lists switched off is never downloaded,
// so it has no installed row - and the library and the Profile card render
// it anyway: listed, off, not downloaded, with Install… as the way to
// switch it on.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// newE2EFixtureWithAnImportedOffMod imports - through core's own
// PlanImport/ApplyImport, downloads allowed - a profile "imported" that
// lists Better Boots at 1.0 switched off, and makes it the active profile.
// The document is an `lmm profile export` of a scratch profile.
func newE2EFixtureWithAnImportedOffMod(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	ctx := t.Context()
	pm := f.Svc.NewProfileManager()
	_, err := pm.Create(ctx, f.Game.ID, "imported")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, f.Game.ID, "imported",
		domain.ModReference{SourceID: "fake", ModID: e2eSearchInstallModID, Version: "1.0", Disabled: true}))
	doc, err := pm.Export(ctx, f.Game.ID, "imported")
	require.NoError(t, err)
	require.NoError(t, pm.Delete(ctx, f.Game.ID, "imported"))

	plan, err := f.Svc.PlanImport(ctx, f.Game, doc)
	require.NoError(t, err)
	_, err = f.Svc.ApplyImport(ctx, f.Game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, f.Game.ID, "imported"))
	f.Profile = "imported"
	require.Zero(t, f.Src.downloadCount(), "an import never downloads a disabled mod")
	return f
}

func TestE2E_ListedOffMod_IsAFirstClassRowAndInstallSwitchesItOn(t *testing.T) {
	f := newE2EFixtureWithAnImportedOffMod(t)
	key := "fake:" + e2eSearchInstallModID

	var libraryRow, cardRow, cardTitle string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library [data-testid="listed-off"] [data-mod="`+key+`"]') !== null`),
		textContent(`.library [data-testid="listed-off"] [data-mod="`+key+`"]`, &libraryRow),
		pollUntil(`document.querySelector('.card--profile [data-testid="profile-listed-off"]') !== null`),
		textContent(`.card--profile [data-testid="profile-listed-off"]`, &cardRow),
		textContent(`.card--profile .card__title`, &cardTitle),
	)
	assert.Contains(t, libraryRow, key)
	assert.Contains(t, libraryRow, "1.0")
	assert.Contains(t, libraryRow, "Off")
	assert.Contains(t, libraryRow, "Not downloaded")
	assert.Contains(t, libraryRow, "Installing downloads it and switches it on.")
	assert.Contains(t, cardRow, "1 mod in this profile is switched off and not downloaded")
	assert.Equal(t, "◎ Profile (1)", strings.TrimSpace(cardTitle),
		"the off mod is not also counted as one Apply profile would install")

	// Install… from the library row: the plan names the listed version,
	// and confirming downloads and deploys it, switched on.
	var plan string
	f.runInBrowser(t,
		chromedp.Click(`.library [data-mod="`+key+`"] [data-action="install-listed-off"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="install"]`, &plan),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		pollUntil(`document.querySelector('.library [data-testid="listed-off"]') === null &&
			[...document.querySelectorAll('.mod-row')].some(r => r.textContent.includes("Better Boots"))`),
	)
	assert.Contains(t, plan, "Better Boots")
	deployed, err := os.ReadFile(filepath.Join(f.Game.ModPath, "Mods", "boots.pak"))
	require.NoError(t, err, "the install downloaded and deployed the listed mod")
	assert.Contains(t, string(deployed), "boots/f1", "the version the profile lists (1.0 is file f1)")
	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", e2eSearchInstallModID, f.Game.ID, "imported")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
	profile, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "imported")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Disabled, "the explicit install cleared the disabled marker")
	assert.Empty(t, f.BrowserErrors())
}
