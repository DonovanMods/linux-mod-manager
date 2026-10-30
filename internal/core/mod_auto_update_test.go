package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoBatchFixture is batchTestGame with mod1 set to the auto policy and
// mod2 left on notify, plus the installed set the check ran against - the
// state #587's auto pass starts from.
func autoBatchFixture(t *testing.T, svc *core.Service) (*domain.Game, []domain.InstalledMod, []domain.Update) {
	t.Helper()
	game, _ := batchTestGame(t, svc)
	ctx := context.Background()
	_, err := svc.SetModUpdatePolicy(ctx, "src", "mod1", game.ID, "default", domain.UpdateAuto)
	require.NoError(t, err)
	installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	var updates []domain.Update
	for _, im := range installed {
		updates = append(updates, domain.Update{InstalledMod: im, NewVersion: "2.0"})
	}
	return game, installed, updates
}

// TestAutoUpdateKeys_TakesOnlyTheAutoRowsTheBatchWouldAttempt pins #587's
// selection: an auto-policy row, and only one the batch would actually try
// - a locked (#97), external (#269) or manual-only (#543) row would only be
// declined, so it is never worth starting a job for.
func TestAutoUpdateKeys_TakesOnlyTheAutoRowsTheBatchWouldAttempt(t *testing.T) {
	row := func(id string, policy domain.UpdatePolicy) domain.InstalledMod {
		return domain.InstalledMod{Mod: domain.Mod{ID: id, SourceID: "src"}, UpdatePolicy: policy}
	}
	external := row("external", domain.UpdateAuto)
	external.External = true
	manual := row("manual", domain.UpdateAuto)
	manual.ManualOnly = true

	keys := core.AutoUpdateKeys([]domain.Update{
		{InstalledMod: row("auto", domain.UpdateAuto), NewVersion: "2"},
		{InstalledMod: row("notify", domain.UpdateNotify), NewVersion: "2"},
		{InstalledMod: row("locked", domain.UpdateAuto), NewVersion: "2", Locked: true},
		{InstalledMod: external, NewVersion: "2"},
		{InstalledMod: manual, NewVersion: "2"},
		{InstalledMod: row("auto2", domain.UpdateAuto), NewVersion: "3"},
	})
	assert.Equal(t, []string{"src:auto", "src:auto2"}, keys)
}

func TestPlanAutoUpdate_NoAutoRowPlansNothing(t *testing.T) {
	svc := newFlowsTestService(t)
	game, updates := batchTestGame(t, svc)
	installed, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)

	plan, err := svc.PlanAutoUpdate(context.Background(), game, "default", installed, updates)
	require.NoError(t, err)
	assert.Nil(t, plan, "notify rows are the user's to apply")
}

// TestPlanAutoUpdate_InactiveProfileIsLeftAlone: an update writes into the
// game directory, which holds the active profile's mods - so auto-updating
// a profile that is not deployed there would deploy it (#587 decision 3).
// The pass plans nothing for it, and says nothing: it is not an error to
// look at another profile.
func TestPlanAutoUpdate_InactiveProfileIsLeftAlone(t *testing.T) {
	svc := newFlowsTestService(t)
	game, _, updates := autoBatchFixture(t, svc)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "other")
	require.NoError(t, err)

	_, err = svc.PlanUpdateBatchFrom(context.Background(), game, "other", updates, nil)
	require.ErrorIs(t, err, core.ErrProfileNotActive, "the premise: other is not the deployed profile")

	plan, err := svc.PlanAutoUpdate(context.Background(), game, "other", nil, updates)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

// TestAutoUpdate_AppliesExactlyTheAutoRows is the pass end to end: the
// auto row moves, the notify row - the control - stays where it was.
func TestAutoUpdate_AppliesExactlyTheAutoRows(t *testing.T) {
	svc := newFlowsTestService(t)
	game, installed, updates := autoBatchFixture(t, svc)
	ctx := context.Background()

	plan, err := svc.PlanAutoUpdate(ctx, game, "default", installed, updates)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, []string{"src:mod1"}, plan.Mods)
	assert.Nil(t, plan.Running)

	run, err := svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.NoError(t, err)
	result, err := run.Apply(ctx, nil)
	require.NoError(t, err)
	require.Len(t, result.Applied, 1)
	assert.Equal(t, "Mod One", result.Applied[0].Name)

	one, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", one.Version)
	two, err := svc.GetInstalledMod(ctx, "src", "mod2", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", two.Version, "a notify row is never applied automatically")
}

// TestAutoUpdate_LockedAutoRowIsNeverAttempted: a lock outranks the policy.
func TestAutoUpdate_LockedAutoRowIsNeverAttempted(t *testing.T) {
	svc := newFlowsTestService(t)
	game, installed, updates := autoBatchFixture(t, svc)
	updates[0].Locked = true

	plan, err := svc.PlanAutoUpdate(context.Background(), game, "default", installed, updates)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

// TestBeginAutoUpdate_StepsAsideWhileAMutationRuns: the auto pass never
// queues behind a running mutation (#587 decision 1) - it is refused at
// once, having changed nothing, and the next check tries again. The second
// Begin here is also the "two checks at once" case: one applies, the other
// steps aside rather than applying the same rows twice.
func TestBeginAutoUpdate_StepsAsideWhileAMutationRuns(t *testing.T) {
	svc := newFlowsTestService(t)
	game, installed, updates := autoBatchFixture(t, svc)
	ctx := context.Background()

	plan, err := svc.PlanAutoUpdate(ctx, game, "default", installed, updates)
	require.NoError(t, err)
	first, err := svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.NoError(t, err)

	_, err = svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.ErrorIs(t, err, core.ErrOperationInProgress)

	first.Abandon()
	first.Abandon() // idempotent
	again, err := svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.NoError(t, err, "an abandoned run must give the slot back")
	again.Abandon()
}

// TestBeginAutoUpdate_ACheckOlderThanTheLastChangeStepsAside: a check that
// began before another apply finished carries that apply's rows as still
// outstanding. Its plan is held to the installed set the CHECK read, so it
// is refused as stale instead of applying them a second time.
func TestBeginAutoUpdate_ACheckOlderThanTheLastChangeStepsAside(t *testing.T) {
	svc := newFlowsTestService(t)
	game, installed, updates := autoBatchFixture(t, svc)
	ctx := context.Background()

	plan, err := svc.PlanAutoUpdate(ctx, game, "default", installed, updates)
	require.NoError(t, err)
	run, err := svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.NoError(t, err)
	_, err = run.Apply(ctx, nil)
	require.NoError(t, err)

	stale, err := svc.PlanAutoUpdate(ctx, game, "default", installed, updates)
	require.NoError(t, err)
	require.NotNil(t, stale)
	_, err = svc.BeginAutoUpdate(ctx, game, stale, core.UpdateBatchOptions{})
	require.ErrorIs(t, err, core.ErrStalePlan)

	// And the slot is free again afterwards.
	_, err = svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// TestPlanAutoUpdate_GameRunningPostpones is #587 decision 2: while the
// game is running the pass is postponed - nothing to begin - and the plan
// says what was seen, for the "waiting for <game> to close" line.
func TestPlanAutoUpdate_GameRunningPostpones(t *testing.T) {
	svc := newFlowsTestService(t)
	game, installed, updates := autoBatchFixture(t, svc)
	svc.SetGameProbe(&core.GameProbe{
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("lmm-launch-g1-0a1b2c3d.service loaded active running lmm: Game\n"), nil
		},
	})

	plan, err := svc.PlanAutoUpdate(context.Background(), game, "default", installed, updates)
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.NotNil(t, plan.Running)
	assert.Equal(t, core.GameRunningLaunchUnit, plan.Running.Signal)

	_, err = svc.BeginAutoUpdate(context.Background(), game, plan, core.UpdateBatchOptions{})
	require.ErrorIs(t, err, core.ErrGameRunning)

	one, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", one.Version, "nothing moves while the game runs")
}

// TestGameRunning covers the probe's two signals and its fall-throughs.
func TestGameRunning(t *testing.T) {
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", InstallPath: "/games/Thing"}
	noUnits := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }

	t.Run("no probe registered is unknown, so not running", func(t *testing.T) {
		svc := newFlowsTestService(t)
		assert.False(t, svc.GameRunning(ctx, game).Running)
	})

	t.Run("an active launch unit of this game", func(t *testing.T) {
		svc := newFlowsTestService(t)
		var args []string
		svc.SetGameProbe(&core.GameProbe{Run: func(_ context.Context, name string, a ...string) ([]byte, error) {
			args = append([]string{name}, a...)
			return []byte("lmm-launch-g1-deadbeef.service loaded active running lmm: Game\n"), nil
		}})
		got := svc.GameRunning(ctx, game)
		assert.True(t, got.Running)
		assert.Equal(t, core.GameRunningLaunchUnit, got.Signal)
		assert.Equal(t, "lmm-launch-g1-deadbeef.service", got.Detail)
		assert.Equal(t, "systemctl", args[0])
		assert.Contains(t, args, "--user")
		assert.Contains(t, args, "lmm-launch-g1-*")
	})

	t.Run("another game's unit sharing the prefix does not count", func(t *testing.T) {
		svc := newFlowsTestService(t)
		svc.SetGameProbe(&core.GameProbe{Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("lmm-launch-g1-extra-deadbeef.service loaded active running lmm: Other\n"), nil
		}})
		assert.False(t, svc.GameRunning(ctx, game).Running)
	})

	t.Run("no systemd falls through to the process scan", func(t *testing.T) {
		svc := newFlowsTestService(t)
		svc.SetGameProbe(&core.GameProbe{
			Run: func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("systemctl: not found")
			},
			Processes: func() ([]core.ProcessInfo, error) {
				return []core.ProcessInfo{
					{PID: 10, Exe: "/usr/bin/bash", Cmdline: []string{"bash"}},
					{PID: 11, Exe: "/games/Thing/bin/thing", Cmdline: []string{"/games/Thing/bin/thing"}},
				}, nil
			},
		})
		got := svc.GameRunning(ctx, game)
		assert.True(t, got.Running)
		assert.Equal(t, core.GameRunningProcess, got.Signal)
		assert.Equal(t, "/games/Thing/bin/thing", got.Detail)
	})

	t.Run("a Wine or Proton game is found by the exe its cmdline names", func(t *testing.T) {
		svc := newFlowsTestService(t)
		svc.SetGameProbe(&core.GameProbe{Run: noUnits, Processes: func() ([]core.ProcessInfo, error) {
			return []core.ProcessInfo{
				{PID: 20, Exe: "/opt/proton/files/bin/wine64-preloader", Cmdline: []string{`Z:\games\Thing\Game.exe`, "-dx12"}},
			}, nil
		}})
		got := svc.GameRunning(ctx, game)
		assert.True(t, got.Running)
		assert.Equal(t, core.GameRunningProcess, got.Signal)
	})

	t.Run("a sibling directory sharing the name prefix is not under the install path", func(t *testing.T) {
		svc := newFlowsTestService(t)
		svc.SetGameProbe(&core.GameProbe{Run: noUnits, Processes: func() ([]core.ProcessInfo, error) {
			return []core.ProcessInfo{{PID: 30, Exe: "/games/ThingTwo/thing", Cmdline: []string{"/games/ThingTwo/thing"}}}, nil
		}})
		assert.False(t, svc.GameRunning(ctx, game).Running)
	})

	t.Run("a game with no install path is never matched by process", func(t *testing.T) {
		svc := newFlowsTestService(t)
		svc.SetGameProbe(&core.GameProbe{Run: noUnits, Processes: func() ([]core.ProcessInfo, error) {
			return []core.ProcessInfo{{PID: 40, Exe: "/anything", Cmdline: []string{"/anything"}}}, nil
		}})
		assert.False(t, svc.GameRunning(ctx, &domain.Game{ID: "g1", InstallPath: "/"}).Running)
		assert.False(t, svc.GameRunning(ctx, &domain.Game{ID: "g1"}).Running)
	})

	t.Run("a failing process scan is unknown, so not running", func(t *testing.T) {
		svc := newFlowsTestService(t)
		svc.SetGameProbe(&core.GameProbe{Run: noUnits, Processes: func() ([]core.ProcessInfo, error) {
			return nil, errors.New("no /proc")
		}})
		assert.False(t, svc.GameRunning(ctx, game).Running)
	})
}
