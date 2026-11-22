package serve_test

// #668: the Conflicts card's "Mark as intended", the collapsed "N known
// conflicts" line with its Unmark, the library's muted badge, and the
// reorder preview that shows a flipped winner as a conflict again.

import (
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_KnownConflicts_MarkAsIntendedAndUnmark(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="row-conflict"]`, chromedp.ByQuery),
		chromedp.Click(`.card--conflicts [data-action="mark-known"]`, chromedp.ByQuery),
		// The one conflict is known now: the card goes - it never counts
		// toward the attention area - and the quiet line holds it.
		waitGone(`.card--conflicts`),
		chromedp.WaitVisible(`[data-testid="known-conflicts"]`, chromedp.ByQuery),
		waitGone(`[data-testid="row-conflict"]`),
		chromedp.WaitVisible(`[data-testid="row-known-conflict"]`, chromedp.ByQuery),
	)
	// The mark is muted: the theme's muted text colour, not the danger tone.
	var muted bool
	f.runInBrowser(t, chromedp.Evaluate(`(() => {
		const probe = document.createElement("span");
		probe.style.color = "var(--text-muted)";
		document.body.appendChild(probe);
		const want = getComputedStyle(probe).color;
		probe.remove();
		return getComputedStyle(document.querySelector('[data-testid="row-known-conflict"]')).color === want;
	})()`, &muted))
	assert.True(t, muted, "the known-conflict badge is muted")

	profile, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "fake:y", Loser: "fake:x", Origin: domain.KnownConflictUser}}, profile.KnownConflicts,
		"Mark as intended marks the winner over the other mod")

	var summary string
	var open bool
	f.runInBrowser(t,
		textContent(`[data-testid="known-conflicts"] summary`, &summary),
		chromedp.Evaluate(`document.querySelector('[data-testid="known-conflicts"]').open`, &open),
	)
	assert.Equal(t, "1 known conflict", summary)
	assert.False(t, open, "the known conflicts start collapsed")

	var body string
	f.runInBrowser(t,
		chromedp.Click(`[data-testid="known-conflicts"] summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="known-conflicts"] [data-action="unmark-known"]`, chromedp.ByQuery),
		textContent(`[data-testid="known-conflicts"]`, &body),
	)
	assert.Contains(t, body, "shared.esp")
	assert.Contains(t, body, "wins: Mod Y")

	f.runInBrowser(t,
		chromedp.Click(`[data-testid="known-conflicts"] [data-action="unmark-known"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		waitGone(`[data-testid="known-conflicts"]`),
		chromedp.WaitVisible(`[data-testid="row-conflict"]`, chromedp.ByQuery),
	)
	profile, err = f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.KnownConflicts, "Unmark removes the pair")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_KnownConflicts_MarkAllAndTheReorderPreviewFlip(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.Click(`.card--conflicts [data-action="mark-all-known"]`, chromedp.ByQuery),
		waitGone(`.card--conflicts`),
		chromedp.WaitVisible(`[data-testid="known-conflicts"]`, chromedp.ByQuery),
	)
	conflicts, err := f.Svc.GetProfileConflicts(t.Context(), f.Game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.True(t, conflicts[0].Known)

	// The library's own Reorder…: the preview marks the current winner
	// known, and moving X below Y makes the conflict a conflict again.
	f.runInBrowser(t,
		chromedp.Click(`[data-action="reorder"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reorder-preview [data-testid="reorder-known"]`, chromedp.ByQuery),
	)
	var moved bool
	f.runInBrowser(t,
		chromedp.Evaluate(`
			(() => {
				const btn = Array.from(document.querySelectorAll(".reorder-row")).find((r) => r.textContent.includes("Mod X"))
					?.querySelector('[aria-label="Move Mod X to highest priority"]');
				if (!btn || btn.disabled) return false;
				btn.click();
				return true;
			})()
		`, &moved),
	)
	require.True(t, moved)
	f.runInBrowser(t, chromedp.WaitVisible(`.reorder-preview [data-testid="reorder-unknown"]`, chromedp.ByQuery))

	f.runInBrowser(t,
		chromedp.Click(`[data-action="save-order"]`, chromedp.ByQuery),
		waitGone(`[data-testid="reorder-list"]`),
		// Saved, the flipped conflict is back on the card.
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		c, err := f.Svc.GetProfileConflicts(t.Context(), f.Game, "default")
		return err == nil && len(c) == 1 && !c[0].Known && c[0].LoadOrderWinner.Key == "fake:x"
	}, 5*time.Second, 50*time.Millisecond, "X wins now, which was never marked")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_KnownConflicts_ModPanelSaysKnown: the mod panel lists a known
// conflict with its role and "known".
func TestE2E_KnownConflicts_ModPanelSaysKnown(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)
	_, err := f.Svc.MarkConflictsKnown(t.Context(), f.Game, "default", []core.ConflictPair{{Winner: "y", Loser: "x"}})
	require.NoError(t, err)

	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="row-known-conflict"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row__name")).find((n) => n.textContent.includes("Mod X")).click();
		`, nil),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over .plan__paths`, chromedp.ByQuery),
		textContent(`.slide-over`, &body),
	)
	assert.Contains(t, body, "shared.esp (loses to Mod Y · known)")
	// No BrowserErrors check here: the fixture's fake source does not
	// serve Mod X's detail, so the panel's own changelog read 404s - a
	// fixture fact, not this feature's.
}
