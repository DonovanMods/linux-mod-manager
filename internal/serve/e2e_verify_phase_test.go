package serve_test

// Issue 677's web half: the Health card's verify reported only its version
// pass, so its loading line held "Verifying 16 mods (16/16)…" while the
// deployed-content pass, the records pass and the convergence ran on -
// most of a long verify. A read_progress frame now names the pass
// (read_progress.go's Phase), and the line says that pass and its own
// position.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

// TestE2E_HealthLoadingLineNamesTheVerifyPhase renders Mission Control's
// pending-reads region with a Health load in flight and a frame from each
// verify phase, and reads the line the browser painted for each.
func TestE2E_HealthLoadingLineNamesTheVerifyPhase(t *testing.T) {
	f := newE2EFixture(t)

	frames := []struct{ name, frame, want string }{
		{"versions", `{ phase: "versions", index: 16, total: 16, mod_name: "A" }`, "Verifying 16 mods (16/16)…"},
		{"files", `{ phase: "files", index: 3, total: 16 }`, "Checking downloaded files (3/16)…"},
		{"deployed", `{ phase: "deployed", index: 1200, total: 23236 }`, "Checking deployed files (1,200/23,236)…"},
		{"blocked", `{ phase: "blocked", index: 4, total: 16 }`, "Checking mods for paths lmm could not deploy (4/16)…"},
		{"records", `{ phase: "records", index: 0, total: 0 }`, "Checking the profile's deployment records…"},
		{"convergence", `{ phase: "convergence", index: 0, total: 0 }`, "Checking for stale deployed files…"},
	}

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { PendingReads } = await import("/static/app/components/pendingreads.js");
			const container = document.createElement("div");
			document.body.appendChild(container);
			window.__phaseLine = (frame) => render(h(PendingReads, {
				state: { loading: { health: { token: "t677" } }, readProgress: { t677: { token: "t677", read: "health", ...frame } } },
				mods: null,
			}), container);
			window.__phaseText = () => container.querySelector('[data-pending-read="health"] .pending-read__text')?.textContent ?? "";
		})()`, nil, awaitPromise),
	)
	for _, tc := range frames {
		var got string
		f.runInBrowser(t,
			chromedp.Evaluate(`window.__phaseLine(`+tc.frame+`)`, nil),
			pollUntil(`window.__phaseText() === `+jsString(tc.want)),
			chromedp.Evaluate(`window.__phaseText()`, &got),
		)
		assert.Equal(t, tc.want, got, tc.name)
	}

	var bar map[string]bool
	f.runInBrowser(t, chromedp.Evaluate(`(() => {
		const el = document.querySelector('[data-pending-read="health"] .job-progress__bar');
		return { indeterminate: el.classList.contains("job-progress__bar--indeterminate") };
	})()`, &bar))
	assert.True(t, bar["indeterminate"], "a pass with no position has no fraction to fill")
	assert.Empty(t, f.BrowserErrors())
}
