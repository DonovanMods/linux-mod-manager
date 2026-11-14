package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #639 re-review: a same-version update commits the new file to its own
// entry, so a later deploy never brings the superseded file back.
func TestFileSwap_SameVersionUpdateStaysRetiredAfterDeploy(t *testing.T) {
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
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "after update")
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "after deploy: the superseded file must stay retired")
}
