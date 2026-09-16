package serve_test

// The browser scenarios for the verify controls and the per-mod health state
// (#418).
//
// The owner's hand-test note: "No clear way for a user to validate (or see
// the current validation of) a single or all mods." Both halves of that are
// claims about the rendered page. The state half especially: a healthy mod
// and an unchecked one BOTH rendered as the absence of a warning badge, so
// the thing that was missing was visible only by looking at the page and
// asking what it was not saying.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// e2eRowHealth is what rowHealthJS reads back for one row.
type e2eRowHealth struct {
	State string `json:"state"`
	Badge string `json:"badge"`
	Title string `json:"title"`
}

// rowHealthJS reads one library row's health badge by the mod it names.
func rowHealthJS(name string) string {
	return `(() => {
		const row = Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes(` + strconvQuote(name) + `));
		const badge = row && row.querySelector('[data-testid="row-health"]');
		if (!badge) return null;
		return {
			state: badge.getAttribute("data-health"),
			badge: badge.textContent.trim(),
			title: badge.getAttribute("title"),
		};
	})()`
}

// TestE2E_RowHealthStateIsReadableAtAGlance is issue 418's state half: a row
// says which of the three things is true of it, rather than carrying a mark
// for one of them and nothing for the other two.
//
// The fixture has both cases in one library: Better Boots records 1.0 while
// its file now reports 2.0 (a version_mismatch finding), and the two
// conflict mods are checked and clean.
func TestE2E_RowHealthStateIsReadableAtAGlance(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var unhealthy, healthy e2eRowHealth
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		// The health read is one of Mission Control's supplementary
		// documents, so the rows start as "unknown" and settle once it
		// lands - which is itself the honest sequence.
		pollUntil(`document.querySelector('[data-testid="row-health"][data-health="issues"]') !== null`),
		chromedp.Evaluate(rowHealthJS("Better Boots"), &unhealthy),
		chromedp.Evaluate(rowHealthJS("Mod X"), &healthy),
	)

	assert.Equal(t, "issues", unhealthy.State)
	assert.Contains(t, unhealthy.Title, "health finding",
		"and the count is what the title says, not a bare word")

	require.NotEmpty(t, healthy.State, "a clean row carries a badge of its own now")
	assert.Equal(t, "ok", healthy.State,
		"a checked, clean mod says so - it used to be indistinguishable from one nothing had looked at")
	assert.Contains(t, healthy.Title, "no issues")
	assert.NotEqual(t, unhealthy.Badge, healthy.Badge,
		"and the two states are not the same glyph")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_VerifyIsReachableFromTheLibraryHeader is the control half. The
// Health card offers a re-verify, but an attention card renders only when it
// has something to say - so on a healthy profile, which is exactly when "is
// my install still OK?" has no other answer, there was no verify control on
// screen at all.
func TestE2E_VerifyIsReachableFromTheLibraryHeader(t *testing.T) {
	// A library with nothing wrong: no Health card renders for this one.
	f := newE2EFixtureWithLibrarySample(t)

	var cards int
	var label string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__toolbar [data-action="verify"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".card--health").length`, &cards),
		textContent(`.library__toolbar [data-action="verify"]`, &label),
	)
	require.Zero(t, cards, "this profile has nothing to report, so no Health card renders")
	assert.Equal(t, "Verify", label)

	// Pressing it re-runs the check and hands the control back.
	f.runInBrowser(t,
		chromedp.Click(`.library__toolbar [data-action="verify"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library__toolbar [data-action="verify"]').disabled === false`),
		pollUntil(`document.querySelectorAll('[data-testid="row-health"][data-health="ok"]').length === 3`),
	)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_RowMenuOffersVerifyAndRepair is the per-row half: the question
// gets asked at the row, so the actions are offered there. Verify is the
// whole-profile check (lmm has no per-mod one, and the control's title says
// so rather than implying otherwise); Repair really is per-mod, and opens
// the same verify_fix plan the Health card's own per-finding Repair does.
func TestE2E_RowMenuOffersVerifyAndRepair(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var items []string
	var verifyTitle string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="row-health"][data-health="issues"]') !== null`),
		chromedp.Evaluate(openRowMenuJS("Better Boots"), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(rowMenuItemsJS, &items),
		chromedp.Evaluate(`document.querySelector('[data-action="row-verify"]').getAttribute("title")`, &verifyTitle),
	)

	assert.Contains(t, items, "Verify")
	assert.Contains(t, items, "Repair…")
	assert.Contains(t, verifyTitle, "profile as a whole",
		"the control does not imply a per-mod verify lmm does not have")

	// Repair opens the plan scoped to THIS mod.
	var title string
	f.runInBrowser(t,
		chromedp.Click(`[data-action="row-repair"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="verify_fix"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="verify_fix"] .modal__title`, &title),
	)
	assert.Equal(t, "Repair Better Boots", title)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOverStatesThisModsHealth is the same state, on the drill-in.
// The Findings section renders only when there are findings, so a mod with
// none said nothing at all about its health - in both of the two very
// different cases where that is true.
func TestE2E_SlideOverStatesThisModsHealth(t *testing.T) {
	f := newE2EFixtureWithAttention(t)
	// The slide-over's ModDetail read is LIVE (unlike everything Mission
	// Control itself renders), and this fixture registers only Better Boots
	// in the source's catalog - so the healthy row this scenario also opens
	// needs an entry of its own, or its panel reports a genuine 404 that the
	// browser-error bar below would (rightly) fail on.
	src, err := f.Svc.GetSource("fake")
	require.NoError(t, err)
	fake, ok := src.(*fakeSource)
	require.True(t, ok)
	fake.addMod(fakeSourceMod{Mod: domain.Mod{
		ID: "x", SourceID: "fake", Name: "Mod X", Version: "1.0",
	}})

	var unhealthy, healthy string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "boots")),
		chromedp.WaitVisible(`[data-testid="mod-health"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="mod-health"]').getAttribute("data-health") === "issues"`),
		textContent(`[data-testid="mod-health"]`, &unhealthy),
		chromedp.Navigate(f.SlideOverPath("fake", "x")),
		chromedp.WaitVisible(`[data-testid="mod-health"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="mod-health"]').getAttribute("data-health") === "ok"`),
		textContent(`[data-testid="mod-health"]`, &healthy),
	)

	assert.Contains(t, unhealthy, "health finding")
	assert.Contains(t, healthy, "no issues")
	assert.Empty(t, f.BrowserErrors())
}
