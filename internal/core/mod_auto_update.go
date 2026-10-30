// Package core: this file holds the automatic half of an update check -
// the auto update policy acted on (#587).
//
// docs/guide/updates.md has always said an auto-policy mod is updated "when
// lmm checks", and until #587 only `lmm update` in a terminal did that:
// `lmm serve`'s checks listed those updates for the user to install by
// hand. This is the pass a frontend runs after a check that found any. It
// is the same batch `lmm update` applies them through (#324), restricted
// to the rows that policy names, and it adds three rules of its own:
//
//   - it never queues: the mutation slot is taken now or not at all
//     (BeginAutoUpdate), and a check that finds it busy leaves the work to
//     the next one;
//   - it never applies under a running game (GameRunning): the plan says
//     what was seen, so a frontend can say what it is waiting for;
//   - it never deploys a profile the user has not deployed: only the
//     game's active profile - the one whose mods are in the game directory
//   - is ever planned.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ErrGameRunning is BeginAutoUpdate's refusal for a plan made while the
// game was running: the pass is postponed, not failed.
var ErrGameRunning = errors.New("the game is running; its automatic updates wait for it to close")

// ModAutoUpdateState is what the automatic pass is doing about a check's
// auto-policy updates - the wire value of ModAutoUpdate.State.
type ModAutoUpdateState string

// The states a frontend reports.
const (
	// ModAutoUpdateApplying: a job is applying them now.
	ModAutoUpdateApplying ModAutoUpdateState = "applying"
	// ModAutoUpdateWaitingForGame: the game is running, so they are
	// applied at the first check after it closes.
	ModAutoUpdateWaitingForGame ModAutoUpdateState = "waiting_for_game"
)

// ModAutoUpdate is the automatic pass's state on an update check's report
// (UpdateCheckReport.AutoUpdate, #587): which auto-policy updates it is
// applying, or waiting to apply, and why. Only `lmm serve` sets it - `lmm
// update` applies them in the same run and reports the result instead.
type ModAutoUpdate struct {
	// State is "applying" or "waiting_for_game".
	State ModAutoUpdateState `json:"state"`
	// Mods are the updates concerned, as domain.ModKey strings, in the
	// check's order.
	Mods []string `json:"mods"`
	// GameName names the game being waited for, for "waiting for <game>
	// to close". Set only when State is waiting_for_game.
	GameName string `json:"game_name,omitzero"`
	// DetectedBy is how the running game was recognised: "launch_unit" (a
	// game `lmm launch` started) or "process". Set only when State is
	// waiting_for_game.
	DetectedBy GameRunningSignal `json:"detected_by,omitzero"`
	// JobID is the frontend's job applying them. Set only when State is
	// applying.
	JobID string `json:"job_id,omitzero"`
}

// AutoUpdateKeys returns the updates the automatic pass would apply, as
// domain.ModKey strings in the check's order: every auto-policy row except
// those the batch declines without trying - a locked ref (#97), an
// external mod (#269) and a manual-only one (#543). Leaving those out here,
// rather than letting the batch skip them, is what stops a check from
// starting a job that could only ever skip.
func AutoUpdateKeys(updates []domain.Update) []string {
	var keys []string
	for _, u := range updates {
		im := u.InstalledMod
		if im.UpdatePolicy != domain.UpdateAuto || u.Locked || im.External || im.ManualOnly {
			continue
		}
		keys = append(keys, domain.ModKey(im.SourceID, im.ID))
	}
	return keys
}

// AutoUpdatePlan is PlanAutoUpdate's answer: the auto-policy updates to
// apply, or - when Running is set - the reason they wait.
type AutoUpdatePlan struct {
	// Mods are the updates to apply, as domain.ModKey strings.
	Mods []string
	// Running is set when the game was running at plan time; the plan
	// then cannot begin (ErrGameRunning).
	Running *GameRunningStatus

	batch *UpdateBatchPlan
}

// PlanAutoUpdate plans the automatic pass over a check's updates for
// profileName: nil when there is nothing it would apply - no auto-policy
// row it would attempt (AutoUpdateKeys), or a profile that is not the
// game's active one, whose mods are not the ones in the game directory.
//
// installed is the set the check ran against. The plan's freshness
// precondition is THAT set, not the one current now, so a check that
// started before some other apply finished cannot apply what that apply
// already did: BeginAutoUpdate refuses it as stale.
//
// It reads the machine (GameRunning) and the database; it changes nothing.
func (s *Service) PlanAutoUpdate(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod, updates []domain.Update) (*AutoUpdatePlan, error) {
	keys := AutoUpdateKeys(updates)
	if len(keys) == 0 {
		return nil, nil
	}
	if err := s.requireActiveProfile(ctx, game.ID, profileName, VerbUpdateMany); err != nil {
		if errors.Is(err, ErrProfileNotActive) {
			return nil, nil
		}
		return nil, err
	}
	plan := &AutoUpdatePlan{Mods: keys}
	if running := s.GameRunning(ctx, game); running.Running {
		plan.Running = &running
		return plan, nil
	}
	snapshot, err := s.snapshotOf(game.ID, installed)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(keys))
	for _, k := range keys {
		wanted[k] = true
	}
	plan.batch = &UpdateBatchPlan{GameID: game.ID, Profile: profileName, snapshot: snapshot}
	for _, u := range updates {
		if wanted[domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)] {
			plan.batch.Updates = append(plan.batch.Updates, u)
		}
	}
	return plan, nil
}

// AutoUpdateRun is a begun automatic pass: it holds the mutation slot from
// BeginAutoUpdate until Apply returns or Abandon is called, so a frontend
// can decide to run it - start a job for it - knowing it will not wait.
type AutoUpdateRun struct {
	s       *Service
	game    *domain.Game
	plan    *UpdateBatchPlan
	opts    UpdateBatchOptions
	release func()
}

// BeginAutoUpdate takes the mutation slot for plan now, or refuses at once:
// ErrOperationInProgress (or OperationInProgressError, another lmm process)
// while a mutation runs, ErrGameRunning for a plan made under a running
// game, and ErrStalePlan when the installed set has moved since the check
// the plan was made from. Each of those is "not this time" - the next
// check plans again - and none of them changed anything.
//
// The returned run must be Applied or Abandoned; either releases the slot.
func (s *Service) BeginAutoUpdate(ctx context.Context, game *domain.Game, plan *AutoUpdatePlan, opts UpdateBatchOptions) (*AutoUpdateRun, error) {
	if plan == nil {
		return nil, errors.New("no automatic update planned")
	}
	if plan.Running != nil {
		return nil, ErrGameRunning
	}
	release, err := s.tryBeginOp(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkPlanFresh(ctx, plan.batch.GameID, plan.batch.Profile, plan.batch.snapshot); err != nil {
		release()
		return nil, fmt.Errorf("automatic update: %w", err)
	}
	// The snapshot is versions and enabled state only. A mod moved off the
	// auto policy since the check is the user's decision, so it is dropped
	// here, under the slot, where no further change can slip in.
	batch, err := s.stillOnAuto(ctx, plan.batch)
	if err != nil {
		release()
		return nil, err
	}
	return &AutoUpdateRun{s: s, game: game, plan: batch, opts: opts, release: release}, nil
}

// stillOnAuto returns batch narrowed to the mods whose installed row is
// still on the auto policy, or ErrStalePlan when none is.
func (s *Service) stillOnAuto(ctx context.Context, batch *UpdateBatchPlan) (*UpdateBatchPlan, error) {
	rows, err := s.GetInstalledMods(ctx, batch.GameID, batch.Profile)
	if err != nil {
		return nil, fmt.Errorf("automatic update: %w", err)
	}
	auto := make(map[string]bool, len(rows))
	for _, r := range rows {
		auto[domain.ModKey(r.SourceID, r.ID)] = r.UpdatePolicy == domain.UpdateAuto
	}
	narrowed := *batch
	narrowed.Updates = nil
	for _, u := range batch.Updates {
		if auto[domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)] {
			narrowed.Updates = append(narrowed.Updates, u)
		}
	}
	if len(narrowed.Updates) == 0 {
		return nil, fmt.Errorf("automatic update: %w: no mod in it is on the auto policy any more", ErrStalePlan)
	}
	return &narrowed, nil
}

// Apply runs the begun pass - ApplyUpdateBatch's body, under the slot
// BeginAutoUpdate took - and releases the slot. ctx and sink are
// ApplyUpdateBatch's.
func (r *AutoUpdateRun) Apply(ctx context.Context, sink EventSink) (*UpdateBatchResult, error) {
	defer r.release()
	return r.s.applyUpdateBatch(ctx, r.game, r.plan, r.opts, sink)
}

// Abandon releases the slot of a run that will not be applied. Safe to
// call more than once, and after Apply.
func (r *AutoUpdateRun) Abandon() { r.release() }
