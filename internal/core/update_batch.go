// Package core: this file holds the BATCH update flow -
// PlanUpdateBatch/PlanUpdateBatchFrom and ApplyUpdateBatch, their options/
// plan/result types and the per-item outcome vocabulary (#324).
//
// Why it exists: `lmm update --all` and the web UI's per-item update
// selection (#74) were two hand-written loops over the single-mod pair, and
// the batch semantics that matter - which order the items are applied in,
// what a per-item failure does to the rest of the run, how a locked ref is
// refused, and where the ONE freshness window sits - lived in those two
// loops rather than in core. Two copies of a policy is one copy too many:
// serve's loop attempted a locked mod and reported the refusal as a
// failure, while the CLI filtered locked mods out before its loop and
// reported them together, so the same profile answered the same question
// two different ways depending on which frontend asked.
//
// The shape it keeps from those loops (both were right about this): the
// batch plan is the SELECTION, not N pre-computed single-mod plans. Ruling
// 5 makes a plan a contract about a world that has not moved, and the first
// item's apply moves it for every item after it - so each item is re-planned
// via PlanUpdateFrom immediately before its own apply, inside the apply,
// costing no second source query. The batch's own freshness window is
// checked exactly once, at the top of ApplyUpdateBatch: it proves the world
// has not moved since the user was shown the selection.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// UpdateBatchPlan is the pure, displayable result of PlanUpdateBatch: the
// selection of updates a batch would apply, in the order it would apply
// them, plus the selected keys the check found no update for.
//
// It is the frontends' shared confirm document - serve's
// POST /api/v1/plans/updates answers it verbatim.
type UpdateBatchPlan struct {
	// GameID/Profile identify what the selection was computed against -
	// ApplyUpdateBatch re-checks its freshness precondition against exactly
	// these two.
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Updates are the selected rows the update check actually found, in the
	// order it reported them - which is the order ApplyUpdateBatch applies
	// them in (see its doc comment for the full ordering contract).
	Updates []domain.Update `json:"updates"`
	// NotFound are selected keys the check reported no update for - a mod
	// updated by someone else since the selection was made, or a stale
	// bookmark. They are named rather than silently dropped: a user who
	// ticked five boxes and sees four in the plan deserves to know which one
	// went missing. Sorted, so a re-plan of the same selection is stable.
	NotFound []string `json:"not_found,omitzero"`
	// snapshot is the installed-mod set this plan was computed against
	// (Ruling 5): ApplyUpdateBatch re-derives it under beginOp and returns
	// ErrStalePlan when it no longer matches. Unexported and outside the
	// wire contract, mirroring UpdatePlan.snapshot exactly.
	snapshot installedSnapshot `json:"-"`
}

// UpdateBatchOptions configures ApplyUpdateBatch. Force and SkipHooks are
// handed to every item's own UpdateOptions unchanged; StopOnError is the
// batch's own policy.
type UpdateBatchOptions struct {
	// Force: continue past a failing uninstall.before_each/install.before_each
	// hook (warn instead of fail) - see UpdateOptions.Force.
	Force bool
	// SkipHooks: run no hooks even when hooks are configured (the CLI's
	// --no-hooks) - see UpdateOptions.SkipHooks.
	SkipHooks bool
	// StopOnError abandons the rest of the batch after the first item that
	// FAILS (not one that is skipped), returning the item's own error
	// alongside the partial result.
	//
	// The default (false) is what both hand-written loops did and what a
	// batch should do: a locked mod refuses, a source hiccups, and the
	// remaining rows still get their update, with the failures named in the
	// result rather than thrown away. StopOnError exists for a caller that
	// would rather stop and be told than half-apply a set it thinks of as
	// one change; nothing in lmm passes it today.
	StopOnError bool
}

// UpdateBatchFailure is one item the batch could not update, with the
// reason as text - a source failure, a stale row, a hook that would not
// run. A lock refusal is NOT a failure: it is a skip (see
// UpdateBatchResult.Skipped).
type UpdateBatchFailure struct {
	// Mod is the item's domain.ModKey ("<source-id>:<mod-id>").
	Mod string `json:"mod"`
	// Name is the mod's display name as the check reported it, empty only
	// when the check itself had none.
	Name string `json:"name,omitzero"`
	// Error is the failure, rendered. It is the whole of what crosses the
	// wire: a JSON document cannot carry a typed error, so a caller that
	// needs to branch on the cause must be in-process and use Cause below.
	Error string `json:"error"`

	// cause is the original error, kept for an in-process caller. It is
	// deliberately unexported: it is not part of the wire contract, and a
	// document decoded from JSON has none.
	cause error
}

// Cause returns the original error behind this failure for an in-process
// caller (errors.Is/errors.As), or nil for a failure that came off the wire
// - the typed error does not survive JSON, only the Error text does.
func (f UpdateBatchFailure) Cause() error { return f.cause }

// UpdateBatchResult reports the outcome of ApplyUpdateBatch: one entry per
// item, sorted into what happened to it. The per-item entries are the
// frozen UpdateApplyResult verbatim - a batch is a sequence of single
// updates, and its report should read as one.
//
// Applied + Failed + Skipped account for every item in the plan the batch
// reached; a run that stopped early (cancellation, or StopOnError) simply
// has fewer than len(plan.Updates) of them, which is what makes the result
// worth returning alongside the error.
type UpdateBatchResult struct {
	// GameID/Profile identify what was applied, so a stored result document
	// is self-describing.
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Applied are the items that completed, in application order.
	Applied []UpdateApplyResult `json:"applied"`
	// Failed are the items that could not be applied, in application order.
	Failed []UpdateBatchFailure `json:"failed,omitzero"`
	// Skipped are the items the batch declined to attempt, in application
	// order - today exactly the locked refs (#97): Status is UpdateSkipped
	// and Reason is the engine's own refusal sentence, so a frontend renders
	// the lock refusal without re-wording it. Kept apart from Failed
	// because "we did not try, and here is why" is a different fact from
	// "we tried and it broke".
	Skipped []UpdateApplyResult `json:"skipped,omitzero"`
	// ErrorMessage carries a partial update-CHECK failure (the source query
	// that produced plan.Updates, not the apply below) - ApplyUpdateBatch
	// itself never sets this; it is a caller-populated field for a frontend
	// that ran its own check before planning (`lmm update --all --json`'s
	// CheckGameUpdates call, mirroring UpdateCheckReport.ErrorMessage) to
	// carry that failure onto the SAME document the apply produced, rather
	// than losing it once the run moves past the check-only report.
	ErrorMessage string `json:"error,omitempty"`
}

// PlanUpdateBatch computes the batch plan for selection in profileName: it
// runs the same CheckGameUpdates every update surface runs, then keeps the
// selected rows.
//
// selection names installed mods by domain.ModKey ("<source-id>:<mod-id>").
// A nil selection means "every update the check found"; a non-nil one
// filters, and every selected key with no update behind it is reported in
// the plan's NotFound rather than dropped.
//
// Network reads (CheckGameUpdates, which delegates to each registered
// source's CheckUpdates) are expected; no DB write, filesystem write, hook
// execution, or download ever happens here. A caller that ALREADY has the
// check's updates in hand should call PlanUpdateBatchFrom instead, for the
// same reason PlanUpdateFrom exists: re-checking would cost a second live
// source query per source and could disagree with what the user was shown.
func (s *Service) PlanUpdateBatch(ctx context.Context, game *domain.Game, profileName string, selection []string) (*UpdateBatchPlan, error) {
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	updates, err := s.CheckGameUpdates(ctx, game, profileName, installed, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to check updates: %w", err)
	}
	return s.PlanUpdateBatchFrom(ctx, game, profileName, updates, selection)
}

// PlanUpdateBatchFrom builds the batch plan from updates the caller already
// found - the same UpdateBatchPlan PlanUpdateBatch would return for the
// same check, computed WITHOUT re-invoking CheckGameUpdates. It is
// PlanUpdateFrom's batch-level twin and exists for the same reason (#289
// review, Important 1): `lmm update` prints its table from one check, and
// re-running that check to plan the apply would both double the live source
// queries and let the plan disagree with the table the user is reading.
//
// Only a local read happens here: the Ruling-5 installed-mod snapshot.
func (s *Service) PlanUpdateBatchFrom(ctx context.Context, game *domain.Game, profileName string, updates []domain.Update, selection []string) (*UpdateBatchPlan, error) {
	snapshot, err := s.currentInstalledSnapshot(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	plan := &UpdateBatchPlan{
		GameID:   game.ID,
		Profile:  profileName,
		Updates:  make([]domain.Update, 0, len(updates)),
		snapshot: snapshot,
	}

	if selection == nil {
		plan.Updates = append(plan.Updates, updates...)
		return plan, nil
	}

	wanted := make(map[string]bool, len(selection))
	for _, key := range selection {
		wanted[key] = true
	}
	for _, upd := range updates {
		key := domain.ModKey(upd.InstalledMod.SourceID, upd.InstalledMod.ID)
		if wanted[key] {
			plan.Updates = append(plan.Updates, upd)
			delete(wanted, key)
		}
	}
	// Reported in the SELECTION's own order rather than map order, so a
	// re-plan of the same selection produces the same document.
	for _, key := range selection {
		if wanted[key] {
			plan.NotFound = append(plan.NotFound, key)
			delete(wanted, key)
		}
	}
	return plan, nil
}

// ApplyUpdateBatch applies plan's selection.
//
// Ordering: items are applied strictly in plan.Updates order - the order
// CheckGameUpdates reported them, which is the order the plan document
// showed the user. Nothing here reorders, groups by source, or parallelises;
// an update writes the DB, the profile and the game directory, and the
// mutation slot beginOp holds is service-wide anyway.
//
// One freshness window: checkPlanFresh runs ONCE, as the first statement
// inside the op, against the whole batch's snapshot. A world that moved
// between the selection and the confirm refuses the batch as stale having
// changed nothing. Inside the loop each item is then re-planned with
// PlanUpdateFrom immediately before its own apply - because the previous
// item's apply legitimately moved the world, so a per-item plan computed up
// front would be stale by construction (kind_updates.go and the CLI's --all
// loop both do this, for this reason).
//
// Per-item outcomes:
//
//   - A LOCKED ref is skipped, never attempted (#97): ApplyUpdate refuses on
//     the lock alone, so trying it would only turn a policy decision into an
//     error. The skip carries UpdateSkipped and the plan's own Refusal
//     sentence as its Reason.
//   - A RecompileNeeded row (#196/#197 merged-pak staleness) is routed to
//     the merged-pak regen instead of the version-bump path, exactly as
//     `lmm update <mod>` routes it - such a row carries no version change,
//     so applying it as one would fail the no-op guard.
//   - Any other error is recorded as an UpdateBatchFailure and the batch
//     continues, unless StopOnError is set.
//
// Cancellation (Ruling 16): ctx is checked BETWEEN items, never mid-item -
// the in-flight item's own paired DB/profile writes complete through core's
// completeProfileWrite/completeDBWrite helpers, and the run then stops and
// returns ctx.Err() alongside the partial result. A cancelled batch
// therefore leaves every item it reached either fully applied or fully
// untouched.
//
// sink may be nil; every item's own ApplyUpdate events flow through it
// unchanged, bracketed by this flow's own UpdateBatchItem* events so a
// renderer can tell whose progress it is watching.
func (s *Service) ApplyUpdateBatch(ctx context.Context, game *domain.Game, plan *UpdateBatchPlan, opts UpdateBatchOptions, sink EventSink) (*UpdateBatchResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &UpdateBatchResult{GameID: plan.GameID, Profile: plan.Profile}, err
	}
	defer release()
	return s.applyUpdateBatch(ctx, game, plan, opts, sink)
}

func (s *Service) applyUpdateBatch(ctx context.Context, game *domain.Game, plan *UpdateBatchPlan, opts UpdateBatchOptions, sink EventSink) (*UpdateBatchResult, error) {
	result := &UpdateBatchResult{GameID: plan.GameID, Profile: plan.Profile}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	// Ruling 5, once for the whole batch: first statement inside the op,
	// before any lock check, hook or side effect.
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}

	total := len(plan.Updates)
	for i, upd := range plan.Updates {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		mod := upd.InstalledMod
		key := domain.ModKey(mod.SourceID, mod.ID)
		scope := Scope{
			Op:      OpUpdate,
			Mod:     &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version},
			ModName: mod.Name,
			Index:   i + 1,
			Total:   total,
		}
		fail := func(err error) {
			result.Failed = append(result.Failed, UpdateBatchFailure{
				Mod: key, Name: mod.Name, Error: err.Error(), cause: err,
			})
			emit(ModEvent{Scope: scope, Phase: UpdateBatchItemFailed, Detail: err.Error()})
		}

		emit(ModEvent{Scope: scope, Phase: UpdateBatchItem, Version: upd.NewVersion})

		itemPlan, err := s.PlanUpdateFrom(ctx, game, plan.Profile, upd)
		if err != nil {
			fail(err)
			if opts.StopOnError {
				return result, err
			}
			continue
		}

		if itemPlan.Locked {
			skip := planUpdateSkip(itemPlan, upd.NewVersion)
			result.Skipped = append(result.Skipped, skip)
			emit(ModEvent{Scope: scope, Phase: UpdateBatchItemSkipped, Detail: skip.Reason})
			continue
		}

		var applied *UpdateApplyResult
		if itemPlan.RecompileNeeded {
			// #196/#197: no version change to apply, only the compiled
			// artifact to rebuild - the same routing applyUpdate (cmd/lmm)
			// does for a single row.
			applied, err = s.applyMergedPakRegen(ctx, game, plan.Profile, sink)
		} else {
			applied, err = s.applyUpdate(ctx, game, itemPlan,
				UpdateOptions{Force: opts.Force, SkipHooks: opts.SkipHooks}, sink)
		}
		if err != nil {
			fail(err)
			// Ruling 16: a cancelled item has already completed its own
			// paired writes; stop the run here rather than carrying on into
			// the next item with a dead context.
			if cerr := ctx.Err(); cerr != nil {
				return result, cerr
			}
			if opts.StopOnError {
				return result, err
			}
			continue
		}
		result.Applied = append(result.Applied, *applied)
		emit(ModEvent{Scope: scope, Phase: UpdateBatchItemApplied, Version: applied.ToVersion})
	}

	return result, nil
}

// planUpdateSkip renders a locked item's refusal as the UpdateApplyResult a
// batch records for it: the same document an applied item produces, with
// UpdateSkipped and the plan's own precomputed Refusal sentence, so a
// renderer needs no second vocabulary for "not attempted".
//
// Mod carries the ref as it STANDS - the lock's target version, flagged
// locked - matching cmd/lmm's own planUpdateResult for the identical case:
// nothing was written, so the ref the document names must be the one still
// in the profile.
func planUpdateSkip(plan *UpdatePlan, toVersion string) UpdateApplyResult {
	return UpdateApplyResult{
		Mod: domain.ModReference{
			SourceID: plan.Mod.SourceID, ModID: plan.Mod.ID,
			Version: plan.LockedVersion, Locked: true,
		},
		Name:        plan.Mod.Name,
		FromVersion: plan.Mod.Version,
		ToVersion:   toVersion,
		Status:      UpdateSkipped,
		Reason:      plan.Refusal,
	}
}
