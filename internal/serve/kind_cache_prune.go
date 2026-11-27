// kind_cache_prune.go registers the "cache_prune" plan kind - `lmm cache
// prune` as a Plan -> confirm -> job flow (#678), the web UI's Setup >
// Storage "Prune…" control.
//
// core.PlanCachePrune is the preview (every entry it removes, by game,
// mod and version, with its size) and core.ApplyCachePrune re-plans under
// the mutation slot and refuses a stale plan, so nothing is rebuilt here.
//
// The scope is the SELECTED game, or every game when the body says
// all_games - `lmm cache prune` with and without --game.
package serve

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "cache_prune",
		PlanOptions:  decodeKindOptions[cachePrunePlanRequest],
		ApplyOptions: decodeKindOptions[cachePruneApplyRequest],
		Plan:         planCachePruneKind,
		Apply:        applyCachePruneKind,
	})
}

// cachePrunePlanRequest is POST /api/v1/plans/cache_prune's body. An empty
// body prunes the selected game's cache.
type cachePrunePlanRequest struct {
	// AllGames prunes every configured game's cache, as `lmm cache prune`
	// without --game does.
	AllGames bool `json:"all_games,omitzero"`
}

// cachePruneApplyRequest is the job's options: a prune has none.
type cachePruneApplyRequest struct{}

// planCachePruneKind implements planKind.Plan for "cache_prune".
func planCachePruneKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(cachePrunePlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("cache prune plan: unexpected options type %T", opts)
	}
	var games []*domain.Game
	if !req.AllGames {
		games = []*domain.Game{sel.Game}
	}
	plan, err := s.svc.PlanCachePrune(ctx, games)
	if err != nil {
		return nil, nil, err
	}
	return plan, plan, nil
}

// applyCachePruneKind implements planKind.Apply for "cache_prune".
func applyCachePruneKind(ctx context.Context, s *Server, pending, _ any, sink core.EventSink) (any, error) {
	plan, ok := pending.(*core.CachePrunePlan)
	if !ok {
		return nil, fmt.Errorf("cache prune apply: unexpected pending type %T", pending)
	}
	return s.svc.ApplyCachePrune(ctx, plan, sink)
}
