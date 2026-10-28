package core_test

// #471: in the disable loop of `lmm profile apply` and `lmm profile switch`,
// an undeploy that failed still counted as a disable: the row was written
// enabled = 0 and deployed = 0 over files still in the game directory, and
// the summary said "Disabled". A failed undeploy now leaves the row as it
// was, is listed with the result's failures, and makes the flow's error the
// incomplete one - as #470 did for a failed enable.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readOnlyGameDir makes f's game directory unwritable until the test ends,
// so nothing can be removed from it.
func readOnlyGameDir(t *testing.T, f *backfillFixture) {
	t.Helper()
	require.NoError(t, os.Chmod(f.gameDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(f.gameDir, 0o755) })
}

func TestProfileApply_AFailedUndeployIsNotADisable(t *testing.T) {
	skipAsRoot(t)
	ctx := context.Background()
	f := liveDirFixture(t)
	require.NoError(t, f.svc.NewProfileManager().SetModDisabled(ctx, f.game.ID, "a", "src", "aonly", true))
	plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
	require.NoError(t, err)
	require.Len(t, plan.ToDisable, 1)
	readOnlyGameDir(t, f)

	result, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)

	var incomplete *core.ProfileApplyIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Zero(t, result.Disabled, "nothing was disabled")
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "aonly", result.Failed[0].ModID)
	assert.Contains(t, result.Failed[0].Reason, "undeploy failed")
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, core.ProfileApplyFailed, result.Outcomes[0].Outcome)
	assert.Contains(t, err.Error(), "src:aonly")

	assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	row := f.installedRow(t, "a", "aonly")
	assert.True(t, row.Enabled, "the row says what the directory holds")
	assert.True(t, row.Deployed)
	assert.Equal(t, []string{"aonly"}, f.disabledRefs(t, "a"), "the document still asks for it off")

	// Once the directory is writable again, the same apply finishes it.
	require.NoError(t, os.Chmod(f.gameDir, 0o755))
	plan, err = f.svc.PlanProfileApply(ctx, f.game, "a")
	require.NoError(t, err)
	require.Len(t, plan.ToDisable, 1, "the failed disable is planned again")
	result, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Disabled)
	assert.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
}

func TestProfileSwitch_AFailedUndeployIsNotADisable(t *testing.T) {
	skipAsRoot(t)
	ctx := context.Background()
	f := liveDirFixture(t)
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
	require.NoError(t, err)
	readOnlyGameDir(t, f)

	result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)

	var incomplete *core.ProfileSwitchIncompleteError
	require.ErrorAs(t, err, &incomplete)
	var failed []string
	for _, ref := range result.Failed {
		failed = append(failed, ref.ModID)
	}
	assert.Contains(t, failed, "aonly")
	assert.Zero(t, result.Disabled, "nothing was disabled")
	assert.Contains(t, err.Error(), "src:aonly")
	assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"))
	row := f.installedRow(t, "a", "aonly")
	assert.True(t, row.Deployed, "a's row still records the files a purge of a has to clear")
}
