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
// The one thing it must NOT do is compute a single core plan up front and
// apply it N times. Ruling 5 makes a plan a contract about a world that has
// not moved, and the first mod's apply moves it for every mod after it -
// which is why cmd/lmm's own bulk loop re-plans each row immediately before
// its apply (applyBulkUpdate). So does this: the batch plan is the SELECTION
// (the updates the check found, filtered to what the user ticked), and the
// job turns each one into a fresh core.UpdatePlan via PlanUpdateFrom - the
// local-only re-plan that costs no second source query - and applies that.
//
// A per-item failure does not abort the batch, matching the CLI loop
// exactly: a locked mod refuses, a source hiccups, and the remaining rows
// still get their update. The failures are named in the result document (and
// on the job page) rather than thrown away, so "3 updated, 1 refused" is
// something a user can actually read.
package serve

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
// are apply-time because ApplyUpdate reads them from opts.
type updatesApplyRequest struct {
	Force     bool `json:"force,omitzero"`
	SkipHooks bool `json:"skip_hooks,omitzero"`
}

// updateOptions renders the request as the core options struct.
func (r updatesApplyRequest) updateOptions() core.UpdateOptions {
	return core.UpdateOptions{Force: r.Force, SkipHooks: r.SkipHooks}
}

// updatesBatchPlan is the batch's wire document - what POST
// /api/v1/plans/updates answers with, and what the confirm page renders.
// There is no core type for it because there is no core batch flow: `lmm
// update` loops over one-mod plans, and so does this kind's Apply. What the
// batch itself owns is the SELECTION, which is what this document carries.
type updatesBatchPlan struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Updates are the selected rows the update check actually found, in the
	// order it reported them.
	Updates []domain.Update `json:"updates"`
	// NotFound are selected keys the check reported no update for - a mod
	// updated by someone else since the page was rendered, or a stale
	// bookmark. They are named rather than silently dropped, because a user
	// who ticked five boxes and sees four in the plan deserves to know which
	// one went missing.
	NotFound []string `json:"not_found,omitzero"`
}

// pendingUpdates is what the plan store holds between Plan and Apply. It
// carries the domain.Updates themselves rather than core plans, because
// each one is re-planned immediately before its own apply (see this file's
// doc comment).
type pendingUpdates struct {
	Game    *domain.Game
	Profile string
	Updates []domain.Update
}

// planUpdatesKind implements planKind.Plan for "updates": run the same
// update check the /updates page ran, then keep the rows the user ticked.
// The check is re-run rather than trusted from the page because the page
// may be minutes old, and applying an update the source no longer offers is
// worse than telling the user it went away.
func planUpdatesKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(updatesPlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("updates plan: unexpected options type %T", opts)
	}

	installed, err := s.svc.GetInstalledMods(ctx, sel.Game.ID, sel.Profile)
	if err != nil {
		return nil, nil, err
	}
	updates, err := s.svc.CheckGameUpdates(ctx, sel.Game, sel.Profile, installed, nil)
	if err != nil {
		return nil, nil, err
	}

	wanted := make(map[string]bool, len(req.Mods))
	for _, key := range req.Mods {
		wanted[key] = true
	}
	picked := make([]domain.Update, 0, len(req.Mods))
	for _, upd := range updates {
		key := domain.ModKey(upd.InstalledMod.SourceID, upd.InstalledMod.ID)
		if wanted[key] {
			picked = append(picked, upd)
			delete(wanted, key)
		}
	}
	missing := make([]string, 0, len(wanted))
	for key := range wanted {
		missing = append(missing, key)
	}
	sort.Strings(missing)

	document := &updatesBatchPlan{
		GameID:   sel.Game.ID,
		Profile:  sel.Profile,
		Updates:  picked,
		NotFound: missing,
	}
	pending := &pendingUpdates{
		Game:    sel.Game,
		Profile: sel.Profile,
		Updates: picked,
	}
	return document, pending, nil
}

// updatesBatchResult is the batch's result document: one entry per mod the
// job actually got through, and one per mod it could not. The per-mod
// entries are the frozen core.UpdateApplyResult verbatim - a batch is a
// sequence of single updates, and its report should read as one.
type updatesBatchResult struct {
	Applied []core.UpdateApplyResult `json:"applied"`
	Failed  []updateBatchFailure     `json:"failed,omitzero"`
}

// updateBatchFailure is one mod the batch could not update, with the reason
// as text - a locked ref's refusal, a source failure, a stale plan.
type updateBatchFailure struct {
	Mod   string `json:"mod"`
	Name  string `json:"name,omitzero"`
	Error string `json:"error"`
}

// applyUpdatesKind implements planKind.Apply for "updates". ctx is the
// job's own context (jobs.go), and it is checked between mods - the same
// place every core batch loop checks it, never mid-file-operation.
func applyUpdatesKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingUpdates)
	if !ok {
		return nil, fmt.Errorf("updates apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(updatesApplyRequest)
	if !ok {
		return nil, fmt.Errorf("updates apply: unexpected options type %T", opts)
	}

	result := &updatesBatchResult{}
	for _, upd := range p.Updates {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		key := domain.ModKey(upd.InstalledMod.SourceID, upd.InstalledMod.ID)
		// Ruling 5: re-plan immediately before this mod's own apply. A plan
		// computed for the whole batch up front would be stale the moment
		// the first mod landed. PlanUpdateFrom reuses the update already
		// found, so this costs no second source query.
		plan, err := s.svc.PlanUpdateFrom(ctx, p.Game, p.Profile, upd)
		if err != nil {
			result.Failed = append(result.Failed, updateBatchFailure{Mod: key, Name: upd.InstalledMod.Name, Error: err.Error()})
			continue
		}
		applied, err := s.svc.ApplyUpdate(ctx, p.Game, plan, req.updateOptions(), sink)
		if err != nil {
			result.Failed = append(result.Failed, updateBatchFailure{Mod: key, Name: upd.InstalledMod.Name, Error: err.Error()})
			continue
		}
		result.Applied = append(result.Applied, *applied)
	}
	return result, nil
}
