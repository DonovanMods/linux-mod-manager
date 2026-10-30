package serve_test

// The browser half of the bundled fonts (#523).
//
// fonts_internal_test.go pins the bytes, the content type and the
// @font-face rules. What only a browser can answer is whether the face is
// actually USED: a font the Content-Security-Policy refused, a woff2 the
// browser could not decode, or a family name that matches no rule all fail
// the same quiet way - the text falls back to the system face, the page
// looks fine, and every Go test is green.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_BundledFontsLoadAndDrawThePage(t *testing.T) {
	f := newE2EFixture(t)

	type face struct {
		Family string `json:"family"`
		Style  string `json:"style"`
		Weight string `json:"weight"`
		// Status is the face's state once the page has rendered, before
		// this test asks for anything: "loaded" means the page's own text
		// pulled it in.
		Status string `json:"status"`
		// Load is what an explicit load() of the face came to: "loaded",
		// or the error a refused or undecodable font rejects with.
		Load string `json:"load"`
	}
	var got struct {
		Faces      []face `json:"faces"`
		BodyFamily string `json:"bodyFamily"`
		InterReady bool   `json:"interReady"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			await document.fonts.ready;
			const faces = [...document.fonts].map((ff) => ({
				family: ff.family.replace(/"/g, ""),
				style: ff.style,
				weight: ff.weight,
				status: ff.status,
			}));
			const loads = await Promise.all([...document.fonts].map((ff) =>
				ff.load().then(() => "loaded", (e) => String(e))));
			loads.forEach((l, i) => { faces[i].load = l; });
			return {
				faces,
				bodyFamily: getComputedStyle(document.body).fontFamily,
				interReady: document.fonts.check('15px "Inter"'),
			};
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Len(t, got.Faces, 4, "app.css declares two Inter and two JetBrains Mono faces: %+v", got.Faces)
	for _, ff := range got.Faces {
		assert.Equal(t, "loaded", ff.Load,
			"%s %s %s must load from /vendor/ - a CSP refusal or a bad woff2 rejects here", ff.Family, ff.Style, ff.Weight)
	}
	var upright *face
	for i := range got.Faces {
		if got.Faces[i].Family == "Inter" && got.Faces[i].Style == "normal" {
			upright = &got.Faces[i]
		}
	}
	require.NotNil(t, upright, "the upright Inter is declared")
	assert.Equal(t, "loaded", upright.Status, "the page's own text pulled in the upright Inter")
	assert.True(t, got.InterReady)
	assert.Regexp(t, `^"?Inter"?,`, got.BodyFamily, "the body is set in the bundled face first")
	assert.Empty(t, f.BrowserErrors())
}
