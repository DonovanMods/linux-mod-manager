package serve_test

// TestE2E_ActivityTray_KeepsItsSizeThroughEveryFrame is issue 670, the
// activity tray's share of issue 665's cause: the tray set only a
// min-width, so it grew to fit a running row's one nowrap detail line, and
// that line - "<phase> · <mod name> · i of n · <bytes>" - changes length
// with every frame of a multi-mod job. The dropdown's width (and, placed
// against the viewport edge, its position) jumped while the job ran.
//
// The test renders the real ActivityBell, open, anchored top right as the
// bell is on Mission Control, and drives one running job through frames
// with short, long and unbroken mod names, with and without download bytes,
// with an update's step and an event's own detail, and with no frame at
// all ("Working…"), measuring the tray's and the row's boxes after each.
// Every box must be the same one, at a desktop width and at a phone width,
// where the tray is the viewport less its gutters. Each detail line's full
// text stays in its title.

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trayBoxes is one frame's measurement: the tray's box and its row's.
type trayBoxes struct {
	Tray, Row toastBox
}

// traySizeView is what trayStableSizeJS reports.
type traySizeView struct {
	Frames   []trayBoxes
	Titles   []string
	RootFont float64
	Gutter   float64
	Viewport float64
}

const trayStableSizeJS = `(async () => {
	const { render, h } = await import("/static/app/render.js");
	const { ActivityBell } = await import("/static/app/components/tray.js");
	const { gutterPx } = await import("/static/app/menuplacement.js");
	// The top bar's own corner: the bell at the right edge, so the tray is
	// placed against the viewport as it is on Mission Control.
	const container = document.createElement("div");
	container.style.cssText = "position: fixed; top: 0.5rem; right: 0.5rem; z-index: 100;";
	document.body.appendChild(container);
	const settle = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
	const box = (el) => {
		const r = el.getBoundingClientRect();
		return { X: r.x, Y: r.y, Width: r.width, Height: r.height, Text: el.textContent.replace(/\s+/g, " ").trim() };
	};

	const job = { id: "job-670", kind: "profile_import", state: "running", event_count: 1, game: "e2e", profile: "default" };
	const longName = "Welcome to Night City - Extended Ultra Realistic Lighting Overhaul and Weather Pack (Main File)";
	const unbroken = "AnExtremelyLongModNameWithNoSpacesAtAllThatCannotWrapAnywhereWhatsoever_v1.2.3";
	const steps = [
		[undefined, undefined],
		[{ type: "progress", phase: "profile_import_installing", mod_name: "A", index: 1, total: 9 }, undefined],
		[{ type: "download", phase: "downloading", mod_name: longName, index: 9, total: 9, total_bytes: 123456789, percent: 42 }, undefined],
		[{ type: "download", mod_name: "B", index: 10, total: 600, downloaded: 5000 }, undefined],
		[{ type: "progress", phase: "deploying", mod_name: unbroken, index: 99, total: 600 }, undefined],
		[{ type: "download", mod_name: unbroken, index: 100, total: 600, total_bytes: 9, percent: 100 }, undefined],
		[{ type: "progress", op: "update", phase: "update_installing", mod_name: longName }, { op: "update", index: 3, total: 12, mod_name: longName }],
		[{ type: "warning", phase: "installing", mod_name: "C", detail: "the archive held a second copy of a file another mod in this profile already deploys, so it was left as it was" }, undefined],
		[{ type: "progress", phase: "installing", index: 600, total: 600 }, undefined],
	];
	const frames = [];
	const titles = [];
	for (const [frame, position] of steps) {
		const state = {
			jobsIndex: [job],
			jobProgress: frame ? { [job.id]: frame } : {},
			jobPositions: position ? { [job.id]: position } : {},
			liveTodos: {},
			activityAckedAt: 0,
		};
		render(h(ActivityBell, { state, open: true, onOpen() {}, onClose() {}, actions: {} }), container);
		await settle();
		const tray = container.querySelector(".tray");
		// The drop-in (menu-drop) is not a resize: measure the tray at rest.
		await Promise.all(tray.getAnimations().map((a) => a.finished.catch(() => {})));
		await settle();
		const row = tray.querySelector('.tray__row[data-state="running"]');
		frames.push({ Tray: box(tray), Row: box(row) });
		for (const el of row.querySelectorAll(".tray__progress [title]")) titles.push(el.getAttribute("title"));
	}
	const gutter = gutterPx(container.querySelector(".tray"));
	render(null, container);
	container.remove();
	return {
		Frames: frames, Titles: titles,
		RootFont: parseFloat(getComputedStyle(document.documentElement).fontSize),
		Gutter: gutter, Viewport: document.documentElement.clientWidth,
	};
})()`

func TestE2E_ActivityTray_KeepsItsSizeThroughEveryFrame(t *testing.T) {
	for _, width := range []int64{1280, 360} {
		t.Run(fmt.Sprintf("%dpx", width), func(t *testing.T) {
			f := newE2EFixture(t)
			var view traySizeView
			f.runInBrowser(t,
				chromedp.EmulateViewport(width, 900),
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
				chromedp.Evaluate(trayStableSizeJS, &view, awaitPromise),
			)
			require.NotEmpty(t, view.Frames)
			first := view.Frames[0]
			for i, got := range view.Frames {
				assert.Equalf(t, first.Tray.Width, got.Tray.Width, "frame %d (%q) changed the tray's width", i, got.Row.Text)
				assert.Equalf(t, first.Tray.Height, got.Tray.Height, "frame %d (%q) changed the tray's height", i, got.Row.Text)
				assert.Equalf(t, first.Tray.X, got.Tray.X, "frame %d (%q) moved the tray sideways", i, got.Row.Text)
				assert.Equalf(t, first.Tray.Y, got.Tray.Y, "frame %d (%q) moved the tray up or down", i, got.Row.Text)
				assert.Equalf(t, first.Row.Width, got.Row.Width, "frame %d (%q) changed the row's width", i, got.Row.Text)
				assert.Equalf(t, first.Row.Height, got.Row.Height, "frame %d (%q) changed the row's height", i, got.Row.Text)
				assert.Equalf(t, first.Row.X, got.Row.X, "frame %d (%q) moved the row sideways", i, got.Row.Text)
				assert.Equalf(t, first.Row.Y, got.Row.Y, "frame %d (%q) moved the row up or down", i, got.Row.Text)
			}

			// 26rem, or the viewport less both gutters when that is narrower,
			// and inside the viewport either way.
			want := min(26*view.RootFont, view.Viewport-2*view.Gutter)
			assert.InDelta(t, want, first.Tray.Width, 0.5, "the tray is 26rem wide, capped at the viewport less its gutters")
			assert.GreaterOrEqual(t, first.Tray.X, view.Gutter-0.5, "the tray keeps its left gutter")
			assert.LessOrEqual(t, first.Tray.X+first.Tray.Width, view.Viewport-view.Gutter+0.5, "the tray keeps its right gutter")

			// The ellipsised text is still there in full, in a title.
			assert.Contains(t, view.Titles, "Downloading · "+"Welcome to Night City - Extended Ultra Realistic Lighting Overhaul and Weather Pack (Main File)"+" · 9 of 9")
			assert.Contains(t, view.Titles, "42% of 118 MB")
			assert.Contains(t, view.Titles, "the archive held a second copy of a file another mod in this profile already deploys, so it was left as it was")
			assert.Empty(t, f.BrowserErrors())
		})
	}
}
