package core_test

// handoff_plan_bench_test.go measures what #638's hand-off preview costs a
// plan at scale: a 500-mod profile whose mods share paths, planned with 1
// and with 50 of its mods turned off, by `profile apply` and by a switch.
// It is a measurement, not a ratchet - wall-clock time depends on the
// machine - so it runs only when LMM_HANDOFF_BENCH is set:
//
//	LMM_HANDOFF_BENCH=1 go test ./internal/core/ -run TestHandOffPlanCost -v -count=1
//
// The ratchet on the work a plan does is TestPlanHandOffs_PlanWorkIsBounded.

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/require"
)

// seedHandOffScale seeds n enabled mods in load order: mod j ships three
// files of its own and shared/s<j%groups>.esp, so every shared path is
// shipped by n/groups mods spread through the order. It deploys the
// profile.
func seedHandOffScale(t *testing.T, n, groups int) (*core.Service, *domain.Game, []string) {
	t.Helper()
	ctx := context.Background()
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	ids := make([]string, n)
	refs := make([]domain.ModReference, n)
	for j := range n {
		id := fmt.Sprintf("m%03d", j)
		ids[j] = id
		seedNamedInstalledMod(t, svc, game, "src", id, "Mod "+id, "1.0", true, map[string][]byte{
			fmt.Sprintf("shared/s%02d.esp", j%groups): []byte(id),
			"own/" + id + "/a.txt":                    []byte(id),
			"own/" + id + "/b.txt":                    []byte(id),
			"own/" + id + "/c.txt":                    []byte(id),
		})
		refs[j] = domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}
	}
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	for _, ref := range refs {
		require.NoError(t, pm.AddMod(ctx, game.ID, "default", ref))
	}
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game, ids
}

// medianPlan is the median of five runs of plan, and the heir listings
// one run makes.
func medianPlan(t *testing.T, plan func()) (time.Duration, int) {
	t.Helper()
	var times []time.Duration
	var listings int
	for range 5 {
		listings = 0
		restore := core.CountHeirListingsForTest(func() { listings++ })
		start := time.Now()
		plan()
		times = append(times, time.Since(start))
		restore()
	}
	slices.Sort(times)
	return times[2], listings
}

func TestHandOffPlanCost(t *testing.T) {
	if os.Getenv("LMM_HANDOFF_BENCH") == "" {
		t.Skip("set LMM_HANDOFF_BENCH=1 to measure")
	}
	const n, groups = 500, 50
	for _, off := range []int{1, 50} {
		t.Run(fmt.Sprintf("apply_off_%d", off), func(t *testing.T) {
			ctx := context.Background()
			svc, game, ids := seedHandOffScale(t, n, groups)
			pm := svc.NewProfileManager()
			for _, id := range ids[n-off:] {
				require.NoError(t, pm.SetModDisabled(ctx, game.ID, "default", "src", id, true))
			}
			d, listings := medianPlan(t, func() {
				plan, err := svc.PlanProfileApply(ctx, game, "default")
				require.NoError(t, err)
				require.Len(t, plan.ToDisable, off)
			})
			t.Logf("apply plan, %d of %d off: %v, %d heir listings", off, n, d, listings)
		})
		t.Run(fmt.Sprintf("switch_off_%d", off), func(t *testing.T) {
			ctx := context.Background()
			svc, game, ids := seedHandOffScale(t, n, groups)
			pm := svc.NewProfileManager()
			_, err := pm.Create(ctx, game.ID, "other")
			require.NoError(t, err)
			for _, id := range ids[:n-off] {
				row, err := svc.GetInstalledMod(ctx, "src", id, game.ID, "default")
				require.NoError(t, err)
				cp := *row
				cp.ProfileName = "other"
				require.NoError(t, svc.SaveInstalledMod(ctx, &cp))
				require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
			}
			d, listings := medianPlan(t, func() {
				plan, err := svc.PlanProfileSwitch(ctx, game, "other")
				require.NoError(t, err)
				require.Len(t, plan.ToDisable, off)
			})
			t.Logf("switch plan, %d of %d off: %v, %d heir listings", off, n, d, listings)
		})
	}
}
