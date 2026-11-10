package serve_test

// #521: the web UI's type is --font-sans, a system-ui stack, so its text is
// drawn in whatever face the host resolves - Liberation Sans on one machine,
// DejaVu Sans (about a tenth wider) on a hosted Ubuntu runner, and wider
// still for a user who picked a wide default. A layout check that only ever
// runs in the face the developer happens to have proves the layout for that
// face alone: #518's and #520's passed locally and failed on CI. So every
// layout check runs again under a face no host can make narrower.
//
// #523 then bundled Inter as the first --font-sans, so the default pass now
// measures the same face on every host. The wider passes stay: they are
// the page a user gets if that face cannot load, and they keep the layout
// honest about text it cannot predict.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// e2eFace is a typeface the layout checks run under.
type e2eFace struct {
	name string
	// family is the font-family forced onto every element, or "" to leave
	// the stylesheet's own in place.
	family string
}

// e2eDefaultFace leaves the stylesheet's own --font-sans in place: the
// bundled Inter (#523).
var e2eDefaultFace = e2eFace{name: "default"}

// e2eWideFace is the platform's monospace: present on every host, and
// wider than any proportional sans a host resolves system-ui to. Named
// twice because a family of exactly "monospace" is Chromium's legacy quirk
// that also drops the default font size from 16px to 13px - which would
// shrink every rem and make the page NARROWER, the opposite of the stress.
var e2eWideFace = e2eFace{name: "wide", family: "monospace, monospace"}

// e2eWideSansFace is the widest proportional sans the host has: DejaVu Sans
// (hosted Ubuntu's own sans-serif), Verdana, then Noto Sans, then whatever
// sans-serif resolves to. It is what a CI runner drew the page in before
// #523, and what it would fall back to without the bundled face.
var e2eWideSansFace = e2eFace{name: "wide-sans", family: `"DejaVu Sans", Verdana, "Noto Sans", sans-serif`}

// e2eLayoutFaces is the set every layout check is table-driven over.
var e2eLayoutFaces = []e2eFace{e2eDefaultFace, e2eWideSansFace, e2eWideFace}

// faceScript installs face's override on the current document: a
// constructed stylesheet, adopted after the page's own, so it is CSSOM
// rather than an inline <style> the Content-Security-Policy would (rightly)
// refuse.
func (face e2eFace) faceScript() string {
	return fmt.Sprintf(`(() => {
		const sheet = new CSSStyleSheet();
		sheet.replaceSync(%q);
		document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
	})()`, "*, *::before, *::after, ::placeholder { font-family: "+face.family+" !important; }")
}

// useFace makes every document f's browser loads from here until the test
// ends draw its text in face - installed before the page's own script runs,
// so whatever the SPA measures while it renders, it measures in face too.
// Call it before the Navigate the checks follow; the default face is a no-op.
func useFace(t *testing.T, browserCtx context.Context, face e2eFace) {
	t.Helper()
	if face.family == "" {
		return
	}
	var id page.ScriptIdentifier
	runE2EActions(t, browserCtx, []chromedp.Action{chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		id, err = page.AddScriptToEvaluateOnNewDocument(face.faceScript()).Do(ctx)
		return err
	})})
	t.Cleanup(func() {
		runE2EActions(t, browserCtx, []chromedp.Action{page.RemoveScriptToEvaluateOnNewDocument(id)})
	})
}

// faceInEffect fails unless the page's body is drawn in face - so a stress
// pass whose override silently did not apply cannot pass as the default one.
//
// The default face is the bundled Inter (#523), which arrives over the
// network with font-display: swap, so the page can paint in the fallback
// first. This waits for the page's font loads to settle and then requires
// Inter to be the face in use, so the checks that follow measure Inter and
// never a fallback caught mid-swap.
func faceInEffect(face e2eFace) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var got struct {
			Family string `json:"family"`
			Inter  string `json:"inter"`
		}
		if err := chromedp.Evaluate(`(async () => {
			await document.fonts.ready;
			const inter = [...document.fonts].find((ff) =>
				ff.family.replace(/"/g, "") === "Inter" && ff.style === "normal");
			return {
				family: getComputedStyle(document.body).fontFamily,
				inter: inter ? inter.status : "undeclared",
			};
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}).Do(ctx); err != nil {
			return err
		}
		if face.family != "" && got.Family != face.family {
			return fmt.Errorf("the %s face is not in effect: body's font-family is %q", face.name, got.Family)
		}
		if face.family == "" && !strings.HasPrefix(strings.Trim(got.Family, `"`), "Inter") {
			return fmt.Errorf("the default face is not in effect: body's font-family is %q", got.Family)
		}
		if face.family == "" && got.Inter != "loaded" {
			return fmt.Errorf("the default face is not in effect: the bundled Inter is %s, so the page is drawn in a fallback", got.Inter)
		}
		return nil
	})
}

// forEachFace runs check as one subtest per layout face, with the face
// installed on browserCtx for that subtest only.
func forEachFace(t *testing.T, browserCtx context.Context, check func(t *testing.T, face e2eFace)) {
	t.Helper()
	for _, face := range e2eLayoutFaces {
		t.Run(face.name, func(t *testing.T) {
			useFace(t, browserCtx, face)
			check(t, face)
		})
	}
}
