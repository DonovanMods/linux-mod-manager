package serve_test

// TestE2E_JobProgress_FetchesAFinishedJobsResultOnce is review F5: a
// finished job now fetches its result document twice. jobprogress.js's
// JobProgress calls BOTH useJobResultTally and useJobResultWarnings (C1's
// own unconditional-call rule) for the same jobID in the same render, so
// both hooks' effects fire together, before either's fetch has resolved -
// jobresult.js's own tallyCache is filled only inside the fetch's .then, so
// neither effect sees the other's request in flight. jobresult.js's doc
// comment already claims "fetches its result exactly once"; this pins it.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

func TestE2E_JobProgress_FetchesAFinishedJobsResultOnce(t *testing.T) {
	f := newE2EFixture(t)

	var fetches int
	var text string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			window.__jobResultFetches = 0;
			const real = window.fetch.bind(window);
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/jobs/fake-job-1")) {
					window.__jobResultFetches++;
					return Promise.resolve(new Response(JSON.stringify({
						id: "fake-job-1", kind: "deploy", state: "succeeded",
						result: { applied: [{ mod_id: "m", source_id: "s" }], failed: [] },
					}), { status: 200, headers: { "Content-Type": "application/json" } }));
				}
				return real(input, init);
			};
		})()`, nil),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { JobProgress } = await import("/static/app/components/jobprogress.js");

			const container = document.createElement("div");
			document.body.appendChild(container);
			window.__jobProgressContainer = container;

			render(h(JobProgress, {
				jobID: "fake-job-1",
				summary: { id: "fake-job-1", kind: "deploy", state: "succeeded" },
				frame: {},
				actions: null,
				onDismiss: () => {},
			}), container);
		})()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		pollUntil(`(() => {
			const el = window.__jobProgressContainer.querySelector(".job-progress__text");
			return Boolean(el) && el.textContent.trim() !== "" && el.textContent.trim() !== "Done";
		})()`),
		chromedp.Evaluate(`window.__jobProgressContainer.querySelector(".job-progress__text").textContent`, &text),
		chromedp.Evaluate(`window.__jobResultFetches`, &fetches),
		chromedp.Evaluate(`(() => { window.__jobProgressContainer.remove(); })()`, nil),
	)
	assert.NotEmpty(t, text, "the tally resolved, so both hooks read the fetch")
	assert.Equal(t, 1, fetches, "a finished job's result must be fetched exactly once, not once per hook")
	assert.Empty(t, f.BrowserErrors())
}
