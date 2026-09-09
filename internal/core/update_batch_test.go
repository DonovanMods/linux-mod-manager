package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchTestGame is the two-mod fixture every test below starts from: two
// updatable mods on one profile, one source that serves a new file for
// each. The returned updates are in the order a check would report them.
func batchTestGame(t *testing.T, svc *core.Service) (*domain.Game, []domain.Update) {
	t.Helper()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	one := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	two := seedUpdatableMod(t, svc, game, "src", "mod2", "Mod Two", "1.0", []string{"old-2"},
		map[string][]byte{"mod2-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "new-1", Name: "New File", FileName: "new.esp", IsPrimary: true},
		},
	}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	return game, []domain.Update{
		{InstalledMod: *one, NewVersion: "2.0"},
		{InstalledMod: *two, NewVersion: "2.0"},
	}
}

func TestPlanUpdateBatchFrom_NilSelectionTakesEveryUpdate(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)
	assert.Equal(t, game.ID, plan.GameID)
	assert.Equal(t, "default", plan.Profile)
	require.Len(t, plan.Updates, 2)
	assert.Equal(t, "mod1", plan.Updates[0].InstalledMod.ID)
	assert.Equal(t, "mod2", plan.Updates[1].InstalledMod.ID)
	assert.Empty(t, plan.NotFound)
}

// TestPlanUpdateBatchFrom_SelectionFiltersAndNamesWhatWentMissing pins the
// two halves of the selection contract: only ticked rows are planned, and a
// ticked key with no update behind it is REPORTED rather than dropped.
func TestPlanUpdateBatchFrom_SelectionFiltersAndNamesWhatWentMissing(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates,
		[]string{"src:mod2", "src:ghost", "curseforge:7"})
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	assert.Equal(t, "mod2", plan.Updates[0].InstalledMod.ID)
	assert.Equal(t, []string{"src:ghost", "curseforge:7"}, plan.NotFound,
		"missing keys are reported in the SELECTION's own order, so a re-plan is stable")
}

func TestPlanUpdateBatchFrom_EmptySelectionPlansNothing(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, []string{})
	require.NoError(t, err)
	assert.Empty(t, plan.Updates)
	assert.Empty(t, plan.NotFound)
}

// TestApplyUpdateBatch_AppliesEveryItemInPlanOrder is the happy path: both
// mods update, both are reported as applied, in the order the plan listed
// them (ApplyUpdateBatch's documented ordering contract).
func TestApplyUpdateBatch_AppliesEveryItemInPlanOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Applied, 2)
	assert.Equal(t, "Mod One", result.Applied[0].Name)
	assert.Equal(t, "Mod Two", result.Applied[1].Name)
	assert.Empty(t, result.Failed)
	assert.Empty(t, result.Skipped)
	assert.Equal(t, game.ID, result.GameID)
	assert.Equal(t, "default", result.Profile)

	for _, id := range []string{"mod1", "mod2"} {
		got, err := svc.GetInstalledMod(context.Background(), "src", id, "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, "2.0", got.Version, "%s must have actually moved", id)
	}
}

// TestApplyUpdateBatch_LockedRefIsSkippedNotAttempted is the #97 rule at
// batch level: ApplyUpdate refuses on the lock alone, so the batch declines
// to try at all and records the engine's own refusal sentence. The rest of
// the batch is unaffected.
func TestApplyUpdateBatch_LockedRefIsSkippedNotAttempted(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), "g1", "default", "src", "mod1", ""))

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err, "a locked ref is a policy outcome, never an error for the batch as a whole")

	require.Len(t, result.Skipped, 1)
	skip := result.Skipped[0]
	assert.Equal(t, "Mod One", skip.Name)
	assert.Equal(t, core.UpdateSkipped, skip.Status)
	assert.True(t, skip.Mod.Locked)
	assert.Contains(t, skip.Reason, "is locked at v")
	assert.Contains(t, skip.Reason, "lmm mod unlock -s src -p default mod1")
	assert.NotContains(t, skip.Reason, "lmm mod lock",
		"this gate refuses on the lock alone, so moving the lock is not a remedy (#97 / unit Q I1)")

	assert.Empty(t, result.Failed, "a refusal must not be reported as a failure")
	require.Len(t, result.Applied, 1)
	assert.Equal(t, "Mod Two", result.Applied[0].Name)

	locked, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", locked.Version, "a skipped mod must be untouched")
}

// TestApplyUpdateBatch_PerItemFailureDoesNotAbortTheBatch is the default
// StopOnError=false contract both hand-written loops had.
func TestApplyUpdateBatch_PerItemFailureDoesNotAbortTheBatch(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	// mod1 names a source that was never registered, so its own re-plan
	// fails while mod2's still succeeds.
	updates[0].InstalledMod.SourceID = "gone"

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "gone:mod1", result.Failed[0].Mod)
	assert.Equal(t, "Mod One", result.Failed[0].Name)
	assert.NotEmpty(t, result.Failed[0].Error)
	assert.Error(t, result.Failed[0].Cause(), "an in-process caller keeps the typed error")

	require.Len(t, result.Applied, 1)
	assert.Equal(t, "Mod Two", result.Applied[0].Name, "a later item still gets its update")
}

// TestApplyUpdateBatch_StopOnErrorAbandonsTheRest is the opposite policy:
// the first failure ends the run, with the partial result still returned.
func TestApplyUpdateBatch_StopOnErrorAbandonsTheRest(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)
	updates[0].InstalledMod.SourceID = "gone"

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan,
		core.UpdateBatchOptions{StopOnError: true}, nil)
	require.Error(t, err)
	require.Len(t, result.Failed, 1)
	assert.Empty(t, result.Applied, "the run stopped before the second item")

	untouched, err := svc.GetInstalledMod(context.Background(), "src", "mod2", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", untouched.Version)
}

// TestApplyUpdateBatch_StalePlanRefusesTheWholeBatch is Ruling 5 applied
// ONCE, to the batch: a world that moved between the selection and the
// confirm is refused having changed nothing at all.
func TestApplyUpdateBatch_StalePlanRefusesTheWholeBatch(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	// Someone else disabled a mod between the plan and the apply.
	_, err = svc.DisableMod(context.Background(), game, "default", "src", "mod2")
	require.NoError(t, err)

	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrStalePlan)
	assert.Empty(t, result.Applied)
	assert.Empty(t, result.Failed)

	untouched, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", untouched.Version, "a stale batch must change nothing at all")
}

// TestApplyUpdateBatch_CancelledBeforeAnItemStopsAndReports is Ruling 16 at
// batch level: ctx is checked BETWEEN items, so a cancellation that lands
// after the first item leaves that item fully applied and the run reports
// context.Canceled with the partial result intact.
func TestApplyUpdateBatch_CancelledBeforeAnItemStopsAndReports(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	sink := func(e core.Event) {
		p, ok := e.(core.ModEvent)
		if ok && p.Phase == core.UpdateBatchItemApplied {
			cancel() // the first item is done; stop before the second starts
		}
	}

	result, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, sink)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.Len(t, result.Applied, 1, "the item that was in flight completed")
	assert.Equal(t, "Mod One", result.Applied[0].Name)

	first, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", first.Version, "its DB row was written")
	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	for _, ref := range profile.Mods {
		if ref.ModID == "mod1" {
			assert.Equal(t, "2.0", ref.Version, "and its PAIRED profile write completed too (Ruling 16)")
		}
	}

	second, err := svc.GetInstalledMod(context.Background(), "src", "mod2", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", second.Version, "the item after the cancellation was never started")
}

// TestApplyUpdateBatch_BracketsEveryItemWithEvents pins the per-item event
// bracket a frontend attributes progress with.
func TestApplyUpdateBatch_BracketsEveryItemWithEvents(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), "g1", "default", "src", "mod2", ""))

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	type tick struct {
		phase core.DeployPhase
		name  string
		index int
		total int
	}
	var ticks []tick
	sink := func(e core.Event) {
		me, ok := e.(core.ModEvent)
		if !ok {
			return
		}
		switch me.Phase {
		case core.UpdateBatchItem, core.UpdateBatchItemApplied, core.UpdateBatchItemSkipped, core.UpdateBatchItemFailed:
			ticks = append(ticks, tick{me.Phase, me.ModName, me.Index, me.Total})
		}
	}

	_, err = svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []tick{
		{core.UpdateBatchItem, "Mod One", 1, 2},
		{core.UpdateBatchItemApplied, "Mod One", 1, 2},
		{core.UpdateBatchItem, "Mod Two", 2, 2},
		{core.UpdateBatchItemSkipped, "Mod Two", 2, 2},
	}, ticks)
}

// TestApplyUpdateBatch_BeginOpFailure_ResultIdentifiesTheBatch pins the #324
// review's Minor 7: a beginOp failure (here, a pre-cancelled ctx, which
// beginOp refuses before ever touching the semaphore) must still return a
// result naming which game/profile the batch was for, matching every other
// exit of ApplyUpdateBatch - a stored serve job Result should never carry an
// empty game_id/profile.
func TestApplyUpdateBatch_BeginOpFailure_ResultIdentifiesTheBatch(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", updates, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, game.ID, result.GameID)
	assert.Equal(t, "default", result.Profile)
}

// TestUpdateBatchFailure_CauseIsNilOffTheWire documents the one thing the
// wire cannot carry: a decoded failure has its Error text and no typed
// cause.
func TestUpdateBatchFailure_CauseIsNilOffTheWire(t *testing.T) {
	var decoded core.UpdateBatchFailure
	decoded.Mod, decoded.Error = "src:mod1", "boom"
	assert.Equal(t, "boom", decoded.Error)
	assert.NoError(t, decoded.Cause())
}
