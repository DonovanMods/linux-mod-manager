// plankinds.go holds the CLOSED table of mutation kinds the job API
// accepts. `POST /api/v1/plans/{kind}` is not an open dispatcher over
// core: {kind} must name a registered entry, and anything else is bad
// input (a 400 envelope whose details list what IS registered, generated
// from the table so the two can never drift apart).
//
// One entry describes one mutation flow end to end - how its request body
// decodes, how its Plan is computed, what is stored server-side between
// Plan and Apply, how its Apply runs, and how its result reads on the job
// page. Task 7 registers exactly one kind ("deploy", kind_deploy.go);
// docs/plans/2026-08-30-serve-impl.md Tasks 8 and 9 register the rest
// against this same surface - install, uninstall, updates, switch,
// profile_apply and verify_fix - without changing it.
package serve

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
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

	// Title is the human label the job page (and Tasks 8/9's confirm
	// pages) put in front of the user, e.g. "Deploy".
	Title string

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

	// Summarize turns a successful Apply's result document into the handful
	// of facts the job page renders, so a user reading a finished job sees
	// "3 deployed" rather than a JSON dump.
	Summarize func(result any) []resultFact

	// Form is the HTML half of this kind: how a browser form's fields decode
	// into the SAME options the JSON decoders above produce, and how its
	// plan reads on the confirm page (docs/plans/2026-08-30-serve-impl.md
	// Task 8). It is nil for a kind only /api/v1 can reach - a page route
	// that finds nil refuses rather than dereferencing it, which is what
	// keeps "registered as a plan kind" and "reachable from a form" two
	// separate decisions. Every kind registered today has one; the nil case
	// remains the supported way to add a JSON-only mutation later without
	// also having to design a page for it.
	Form *kindForm
}

// kindForm is one kind's browser-facing half. The two decoders take the
// whole request rather than a parsed form because a mutation's target
// generally lives in the PATH (/mods/{source}/{id}/install,
// /profiles/{name}/switch), not in the body: the path is what the read
// pages' form actions already encode, and taking it from there means a
// submitted body can never name a different target than the URL the user
// acted on. The flows whose target is not a single named thing - the
// updates batch, deploy, the health repair - take it from the ticked set or
// from the resolved game+profile selection instead, and never from a body
// field a page did not render.
type kindForm struct {
	// PlanOptions builds this kind's plan-time options from the request -
	// the same value type PlanOptions' JSON decoder produces.
	PlanOptions func(r *http.Request) (any, error)

	// ApplyOptions builds this kind's apply-time options from the confirm
	// form - the same value type ApplyOptions' JSON decoder produces. It is
	// also called on the ENTRY submission, so the confirm page can render
	// the options the user already chose (a version picked on the mod-detail
	// page, say) as its own initial values.
	ApplyOptions func(r *http.Request) (any, error)

	// Confirm renders the stored pending plan as the confirm page's display
	// data, with opts (this kind's apply-time options, from ApplyOptions
	// above) supplying the current value of every option the form offers.
	Confirm func(pending, opts any) confirmView

	// PlanIsCurrent reports whether the plan a confirm submission is about
	// to redeem was computed from the options that submission carries. It
	// is for the kinds whose options are PLAN-time: deploy's --purge
	// changes what the plan SAYS, and ApplyDeploy must receive the very
	// options its plan was computed from, so a user who ticks a box and
	// presses the primary button would otherwise get a mutation they never
	// previewed. Returning false re-plans onto a fresh confirm page instead
	// of applying.
	//
	// Nil means "this kind's options are apply-time", which is the common
	// case (install, uninstall, updates): the confirm form's own values are
	// what applies, so there is nothing to disagree with - Apply always
	// receives exactly the options the submission carries, regardless of
	// what PlanIsCurrent would have said. That does NOT mean an apply-time
	// option can never appear in the plan text: uninstall's KeepCache and
	// SkipHooks are apply-time (unlock only, per this doc), yet the stored
	// plan document echoes them (kind_uninstall.go), so ticking one and
	// pressing the primary button applies the tick correctly even though
	// the confirm page's plan-derived summary above it can lag one step
	// behind until "Update plan" is pressed (task-9 review Minor 3) - a
	// display staleness, never a mismatch in what actually happens.
	PlanIsCurrent func(pending any, r *http.Request) bool
}

// confirmView is a plan as the confirm page shows it: what the mutation
// would do, and the options the user may still change before committing.
// It is deliberately display data rather than a plan type - one template
// renders every kind, so a kind adds a flow without adding a template.
type confirmView struct {
	// Heading names the specific thing being acted on ("Better Boots
	// 1.2.0"), under the kind's own title.
	Heading string

	// Facts are the plan's headline label/value rows (the same shape a
	// finished job's result readout uses).
	Facts []resultFact

	// Lists are the plan's enumerations - the files that would be removed,
	// the dependencies that would come along, the hooks that would run.
	Lists []confirmList

	// Versions and Version drive the version <select> (#225): the versions
	// the source offers, and the one currently chosen. Empty Versions means
	// the source reports none, and no select renders.
	Versions []string
	Version  string

	// Files drives the file checkboxes (#225). It is rendered ONLY when the
	// plan actually offers a choice - a pool of two or more candidate files
	// - so a single-file mod shows the file as a fact, not as a control
	// with one option.
	Files []confirmFile

	// Toggles are the kind's boolean options (#226: uninstall's keep-cache,
	// the hook and force switches), rendered as checkboxes.
	Toggles []confirmToggle

	// Submit is the primary button's label ("Install", "Uninstall").
	Submit string

	// Hidden are extra fields the confirm form must carry back verbatim -
	// the part of a submission that is neither an option the page offers
	// nor something the route's own path already says. #74's batch needs
	// it: the ticked mod set arrived as repeated form fields, so without
	// carrying them the "Update plan" button would re-plan an empty
	// selection. A mod-scoped flow leaves it nil, since its target is in
	// the path.
	Hidden []queryParam

	// AcceptConflicts reports that this kind's apply options ALREADY carry
	// the overwrite decision - i.e. the user has been through a conflict
	// refusal once. It is what keeps that decision sticky across a re-plan
	// ("Update plan"), where the page is rendered from the form rather than
	// from the failure that prompted it.
	AcceptConflicts bool
}

// HasOptions reports whether this view offers anything the user can change
// - which is what makes the confirm page's "Update plan" button worth
// rendering. Changing an option can change the plan itself (a pinned
// version narrows the file pool; skipping hooks empties the hook list), so
// a page that offers options must also offer a way to see them applied
// before committing.
func (v confirmView) HasOptions() bool {
	return len(v.Versions) > 0 || len(v.Files) > 0 || len(v.Toggles) > 0
}

// confirmList is one named enumeration on a confirm page.
type confirmList struct {
	Label string
	Items []string
}

// confirmFile is one selectable file in a confirm page's file picker.
type confirmFile struct {
	ID       string
	Label    string
	Selected bool
}

// confirmToggle is one boolean option a confirm page offers. Name is the
// form field it submits under, and is what the kind's ApplyOptions reads
// back.
type confirmToggle struct {
	Name    string
	Label   string
	Help    string
	Checked bool
}

// resultFact is one label/value row of a finished job's result readout.
// Failure marks a row that represents a not-succeeded outcome (a failed
// install, an unresolvable profile_apply entry, a verify_fix finding still
// outstanding after the repair) - the semantic signal resultPageData.Partial
// keys on (epic re-review N-3: the predicate previously matched the
// literal label "Failed", which install and profile_apply happen to use
// but verify_fix's "Still reported" rows never did, so a repair that left
// findings outstanding rendered the plain green "Done." anyway).
type resultFact struct {
	Label   string
	Value   string
	Failure bool
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

// jobKindTitle is a job kind's human title, from whichever of the two
// registries holds it - the plan kinds above, or the two plan-free toggles
// (kind_toggle.go). It falls back to the stored kind name for a job whose
// kind is no longer registered (only possible across a code change, never
// within one run).
func jobKindTitle(kind string) string {
	if k, ok := lookupPlanKind(kind); ok {
		return k.Title
	}
	if k, ok := lookupToggleKind(kind); ok {
		return k.Title
	}
	return kind
}

// jobKindFacts is a finished job's result readout, from whichever registry
// owns its kind. An unknown kind (or a kind whose Summarize does not
// recognise the result type) renders no facts rather than a JSON dump.
func jobKindFacts(kind string, result any) []resultFact {
	if k, ok := lookupPlanKind(kind); ok {
		return k.Summarize(result)
	}
	if k, ok := lookupToggleKind(kind); ok {
		return k.Summarize(result)
	}
	return nil
}
