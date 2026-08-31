// kind_updates.go registers the "updates" plan kind - #74's per-item update
// selection (docs/plans/2026-08-30-serve-impl.md Task 9): the /updates
// checkbox set becomes ONE plan, ONE confirm page and ONE job, and only the
// ticked rows are applied.
//
// It is the first kind whose target is a SET rather than one mod, and that
// changes exactly one thing about the shape: the selection arrives as
// repeated form fields instead of path segments, so the confirm page carries
// it back through confirmView.Hidden. Everything else - the plan store, the
// job, the ?sync=1 fallback, the CSRF gate - is the same machinery every
// other flow uses.
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
	"net/http"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// updateModField is the /updates table's checkbox name. Each ticked box
// submits one "<source-id>:<mod-id>" value - domain.ModKey's own spelling,
// so the page, the form and the plan all name a mod the same way.
const updateModField = "mod"

func init() {
	registerPlanKind(planKind{
		Name:         "updates",
		Title:        "Update",
		PlanOptions:  decodeKindOptions[updatesPlanRequest],
		ApplyOptions: decodeKindOptions[updatesApplyRequest],
		Plan:         planUpdatesKind,
		Apply:        applyUpdatesKind,
		Summarize:    summarizeUpdatesResult,
		Form: &kindForm{
			PlanOptions:  updatesPlanForm,
			ApplyOptions: updatesApplyForm,
			Confirm:      confirmUpdatesPlan,
		},
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
	Game     *domain.Game
	Profile  string
	Updates  []domain.Update
	NotFound []string
	// Selection is the submitted key list, carried so the confirm page can
	// re-send it and a re-plan can compute the same batch.
	Selection []string
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
		Game:      sel.Game,
		Profile:   sel.Profile,
		Updates:   picked,
		NotFound:  missing,
		Selection: append([]string(nil), req.Mods...),
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

// summarizeUpdatesResult implements planKind.Summarize for "updates".
func summarizeUpdatesResult(result any) []resultFact {
	res, ok := result.(*updatesBatchResult)
	if !ok {
		return nil
	}

	facts := make([]resultFact, 0, len(res.Applied)+len(res.Failed)+1)
	for _, applied := range res.Applied {
		facts = append(facts, resultFact{
			Label: "Updated",
			Value: fmt.Sprintf("%s %s -> %s", applied.Name, applied.FromVersion, applied.ToVersion),
		})
		for _, w := range applied.Warnings {
			facts = append(facts, resultFact{Label: "Warning", Value: applied.Name + ": " + w})
		}
	}
	for _, failed := range res.Failed {
		facts = append(facts, resultFact{Label: "Not updated", Value: updateFailureText(failed)})
	}
	if len(facts) == 0 {
		facts = append(facts, resultFact{Label: "Updates", Value: "nothing was left to apply"})
	}
	return facts
}

// updateFailureText names one refused or failed mod on the result readout.
func updateFailureText(failed updateBatchFailure) string {
	name := failed.Name
	if name == "" {
		name = failed.Mod
	}
	return name + " - " + failed.Error
}

// updatesPlanForm implements kindForm.PlanOptions: the ticked checkboxes.
func updatesPlanForm(r *http.Request) (any, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("reading form: %w", err)
	}
	return updatesPlanRequest{Mods: append([]string(nil), r.Form[updateModField]...)}, nil
}

// updatesApplyForm implements kindForm.ApplyOptions: the confirm page's two
// checkboxes, read back into the same type the JSON decoder produces.
func updatesApplyForm(r *http.Request) (any, error) {
	return updatesApplyRequest{
		Force:     formFlag(r, forceField),
		SkipHooks: formFlag(r, skipHooksField),
	}, nil
}

// confirmUpdatesPlan implements kindForm.Confirm: which mods would move,
// from what to what, and what would be refused if it were attempted.
func confirmUpdatesPlan(pending, opts any) confirmView {
	p, ok := pending.(*pendingUpdates)
	if !ok {
		return confirmView{Submit: "Update"}
	}
	req, _ := opts.(updatesApplyRequest)

	view := confirmView{
		Heading: fmt.Sprintf("%d selected mod(s)", len(p.Updates)),
		Submit:  "Update",
		Facts: []resultFact{
			{Label: "Profile", Value: p.Profile},
			{Label: "To update", Value: fmt.Sprintf("%d", len(p.Updates))},
		},
		Toggles: []confirmToggle{
			{
				Name:    skipHooksField,
				Label:   "Skip hooks",
				Help:    "run no install.* or uninstall.* hooks at all",
				Checked: req.SkipHooks,
			},
			{
				Name:    forceField,
				Label:   "Continue past a failing before-hook",
				Help:    "the failure is recorded as a warning instead of stopping that mod",
				Checked: req.Force,
			},
		},
		Hidden: updateSelectionFields(p.Selection),
	}

	var moves, locked []string
	for _, upd := range p.Updates {
		if upd.Locked {
			locked = append(locked, fmt.Sprintf("%s (locked at %s)", upd.InstalledMod.Name, upd.LockedVersion))
			continue
		}
		moves = append(moves, updateMoveText(upd))
	}
	if len(moves) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Mods that would be updated", Items: moves})
	}
	if len(locked) > 0 {
		view.Lists = append(view.Lists, confirmList{
			Label: "Locked, so the update would be refused",
			Items: locked,
		})
	}
	if len(p.NotFound) > 0 {
		view.Lists = append(view.Lists, confirmList{
			Label: "Selected, but no update is available any more",
			Items: p.NotFound,
		})
	}
	return view
}

// updateMoveText renders one row the way the /updates table does: a version
// move, or a recompile with the reason that triggered it (#196/#197, where
// NewVersion deliberately equals the installed version).
func updateMoveText(upd domain.Update) string {
	if upd.RecompileNeeded {
		text := upd.InstalledMod.Name + " - recompile"
		if upd.RecompileReason != "" {
			text += " (" + upd.RecompileReason + ")"
		}
		return text
	}
	return fmt.Sprintf("%s %s -> %s", upd.InstalledMod.Name, upd.InstalledMod.Version, upd.NewVersion)
}

// updateSelectionFields renders the ticked set as the hidden fields the
// confirm form carries back, so "Update plan" re-plans the SAME batch
// rather than an empty one.
func updateSelectionFields(selection []string) []queryParam {
	if len(selection) == 0 {
		return nil
	}
	fields := make([]queryParam, 0, len(selection))
	for _, key := range selection {
		fields = append(fields, queryParam{Key: updateModField, Value: key})
	}
	sortQueryParams(fields)
	return fields
}
