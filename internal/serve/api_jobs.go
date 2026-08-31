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
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
)

// maxAPIRequestBytes bounds how much of a request body /api/v1 will read.
// Nothing this API accepts is large - a plan request is a handful of
// options, a job request is an id plus options - so a generous megabyte
// still refuses a client (or a bug) that tries to stream into memory.
const maxAPIRequestBytes = 1 << 20

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
