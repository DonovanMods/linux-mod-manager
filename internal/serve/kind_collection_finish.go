// kind_collection_finish.go registers the "collection_finish" plan kind:
// `lmm profile finish` driven from the browser (#660) - the web UI's Finish
// collection panel. Each item's action is a job of its own, or several at
// once; an installer that asks comes back as the job's
// fomod_choices_required question, answered in the wizard and sent back in
// the apply's options, as for an install. A manual download is finished
// through the profile_fill kind, which this one does not repeat.
//
// GET /api/v1/profiles/{name}/todos (handleAPIProfileTodos, below) is the
// list itself: core.CollectionTodoReport.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "collection_finish",
		PlanOptions:  decodeKindOptions[collectionFinishPlanRequest],
		ApplyOptions: decodeKindOptions[collectionFinishApplyRequest],
		Plan:         planCollectionFinishKind,
		Apply:        applyCollectionFinishKind,
	})
}

// collectionFinishPlanRequest is POST /api/v1/plans/collection_finish's
// request body: which items to resolve and how, each named by its
// "<source-id>:<mod-id>" key (core.CollectionFinishRequest). The profile
// is the request's ?profile= selection.
type collectionFinishPlanRequest struct {
	Retry      []string `json:"retry,omitzero"`
	RetryAll   bool     `json:"retry_all,omitzero"`
	UseCurrent []string `json:"use_current,omitzero"`
	SkipFile   []string `json:"skip_file,omitzero"`
	Skip       []string `json:"skip,omitzero"`
}

// collectionFinishApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a collection_finish plan.
type collectionFinishApplyRequest struct {
	// Fomod answers FOMOD installer questions, keyed by
	// "<source-id>:<mod-id>", that a job came back with (#615's wizard).
	Fomod map[string]domain.FomodChoices `json:"fomod,omitzero"`
}

// pendingCollectionFinish is what the plan store holds between Plan and
// Apply.
type pendingCollectionFinish struct {
	Game *domain.Game
	Plan *core.CollectionFinishPlan
}

// planCollectionFinishKind implements planKind.Plan for
// "collection_finish". A request naming no item on the list, or a choice
// the item does not offer, is the caller's (400).
func planCollectionFinishKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(collectionFinishPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("collection_finish plan: unexpected options type %T", opts)
	}
	plan, err := s.svc.PlanCollectionFinish(ctx, sel.Game, core.CollectionFinishRequest{
		Profile: sel.Profile, Retry: req.Retry, RetryAll: req.RetryAll,
		UseCurrent: req.UseCurrent, SkipFile: req.SkipFile, Skip: req.Skip,
	})
	if err != nil {
		if core.IsBadFinishChoice(err) || errors.Is(err, core.ErrNotACollectionProfile) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
		return nil, nil, err
	}
	return plan, &pendingCollectionFinish{Game: sel.Game, Plan: plan}, nil
}

// applyCollectionFinishKind implements planKind.Apply for
// "collection_finish".
func applyCollectionFinishKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingCollectionFinish)
	if !ok {
		return nil, fmt.Errorf("collection_finish apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(collectionFinishApplyRequest)
	if !ok {
		return nil, fmt.Errorf("collection_finish apply: unexpected options type %T", opts)
	}
	return s.svc.ApplyCollectionFinish(ctx, p.Game, p.Plan, core.CollectionFinishOptions{Fomod: req.Fomod}, sink)
}

// handleAPIProfileTodos answers GET /api/v1/profiles/{name}/todos: the
// profile's collection items left to do (core.CollectionTodoReport) - an
// empty list for a profile with none, or with no collection.
func (s *Server) handleAPIProfileTodos(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.resolveGameAPISelection(w, r)
	if !ok {
		return
	}
	report, err := s.svc.CollectionTodos(r.Context(), sel.Game, r.PathValue("name"))
	switch {
	case errors.Is(err, domain.ErrProfileNotFound):
		s.writeAPIError(w, http.StatusNotFound, err)
		return
	case errors.Is(err, domain.ErrInvalidProfileName):
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	case err != nil:
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}
