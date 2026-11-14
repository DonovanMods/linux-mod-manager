package core_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/require"
)

// TestProfileDrift_Timing300Mods measures the drift summary GameStatus
// carries (#641) on a 300-mod profile deployed by symlink, with and without
// overrides. It only measures, so it runs only when LMM_DRIFT_TIMING is
// set - to the files per mod, or 1 for the default 20:
//
//	LMM_DRIFT_TIMING=100 go test ./internal/core/ -run TestProfileDrift_Timing300Mods -v
func TestProfileDrift_Timing300Mods(t *testing.T) {
	if os.Getenv("LMM_DRIFT_TIMING") == "" {
		t.Skip("set LMM_DRIFT_TIMING to measure")
	}
	filesPerMod := 20
	if n, err := strconv.Atoi(os.Getenv("LMM_DRIFT_TIMING")); err == nil && n > 0 {
		filesPerMod = n
	}
	for _, withOverrides := range []bool{false, true} {
		t.Run(fmt.Sprintf("overrides=%v", withOverrides), func(t *testing.T) {
			f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
			refs := make([]string, 300)
			for i := range refs {
				refs[i] = fmt.Sprintf("m%03d", i)
				files := map[string]string{}
				for j := range filesPerMod {
					files[fmt.Sprintf("Data/%s/f%03d.txt", refs[i], j)] = refs[i]
				}
				f.cachedOnly(t, "default", refs[i], domain.LinkSymlink, files)
			}
			var overrides map[string]string
			if withOverrides {
				overrides = map[string]string{"Engine.ini": "tweaked\n", "mods/Data/m000/f000.txt": "overridden\n"}
				f.stock(t, "Engine.ini", "stock\n")
			}
			f.overriddenProfile(t, "default", true, overrides, refs...)
			f.deploy(t, "default", core.DeployOptions{})

			ctx := context.Background()
			drift, err := f.svc.ProfileDriftForTest(ctx, f.game)
			require.NoError(t, err)
			require.Nil(t, drift, "deployed: no drift")

			measure := func(name string, call func()) {
				var runs []time.Duration
				for range 30 {
					start := time.Now()
					call()
					runs = append(runs, time.Since(start))
				}
				slices.Sort(runs)
				t.Logf("%s: median %v, max %v (30 runs)", name, runs[len(runs)/2], runs[len(runs)-1])
			}
			measure("ProfileDrift", func() {
				_, err := f.svc.ProfileDriftForTest(ctx, f.game)
				require.NoError(t, err)
			})
			measure("GameStatus", func() {
				_, err := f.svc.GameStatus(ctx, f.game)
				require.NoError(t, err)
			})
		})
	}
}
