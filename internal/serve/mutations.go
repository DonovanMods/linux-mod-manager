// mutations.go wires the single-mod mutation flows
// (docs/plans/2026-08-30-serve-impl.md Task 8, #322) onto the form shells
// Task 4 rendered: enable, disable, uninstall and install.
//
// The shape every flow follows is the design's
// (docs/plans/2026-08-30-serve-design.md §"Mutations: Plan -> confirm ->
// Apply"):
//
//	POST /mods/{source}/{id}/{kind}                 -> compute the Plan,
//	                                                   store it, render the
//	                                                   confirm page
//	POST /mods/{source}/{id}/{kind}  (confirm=1)    -> redeem the plan_id,
//	                                                   start the Apply as a
//	                                                   job, 303 to /jobs/{id}
//	POST /mods/{source}/{id}/{kind}?sync=1 (confirm=1) -> run the Apply
//	                                                   inline and render the
//	                                                   result (the no-JS
//	                                                   fallback; identical
//	                                                   end state)
//
// One route per flow, not a plan route plus a separate confirm route: the
// target lives in the PATH, so a confirm submission can never name a
// different mod than the page it was rendered from, and the confirm form's
// action is simply the URL it came from.
//
// Two recoveries are first-class rather than dead ends, because both are
// states an ordinary user reaches by doing nothing wrong:
//
//   - The plan store is single-use with a TTL (plans.go), so a re-submitted
//     or long-abandoned confirm page has no plan left to redeem. That is not
//     an error - it is a re-plan, rendered as a fresh confirm page with a
//     notice.
//   - core.ErrStalePlan means the world moved under the plan. Same answer: a
//     fresh plan on a fresh confirm page.
//
// And one is the flow's whole point: install's conflicts can only be known
// after the download (see core.InstallOptions.AcceptConflicts), so a refused
// conflict arrives as *core.ConflictError from the Apply. The failed job
// stores that envelope with its typed details; its page renders the
// conflicting files and offers Overwrite, which re-plans and re-applies with
// AcceptConflicts against the now-warm cache - no second download.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// The form fields every mutation submission may carry beyond a kind's own
// options. They are the lifecycle of a submission - which step it is, which
// plan it redeems - as opposed to what the mutation should do.
const (
	// confirmField marks a submission as coming FROM a confirm page, i.e.
	// "apply this", as opposed to the entry submission from a read page,
	// which means "show me what this would do".
	confirmField = "confirm"

	// replanField marks a submission that should compute a fresh plan and
	// apply it immediately, without stopping at another confirm page - the
	// recovery actions a failed job's page offers, where the user has
	// already seen the plan and the failure that followed it.
	replanField = "replan"

	// planIDField carries the opaque handle the confirm page was rendered
	// with (plans.go).
	planIDField = "plan_id"

	// syncParam is the no-JS fallback's query parameter: ?sync=1 runs the
	// Apply inline and renders its result instead of starting a job.
	syncParam = "sync"
)

// Notices the confirm page renders above a re-planned mutation. Both say
// the same thing in the end - "this plan is not the one you were looking
// at; here is a current one" - but a user who double-submitted a form and a
// user whose profile changed underneath them are in different situations
// and deserve to be told which one they are in.
const (
	noticePlanUnavailable = "That confirmation had already been used or had expired, so this is a freshly computed plan. Review it and confirm again."
	noticeStalePlan       = "Something changed in this profile while the plan was open, so it was refused. This is a freshly computed plan - review it and confirm again."
	noticeConflicts       = "Installing this mod would overwrite files another installed mod owns. Nothing has been changed. Choose Overwrite to install anyway."
)

// mutationRequest is one mutation submission: which flow, which mod, which
// game/profile, and where in the Plan -> confirm -> Apply sequence it sits.
// A kind's own options are NOT parsed here - they are the kind's business
// (kindForm) - but the raw submitted form is carried so a failed job's page
// can re-offer the identical request as one click.
type mutationRequest struct {
	// Kind is the flow: "enable", "disable", "install", "uninstall".
	Kind string
	// SourceID and ModID are the target, taken from the path.
	SourceID string
	ModID    string
	// Game and Profile are the resolved selection's values, carried as
	// hidden fields so a re-submission scopes to the same profile even if
	// the server's default moved.
	Game    string
	Profile string
	// PlanID is the handle the confirm page was rendered with; empty on an
	// entry submission.
	PlanID planID
	// Confirm, Replan and Sync are the three lifecycle flags above.
	Confirm bool
	Replan  bool
	Sync    bool
	// Form is the submitted fields with the CSRF token removed - what a
	// recovery action re-submits verbatim, so the user's version pick and
	// file selection survive an overwrite or a re-plan.
	Form url.Values
}

// parseMutationRequest reads a submission. The target comes from the path
// (never the body), and every other field from the parsed form, so a GET
// query and a POST body are read the same way the rest of this package
// reads game/profile (selection.go).
func parseMutationRequest(r *http.Request, kind string) mutationRequest {
	// ParseForm has already run in the CSRF middleware; calling it again is
	// free and makes this function correct on its own terms.
	_ = r.ParseForm()

	form := url.Values{}
	for key, values := range r.PostForm {
		if key == csrfFormField {
			continue
		}
		form[key] = append([]string(nil), values...)
	}

	return mutationRequest{
		Kind:     kind,
		SourceID: r.PathValue("source"),
		ModID:    r.PathValue("id"),
		Game:     r.FormValue(gameParam),
		Profile:  r.FormValue(profileParam),
		PlanID:   planID(r.FormValue(planIDField)),
		Confirm:  formFlag(r, confirmField),
		Replan:   formFlag(r, replanField),
		Sync:     r.URL.Query().Get(syncParam) == "1",
		Form:     form,
	}
}

// formFlag reports whether a checkbox-or-hidden field is set. HTML sends an
// unchecked checkbox as no field at all, so presence with any non-empty
// value other than "0"/"false" counts as true.
func formFlag(r *http.Request, name string) bool {
	switch r.FormValue(name) {
	case "", "0", "false", "off":
		return false
	default:
		return true
	}
}

// actionPath is the route this request was submitted to - also where its
// confirm page and any recovery action post back to.
func (req mutationRequest) actionPath() string {
	return "/mods/" + url.PathEscape(req.SourceID) + "/" + url.PathEscape(req.ModID) + "/" + req.Kind
}

// recoveryFields renders the original submission as the hidden fields a
// recovery form re-sends, with overrides replacing (not appending to) any
// field of the same name - so "the same request, but with
// accept_conflicts" is expressible without the browser submitting that
// field twice. An override mapped to nil DROPS the field, which is how the
// re-plan action strips the lifecycle flags off a submission it wants
// treated as a fresh entry. The result is sorted for deterministic
// rendering.
func (req mutationRequest) recoveryFields(overrides url.Values) []queryParam {
	merged := url.Values{}
	for key, values := range req.Form {
		if _, replaced := overrides[key]; replaced {
			continue
		}
		merged[key] = values
	}
	for key, values := range overrides {
		merged[key] = values
	}
	// planIDField is never re-sent: the plan it named is gone by definition
	// on every path that renders a recovery action.
	merged.Del(planIDField)
	return sortedParams(merged)
}

// sortedParams flattens values into queryParam rows, sorted by key then
// value, so a rendered form's hidden fields are stable across requests.
func sortedParams(values url.Values) []queryParam {
	params := make([]queryParam, 0, len(values))
	for key, vs := range values {
		for _, v := range vs {
			params = append(params, queryParam{Key: key, Value: v})
		}
	}
	sortQueryParams(params)
	return params
}

// handleModEnable and handleModDisable answer the two plan-free toggle
// routes (kind_toggle.go).
func (s *Server) handleModEnable(w http.ResponseWriter, r *http.Request) {
	s.handleToggleMutation(w, r, "enable")
}

func (s *Server) handleModDisable(w http.ResponseWriter, r *http.Request) {
	s.handleToggleMutation(w, r, "disable")
}

// handleModInstall and handleModUninstall answer the two planned routes.
func (s *Server) handleModInstall(w http.ResponseWriter, r *http.Request) {
	s.handlePlannedMutation(w, r, "install")
}

func (s *Server) handleModUninstall(w http.ResponseWriter, r *http.Request) {
	s.handlePlannedMutation(w, r, "uninstall")
}

// handleToggleMutation runs one of the two plan-free toggles: no confirm
// page, straight to a job (or an inline Apply under ?sync=1).
func (s *Server) handleToggleMutation(w http.ResponseWriter, r *http.Request, kindName string) {
	kind, ok := lookupToggleKind(kindName)
	if !ok {
		s.renderMutationFailure(w, r, http.StatusNotFound, kindName, fmt.Errorf("unknown mutation %q", kindName))
		return
	}

	sel, ok := s.resolveReadyPageSelection(w, r)
	if !ok {
		return
	}
	req := parseMutationRequest(r, kindName)

	run := func(ctx context.Context, _ core.EventSink) (any, error) {
		return kind.Apply(ctx, s, sel, req.SourceID, req.ModID)
	}

	if req.Sync {
		result, err := run(applyContext(r), nil)
		if err != nil {
			s.renderMutationFailure(w, r, http.StatusInternalServerError, kind.Title, err)
			return
		}
		s.renderMutationResult(w, r, sel, kind.Title, jobKindFacts(kind.Name, result))
		return
	}
	s.startMutationJob(w, r, kind.Name, kind.Title, req, run)
}

// handlePlannedMutation runs one of the Plan -> confirm -> Apply flows. See
// this file's doc comment for the sequence and both recoveries.
func (s *Server) handlePlannedMutation(w http.ResponseWriter, r *http.Request, kindName string) {
	kind, ok := lookupPlanKind(kindName)
	if !ok || kind.Form == nil {
		s.renderMutationFailure(w, r, http.StatusNotFound, kindName, fmt.Errorf("unknown mutation %q", kindName))
		return
	}

	sel, ok := s.resolveReadyPageSelection(w, r)
	if !ok {
		return
	}
	req := parseMutationRequest(r, kindName)

	if !req.Confirm {
		s.planAndConfirm(w, r, kind, sel, req, confirmContext{})
		return
	}

	opts, err := kind.Form.ApplyOptions(r)
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusBadRequest, kind.Title, err)
		return
	}

	pending, ok := s.pendingForConfirm(w, r, kind, sel, req)
	if !ok {
		return
	}

	run := func(ctx context.Context, sink core.EventSink) (any, error) {
		return kind.Apply(ctx, s, pending, opts, sink)
	}

	if req.Sync {
		s.applyPlannedInline(w, r, kind, sel, req, run)
		return
	}
	s.startMutationJob(w, r, kind.Name, kind.Title, req, run)
}

// pendingForConfirm resolves the plan a confirm submission should apply.
// The single-use plan_id is tried first; when it is gone (used, expired,
// never issued) the answer depends on what the submission asked for: a
// recovery action (replan) computes a fresh plan and carries on, while an
// ordinary confirm submission stops at a fresh confirm page. It writes the
// response itself on every path that does not return a plan.
func (s *Server) pendingForConfirm(w http.ResponseWriter, r *http.Request, kind planKind, sel selection, req mutationRequest) (any, bool) {
	if req.PlanID != "" {
		// PEEK before taking, so a handle belonging to a DIFFERENT flow is
		// refused without being consumed - the same reason POST
		// /api/v1/jobs peeks (api_jobs.go). Without it, an install confirm
		// carrying an uninstall's plan_id would burn that other plan and
		// then fail the job on a type assertion.
		if stored := s.takePlanOfKind(req.PlanID, kind.Name); stored != nil {
			return stored, true
		}
	}

	if !req.Replan {
		s.planAndConfirm(w, r, kind, sel, req, confirmContext{Notice: noticePlanUnavailable})
		return nil, false
	}

	planOpts, err := kind.Form.PlanOptions(r)
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusBadRequest, kind.Title, err)
		return nil, false
	}
	_, pending, err := kind.Plan(r.Context(), s, sel, planOpts)
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusInternalServerError, kind.Title, err)
		return nil, false
	}
	return pending, true
}

// takePlanOfKind redeems id only when the plan stored under it belongs to
// kind, returning nil when it is missing, expired, already applied, or
// another flow's. A mismatched kind leaves that other plan takeable.
func (s *Server) takePlanOfKind(id planID, kind string) any {
	storedKind, ok := s.plans.Kind(id)
	if !ok {
		s.log.Debug("serve: confirm submitted an unavailable plan", "kind", kind)
		return nil
	}
	if storedKind != kind {
		s.log.Warn("serve: confirm submitted another flow's plan", "want", kind, "got", storedKind)
		return nil
	}
	stored, err := s.plans.Take(id)
	if err != nil {
		s.log.Debug("serve: plan became unavailable between peek and take", "kind", kind, "err", err)
		return nil
	}
	return stored.Plan
}

// applyPlannedInline is the ?sync=1 fallback for a planned kind: the Apply
// runs on this request's goroutine and its outcome is rendered directly.
// The two answerable failures land where they land on the job path - a
// re-plan confirm page for a stale plan, a conflict confirm page offering
// Overwrite - so the two paths differ in when the user learns the outcome,
// never in what the outcome is.
func (s *Server) applyPlannedInline(w http.ResponseWriter, r *http.Request, kind planKind, sel selection, req mutationRequest, run func(context.Context, core.EventSink) (any, error)) {
	result, err := run(applyContext(r), nil)

	var conflictErr *core.ConflictError
	switch {
	case err == nil:
		s.renderMutationResult(w, r, sel, kind.Title, jobKindFacts(kind.Name, result))
	case errors.As(err, &conflictErr):
		s.planAndConfirm(w, r, kind, sel, req, confirmContext{
			Notice:    noticeConflicts,
			Conflicts: conflictErr.Conflicts,
			Overwrite: true,
		})
	case errors.Is(err, core.ErrStalePlan):
		s.planAndConfirm(w, r, kind, sel, req, confirmContext{Notice: noticeStalePlan})
	default:
		s.renderMutationFailure(w, r, http.StatusInternalServerError, kind.Title, err)
	}
}

// applyContext is the context an inline (?sync=1) Apply runs under: the
// request's values, with its CANCELLATION dropped. A mutation must not be
// torn up half-written because the browser tab that started it went away -
// the same rule the job registry enforces by rooting jobs at the server's
// context (jobs.go), applied to the one path where the Apply really does
// run on a request goroutine. It uses context.WithoutCancel rather than
// context.Background so this package's sanctioned Background call count
// stays at zero.
func applyContext(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}

// startMutationJob starts run as a job carrying req as its redo value (so
// the job page can offer a recovery action) and redirects to the job page.
// 303 See Other is the POST-to-GET redirect: a reload of the job page must
// not re-submit the mutation.
func (s *Server) startMutationJob(w http.ResponseWriter, r *http.Request, kindName, title string, req mutationRequest, run func(context.Context, core.EventSink) (any, error)) {
	id, err := s.jobs.StartWith(kindName, req, run)
	if errors.Is(err, errRegistryClosing) {
		s.renderMutationFailure(w, r, http.StatusServiceUnavailable, title, err)
		return
	}
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusInternalServerError, title, err)
		return
	}
	http.Redirect(w, r, "/jobs/"+string(id), http.StatusSeeOther)
}

// confirmContext is what planAndConfirm needs beyond the plan itself: why
// the page is being rendered (Notice), the conflicts a refused Apply
// reported, and whether the submit button should carry AcceptConflicts.
type confirmContext struct {
	Notice    string
	Conflicts []core.Conflict
	Overwrite bool
}

// planAndConfirm computes the kind's plan, stores it, and renders the
// confirm page. It is the entry submission's whole job, and also where both
// recoveries land.
func (s *Server) planAndConfirm(w http.ResponseWriter, r *http.Request, kind planKind, sel selection, req mutationRequest, cc confirmContext) {
	planOpts, err := kind.Form.PlanOptions(r)
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusBadRequest, kind.Title, err)
		return
	}
	_, pending, err := kind.Plan(r.Context(), s, sel, planOpts)
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusInternalServerError, kind.Title, err)
		return
	}
	applyOpts, err := kind.Form.ApplyOptions(r)
	if err != nil {
		s.renderMutationFailure(w, r, http.StatusBadRequest, kind.Title, err)
		return
	}

	view := kind.Form.Confirm(pending, applyOpts)
	action := req.actionPath()
	data := confirmPageData{
		pageChrome: s.chrome(r, kind.Title, &sel),
		KindTitle:  kind.Title,
		Action:     action,
		SyncAction: action + "?" + syncParam + "=1",
		PlanID:     s.plans.Put(pending, kind.Name),
		Notice:     cc.Notice,
		Conflicts:  cc.Conflicts,
		// Either the failure that produced this page said so, or the
		// submission that produced it already carried the decision (see
		// confirmView.AcceptConflicts).
		Overwrite: cc.Overwrite || view.AcceptConflicts,
		View:      view,
	}
	s.render(w, confirmTemplate, data)
}

// confirmPageData is the confirm page's template data. Everything
// kind-specific reaches the template through View (confirmView), so one
// template renders every flow.
type confirmPageData struct {
	pageChrome

	// KindTitle is the flow's human label ("Install").
	KindTitle string
	// Action is where the confirm form posts; SyncAction is the same route
	// with ?sync=1, used by the "run and wait" submit button.
	Action     string
	SyncAction string
	// PlanID is the handle this page's form redeems.
	PlanID planID
	// Notice explains why this page is being shown again, when it is.
	Notice string
	// Conflicts is the refused Apply's conflict list, rendered above the
	// form; Overwrite makes the form submit AcceptConflicts.
	Conflicts []core.Conflict
	Overwrite bool
	// View is the kind's own display data.
	View confirmView
}

// resultPageData is the ?sync=1 fallback's outcome page, and the page every
// mutation failure that never became a job renders.
type resultPageData struct {
	pageChrome

	// KindTitle is the flow's human label.
	KindTitle string
	// Failed distinguishes the two renderings; Message is the failure's
	// text and Details its typed payload as JSON, exactly as the job page
	// renders a failed job.
	Failed  bool
	Message string
	Details string
	// Facts is a successful Apply's readout (the kind's Summarize).
	Facts []resultFact
}

// renderMutationResult renders a successful inline Apply. It takes the
// selection the handler already resolved rather than resolving it again:
// the mutation has COMMITTED by this point, so a second resolution failing
// must never turn a successful mutation into an error page.
func (s *Server) renderMutationResult(w http.ResponseWriter, r *http.Request, sel selection, title string, facts []resultFact) {
	s.render(w, resultTemplate, resultPageData{
		pageChrome: s.chrome(r, title, &sel),
		KindTitle:  title,
		Facts:      facts,
	})
}

// renderMutationFailure renders a mutation failure as a real page with the
// given status - never bare error text (WEBUI.md), and never a JSON
// envelope, since these routes are the browser's, not /api/v1's. The typed
// details go through the same encoder the API uses, so a page and an API
// response can never describe one failure differently.
func (s *Server) renderMutationFailure(w http.ResponseWriter, r *http.Request, status int, title string, err error) {
	s.log.Error("serve: mutation failed", "kind", title, "status", status, "err", err)

	data := resultPageData{
		pageChrome: s.chrome(r, title, nil),
		KindTitle:  title,
		Failed:     true,
		Message:    err.Error(),
	}
	if details := errorDetails(err); details != nil {
		data.Details = s.renderJobErrorDetails(details)
	}
	s.renderStatus(w, status, resultTemplate, data)
}

// resolveReadyPageSelection is resolveReadyAPISelection's page twin: a
// mutation needs both a game and a profile, and an unresolvable selection
// gets a real HTML page (404) listing what went wrong rather than the JSON
// envelope an API caller would want.
func (s *Server) resolveReadyPageSelection(w http.ResponseWriter, r *http.Request) (selection, bool) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return sel, false
	}
	if !sel.ready() {
		msg := sel.Warning
		if msg == "" {
			msg = "no games configured"
		}
		s.renderStatus(w, http.StatusNotFound, resultTemplate, resultPageData{
			pageChrome: s.chrome(r, "Mutation", &sel),
			KindTitle:  "Mutation",
			Failed:     true,
			Message:    msg,
		})
		return sel, false
	}
	return sel, true
}
