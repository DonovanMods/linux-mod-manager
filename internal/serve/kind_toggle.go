// kind_toggle.go holds the enable/disable flows - the ONE sanctioned
// mutation path in `lmm serve` that runs WITHOUT a plan step
// (docs/plans/2026-08-30-serve-impl.md Task 8, which names them explicitly:
// "these are direct core calls EnableMod/DisableMod: run as jobs WITHOUT a
// plan step, they are single-row toggles - the ONE sanctioned non-Plan
// path, documented").
//
// Why they are exempt, precisely: a Plan exists to show a user what an
// Apply is about to do while there is still a decision to make. Enable and
// disable have no decision - no options, no file selection, no conflict
// question - and no preview a confirm page could render beyond restating
// the button the user just pressed. They are also the two core calls with
// no Plan/Apply pair to reach for: EnableMod/DisableMod are single beginOp-
// gated calls by design (CLAUDE.md's "a handful of single-step flows with
// nothing to preview"), not Ruling-10 convenience wrappers over a pair -
// which remain forbidden as serve entry points, here as everywhere.
//
// They still run as JOBS, so everything downstream of a mutation is
// identical to every other flow: the same /jobs/{id} page, the same SSE
// stream and its terminal frame, the same failure envelope. What they do
// not have is progress EVENTS: EnableMod and DisableMod take no
// core.EventSink (internal/core/mod_toggle.go), so their stream carries the
// state transition and nothing else. serve does not manufacture core events
// to fill that gap - an event a core flow never emitted would be a
// frontend inventing the contract it is supposed to render.
package serve

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// toggleKind is one plan-free mutation: a single core call against a
// resolved game/profile and one mod, run as a job. It is a separate, much
// smaller table than planKind on purpose - registering these as plan kinds
// would put POST /api/v1/plans/enable on the wire, an endpoint that could
// only ever answer "there is nothing to plan".
type toggleKind struct {
	// Name is the route segment (/mods/{source}/{id}/enable) and the kind
	// stored on the job.
	Name string
	// Title is the human label the job page and result page show.
	Title string
	// Apply runs the core call. It takes the job's own context, never the
	// request's (jobs.go).
	Apply func(ctx context.Context, s *Server, sel selection, sourceID, modID string) (any, error)
	// Summarize turns the core result into the job page's readout, exactly
	// as planKind.Summarize does.
	Summarize func(result any) []resultFact
}

// toggleKinds is the closed table of plan-free mutation kinds, written only
// from init below and read-only from every request.
var toggleKinds = map[string]toggleKind{}

// registerToggleKind adds k to the table, panicking on a duplicate for the
// same reason registerPlanKind does.
func registerToggleKind(k toggleKind) {
	if _, dup := toggleKinds[k.Name]; dup {
		panic(fmt.Sprintf("serve: toggle kind %q is registered twice", k.Name))
	}
	toggleKinds[k.Name] = k
}

// lookupToggleKind returns the entry registered under name.
func lookupToggleKind(name string) (toggleKind, bool) {
	k, ok := toggleKinds[name]
	return k, ok
}

func init() {
	registerToggleKind(toggleKind{
		Name:  "enable",
		Title: "Enable",
		Apply: func(ctx context.Context, s *Server, sel selection, sourceID, modID string) (any, error) {
			return s.svc.EnableMod(ctx, sel.Game, sel.Profile, sourceID, modID)
		},
		Summarize: summarizeEnableResult,
	})
	registerToggleKind(toggleKind{
		Name:  "disable",
		Title: "Disable",
		Apply: func(ctx context.Context, s *Server, sel selection, sourceID, modID string) (any, error) {
			return s.svc.DisableMod(ctx, sel.Game, sel.Profile, sourceID, modID)
		},
		Summarize: summarizeDisableResult,
	})
}

// summarizeEnableResult reads an EnableResult the way a user would: did
// anything change, and what did the flow have to say about it.
func summarizeEnableResult(result any) []resultFact {
	res, ok := result.(*core.EnableResult)
	if !ok {
		return nil
	}
	return toggleFacts(res.Changed, "enabled", res.Notes, res.Warnings)
}

// summarizeDisableResult is summarizeEnableResult for a DisableResult.
func summarizeDisableResult(result any) []resultFact {
	res, ok := result.(*core.DisableResult)
	if !ok {
		return nil
	}
	return toggleFacts(res.Changed, "disabled", res.Notes, res.Warnings)
}

// toggleFacts renders the shared shape of both toggle results. An unchanged
// toggle is reported as such rather than as a bare success: "it was already
// disabled" is the whole answer in that case, and core returns it as a
// non-error precisely so a frontend can say so.
func toggleFacts(changed bool, verb string, notes, warnings []string) []resultFact {
	state := "already " + verb
	if changed {
		state = verb
	}
	facts := []resultFact{{Label: "Mod", Value: state}}
	for _, w := range warnings {
		facts = append(facts, resultFact{Label: "Warning", Value: w})
	}
	for _, n := range notes {
		facts = append(facts, resultFact{Label: "Note", Value: n})
	}
	return facts
}
