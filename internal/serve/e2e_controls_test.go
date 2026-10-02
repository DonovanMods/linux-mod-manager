package serve_test

// #518: the control system as a browser computes it. The CSS ratchets
// (control_system_test.go) prove no rule reshapes a button by hand; only a
// browser can say the result lines up - that a select, a search field and
// a button in one bar really do come out the same height.

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/css"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// barControlsJS measures every visible control - button, select, text-like
// input - inside each of the two bars, and reads the facts the control
// system promises about the top bar's own triggers and search box.
const barControlsJS = `(() => {
	const visible = (el) => {
		const r = el.getBoundingClientRect();
		const s = getComputedStyle(el);
		return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none";
	};
	const controls = (sel) => {
		const root = document.querySelector(sel);
		if (!root) return null;
		return [...root.querySelectorAll('button, select, input:not([type="checkbox"]):not([type="radio"]):not([type="file"])')]
			.filter(visible)
			.map((el) => ({
				what: el.tagName.toLowerCase() + (el.className ? "." + String(el.className).trim().split(/\s+/).join(".") : "") +
					(el.dataset.action ? '[data-action="' + el.dataset.action + '"]' : "") +
					(el.dataset.picker ? '[data-picker="' + el.dataset.picker + '"]' : ""),
				height: el.getBoundingClientRect().height,
			}));
	};
	const quiet = (sel) => {
		const el = document.querySelector(sel);
		return el !== null && el.classList.contains("button") && el.classList.contains("button--quiet");
	};
	return {
		bar: controls(".app-bar"),
		toolbar: controls(".library__toolbar"),
		quietTriggers: {
			game: quiet('[data-picker="game"]'),
			profile: quiet('[data-picker="profile"]'),
			activity: quiet('[data-picker="activity"]'),
			addMods: quiet('[data-action="add-mods"]'),
		},
		ownClears: [...document.querySelectorAll(".app-bar__search .omnibar__clear")].filter(visible).length,
	};
})()`

type barControl struct {
	What   string  `json:"what"`
	Height float64 `json:"height"`
}

type barControlsReport struct {
	Bar           []barControl    `json:"bar"`
	Toolbar       []barControl    `json:"toolbar"`
	QuietTriggers map[string]bool `json:"quietTriggers"`
	OwnClears     int             `json:"ownClears"`
}

// nativeSearchCancelDisplay reports the computed display of the browser's
// own search-cancel button inside the search input whose class list
// includes class: the element lives in the input's user-agent shadow tree,
// which getComputedStyle(input, "::-webkit-search-cancel-button") does not
// resolve (it answers with the input's own style), so it is read over the
// DevTools protocol instead. "" means no such input or no such button.
func nativeSearchCancelDisplay(class string, display *string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		root, err := dom.GetDocument().WithDepth(-1).WithPierce(true).Do(ctx)
		if err != nil {
			return err
		}
		var cancel *cdp.Node
		var find func(n *cdp.Node)
		find = func(n *cdp.Node) {
			if n.NodeName == "INPUT" && n.AttributeValue("type") == "search" &&
				slices.Contains(strings.Fields(n.AttributeValue("class")), class) {
				for _, shadow := range n.ShadowRoots {
					findCancel(shadow, &cancel)
				}
			}
			for _, c := range n.Children {
				find(c)
			}
		}
		find(root)
		if cancel == nil {
			return nil
		}
		style, _, err := css.GetComputedStyleForNode(cancel.NodeID).Do(ctx)
		if err != nil {
			return err
		}
		for _, p := range style {
			if p.Name == "display" {
				*display = p.Value
			}
		}
		return nil
	})
}

// findCancel walks a user-agent shadow tree for the search-cancel button.
func findCancel(n *cdp.Node, out **cdp.Node) {
	if *out != nil {
		return
	}
	if n.AttributeValue("pseudo") == "-webkit-search-cancel-button" {
		*out = n
		return
	}
	for _, c := range n.Children {
		findCancel(c, out)
	}
	for _, c := range n.ShadowRoots {
		findCancel(c, out)
	}
}

// assertOneHeight fails unless every control shares the first's height to
// within half a pixel.
func assertOneHeight(t *testing.T, where string, controls []barControl) {
	t.Helper()
	require.NotEmpty(t, controls, "%s: no visible controls measured", where)
	want := controls[0].Height
	for _, c := range controls {
		assert.LessOrEqual(t, math.Abs(c.Height-want), 0.5,
			"%s: %s is %.2fpx tall, %s is %.2fpx - controls in one bar share the control height",
			where, c.What, c.Height, controls[0].What, want)
	}
}

// TestE2E_BarControlsShareOneHeight opens Mission Control with a query in the
// omnibar (so its clear and "search sources" controls render too) in each
// theme, and holds the top bar and the library toolbar to one control
// height each - the same one - with the pickers and the add-mods menu on the
// quiet variant and exactly one clear control in the search box.
func TestE2E_BarControlsShareOneHeight(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	for _, theme := range []string{"light", "dark"} {
		t.Run(theme, func(t *testing.T) {
			var got barControlsReport
			var cancelDisplay string
			f.runInBrowser(t,
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
				chromedp.WaitVisible(`.library__toolbar`, chromedp.ByQuery),
				chromedp.Evaluate(`document.documentElement.setAttribute("data-theme", "`+theme+`")`, nil),
				chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
				chromedp.WaitVisible(`.omnibar__clear`, chromedp.ByQuery),
				chromedp.Evaluate(barControlsJS, &got),
				css.Enable(),
				nativeSearchCancelDisplay("omnibar", &cancelDisplay),
			)

			assertOneHeight(t, theme+" top bar", got.Bar)
			assertOneHeight(t, theme+" library toolbar", got.Toolbar)
			if len(got.Bar) > 0 && len(got.Toolbar) > 0 {
				assert.InDelta(t, got.Bar[0].Height, got.Toolbar[0].Height, 0.5,
					"%s: the top bar and the library toolbar use the same default control height", theme)
			}
			for trigger, quiet := range got.QuietTriggers {
				assert.True(t, quiet, "%s: the %s trigger is a quiet button (button button--quiet)", theme, trigger)
			}
			assert.Equal(t, 1, got.OwnClears, "%s: the search box has its own clear control", theme)
			assert.Equal(t, "none", cancelDisplay,
				"%s: the browser's own search-cancel is hidden, so the search box shows one clear control, not two", theme)
		})
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
