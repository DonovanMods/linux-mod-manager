package serve_test

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ModPage_AnotherProfileIsNotARereadOfThisOne: the full mod page
// keeps its live documents through a re-read of the mod already on screen
// (issue 615 follow-up), but every one of them is the PROFILE's - the
// installed record, its lock and policy, the update check. The same mod
// reached in another profile without a page load (Back, a link) is a
// different page: at no point may it show the first profile's answers.
//
// fake/a is locked in "default" and not in "unlocked", a duplicate of
// it; the meta line's " · locked" comes from the live detail's installed
// record, so a sampler that reads it on every DOM change sees whether the
// default profile's detail was ever on screen under the other's route.
func TestE2E_ModPage_AnotherProfileIsNotARereadOfThisOne(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)
	ctx := t.Context()
	_, err := f.Svc.DuplicateProfile(ctx, f.Game.ID, "default", "unlocked")
	require.NoError(t, err)
	require.NoError(t, f.Svc.NewProfileManager().SetModLock(ctx, f.Game.ID, "default", "fake", "a", "1.0"))

	otherPath := "/g/" + f.Game.ID + "/unlocked/mod/fake/a"
	var sawLocked bool
	var finalMeta string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		pollUntil(`(document.querySelector(".mod-page__meta")?.textContent ?? "").includes("locked")`),
		chromedp.Evaluate(detailWatchJS("/api/v1/mods/fake/a"), nil),
		chromedp.Evaluate(fmt.Sprintf(`(() => {
			window.__sawLocked = false;
			const sample = () => {
				const meta = document.querySelector(".mod-page__meta");
				if (location.pathname === %q && meta && meta.textContent.includes("locked"))
					window.__sawLocked = true;
			};
			new MutationObserver(sample).observe(document.body,
				{ childList: true, subtree: true, characterData: true });
			window.addEventListener("popstate", () => queueMicrotask(sample));
		})()`, otherPath), nil),
		chromedp.Evaluate(`window.__detailWatch.mark = window.__detailWatch.reads`, nil),
		// The router's own navigate: a pushState and a popstate, no load.
		chromedp.Evaluate(fmt.Sprintf(`history.pushState(null, "", %q); dispatchEvent(new PopStateEvent("popstate"))`, otherPath), nil),
		pollUntil(detailReadLandedJS),
		chromedp.WaitVisible(`.mod-page__meta`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`window.__sawLocked`, &sawLocked),
		textContent(`.mod-page__meta`, &finalMeta),
	)
	assert.False(t, sawLocked, "the default profile's lock was on screen under the other profile's route")
	assert.NotContains(t, finalMeta, "locked", "the other profile's own detail answered")
	assert.Contains(t, finalMeta, "fake/a")
	assert.Empty(t, f.BrowserErrors())
}
