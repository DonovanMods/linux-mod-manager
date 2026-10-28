package serve

// plankind_fallback_test.go registers ONE test-only plan kind that has no
// renderer in the SPA's own table (spa/app/components/planrenderers.js) -
// the live driver for GenericPlanView, which issue 332's review carried
// forward as m8 ("unreachable/untested until switch/profile_apply are
// wired") and issue 334's own wiring of those two kinds would otherwise
// have closed by making the fallback genuinely unreachable.
//
// It exists because the fallback's entire contract is about a kind that
// does NOT exist yet: a mutation wired in a future unit, before its
// renderer is written, must still preview honestly rather than show an
// empty modal with a live Confirm button under it. The only way to prove
// that is to have such a kind - so this file makes one, in a _test.go file,
// so it can never reach a production binary. (Both the internal and the
// external test package compile into the same test binary, so this init
// runs for the chromedp scenarios in package serve_test too.)
//
// It deliberately mutates NOTHING. A fallback preview is about rendering,
// and a kind whose Apply changed real state would make the scenario about
// something else.

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// fallbackKindName is the kind the SPA has no renderer for. The name says
// what it is for, because it shows up in the unknown-kind envelope's own
// "supported kinds" list for every test in this binary.
const fallbackKindName = "e2e_no_renderer"

func init() {
	registerPlanKind(planKind{
		Name:         fallbackKindName,
		PlanOptions:  decodeKindOptions[fallbackPlanRequest],
		ApplyOptions: decodeKindOptions[fallbackApplyRequest],
		Plan:         planFallbackKind,
		Apply:        applyFallbackKind,
	})
}

// fallbackPlanRequest is the kind's plan-time options: nothing.
type fallbackPlanRequest struct{}

// fallbackApplyRequest is the kind's apply-time options: nothing.
type fallbackApplyRequest struct{}

// fallbackPlanDocument is the plan this kind puts on the wire - a shape no
// renderer knows, which is the point: GenericPlanView has to render it from
// the document alone.
type fallbackPlanDocument struct {
	Profile string   `json:"profile"`
	Steps   []string `json:"steps"`
}

// fallbackResultDocument is what its Apply answers with.
type fallbackResultDocument struct {
	Profile string `json:"profile"`
	Applied int    `json:"applied"`
}

func planFallbackKind(_ context.Context, _ *Server, sel selection, _ any) (any, any, error) {
	doc := &fallbackPlanDocument{
		Profile: sel.Profile,
		Steps:   []string{"first unrendered step", "second unrendered step"},
	}
	return doc, doc, nil
}

func applyFallbackKind(_ context.Context, _ *Server, pending, _ any, _ core.EventSink) (any, error) {
	doc, _ := pending.(*fallbackPlanDocument)
	return &fallbackResultDocument{Profile: doc.Profile, Applied: len(doc.Steps)}, nil
}
