package serve_test

// TestE2E_JobProgress_FetchesAFinishedJobsResultOnce is review F5: a
// finished job now fetches its result document twice. jobprogress.js's
// JobProgress calls several result hooks (useJobResultWarnings,
// useJobResultPurge, ... - C1's own unconditional-call rule) for the same
// jobID in the same render, so their effects fire together, before any
// fetch has resolved - jobresult.js's own readCache is filled only inside
// the fetch's .then, so no effect sees another's request in flight.
// jobresult.js's doc comment already claims "fetches its result exactly
// once"; this pins it. (The outcome tally is no longer one of these reads:
// it is on the job summary since issue 364, so the signal that the shared
// read resolved is a switch's result warning instead.)

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
						id: "fake-job-1", kind: "switch", state: "succeeded",
						result: { warnings: ["run lmm deploy to finish the switch"] },
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
				summary: { id: "fake-job-1", kind: "switch", state: "succeeded" },
				frame: {},
				actions: null,
				onDismiss: () => {},
			}), container);
		})()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		pollUntil(`window.__jobProgressContainer.querySelector('[data-testid="job-result-warning"]') !== null`),
		chromedp.Evaluate(`window.__jobProgressContainer.querySelector('[data-testid="job-result-warning"]').textContent`, &text),
		chromedp.Evaluate(`window.__jobResultFetches`, &fetches),
		chromedp.Evaluate(`(() => { window.__jobProgressContainer.remove(); })()`, nil),
	)
	assert.Contains(t, text, "run lmm deploy", "the shared read resolved, so every hook read the fetch")
	assert.Equal(t, 1, fetches, "a finished job's result must be fetched exactly once, not once per hook")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_JobProgress_ShowsPurgeFilesKeptAndRemoved is #478's browser
// regression: a completed purge no longer hides a user-owned file, a file
// another game still records, or files removed under the earlier mod_path.
func TestE2E_JobProgress_ShowsPurgeFilesKeptAndRemoved(t *testing.T) {
	f := newE2EFixture(t)

	var text string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const real = window.fetch.bind(window);
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/jobs/fake-purge-job")) {
					return Promise.resolve(new Response(JSON.stringify({
						id: "fake-purge-job", kind: "purge", state: "succeeded",
						result: {
							purged: 1, removed_paths: 2,
							kept: [
								{ path: "Data/edited.esp", reason: "user_file", note: "its content changed after lmm deployed it", mod_path: "/games/old-data" },
								{ path: "Data/shared.esp", reason: "other_game", games: ["sky2"] },
							],
						},
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
			window.__purgeJobResultContainer = container;
			render(h(JobProgress, {
				jobID: "fake-purge-job",
				summary: { id: "fake-purge-job", kind: "purge", state: "succeeded" },
				frame: {}, actions: null, onDismiss: () => {},
			}), container);
		})()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		pollUntil(`window.__purgeJobResultContainer.querySelector('[data-testid="purge-job-result"]') !== null`),
		chromedp.Evaluate(`window.__purgeJobResultContainer.querySelector('[data-testid="purge-job-result"]').textContent`, &text),
		chromedp.Evaluate(`(() => { window.__purgeJobResultContainer.remove(); })()`, nil),
	)
	assert.Contains(t, text, "Removed 2 files deployed under an earlier mod_path.")
	assert.Contains(t, text, "Kept your file; lmm no longer tracks it (its content changed after lmm deployed it): /games/old-data/Data/edited.esp")
	assert.Contains(t, text, "Left in place (still recorded by game sky2): Data/shared.esp")
	assert.Empty(t, f.BrowserErrors())
}
