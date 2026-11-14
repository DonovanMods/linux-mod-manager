package serve_test

// #641 in the browser: a profile whose version was edited by hand - every
// mod installed, so nothing is "not installed" - is offered Apply on the
// Profile card, which names the reason, and applying it installs the
// version the profile names.

import (
	"context"
	"encoding/json"
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

// TestE2E_DriftSummary_WordsAsTheSharedFixture holds cards.js's drift
// wording to internal/core/testdata/drift_summary.json, the fixture
// cmd/lmm's driftSummary is held to as well
// (TestDriftSummary_WordsAsTheSharedFixture): the two frontends name a
// drift the same way.
func TestE2E_DriftSummary_WordsAsTheSharedFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "core", "testdata", "drift_summary.json"))
	require.NoError(t, err)
	var fixture struct {
		Header string `json:"header"`
		Cases  []struct {
			Text string `json:"text"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))

	f := newE2EFixture(t)
	var got struct {
		Header string   `json:"header"`
		Lines  []string `json:"lines"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { driftSummary, DRIFT_HEADER } = await import("/static/app/components/cards.js");
			const fixture = `+string(data)+`;
			return {
				header: DRIFT_HEADER,
				lines: fixture.cases.map((c) => driftSummary(c.drift, c.skip ?? [])),
			};
		})()`, &got, awaitPromise),
	)
	assert.Equal(t, fixture.Header, got.Header)
	require.Len(t, got.Lines, len(fixture.Cases))
	for i, c := range fixture.Cases {
		assert.Equal(t, c.Text, got.Lines[i])
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfileCard_NamesSwitchedOffBesideTheNotListedSentence (#641
// review): when the card's sentence is the not-listed one, only the
// unlisted mods it already counts are left out of the drift row - the mods
// the profile switches off but that are still enabled are named.
func TestE2E_ProfileCard_NamesSwitchedOffBesideTheNotListedSentence(t *testing.T) {
	f := newE2EFixture(t)
	var got struct {
		Card  string `json:"card"`
		Drift string `json:"drift"`
		Apply bool   `json:"apply"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { AttentionCards } = await import("/static/app/components/cards.js");
			const { initialState } = await import("/static/app/store.js");
			const { h, render } = await import("/static/app/render.js");
			const mount = document.createElement("div");
			document.body.append(mount);
			const state = initialState();
			state.route = { game: "g1", profile: "default" };
			state.status = {
				active_profile: "default",
				profiles: [{ name: "default", mod_count: 1 }],
				profile_drift: { profile: "default", unlisted: 1, switched_off: 1 },
			};
			const mods = { mods: [
				{ key: "fake:a", enabled: true },
				{ key: "fake:b", enabled: true },
			] };
			render(h(AttentionCards, { state, actions: { openPlan() {} }, mods }), mount);
			const out = {
				card: mount.querySelector(".card--profile")?.textContent ?? "",
				drift: mount.querySelector('[data-testid="profile-drift"]')?.textContent ?? "",
				apply: mount.querySelector('[data-action="apply-profile"]') !== null,
			};
			mount.remove();
			return out;
		})()`, &got, awaitPromise),
	)
	assert.Contains(t, got.Card, "1 installed mod is not in this profile's load order")
	assert.Equal(t, "Differs from what is installed: 1 mod enabled that the profile switches off", got.Drift)
	assert.True(t, got.Apply, "the active profile's drift offers Apply")
	assert.Empty(t, f.BrowserErrors())
}
