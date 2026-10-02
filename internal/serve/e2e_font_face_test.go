package serve_test

// #521: the web UI's type is --font-sans, a system-ui stack, so its text is
// drawn in whatever face the host resolves - Liberation Sans on one machine,
// DejaVu Sans (about a tenth wider) on a hosted Ubuntu runner, and wider
// still for a user who picked a wide default. A layout check that only ever
// runs in the face the developer happens to have proves the layout for that
// face alone: #518's and #520's passed locally and failed on CI. So every
// layout check runs again under a face no host can make narrower.

import (
	"context"
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// e2eFace is a typeface the layout checks run under.
type e2eFace struct {
	name string
	// family is the font-family forced onto every element, or "" to leave
	// the stylesheet's own in place.
	family string
}

// e2eDefaultFace leaves the stylesheet's own --font-sans in place.
var e2eDefaultFace = e2eFace{name: "default"}

// e2eWideFace is the platform's monospace: present on every host, and
// wider than any proportional sans a host resolves system-ui to. Named
// twice because a family of exactly "monospace" is Chromium's legacy quirk
// that also drops the default font size from 16px to 13px - which would
// shrink every rem and make the page NARROWER, the opposite of the stress.
var e2eWideFace = e2eFace{name: "wide", family: "monospace, monospace"}

// e2eWideSansFace is the widest proportional sans the host has: DejaVu Sans
// (hosted Ubuntu's own sans-serif), Verdana, then Noto Sans, then whatever
// sans-serif resolves to. It is what a CI runner draws the page in.
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
func faceInEffect(face e2eFace) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var family string
		if err := chromedp.Evaluate(`getComputedStyle(document.body).fontFamily`, &family).Do(ctx); err != nil {
			return err
		}
		if face.family != "" && family != face.family {
			return fmt.Errorf("the %s face is not in effect: body's font-family is %q", face.name, family)
		}
		if face.family == "" && family == e2eWideFace.family {
			return fmt.Errorf("the default face is not in effect: body's font-family is %q", family)
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
