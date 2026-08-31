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
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// toggleKind is one plan-free mutation: a single core call against a
// resolved game/profile and one mod, run as a job. It is a separate, much
// smaller table than planKind on purpose - registering these as plan kinds
// would put POST /api/v1/plans/enable on the wire, an endpoint that could
// only ever answer "there is nothing to plan".
type toggleKind struct {
	// Name is the route segment (/api/v1/mods/{source}/{id}/enable) and the
	// kind stored on the job.
	Name string
	// Apply runs the core call. It takes the job's own context, never the
	// request's (jobs.go).
	Apply func(ctx context.Context, s *Server, sel selection, sourceID, modID string) (any, error)
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
		Name: "enable",
		Apply: func(ctx context.Context, s *Server, sel selection, sourceID, modID string) (any, error) {
			return s.svc.EnableMod(ctx, sel.Game, sel.Profile, sourceID, modID)
		},
	})
	registerToggleKind(toggleKind{
		Name: "disable",
		Apply: func(ctx context.Context, s *Server, sel selection, sourceID, modID string) (any, error) {
			return s.svc.DisableMod(ctx, sel.Game, sel.Profile, sourceID, modID)
		},
	})
}

// handleAPIModEnable answers POST /api/v1/mods/{source}/{id}/enable.
func (s *Server) handleAPIModEnable(w http.ResponseWriter, r *http.Request) {
	s.startToggleJob(w, r, "enable")
}

// handleAPIModDisable answers POST /api/v1/mods/{source}/{id}/disable.
func (s *Server) handleAPIModDisable(w http.ResponseWriter, r *http.Request) {
	s.startToggleJob(w, r, "disable")
}

// startToggleJob is the shared half of the two toggle endpoints: resolve
// the game/profile selection the same way every other scoped endpoint does,
// then start the core call as a job and answer 202 with its id - the same
// document POST /api/v1/jobs answers with, because from a client's point of
// view the only difference is that there was no plan to redeem first.
//
// The two enable/disable routes are registered explicitly (routes.go) rather
// than as one {action} wildcard, so an unknown action falls through to the
// /api/v1/ subtree fallback's JSON 404 instead of reaching a handler that
// would have to invent its own refusal. The queue-depth check and the
// draining-registry 503 are POST /api/v1/jobs's, for the same reasons
// (api_jobs.go): a toggle occupies the same one-mutation-at-a-time slot in
// core that every other job does.
func (s *Server) startToggleJob(w http.ResponseWriter, r *http.Request, kindName string) {
	kind, ok := lookupToggleKind(kindName)
	if !ok {
		s.writeAPIError(w, http.StatusInternalServerError, fmt.Errorf("unregistered toggle kind %q", kindName))
		return
	}

	sourceID, modID := r.PathValue("source"), r.PathValue("id")
	if sourceID == "" || modID == "" {
		s.writeAPIError(w, http.StatusBadRequest, errors.New("the mod's source and id are both required"))
		return
	}

	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	if depth := s.jobs.QueueDepth(); depth > maxQueuedJobs {
		s.log.Warn("serve: refusing a toggle, queue depth exceeded", "depth", depth, "max", maxQueuedJobs)
		s.writeAPIError(w, http.StatusConflict,
			fmt.Errorf("%d operations are already running; wait for one to finish and try again", depth))
		return
	}

	id, err := s.jobs.Start(kind.Name, func(ctx context.Context, _ core.EventSink) (any, error) {
		return kind.Apply(ctx, s, sel, sourceID, modID)
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
