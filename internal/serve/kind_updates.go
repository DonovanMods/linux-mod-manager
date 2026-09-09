// kind_updates.go registers the "updates" plan kind - #74's per-item update
// selection (docs/plans/2026-08-30-serve-impl.md Task 9): a set of ticked
// mods becomes ONE plan and ONE job, and only the ticked rows are applied.
//
// It is the first kind whose target is a SET rather than one mod, and that
// changes exactly one thing about the shape: the selection arrives as
// repeated keys in the plan request body (updatesPlanRequest.Mods) instead
// of a path segment. Everything else - the plan store, the job, the CSRF
// gate - is the same machinery every other flow uses.
//
// #324 (Unit 8): the batch itself is now core's. This file used to own the
// loop AND the two documents it answered with, because there was no core
// batch flow to defer to - which meant the batch's real decisions (ordering,
// what a per-item failure does to the rest, how a locked ref is refused)
// lived here, in a shape that disagreed with `lmm update --all`'s own loop
// about the last of those. Both loops are gone; this kind is now the thin
// adapter the Phase-3 rule asks for - decode the selection, call
// Service.PlanUpdateBatch, hand the plan back on confirm to
// Service.ApplyUpdateBatch - and the documents on the wire are core's
// UpdateBatchPlan and UpdateBatchResult verbatim.
//
// What the SPA sees is a superset of what it saw before: UpdateBatchPlan
// carries updates/not_found/game_id/profile exactly as the retired
// updatesBatchPlan did, and UpdateBatchResult adds game_id/profile plus a
// `skipped` list to the applied/failed pair the retired updatesBatchResult
// had (verified key-for-key against both sets of goldens). The one BEHAVIOUR
// change: a locked ref now lands in `skipped` with the engine's own refusal
// sentence instead of in `failed` with the wrapped error - the #97 rule the
// CLI already followed.
package serve

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "updates",
		PlanOptions:  decodeKindOptions[updatesPlanRequest],
		ApplyOptions: decodeKindOptions[updatesApplyRequest],
		Plan:         planUpdatesKind,
		Apply:        applyUpdatesKind,
	})
}

// updatesPlanRequest is POST /api/v1/plans/updates' request body: which
// installed mods to update, as domain.ModKey strings.
type updatesPlanRequest struct {
	Mods []string `json:"mods"`
}

// validate implements validatingOptions. An empty selection is refused
// here rather than planned as a batch of nothing; the browser route answers
// it earlier still, with a page rather than an error (mutations.go).
func (r *updatesPlanRequest) validate() error {
	if len(r.Mods) == 0 {
		return errors.New(`"mods" must name at least one installed mod`)
	}
	for _, key := range r.Mods {
		if _, _, ok := splitModKey(key); !ok {
			return fmt.Errorf("mod %q is not a \"<source-id>:<mod-id>\" key", key)
		}
	}
	return nil
}

// splitModKey splits domain.ModKey's "<source-id>:<mod-id>" back into its
// halves. It cuts at the FIRST colon: a source id is a registry key and
// never contains one, while a mod id from a custom source might.
func splitModKey(key string) (sourceID, modID string, ok bool) {
	sourceID, modID, ok = strings.Cut(key, ":")
	if !ok || sourceID == "" || modID == "" {
		return "", "", false
	}
	return sourceID, modID, true
}

// updatesApplyRequest is the "options" member POST /api/v1/jobs accepts for
// an updates plan. Both members mirror `lmm update --force/--no-hooks` and
// are apply-time because ApplyUpdateBatch reads them from opts.
type updatesApplyRequest struct {
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// batchOptions renders the request as the core options struct. StopOnError
// is deliberately not exposed: the web UI's batch is a set of independent
// rows a user ticked, and abandoning the untried ones because an early one
// failed is not what that gesture means (core's default, and both the
// pre-#324 loops', is to carry on and name the failures).
func (r updatesApplyRequest) batchOptions() core.UpdateBatchOptions {
	return core.UpdateBatchOptions{Force: r.Force, SkipHooks: r.SkipHooks}
}

// pendingUpdates is what the plan store holds between Plan and Apply: the
// core plan itself (pointer identity preserved, so its unexported freshness
// snapshot survives to ApplyUpdateBatch's staleness check) and the game it
// was computed for - the same shape pendingSwitch and every other Plan/Apply
// kind uses.
type pendingUpdates struct {
	Game *domain.Game
	Plan *core.UpdateBatchPlan
}

// planUpdatesKind implements planKind.Plan for "updates": PlanUpdateBatch
// runs the same update check the /updates surface ran, then keeps the rows
// the user ticked. The check is re-run rather than trusted from the page
// because the page may be minutes old, and applying an update the source no
// longer offers is worse than telling the user it went away - which the
// plan's own NotFound does.
func planUpdatesKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(updatesPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("updates plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanUpdateBatch(ctx, sel.Game, sel.Profile, req.Mods)
	if err != nil {
		return nil, nil, err
	}
	return plan, &pendingUpdates{Game: sel.Game, Plan: plan}, nil
}

// applyUpdatesKind implements planKind.Apply for "updates". ctx is the
// job's own context (jobs.go); core checks it between items, never
// mid-file-operation (Ruling 16).
func applyUpdatesKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingUpdates)
	if !ok {
		return nil, fmt.Errorf("updates apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(updatesApplyRequest)
	if !ok {
		return nil, fmt.Errorf("updates apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyUpdateBatch(ctx, p.Game, p.Plan, req.batchOptions(), sink)
}
