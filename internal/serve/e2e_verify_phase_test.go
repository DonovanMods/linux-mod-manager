package serve_test

// Issue 677's web half: the Health card's verify reported only its version
// pass, so its loading line held "Verifying 16 mods (16/16)…" while the
// deployed-content pass, the records pass and the convergence ran on -
// most of a long verify. A read_progress frame now names the pass
// (read_progress.go's Phase), and the line says that pass and its own
// position.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
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
		// A pass a newer core names that this page does not know: its
		// position is still said, without calling the files mods.
		{"unknown", `{ phase: "someday", index: 1200, total: 23236 }`, "Verifying your mods (1,200/23,236)…"},
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

// TestE2E_VerifyFixTrayRowNamesThePass (#677 review): a verify --fix job's
// tray row read "1200 of 23236" with no pass named, because its frame
// carried no phase. The frame now carries "verify_" + the pass, and the row
// says it in the Health loading line's words.
func TestE2E_VerifyFixTrayRowNamesThePass(t *testing.T) {
	f := newE2EFixture(t)

	var got map[string]string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { progressText } = await import("/static/app/progress.js");
			const frame = (phase, index, total, mod_name) => progressText({ type: "verify", op: "verify", phase, index, total, mod_name });
			return {
				versions: frame("verify_versions", 3, 16, "Mod A"),
				files: frame("verify_files", 2, 4),
				deployed: frame("verify_deployed", 1200, 23236),
				blocked: frame("verify_blocked", 4, 16),
				records: frame("verify_records", 0, 0),
				convergence: frame("verify_convergence", 0, 0),
			};
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	assert.Equal(t, map[string]string{
		"versions":    "Verifying mods · Mod A · 3 of 16",
		"files":       "Checking downloaded files · 2 of 4",
		"deployed":    "Checking deployed files · 1200 of 23236",
		"blocked":     "Checking mods for paths lmm could not deploy · 4 of 16",
		"records":     "Checking the profile's deployment records",
		"convergence": "Checking for stale deployed files",
	}, got)
	assert.Empty(t, f.BrowserErrors())
}
