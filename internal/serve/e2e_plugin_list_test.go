package serve_test

// #616 in the browser: a finished deploy's readout says what it did to the
// game's Plugins.txt and lists the plugin list's findings - an error the
// game will not survive marked as one - and a purge's says it put the
// user's own list back. The job documents are core.DeployResult and
// core.PurgeResult as the job API serves them.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

func TestE2E_JobProgress_ShowsThePluginList(t *testing.T) {
	f := newE2EFixture(t)

	var deployText, purgeText string
	var errorRows, warningRows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const real = window.fetch.bind(window);
			const reply = (doc) => Promise.resolve(new Response(JSON.stringify(doc), { status: 200, headers: { "Content-Type": "application/json" } }));
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/jobs/fake-deploy-job")) {
					return reply({
						id: "fake-deploy-job", kind: "deploy", state: "succeeded",
						result: {
							deployed: 2, merged_mods: 0, raw_fallbacks: 0,
							plugins: {
								status: "written", plugins: 3, active: 2, external: 1,
								list_file: "/pfx/drive_c/users/steamuser/AppData/Local/Skyrim Special Edition/Plugins.txt",
								findings: [
									{ code: "plugin_missing_master", severity: "error", plugin: "Patch.esp", missing: ["Absent.esm"],
									  message: "Patch.esp needs the master(s) Absent.esm (not installed)" },
									{ code: "plugin_name_unencodable", severity: "warning", plugin: "x.esp", message: "x.esp cannot be written" },
								],
							},
						},
					});
				}
				if (url.includes("/api/v1/jobs/fake-purge-plugins")) {
					return reply({
						id: "fake-purge-plugins", kind: "purge", state: "succeeded",
						result: { purged: 1, plugins: { status: "restored", plugins: 0, active: 0 } },
					});
				}
				return real(input, init);
			};
		})()`, nil),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { JobProgress } = await import("/static/app/components/jobprogress.js");
			for (const [id, kind, key] of [["fake-deploy-job", "deploy", "__pluginDeploy"], ["fake-purge-plugins", "purge", "__pluginPurge"]]) {
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
		pollUntil(`window.__pluginDeploy.querySelector('[data-testid="plugin-list-result"]') !== null`),
		pollUntil(`window.__pluginPurge.querySelector('[data-testid="plugin-list-result"]') !== null`),
		chromedp.Evaluate(`window.__pluginDeploy.querySelector('[data-testid="plugin-list-result"]').textContent`, &deployText),
		chromedp.Evaluate(`window.__pluginDeploy.querySelectorAll('.plugin-list__finding--error').length`, &errorRows),
		chromedp.Evaluate(`window.__pluginDeploy.querySelectorAll('.plugin-list__finding--warning').length`, &warningRows),
		chromedp.Evaluate(`window.__pluginPurge.querySelector('[data-testid="plugin-list-result"]').textContent`, &purgeText),
		chromedp.Evaluate(`(() => { window.__pluginDeploy.remove(); window.__pluginPurge.remove(); })()`, nil),
	)
	assert.Contains(t, deployText, "Plugins.txt written: 3 plugin(s), 2 active (1 kept that lmm did not deploy)")
	assert.Contains(t, deployText, "/pfx/drive_c/users/steamuser/AppData/Local/Skyrim Special Edition/Plugins.txt")
	assert.Contains(t, deployText, "Error: Patch.esp needs the master(s) Absent.esm (not installed)")
	assert.Contains(t, deployText, "Warning: x.esp cannot be written")
	assert.Equal(t, 1, errorRows)
	assert.Equal(t, 1, warningRows)
	assert.Contains(t, purgeText, "Plugins.txt restored to the list you had before lmm wrote it")
	assert.Empty(t, f.BrowserErrors())
}
