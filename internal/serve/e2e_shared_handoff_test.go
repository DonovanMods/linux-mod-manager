package serve_test

// #633 in the browser: Alpha Mod and Beta Mod both ship shared.pak, and
// Beta, later in load order, has it deployed. Disabling Beta from its full
// mod page hands the path to Alpha in the same job, and the page's readout
// says so where the job finished - the warning the CLI prints - and holds
// it there past the success release, as a switch's notice is held.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestE2E_SharedHandOff_DisablingTheWinnerSaysWhoHasThePath(t *testing.T) {
	f := newE2EFixtureFromSource(t, catalogSource(map[string]string{"a": "Alpha Mod", "b": "Beta Mod"}))
	for _, m := range []struct{ id, name string }{{"a", "Alpha Mod"}, {"b", "Beta Mod"}} {
		seedInstalledMod(t, f.Svc, f.Game,
			domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: f.Game.ID},
			true, map[string][]byte{"shared.pak": []byte(m.name), m.id + ".pak": []byte(m.id)})
		require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, f.Profile,
			domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, f.Profile, core.DeployOptions{}, nil)
	require.NoError(t, err)
	shared := filepath.Join(f.Game.ModPath, "shared.pak")
	got, err := os.ReadFile(shared)
	require.NoError(t, err)
	require.Equal(t, "Beta Mod", string(got))

	const warning = `.mod-page [data-testid="job-result-warning"]`
	var text string
	f.runInBrowser(t,
		scaleUITimers(),
		releaseSucceededReadouts(),
		chromedp.Navigate(f.ModPagePath("fake", "b")),
		pollUntil(modPageButtonJS+` === "Disable"`),
		clickWhenSettled(`.mod-page [data-action="toggle"]`),
		pollUntil(`document.querySelector('`+warning+`') !== null`),
		chromedp.Text(warning, &text, chromedp.ByQuery),
	)
	assert.Contains(t, text, "shared.pak: Alpha Mod's file is deployed there now in place of Beta Mod's")
	holdsFor(t, f.Ctx, readoutReleaseWait, "a disable that handed a shared path on",
		`document.querySelector('`+warning+`') !== null`)

	got, err = os.ReadFile(shared)
	require.NoError(t, err)
	assert.Equal(t, "Alpha Mod", string(got), "the next mod in load order has the path, with no deploy")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// #633 review: an install, an installer reconfigure and a profile import
// replace a live version of a mod, which hands shared paths on, so their
// readouts show the result's warnings as a removal's do.
func TestE2E_SharedHandOff_ReplacingKindsShowTheirWarnings(t *testing.T) {
	f := newE2EFixture(t)
	for _, kind := range []string{"install", "mod_fomod", "profile_import"} {
		var text string
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			chromedp.Evaluate(`(async () => {
				const kind = `+"`"+kind+"`"+`;
				const id = "fake-" + kind;
				const real = window.fetch.bind(window);
				window.fetch = (input, init) => {
					const url = typeof input === "string" ? input : (input && input.url) || "";
					if (url.includes("/api/v1/jobs/" + id)) {
						return Promise.resolve(new Response(JSON.stringify({
							id, kind, state: "succeeded",
							result: { warnings: ["shared.pak: Alpha Mod's file is deployed there now in place of Beta Mod's"] },
						}), { status: 200, headers: { "Content-Type": "application/json" } }));
					}
					return real(input, init);
				};
				const { render, h } = await import("/static/app/render.js");
				const { JobProgress } = await import("/static/app/components/jobprogress.js");
				const container = document.createElement("div");
				document.body.appendChild(container);
				window.__handOffContainer = container;
				render(h(JobProgress, {
					jobID: id, summary: { id, kind, state: "succeeded" },
					frame: {}, actions: null, onDismiss: () => {},
				}), container);
			})()`, nil, awaitPromise),
			pollUntil(`window.__handOffContainer.querySelector('[data-testid="job-result-warning"]') !== null`),
			chromedp.Evaluate(`window.__handOffContainer.querySelector('[data-testid="job-result-warning"]').textContent`, &text),
			chromedp.Evaluate(`(() => { window.__handOffContainer.remove(); })()`, nil),
		)
		assert.Contains(t, text, "Alpha Mod's file is deployed there now", "%s shows its warnings", kind)
	}
	assert.Empty(t, f.BrowserErrors())
}

// #633 review: an update batch carries a tally, which used to answer for
// its toast alone - so a batch whose items handed shared paths on ended in
// a plain toast with no warnings once the page had moved on. Its result is
// read now, the toast is held, and it carries the items' warnings.
func TestE2E_SharedHandOff_AnUpdateBatchWithWarningsHoldsItsToast(t *testing.T) {
	f := newE2EFixture(t)
	var got struct {
		MayHold bool     `json:"mayHold"`
		Held    bool     `json:"held"`
		Plain   bool     `json:"plain"`
		Warns   []string `json:"warns"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const real = window.fetch.bind(window);
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/jobs/fake-batch")) {
					return Promise.resolve(new Response(JSON.stringify({
						id: "fake-batch", kind: "updates", state: "succeeded",
						tally: { kind: "updates", applied: 1, failed: 0 },
						result: { applied: [{ warnings: ["shared.pak: Alpha Mod's file is deployed there now in place of Beta Mod's"] }] },
					}), { status: 200, headers: { "Content-Type": "application/json" } }));
				}
				return real(input, init);
			};
			const { resultMayHoldNextStep, readoutHasNextStep, fetchJobResult } = await import("/static/app/jobresult.js");
			const summary = { id: "fake-batch", kind: "updates", state: "succeeded", tally: { kind: "updates", applied: 1, failed: 0 } };
			const read = await fetchJobResult("fake-batch");
			return {
				mayHold: resultMayHoldNextStep(summary),
				held: readoutHasNextStep(summary, read),
				plain: resultMayHoldNextStep({ kind: "verify_fix", tally: { applied: 1, failed: 0 } }),
				warns: read.warnings,
			};
		})()`, &got, awaitPromise),
	)
	assert.True(t, got.MayHold, "a tallied batch of a kind that shows warnings is read")
	assert.True(t, got.Held, "and held by its items' warnings")
	assert.False(t, got.Plain, "a tally still answers for a kind that shows none")
	require.Len(t, got.Warns, 1)
	assert.Contains(t, got.Warns[0], "Alpha Mod's file is deployed there now")
	assert.Empty(t, f.BrowserErrors())
}
