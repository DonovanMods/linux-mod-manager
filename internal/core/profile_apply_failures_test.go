package core_test

// #470: `lmm profile apply` said "Applied" after a mod inside it failed - an
// install the source could not resolve, or an enable whose deploy failed,
// which was only a --verbose note and counted nowhere. ApplyProfileApply now
// runs to the end as before, records every failure on the result, lists
// what it did with each mod (Outcomes), and returns a
// ProfileApplyIncompleteError carrying that result whenever a mod failed.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileApply_AFailedModMakesTheApplyIncomplete(t *testing.T) {
	ctx := context.Background()
	svc, game := newApplyTestService(t)
	pm := svc.NewProfileManager()
	svc.RegisterSource(newTwoVersionSource(t))

	seedInstalledMod(t, svc, game, "src", "on", "1.0", false, map[string][]byte{"on.esp": []byte("on")})
	seedInstalledMod(t, svc, game, "src", "gone", "1.0", false, map[string][]byte{"gone.esp": []byte("gone")})
	seedInstalledMod(t, svc, game, "src", "unlisted", "1.0", true, map[string][]byte{"unlisted.esp": []byte("u")})
	for _, ref := range []domain.ModReference{
		{SourceID: "src", ModID: "on", Version: "1.0"},
		{SourceID: "src", ModID: "gone", Version: "1.0"},
		{SourceID: "src", ModID: "ghost"},
		{SourceID: "src", ModID: "mod1", Version: "1.0"},
	} {
		require.NoError(t, pm.AddMod(ctx, game.ID, "default", ref))
	}
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToEnable, 2)
	// gone's deploy fails at the enable: its cache entry is pruned after
	// the plan.
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, "src", "gone", "1.0"))

	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)

	var incomplete *core.ProfileApplyIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, "default", incomplete.Profile)
	assert.Same(t, result, incomplete.Result, "the error carries the whole result")
	assert.Equal(t, incomplete.Result, incomplete.Details())

	assert.Equal(t, 1, result.Disabled)
	assert.Equal(t, 1, result.Enabled)
	assert.Equal(t, 1, result.Installed)
	require.Len(t, result.Failed, 2)
	assert.Equal(t, "gone", result.Failed[0].ModID)
	assert.Contains(t, result.Failed[0].Reason, "deploy failed: mod not in cache")
	assert.Equal(t, "ghost", result.Failed[1].ModID)
	assert.Contains(t, result.Failed[1].Reason, "failed to fetch mod")

	for _, want := range []string{`profile "default"`, "2 mod(s)", "src:gone", "deploy failed: mod not in cache", "src:ghost", "failed to fetch mod"} {
		assert.Contains(t, err.Error(), want)
	}

	outcomes := make([]string, 0, len(result.Outcomes))
	for _, o := range result.Outcomes {
		outcomes = append(outcomes, o.ModID+"="+string(o.Outcome))
	}
	assert.Equal(t, []string{"unlisted=disabled", "on=enabled", "gone=failed", "ghost=failed", "mod1=installed"}, outcomes,
		"every mod the apply acted on, in the order it did")
	assert.Equal(t, "1.0", result.Outcomes[1].Version)
	assert.Contains(t, result.Outcomes[2].Reason, "deploy failed")

	// What did not fail is still applied.
	for _, name := range []string{"on.esp", "mod1-old.esp"} {
		_, err := os.Lstat(filepath.Join(game.ModPath, name))
		assert.NoError(t, err, "%s is deployed", name)
	}
}

func TestProfileApply_AnApplyWithNoFailureIsNotAnError(t *testing.T) {
	ctx := context.Background()
	svc, game := newApplyTestService(t)
	seedInstalledMod(t, svc, game, "src", "on", "1.0", false, map[string][]byte{"on.esp": []byte("on")})
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "on", Version: "1.0"}))
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)

	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)

	require.NoError(t, err)
	assert.Empty(t, result.Failed)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, core.ProfileApplyEnabled, result.Outcomes[0].Outcome)
	assert.False(t, errors.As(err, new(*core.ProfileApplyIncompleteError)))
}
