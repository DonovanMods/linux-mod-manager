package core_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C5 profiles whose rows live in file-set variants of listed trees plan
// NoChanges, deploy, verify clean, and survive a same-version update and
// rollback with their list intact.
func TestFileSwap_ListedFileSetVariantsAreStable(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	sum := md5.Sum([]byte("b"))
	listed := domain.FileLists{"21": {{Path: "mod1-b.esp", MD5: hex.EncodeToString(sum[:])}}}
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "third")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "third", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}, FileList: listed}))
	rrSwitchTo(t, svc, game, "third")
	_, err = pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}, FileList: listed}))
	rrSwitchTo(t, svc, game, "other")

	for _, p := range []string{"third", "other", "default", "other"} {
		rrSwitchTo(t, svc, game, p)
		plan, err := svc.PlanProfileApply(ctx, game, p)
		require.NoError(t, err)
		assert.True(t, plan.NoChanges, "%s: apply NoChanges", p)
		_, err = svc.DeployProfile(ctx, game, p, core.DeployOptions{}, nil)
		require.NoError(t, err)
		rep, err := svc.VerifyReport(ctx, game, p, core.VerifyOptions{Tier: core.VerifyLocal, Force: true}, nil)
		require.NoError(t, err)
		assert.Zero(t, rep.Result.Issues, "%s verify: %+v", p, rep.Result.Findings)
	}
	n := 0
	_ = n
	// other: same-version update 20 -> 22, keeping 21's list, then roll back.
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	t.Logf("other before update: variant=%q files=%v", row.Variant, row.FileIDs)
	up, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "other", domain.Update{InstalledMod: *row, NewVersion: "1.0", FileIDReplacements: map[string]string{"20": "22"}})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, up, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	row2, _ := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	t.Logf("other after update: variant=%q files=%v prev=%q/%v", row2.Variant, row2.FileIDs, row2.PreviousVariant, row2.PreviousFileIDs)
	plan, err := svc.PlanProfileApply(ctx, game, "other")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "after update")
	rp, err := svc.PlanRollback(ctx, game, "other", "src", "mod1")
	require.NoError(t, err)
	assert.False(t, rp.CacheMissing)
	_, err = svc.ApplyRollback(ctx, game, rp, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	row3, _ := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	t.Logf("other after rollback: variant=%q files=%v", row3.Variant, row3.FileIDs)
	assert.Equal(t, row.Variant, row3.Variant)
	plan, err = svc.PlanProfileApply(ctx, game, "other")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "after rollback")
	ref, err := pm.Get(ctx, game.ID, "other")
	require.NoError(t, err)
	for _, m := range ref.Mods {
		if m.ModID == "mod1" {
			t.Logf("other ref: files=%v list=%v", m.FileIDs, m.FileList)
			assert.NotEmpty(t, m.FileList, "the list survives")
		}
	}
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.False(t, deployedFile(t, game, "mod1-c.esp"))
}
