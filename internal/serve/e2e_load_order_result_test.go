package serve_test

// #620 in the browser: a finished deploy's readout says what it did to the
// game's archive load order (modlist.txt) and lists its findings, and a
// purge's says it put the user's own list back. The job documents are
// core.DeployResult and core.PurgeResult as the job API serves them.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

func TestE2E_JobProgress_ShowsTheLoadOrder(t *testing.T) {
	f := newE2EFixture(t)

	var deployText, purgeText string
	var warningRows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const real = window.fetch.bind(window);
			const reply = (doc) => Promise.resolve(new Response(JSON.stringify(doc), { status: 200, headers: { "Content-Type": "application/json" } }));
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/jobs/fake-deploy-lo")) {
					return reply({
						id: "fake-deploy-lo", kind: "deploy", state: "succeeded",
						result: {
							deployed: 2, merged_mods: 0, raw_fallbacks: 0,
							load_orders: [{
								id: "cyberpunk2077-archive", status: "written", files: 3, external: 1,
								list_file: "/games/cp/archive/pc/mod/modlist.txt",
								findings: [{ code: "load_order_unlisted", severity: "warning", files: ["loose.archive"],
									message: "1 file(s) in archive/pc/mod were not deployed by lmm and are not in modlist.txt" }],
							}],
						},
					});
				}
				if (url.includes("/api/v1/jobs/fake-purge-lo")) {
					return reply({
						id: "fake-purge-lo", kind: "purge", state: "succeeded",
						result: { purged: 1, load_orders: [{ id: "cyberpunk2077-archive", status: "restored", files: 1 }] },
					});
				}
				return real(input, init);
			};
		})()`, nil),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { JobProgress } = await import("/static/app/components/jobprogress.js");
			for (const [id, kind, key] of [["fake-deploy-lo", "deploy", "__loDeploy"], ["fake-purge-lo", "purge", "__loPurge"]]) {
				const container = document.createElement("div");
				document.body.appendChild(container);
				window[key] = container;
				render(h(JobProgress, {
					jobID: id,
					summary: { id, kind, state: "succeeded" },
					frame: {}, actions: null, onDismiss: () => {},
				}), container);
			}
		})()`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		pollUntil(`window.__loDeploy.querySelector('[data-testid="load-order-result"]') !== null`),
		pollUntil(`window.__loPurge.querySelector('[data-testid="load-order-result"]') !== null`),
		chromedp.Evaluate(`window.__loDeploy.querySelector('[data-testid="load-order-result"]').textContent`, &deployText),
		chromedp.Evaluate(`window.__loDeploy.querySelectorAll('.plugin-list__finding--warning').length`, &warningRows),
		chromedp.Evaluate(`window.__loPurge.querySelector('[data-testid="load-order-result"]').textContent`, &purgeText),
		chromedp.Evaluate(`(() => { window.__loDeploy.remove(); window.__loPurge.remove(); })()`, nil),
	)
	assert.Contains(t, deployText, "Load order cyberpunk2077-archive written: 3 file(s) (1 kept that lmm did not deploy)")
	assert.Contains(t, deployText, "/games/cp/archive/pc/mod/modlist.txt")
	assert.Contains(t, deployText, "Warning: 1 file(s) in archive/pc/mod were not deployed by lmm")
	assert.Equal(t, 1, warningRows)
	assert.Contains(t, purgeText, "Load order cyberpunk2077-archive: the list you had before lmm wrote it is back")
	assert.Empty(t, f.BrowserErrors())
}
