// api_jobs.go implements Task 7's mutation half of /api/v1
// (docs/plans/2026-08-30-serve-design.md §"/api/v1"): POST
// /api/v1/plans/{kind} computes a plan and hands back an opaque handle,
// POST /api/v1/jobs redeems that handle by starting the Apply as a job,
// and GET /api/v1/jobs/{id}[/events] report on it.
//
// The split exists because a plan cannot survive a round trip through the
// browser: every core plan embeds an unexported json:"-" freshness
// snapshot its Apply re-checks, so what comes back from a client is
// structurally a plan and semantically a lie. The client therefore only
// ever holds the handle (plans.go).
package serve

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// maxAPIRequestBytes bounds how much of a request body /api/v1 will read.
// Nothing this API accepts is large - a plan request is a handful of
// options, a job request is an id plus options - so a generous megabyte
// still refuses a client (or a bug) that tries to stream into memory.
const maxAPIRequestBytes = 1 << 20

// maxQueuedJobs is the ruled backpressure threshold: once more than this
// many jobs are in state running, POST /api/v1/jobs refuses new ones with
// a 409 instead of admitting them.
//
// It exists because "running" over-reports. core's beginOp (internal/core/
// ops.go) serialises mutations to one in flight at a time by BLOCKING, not
// by rejecting, so a second mutation job sits in state running while doing
// nothing at all, indistinguishable from the one actually working. Without
// a cap, a stuck deploy plus an impatient client (or a script in a loop)
// piles up an unbounded queue of jobs that each hold their plan, their ring
// buffer and their goroutine while achieving nothing. Eight is generous for
// a single-user local tool - reaching it means something is wrong, and the
// honest answer is to say so.
const maxQueuedJobs = 8

// planResponse is POST /api/v1/plans/{kind}'s success document: the plan
// itself, exactly the frozen core plan type the CLI's --dry-run --json
// emits, plus the handle POST /api/v1/jobs redeems and the kind that says
// which mutation it belongs to.
type planResponse struct {
	PlanID planID `json:"plan_id"`
	Kind   string `json:"kind"`
	Plan   any    `json:"plan"`
}

// planKindDetails is the "details" payload of the 400 envelope an unknown
// {kind} answers with. The list is generated from the registry
// (supportedPlanKinds), so a kind added in a later task appears here
// without anyone remembering to update a message.
type planKindDetails struct {
	SupportedKinds []string `json:"supported_kinds"`
}

// jobStartRequest is POST /api/v1/jobs's request body: the handle a plan
// response issued, plus that kind's apply-time options left as raw JSON so
// the kind's own decoder - not this generic one - defines their shape.
type jobStartRequest struct {
	PlanID  planID         `json:"plan_id"`
	Options jsontext.Value `json:"options,omitzero"`
}

// jobStartResponse is POST /api/v1/jobs's success document: the handle
// GET /api/v1/jobs/{id} and its SSE stream are addressed by.
type jobStartResponse struct {
	JobID jobID `json:"job_id"`
}

// readAPIBody reads r's body under maxAPIRequestBytes. An over-long body
// reports an error rather than being silently truncated into something
// that might still decode.
func readAPIBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAPIRequestBytes))
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	return body, nil
}

// handleAPIPlan answers POST /api/v1/plans/{kind}: it decodes the kind's
// options, resolves the game/profile selection the same way every other
// scoped endpoint does, computes the plan, stores the server-side object,
// and returns the plan document plus its handle.
//
// Order matters. Everything that can reject the request - unknown kind,
// undecodable options, unresolvable selection - runs BEFORE the plan is
// computed and stored, so a refused request never leaves an entry behind
// in the store for a TTL sweep to clean up later.
func (s *Server) handleAPIPlan(w http.ResponseWriter, r *http.Request) {
	kindName := r.PathValue("kind")
	kind, ok := lookupPlanKind(kindName)
	if !ok {
		s.log.Debug("api plan: unknown kind", "kind", kindName)
		s.writeJSON(w, http.StatusBadRequest, apiErrorEnvelope{
			Error:   fmt.Sprintf("unknown plan kind %q", kindName),
			Details: planKindDetails{SupportedKinds: supportedPlanKinds()},
		})
		return
	}

	body, err := readAPIBody(w, r)
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	opts, err := kind.PlanOptions(body)
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	document, pending, err := kind.Plan(r.Context(), s, sel, opts)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}

	s.writeJSON(w, http.StatusOK, planResponse{
		PlanID: s.plans.Put(pending, kind.Name),
		Kind:   kind.Name,
		Plan:   document,
	})
}

// decodeAPIRequest decodes body into v with unknown members rejected, so a
// misspelled member is a 400 instead of a silently dropped field.
func decodeAPIRequest(body []byte, v any) error {
	if err := json.Unmarshal(body, v, json.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("decoding request body: %w", err)
	}
	return nil
}

// handleAPIStartJob answers POST /api/v1/jobs: it redeems a plan_id and
// starts that plan's Apply as a background job, answering 202 Accepted with
// the job's id. 202 rather than 200 is the literal truth - the mutation has
// been accepted and is running, and nothing about its outcome is known yet.
//
// The refusal order is deliberate, and is about not burning a single-use
// plan for a request that was never going to run: the queue check and the
// options decode both happen while the plan is still merely peeked at
// (planStore.Kind), so a client that gets a 409 or a 400 here can retry the
// SAME plan_id once it has fixed the problem. Only two paths consume a plan
// without running it, and both are terminal for that plan by nature: losing
// a race to another Take, and a registry that began draining between the
// peek and the Start (the server is going away, so a re-plan against this
// process could not help anyway).
func (s *Server) handleAPIStartJob(w http.ResponseWriter, r *http.Request) {
	body, err := readAPIBody(w, r)
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	var req jobStartRequest
	if err := decodeAPIRequest(body, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.PlanID == "" {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`missing required member "plan_id"`))
		return
	}

	if depth := s.jobs.QueueDepth(); depth > maxQueuedJobs {
		s.log.Warn("serve: refusing a job, queue depth exceeded", "depth", depth, "max", maxQueuedJobs)
		s.writeAPIError(w, http.StatusConflict,
			fmt.Errorf("%d operations are already running; wait for one to finish and try again", depth))
		return
	}

	kindName, ok := s.plans.Kind(req.PlanID)
	if !ok {
		s.writeAPIError(w, http.StatusConflict, errPlanUnavailable)
		return
	}
	kind, ok := lookupPlanKind(kindName)
	if !ok {
		s.writeAPIError(w, http.StatusInternalServerError, fmt.Errorf("stored plan names unregistered kind %q", kindName))
		return
	}
	opts, err := kind.ApplyOptions(req.Options)
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	stored, err := s.plans.Take(req.PlanID)
	if err != nil {
		s.writeAPIError(w, http.StatusConflict, err)
		return
	}

	pending := stored.Plan
	id, err := s.jobs.Start(kind.Name, func(ctx context.Context, sink core.EventSink) (any, error) {
		return kind.Apply(ctx, s, pending, opts, sink)
	})
	if errors.Is(err, errRegistryClosing) {
		s.writeAPIError(w, http.StatusServiceUnavailable, err)
		return
	}
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}

	s.writeJSON(w, http.StatusAccepted, jobStartResponse{JobID: id})
}

// handleAPIJobStatus answers GET /api/v1/jobs/{id} with the job status
// document (jobStatus, goldened in testdata/json/job_status.golden):
// identity, state, timings, the event counters, and exactly one of the
// core result document or the {"error","details"} envelope. It is the
// no-stream way to read a job - what the /jobs/{id} page renders with
// JavaScript off, and what a script polls.
func (s *Server) handleAPIJobStatus(w http.ResponseWriter, r *http.Request) {
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, j.status())
}

// lookupJob resolves the {id} path value against the registry, writing the
// 404 envelope itself when the job never existed or has aged out of
// retention (jobs.go: the registry keeps the last defaultJobRetention
// jobs, in memory only).
func (s *Server) lookupJob(w http.ResponseWriter, r *http.Request) (*job, bool) {
	id := jobID(r.PathValue("id"))
	j, ok := s.jobs.job(id)
	if !ok {
		s.writeAPIError(w, http.StatusNotFound, fmt.Errorf("unknown job %q", id))
		return nil, false
	}
	return j, true
}

// handleAPINotFound is the /api/v1/ subtree fallback: any path under
// /api/v1 that no route claimed answers the JSON envelope, so EVERY
// /api/v1 response is JSON (Unit 3 review Minor 8, carried to this task).
// Without it net/http answers its own text/plain "404 page not found",
// which a client that only knows how to parse this API's envelope cannot
// read.
//
// A request that reaches here for a path a route DOES claim, just not for
// this method (a POST to a GET-only route), gets a 405 with Allow instead
// of the generic 404 (task-7 review Minor 1): apiAllowedMethods is the only
// way to ask that without actually invoking a handler.
func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	if allow := s.apiAllowedMethods(r); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		s.writeAPIError(w, http.StatusMethodNotAllowed,
			fmt.Errorf("method %s not allowed on %s", r.Method, r.URL.Path))
		return
	}
	s.writeAPIError(w, http.StatusNotFound, fmt.Errorf("no such API endpoint: %s", r.URL.Path))
}

// apiMethodsToProbe are the only methods any /api/v1 route ever uses today
// (routes.go registers each one as either GET or POST) - the closed set
// apiAllowedMethods needs to try, not every HTTP method that exists.
var apiMethodsToProbe = []string{http.MethodGet, http.MethodPost}

// apiAllowedMethods reports which of apiMethodsToProbe a registered
// /api/v1 route actually claims for r's URL, by cloning r with each
// candidate method and asking the mux which pattern it would dispatch to.
// The fallback's own pattern ("/api/v1/", registered in routes.go) doesn't
// count as a claim - it matches every method by design, which is exactly
// why net/http's built-in 405 can't fire on its own here.
func (s *Server) apiAllowedMethods(r *http.Request) []string {
	var allowed []string
	for _, method := range apiMethodsToProbe {
		clone := r.Clone(r.Context())
		clone.Method = method
		if _, pattern := s.mux.Handler(clone); pattern != "" && pattern != "/api/v1/" {
			allowed = append(allowed, method)
		}
	}
	return allowed
}
