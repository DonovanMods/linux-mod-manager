package serve_test

// TestE2E_RunningToast_KeepsItsSizeThroughEveryFrame is issue 665: the
// running-job toast resized on every progress frame of a multi-mod install
// (a collection import), so it flickered and could not be read. The toast
// stack shrink-wrapped its content (max-width and no width) and the detail
// line wrapped freely, while its text - "i of n · <mod name> · downloading
// <bytes>" - changes length with every mod. Anchored bottom right, every
// change in width or line count moved the card.
//
// The test renders the real Toasts component and drives one running toast
// through frames with short and long mod names (one with no spaces to break
// at), with and without download bytes, an uncounted phase, an update's
// step and the "Starting…" of no frame at all, measuring the card's box
// after each. Every box must be the same one, at a desktop width and at a
// phone width (where the toast is the viewport less its gutters, never
// wider). The full text of each line stays in its title.

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toastBox is one measurement of a toast's bounding box.
type toastBox struct {
	X, Y, Width, Height float64
	Text                string
}

// toastSizeView is what toastStableSizeJS reports.
type toastSizeView struct {
	Boxes      []toastBox
	Titles     []string
	RootFont   float64
	Gutter     float64
	Viewport   float64
	OtherWidth float64
	GroupWidth float64
}

const toastStableSizeJS = `(async () => {
	const { render, h } = await import("/static/app/render.js");
	const { Toasts } = await import("/static/app/components/toasts.js");
	const container = document.createElement("div");
	document.body.appendChild(container);
	const settle = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));

	const toast = { id: "t665", tone: "running", jobID: "job-665", kind: "profile_import", collection: true };
	const route = { game: "e2e", view: "home" };
	const longName = "Welcome to Night City - Extended Ultra Realistic Lighting Overhaul and Weather Pack (Main File)";
	const unbroken = "AnExtremelyLongModNameWithNoSpacesAtAllThatCannotWrapAnywhereWhatsoever_v1.2.3";
	const steps = [
		[undefined, undefined],
		[{ type: "progress", phase: "profile_import_installing" }, { index: 1, total: 9, mod_name: "A" }],
		[{ type: "download", total_bytes: 123456789, percent: 42 }, { index: 9, total: 9, mod_name: longName }],
		[{ type: "download", downloaded: 5000 }, { index: 10, total: 600, mod_name: "B" }],
		[{ type: "progress", phase: "deploying" }, { index: 99, total: 600, mod_name: unbroken }],
		[{ type: "download", total_bytes: 9, percent: 100 }, { index: 100, total: 600, mod_name: unbroken }],
		[{ type: "progress", phase: "deploying" }, undefined],
		[{ type: "progress", op: "update", phase: "update_installing" }, { op: "update", index: 3, total: 12, mod_name: longName }],
		[{ type: "download", op: "update", phase: "update_downloading", total_bytes: 2048000, percent: 7 }, { op: "update", index: 4, total: 12, mod_name: "C" }],
		[{ type: "progress", phase: "installing" }, { index: 600, total: 600, phase: "installing" }],
	];
	const boxes = [];
	const titles = [];
	for (const [frame, position] of steps) {
		render(h(Toasts, {
			toasts: [toast], route, onDismiss: () => {}, actions: {},
			progress: frame ? { "job-665": frame } : {},
			positions: position ? { "job-665": position } : {},
		}), container);
		await settle();
		const card = container.querySelector(".toast--running");
		// The entrance (toast-in's translateY) is not a resize: measure
		// the card where it rests.
		await Promise.all(card.getAnimations().map((a) => a.finished.catch(() => {})));
		const r = card.getBoundingClientRect();
		const progress = card.querySelector('[data-testid="toast-progress"]');
		boxes.push({ X: r.x, Y: r.y, Width: r.width, Height: r.height, Text: progress.textContent.replace(/\s+/g, " ").trim() });
		for (const el of progress.querySelectorAll("[title]")) titles.push(el.getAttribute("title"));
	}

	// The other toast kinds share the stack, so they share its width.
	render(h(Toasts, {
		toasts: [{ id: "ok", tone: "success", title: "Done" }], route, onDismiss: () => {}, actions: {},
	}), container);
	await settle();
	const otherWidth = container.querySelector(".toast--success").getBoundingClientRect().width;
	const group = [1, 2, 3, 4].map((i) => ({ id: "g" + i, tone: "running", jobID: "gj" + i, kind: i % 2 ? "deploy" : "profile_import", collection: true }));
	render(h(Toasts, { toasts: group, route, onDismiss: () => {}, actions: {}, progress: {}, positions: {} }), container);
	await settle();
	const groupWidth = container.querySelector(".toast--running").getBoundingClientRect().width;

	render(null, container);
	container.remove();
	const css = getComputedStyle(document.documentElement);
	const rootFont = parseFloat(css.fontSize);
	return {
		Boxes: boxes, Titles: titles, RootFont: rootFont,
		Gutter: 1.5 * rootFont, Viewport: document.documentElement.clientWidth,
		OtherWidth: otherWidth, GroupWidth: groupWidth,
	};
})()`

func TestE2E_RunningToast_KeepsItsSizeThroughEveryFrame(t *testing.T) {
	for _, width := range []int64{1280, 360} {
		t.Run(fmt.Sprintf("%dpx", width), func(t *testing.T) {
			f := newE2EFixture(t)
			var view toastSizeView
			f.runInBrowser(t,
				chromedp.EmulateViewport(width, 900),
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
				chromedp.Evaluate(toastStableSizeJS, &view, awaitPromise),
			)
			require.NotEmpty(t, view.Boxes)
			first := view.Boxes[0]
			for i, box := range view.Boxes {
				assert.Equalf(t, first.Width, box.Width, "frame %d (%q) changed the toast's width", i, box.Text)
				assert.Equalf(t, first.Height, box.Height, "frame %d (%q) changed the toast's height", i, box.Text)
				assert.Equalf(t, first.X, box.X, "frame %d (%q) moved the toast sideways", i, box.Text)
				assert.Equalf(t, first.Y, box.Y, "frame %d (%q) moved the toast up or down", i, box.Text)
			}

			// 24rem, or the viewport less both gutters when that is narrower.
			want := min(24*view.RootFont, view.Viewport-2*view.Gutter)
			assert.InDelta(t, want, first.Width, 0.5, "a running toast is 24rem wide, capped at the viewport less its gutters")
			assert.InDelta(t, want, view.OtherWidth, 0.5, "a completion toast has the stack's one width")
			assert.InDelta(t, want, view.GroupWidth, 0.5, "the grouped running toast has the stack's one width")

			// The ellipsised text is still there in full, in a title.
			assert.Contains(t, view.Titles, "9 of 9 · Welcome to Night City - Extended Ultra Realistic Lighting Overhaul and Weather Pack (Main File)")
			assert.Contains(t, view.Titles, "99 of 600 · AnExtremelyLongModNameWithNoSpacesAtAllThatCannotWrapAnywhereWhatsoever_v1.2.3")
			assert.Contains(t, view.Titles, "downloading 42% of 118 MB")
			assert.Empty(t, f.BrowserErrors())
		})
	}
}
