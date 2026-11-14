package core_test

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// default: {20} -> same-version update to {21} (variant). The plain entry
// (default's rollback target, {20}) is cleared; "other" then installs {22}
// at 1.0. Rolling default back must not deploy 22's tree as 20's.
func TestFileSwap_RollbackTargetIsNeverAnotherSetsTree(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", domain.Update{
		InstalledMod: *row, NewVersion: "1.0", FileIDReplacements: map[string]string{"20": "21"},
	})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	require.NoError(t, os.RemoveAll(svc.GetGameCache(game).ModPath(plain)))

	_, err = pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"22"}}))
	rrSwitchTo(t, svc, game, "other")
	orow, _ := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	t.Logf("other variant=%q", orow.Variant)
	rrSwitchTo(t, svc, game, "default")

	assert.NotEmpty(t, orow.Variant, "22 does not refill the entry default rolls back to")

	rp, err := svc.PlanRollback(ctx, game, "default", "src", "mod1")
	require.NoError(t, err)
	assert.True(t, rp.CacheMissing, "the rollback target is gone")
	_, err = svc.ApplyRollback(ctx, game, rp, core.RollbackOptions{}, nil)
	require.Error(t, err, "and the rollback refuses rather than deploy another tree")
	t.Logf("deployed a=%v b=%v c=%v", deployedFile(t, game, "mod1-a.esp"), deployedFile(t, game, "mod1-b.esp"), deployedFile(t, game, "mod1-c.esp"))
	assert.False(t, deployedFile(t, game, "mod1-c.esp"), "22's file must never deploy as default's 20")
}
