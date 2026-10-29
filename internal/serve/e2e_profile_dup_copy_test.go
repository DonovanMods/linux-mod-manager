package serve_test

// Issues 437 and 438 in the browser: the profiles modal's Duplicate, and
// "Copy to profile…" from a library row's menu and from the mod panel -
// menu, destination, plan, confirm, job, end state.

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// newE2EFixtureWithCopyableMods is newE2EFixture with two mods installed,
// cached and listed in the active "default" - "Copy Me", and "Both Mod",
// which profile "alt" lists too - and a third, empty profile "spare".
func newE2EFixtureWithCopyableMods(t *testing.T) e2eFixture {
	t.Helper()
	src := newFakeSource("fake")
	copyMe := domain.Mod{ID: "copyme", SourceID: "fake", Name: "Copy Me", Version: "1.0"}
	both := domain.Mod{ID: "both", SourceID: "fake", Name: "Both Mod", Version: "2.0"}
	src.addMod(fakeSourceMod{Mod: copyMe}).addMod(fakeSourceMod{Mod: both})
	f := newE2EFixtureFromSource(t, src)

	ctx := t.Context()
	pm := f.Svc.NewProfileManager()
	for _, m := range []domain.Mod{copyMe, both} {
		m.GameID = f.Game.ID
		seedInstalledMod(t, f.Svc, f.Game, m, true, map[string][]byte{m.ID + ".pak": []byte(m.ID)})
		require.NoError(t, pm.AddMod(ctx, f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: m.ID, Version: m.Version}))
	}
	for _, name := range []string{"alt", "spare"} {
		_, err := pm.Create(ctx, f.Game.ID, name)
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(ctx, f.Game.ID, "alt", domain.ModReference{SourceID: "fake", ModID: "both", Version: "1.0"}))
	return f
}

// TestE2E_ProfilesModal_DuplicateCopiesTheProfile: Duplicate opens an
// inline form on a suggested name; saving it adds the copy to the list, and
// the copy is on disk with the source's mods and rows.
func TestE2E_ProfilesModal_DuplicateCopiesTheProfile(t *testing.T) {
	f := newE2EFixtureWithCopyableMods(t)

	var suggested, note string
	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".profile-picker__menu button"))
			.find((b) => b.textContent.includes("Manage profiles")).click()`, nil),
		pollUntil(`document.querySelectorAll(".profiles-row").length === 3`),
		chromedp.Click(`[data-action="duplicate-profile"][data-profile="default"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="profile-duplicate-row"] input[name="duplicate-name"]') !== null`),
		chromedp.Evaluate(`document.querySelector('input[name="duplicate-name"]').value`, &suggested),
		textContent(`[data-testid="profile-duplicate-row"] .profiles-row__note`, &note),
		chromedp.Click(`[data-action="confirm-duplicate"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".profiles-row").length === 4`),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".profiles-row__name")).map((n) => n.firstChild.textContent.trim())`, &names),
	)
	assert.Equal(t, "default-copy", suggested, "the form opens on a suggested name")
	assert.Contains(t, note, "Nothing is deployed until you switch to it")
	assert.Contains(t, names, "default-copy")

	dup, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default-copy")
	require.NoError(t, err)
	assert.False(t, dup.IsDefault)
	require.Len(t, dup.Mods, 2)
	rows, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "default-copy")
	require.NoError(t, err)
	assert.Len(t, rows, 2, "the duplicate's rows exist from the moment it does")

	// The top bar's picker learns the new profile too (afterMutation reloads
	// the status document).
	var picker []string
	f.runInBrowser(t,
		chromedp.Click(`.modal .modal__close`, chromedp.ByQuery),
		waitGone(`.modal`),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".profile-picker__menu [data-profile]")).map((li) => li.dataset.profile)`, &picker),
	)
	assert.Contains(t, picker, "default-copy")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CopyToProfile_FromTheRowMenuAndTheModPanel drives both entry
// points end to end: the row menu's copy into an inactive profile (with the
// destination changed in the modal, which re-plans), and the mod panel's
// copy into a profile that already lists the mod, whose Confirm reads
// "Replace" and replaces it.
func TestE2E_CopyToProfile_FromTheRowMenuAndTheModPanel(t *testing.T) {
	f := newE2EFixtureWithCopyableMods(t)

	var initialTo, replannedTo, destination, confirmLabel, summary, mods string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`Array.from(document.querySelectorAll(".mod-row")).some((r) => r.textContent.includes("Copy Me"))`),
		chromedp.Evaluate(openRowMenuJS("Copy Me"), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Click(`.row-menu [data-action="copy-to-profile"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.modal[data-kind="mod_copy"] [data-testid="copy-destination"]') !== null`),
		chromedp.Evaluate(`document.querySelector('select[name="copy-to"]').value`, &initialTo),
	)
	require.Contains(t, []string{"alt", "spare"}, initialTo, "the modal opens on one of the game's OTHER profiles")
	other := "alt"
	if initialTo == "alt" {
		other = "spare"
	}
	// Change the destination: the select re-plans.
	f.runInBrowser(t,
		chromedp.Evaluate(`(() => {
			const sel = document.querySelector('select[name="copy-to"]');
			sel.value = `+jsString(other)+`;
			sel.dispatchEvent(new Event("change", { bubbles: true }));
		})()`, nil),
		pollUntil(`(document.querySelector('.modal[data-kind="mod_copy"] .plan__summary')?.textContent ?? "").replace(/\s+/g, " ").includes("into `+other+`") && document.querySelector('.modal [data-action="confirm"]:not([disabled])') !== null`),
		chromedp.Evaluate(`document.querySelector('select[name="copy-to"]').value`, &replannedTo),
		textContent(`.modal [data-testid="copy-destination"]`, &destination),
		textContent(`.modal .plan__summary`, &summary),
		textContent(`.modal [data-testid="copy-mods"]`, &mods),
		textContent(`.modal [data-action="confirm"]`, &confirmLabel),
	)
	require.NotEqual(t, initialTo, replannedTo, "the destination select re-planned")
	assert.Equal(t, "Copying Copy Me from profile default into "+replannedTo+".", strings.Join(strings.Fields(summary), " "),
		"the summary reads as a sentence - no words fused across a line break")
	assert.Contains(t, destination, "is not the active profile")
	assert.Contains(t, mods, "1.0", "the pinned version is shown")
	assert.Equal(t, "Copy", strings.TrimSpace(confirmLabel))

	f.runInBrowser(t,
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	require.Eventually(t, func() bool {
		_, err := f.Svc.GetInstalledMod(t.Context(), "fake", "copyme", f.Game.ID, replannedTo)
		return err == nil
	}, e2eTimeout, e2ePollInterval, "the copy is recorded in the chosen profile")
	dest, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, replannedTo)
	require.NoError(t, err)
	assert.NotNil(t, dest.FindRef("fake", "copyme"))

	// The mod panel: "Both Mod" into alt, which lists it already.
	var listed, replaceLabel string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "both")),
		pollUntil(`document.querySelector('.slide-over [data-action="copy-to-profile"]') !== null`),
		clickWhenSettled(`.slide-over [data-action="copy-to-profile"]`),
		pollUntil(`document.querySelector('.modal[data-kind="mod_copy"] select[name="copy-to"]') !== null`),
		chromedp.Evaluate(`(() => {
			const sel = document.querySelector('select[name="copy-to"]');
			if (sel.value !== "alt") {
				sel.value = "alt";
				sel.dispatchEvent(new Event("change", { bubbles: true }));
			}
		})()`, nil),
		pollUntil(`document.querySelector('.modal [data-testid="copy-already-listed"]') !== null && document.querySelector('.modal [data-action="confirm"]:not([disabled])') !== null`),
		textContent(`.modal [data-testid="copy-already-listed"]`, &listed),
		textContent(`.modal [data-action="confirm"]`, &replaceLabel),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	assert.Contains(t, listed, "alt already lists it at 1.0")
	assert.Equal(t, "Replace", strings.TrimSpace(replaceLabel), "Confirm is the replace")
	require.Eventually(t, func() bool {
		row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "both", f.Game.ID, "alt")
		return err == nil && row.Version == "2.0"
	}, e2eTimeout, e2ePollInterval, "the replace records default's version in alt")
	alt, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "alt")
	require.NoError(t, err)
	ref := alt.FindRef("fake", "both")
	require.NotNil(t, ref)
	assert.Equal(t, "2.0", ref.Version, "the ref is replaced in place")
	assert.Empty(t, f.BrowserErrors())
}
