package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoProfileApply_NamesTheVersionAndTheCacheItBorrows is the #445 final
// gate's F-D at the CLI: a mod the apply deploys from another profile's
// cache is shown with the version it picked and whose cache that is, in
// the dry run and before the prompt; a mod of the profile's own keeps its
// line.
func TestDoProfileApply_NamesTheVersionAndTheCacheItBorrows(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t) // "default" is active
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "survival")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "local", "s", "2.0", "s.esp", []byte("s")))
	seedSyncInstalledMod(t, svc, game, "local", "s", "Mod s", "2.0", "survival", false, nil)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "own", "1.0", "own.esp", []byte("own")))
	seedSyncInstalledMod(t, svc, game, "src", "own", "Own Mod", "1.0", "default", false, nil)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "local", ModID: "s", Version: "2.0"}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "own", Version: "1.0"}))

	for _, dryRun := range []bool{true, false} {
		setFlag(t, &profileApplyDryRun, dryRun)
		setFlag(t, &profileApplyYes, true)
		setFlag(t, &jsonOutput, false)
		out := captureStdout(t, func() error {
			return doProfileApply(ctx, svc, game, nil)
		})
		assert.Contains(t, out, "Will enable 2 mod(s):\n")
		assert.Contains(t, out, "  + Mod s (s) v2.0, from profile survival's cache\n", "dry run %v", dryRun)
		assert.Contains(t, out, "  + Own Mod (own)\n", "dry run %v", dryRun)
	}
}
