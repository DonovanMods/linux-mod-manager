package serve_test

// #641 in the browser: a profile whose version was edited by hand - every
// mod installed, so nothing is "not installed" - is offered Apply on the
// Profile card, which names the reason, and applying it installs the
// version the profile names.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_ProfileCard_OffersApplyForAHandEditedVersion(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	ctx := context.Background()
	pm := f.Svc.NewProfileManager()
	// Better Boots installed at 2.0 the ordinary way.
	require.NoError(t, pm.AddMod(ctx, f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: e2eSearchInstallModID, Version: "2.0"}))
	plan, err := f.Svc.PlanProfileApply(ctx, f.Game, "default")
	require.NoError(t, err)
	_, err = f.Svc.ApplyProfileApply(ctx, f.Game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(f.Game.ModPath, "Mods", "boots.pak")
	got, err := os.ReadFile(deployed)
	require.NoError(t, err)
	require.Equal(t, "payload for boots/f2", string(got), "2.0 is deployed")

	// The user edits the profile file by hand: back to 1.0.
	p, err := config.LoadProfile(f.Svc.ConfigDir(), f.Game.ID, "default")
	require.NoError(t, err)
	for i := range p.Mods {
		if p.Mods[i].ModID == e2eSearchInstallModID {
			p.Mods[i].Version = "1.0"
		}
	}
	require.NoError(t, config.SaveProfile(f.Svc.ConfigDir(), p))

	var title, row, planText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.card--profile [data-testid="profile-drift"]') !== null`),
		textContent(`.card--profile .card__title`, &title),
		textContent(`.card--profile [data-testid="profile-drift"]`, &row),
		clickWhenSettled(`[data-action="apply-profile"]`),
		chromedp.WaitVisible(`.modal[data-kind="profile_apply"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_apply"]`, &planText),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		// Applied, the profile matches: the card has nothing left to say.
		waitGone(`.card--profile`),
	)
	assert.Contains(t, title, "Profile (1)")
	assert.Equal(t, "Differs from what is installed: 1 mod at another version", row)
	assert.Contains(t, planText, "Better Boots")

	got, err = os.ReadFile(deployed)
	require.NoError(t, err)
	assert.Equal(t, "payload for boots/f1", string(got), "the apply installed the version the profile names")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_DriftSummary_WordsEveryReasonAsTheCLIDoes pins cards.js's
// driftSummary to cmd/lmm's (TestDriftSummary_NamesEveryReason): the two
// frontends name a drift the same way.
func TestE2E_DriftSummary_WordsEveryReasonAsTheCLIDoes(t *testing.T) {
	f := newE2EFixture(t)
	var lines []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { driftSummary } = await import("/static/app/components/cards.js");
			const all = { profile: "default", not_installed: 1, version: 2, mod_type: 1, files: 1,
				fomod: 1, disabled: 1, extra: 3, overrides: 1 };
			return [
				driftSummary(all),
				driftSummary(all, ["not_installed", "extra"]),
				driftSummary({ overrides: 2 }),
			];
		})()`, &lines, awaitPromise),
	)
	require.Len(t, lines, 3)
	assert.Equal(t, "1 mod not installed, 2 mods at another version, 1 mod of another mod type, 1 mod with other files, "+
		"1 mod with other installer choices, 1 mod disabled, 3 mods enabled that the profile leaves out, 1 override changed", lines[0])
	assert.Equal(t, "2 mods at another version, 1 mod of another mod type, 1 mod with other files, "+
		"1 mod with other installer choices, 1 mod disabled, 1 override changed", lines[1])
	assert.Equal(t, "2 overrides changed", lines[2])
	assert.Empty(t, f.BrowserErrors())
}
