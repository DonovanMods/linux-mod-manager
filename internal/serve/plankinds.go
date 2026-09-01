// plankinds.go holds the CLOSED table of mutation kinds the job API
// accepts. `POST /api/v1/plans/{kind}` is not an open dispatcher over
// core: {kind} must name a registered entry, and anything else is bad
// input (a 400 envelope whose details list what IS registered, generated
// from the table so the two can never drift apart).
//
// One entry describes one mutation flow end to end - how its request body
// decodes, how its Plan is computed, what is stored server-side between
// Plan and Apply, and how its Apply runs. Ten kinds are registered today:
// the eight plan kinds - deploy, install, uninstall, updates, rollback,
// switch, profile_apply and verify_fix - here, plus the two plan-free
// toggles in kind_toggle.go.
//
// The table used to carry a browser-form half as well (planKind.Form, the
// confirm-page decoders and display types). That went with the
// server-rendered page layer the SPA replaced
// (docs/plans/2026-08-31-serve-spa-design.md): a plan is now previewed by
// the SPA from the plan DOCUMENT this table already returns, so the only
// halves left are the ones /api/v1 itself needs.
package serve

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// planKind is one mutation flow's registration. Every field is required;
// a kind registered without one panics the first time a request reaches
// the missing half, which is a wiring bug, not a runtime condition.
type planKind struct {
	// Name is the {kind} path segment - "deploy", "install", ... - and the
	// value stored alongside the plan, so the job endpoint can find this
	// entry again from nothing but a plan_id.
	Name string

	// PlanOptions decodes the POST /api/v1/plans/{kind} request body into
	// this kind's own plan-time options value. An empty body decodes to the
	// zero value, so a caller with nothing to say may send none. Use
	// decodeKindOptions[T]; a T with a validate() error method has it
	// called, so bad input is refused at the boundary rather than inside a
	// core call.
	PlanOptions func(body []byte) (any, error)

	// ApplyOptions decodes the "options" member of POST /api/v1/jobs into
	// this kind's own apply-time options value - the mid-flight decisions
	// v2 Phase 3 Ruling 1 answers by re-running Apply with a different
	// option (install's AcceptConflicts, say), never by calling back into
	// the frontend.
	ApplyOptions func(body []byte) (any, error)

	// Plan computes the mutation's plan for the resolved game/profile
	// selection and returns two things: the DOCUMENT that goes on the wire
	// (the frozen core plan type) and the PENDING value the plan store
	// holds until Apply. They are separate because the pending value keeps
	// what the wire cannot carry - the plan object's own pointer identity,
	// with the unexported json:"-" freshness snapshot Apply re-checks, plus
	// whatever else that Apply needs (the game, the options the plan was
	// computed from). See plans.go.
	Plan func(ctx context.Context, s *Server, sel selection, opts any) (document, pending any, err error)

	// Apply runs the mutation against the pending value the plan store
	// returned, reporting progress to sink and returning the core result
	// document the equivalent CLI command emits under --json. It runs on
	// the job's goroutine under the registry's root context (jobs.go), so
	// it must never touch the request.
	Apply func(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (result any, err error)
}

// planKinds is the registry itself. It is written only from the init
// functions of the files that define each kind, and read-only from every
// request, so it needs no lock.
var planKinds = map[string]planKind{}

// registerPlanKind adds k to the closed table. Registering the same name
// twice panics: a duplicate is a wiring mistake that would silently make
// one of the two entries unreachable, and it can only ever happen at
// program start, where a panic is the cheapest possible failure.
func registerPlanKind(k planKind) {
	if _, dup := planKinds[k.Name]; dup {
		panic(fmt.Sprintf("serve: plan kind %q is registered twice", k.Name))
	}
	planKinds[k.Name] = k
}

// lookupPlanKind returns the entry registered under name.
func lookupPlanKind(name string) (planKind, bool) {
	k, ok := planKinds[name]
	return k, ok
}

// supportedPlanKinds returns every registered kind's name, sorted, for the
// unknown-kind envelope's details. It is generated from the registry
// rather than hand-maintained so a kind added in a later task shows up in
// the error message for free.
func supportedPlanKinds() []string {
	names := make([]string, 0, len(planKinds))
	for name := range planKinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validatingOptions is implemented by a kind's options type that needs
// checking beyond "it was valid JSON of the right shape" - an enum that
// must parse, a required field that must be present. decodeKindOptions
// calls it, so every such failure is reported as bad input at the request
// boundary instead of surfacing later as a core error.
type validatingOptions interface {
	validate() error
}

// decodeKindOptions is the options decoder every kind registers: it
// decodes body into T with unknown members REJECTED (a misspelled option
// must fail loudly rather than be silently ignored), treats an empty body
// as T's zero value, and runs T's validate method when it has one.
func decodeKindOptions[T any](body []byte) (any, error) {
	var opts T
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &opts, json.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("decoding options: %w", err)
		}
	}
	if v, ok := any(&opts).(validatingOptions); ok {
		if err := v.validate(); err != nil {
			return nil, fmt.Errorf("invalid options: %w", err)
		}
	}
	return opts, nil
}
