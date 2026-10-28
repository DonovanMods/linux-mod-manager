package serve_test

// #502: a dropdown opens toward whichever side of its trigger has room.
//
// The activity tray used to be anchored with a hard-coded `right: 0` on the
// assumption that the bell sits at the far right of the top bar. The away
// header (Setup, search, the full mod page) puts the same bell near the LEFT,
// where a 26rem tray hung from its right edge ran off the left of the
// viewport. Layout is something only a browser computes, so these are E2E.

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// menuBox is one open dropdown's box plus its trigger's and the viewport's
// width, all in CSS pixels.
type menuBox struct {
	Left         float64 `json:"left"`
	Right        float64 `json:"right"`
	Width        float64 `json:"width"`
	TriggerLeft  float64 `json:"triggerLeft"`
	TriggerRight float64 `json:"triggerRight"`
	Viewport     float64 `json:"viewport"`
}

// inViewport reports whether the menu lies fully inside [0, viewport].
func (b menuBox) inViewport() bool {
	return b.Left >= 0 && b.Right <= b.Viewport
}

// menuBoxJS measures the open .picker__menu inside the element named by the
// container selector (%q) and that container's trigger.
const menuBoxJS = `(() => {
	const owner = document.querySelector(%q);
	const menu = owner.querySelector(".picker__menu");
	const trigger = owner.querySelector(".picker__trigger");
	const m = menu.getBoundingClientRect();
	const t = trigger.getBoundingClientRect();
	return { left: m.left, right: m.right, width: m.width,
		triggerLeft: t.left, triggerRight: t.right, viewport: window.innerWidth };
})()`

// openMenuAt opens the dropdown whose trigger is trigger (inside owner) on
// url at a width x 700 viewport and returns its box.
func openMenuAt(t *testing.T, f e2eMultiGameFixture, width int, url, owner, trigger string) menuBox {
	t.Helper()
	var box menuBox
	f.runInBrowser(t,
		chromedp.EmulateViewport(int64(width), 700),
		chromedp.Navigate(url),
		pollUntil(fmt.Sprintf(`document.querySelector(%q) !== null`, owner+" "+trigger)),
		clickWhenSettled(owner+" "+trigger),
		pollUntil(fmt.Sprintf(`document.querySelector(%q) !== null`, owner+" .picker__menu")),
		// The menu is placed in a layout effect, before paint; one frame
		// later nothing may still be moving.
		settleEffects(),
		chromedp.Evaluate(fmt.Sprintf(menuBoxJS, owner), &box),
	)
	return box
}

// The away header (setup) puts the bell near the left; its tray must not run
// off the left edge.
func TestE2E_Dropdown_AwayHeaderBellTrayStaysInViewport(t *testing.T) {
	f := newE2EMultiGameFixture(t)
	setup := f.BaseURL + "/g/game-a/default/setup?section=archive"
	box := openMenuAt(t, f, 1006, setup, ".activity-bell", ".activity-bell__trigger")
	assert.True(t, box.inViewport(),
		"the tray (%.0f..%.0f) lies inside the %.0fpx viewport", box.Left, box.Right, box.Viewport)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// The home top bar's bell is at the far right: no regression there.
func TestE2E_Dropdown_HomeBellTrayStaysInViewport(t *testing.T) {
	f := newE2EMultiGameFixture(t)
	box := openMenuAt(t, f, 1006, f.BaseURL+"/g/game-a/default", ".activity-bell", ".activity-bell__trigger")
	assert.True(t, box.inViewport(),
		"the tray (%.0f..%.0f) lies inside the %.0fpx viewport", box.Left, box.Right, box.Viewport)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// A phone-width window: every dropdown still fits.
func TestE2E_Dropdown_NarrowViewportKeepsEveryMenuInside(t *testing.T) {
	f := newE2EMultiGameFixture(t)
	home := f.BaseURL + "/g/game-a/default"
	setup := home + "/setup?section=archive"
	for name, tc := range map[string]struct{ url, owner, trigger string }{
		"home bell tray":   {home, ".activity-bell", ".activity-bell__trigger"},
		"away bell tray":   {setup, ".activity-bell", ".activity-bell__trigger"},
		"home game picker": {home, ".game-picker", ".game-picker__trigger"},
		"profile picker":   {home, ".profile-picker", ".profile-picker__trigger"},
	} {
		box := openMenuAt(t, f, 400, tc.url, tc.owner, tc.trigger)
		assert.True(t, box.inViewport(),
			"%s (%.0f..%.0f) lies inside the %.0fpx viewport", name, box.Left, box.Right, box.Viewport)
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// A picker that already fits keeps opening rightward from its trigger.
func TestE2E_Dropdown_PickerThatFitsKeepsItsSide(t *testing.T) {
	f := newE2EMultiGameFixture(t)
	home := f.BaseURL + "/g/game-a/default"
	for name, owner := range map[string]string{
		"game picker":    ".game-picker",
		"profile picker": ".profile-picker",
	} {
		box := openMenuAt(t, f, 1006, home, owner, owner+"__trigger")
		require.True(t, box.inViewport(), name)
		assert.InDelta(t, box.TriggerLeft, box.Left, 1,
			"%s opens flush with its trigger's left edge when it fits", name)
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// A menu that is open when the window shrinks is placed again.
func TestE2E_Dropdown_ResizeWhileOpenReplacesTheMenu(t *testing.T) {
	f := newE2EMultiGameFixture(t)
	setup := f.BaseURL + "/g/game-a/default/setup?section=archive"
	openMenuAt(t, f, 1006, setup, ".activity-bell", ".activity-bell__trigger")
	var box menuBox
	f.runInBrowser(t,
		chromedp.EmulateViewport(400, 700),
		settleEffects(),
		chromedp.Evaluate(fmt.Sprintf(menuBoxJS, ".activity-bell"), &box),
	)
	assert.True(t, box.inViewport(),
		"the open tray (%.0f..%.0f) follows the window down to %.0fpx", box.Left, box.Right, box.Viewport)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
