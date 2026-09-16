package core_test

// A compile game whose adapter is refused can still be purged and
// uninstalled (removalSnapshotOf), and the merged artifact is part of what
// those remove. The plan must say so exactly when the apply does it (#413
// fix round 4, F7): the purge plan used to need the compiler to NAME the
// artifact, while the purge itself removes it by its record.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compileRefusals are the two ways the fixture's compile game is refused:
// an adapter that does not exist, and one that exists but cannot compile.
var compileRefusals = []string{"no-such-adapter", "generic-files"}

// newRefusedCompileGame is a compile game with its merged artifact built
// and deployed from the given exmodz mods, then refused by adapterName.
func newRefusedCompileGame(t *testing.T, adapterName string, mods ...string) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game, _ := newMergedPakTestGame(t)
	for _, id := range mods {
		seedEnabledExmodzMod(t, svc, game, "fake-compiler", id, "1.0", id+"-file", []byte(id))
	}
	_, err := svc.SyncMergedPak(ctx, game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)

	refused := *game
	refused.Adapter = adapterName
	require.NoError(t, svc.SaveGame(ctx, &refused))
	stored, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	_, err = svc.AdapterFor(stored)
	require.Error(t, err, "fixture: the adapter is refused")
	return svc, stored
}

// artifactOnDisk reports whether game's merged artifact is deployed.
func artifactOnDisk(game *domain.Game) bool {
	_, err := os.Lstat(filepath.Join(game.ModPath, mergedArtifactName))
	return err == nil
}

// TestPurgePlan_MergedArtifact_ARefusedAdapterPlansTheRemovalItMakes: the
// purge removes the recorded artifact, so the plan names that removal.
func TestPurgePlan_MergedArtifact_ARefusedAdapterPlansTheRemovalItMakes(t *testing.T) {
	ctx := context.Background()
	for _, name := range compileRefusals {
		t.Run(name, func(t *testing.T) {
			svc, game := newRefusedCompileGame(t, name, "bear-mount")
			plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			require.NotNil(t, plan.MergedArtifact, "the dry run must name the removal the purge makes")
			assert.Equal(t, core.MergedArtifactRemove, plan.MergedArtifact.Action)
			assert.Equal(t, mergedArtifactName, plan.MergedArtifact.Path)

			_, err = svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			assert.False(t, artifactOnDisk(game))
		})
	}
}

// TestUninstallPlan_MergedArtifact_ARefusedAdapterPlansWhatTheSyncDoes:
// uninstalling the last merge source takes the artifact out with no
// compiler involved, so the plan says remove; with a source left the sync
// needs the compiler, fails, and leaves the artifact - so the plan says
// nothing.
func TestUninstallPlan_MergedArtifact_ARefusedAdapterPlansWhatTheSyncDoes(t *testing.T) {
	ctx := context.Background()
	for _, name := range compileRefusals {
		t.Run(name+"/the last source", func(t *testing.T) {
			svc, game := newRefusedCompileGame(t, name, "bear-mount")
			plan, err := svc.PlanUninstall(ctx, game, "default", "fake-compiler", "bear-mount", core.UninstallOptions{})
			require.NoError(t, err)
			require.NotNil(t, plan.MergedArtifact, "the dry run must name the removal the uninstall makes")
			assert.Equal(t, core.MergedArtifactRemove, plan.MergedArtifact.Action)
			assert.Equal(t, mergedArtifactName, plan.MergedArtifact.Path)

			_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
			require.NoError(t, err)
			assert.False(t, artifactOnDisk(game))
		})

		t.Run(name+"/a source left", func(t *testing.T) {
			svc, game := newRefusedCompileGame(t, name, "bear-mount", "wolf-mount")
			plan, err := svc.PlanUninstall(ctx, game, "default", "fake-compiler", "bear-mount", core.UninstallOptions{})
			require.NoError(t, err)
			assert.Nil(t, plan.MergedArtifact)

			res, err := svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
			require.NoError(t, err)
			assert.True(t, artifactOnDisk(game), "the sync could not run, so the artifact stays")
			assert.NotEmpty(t, res.Warnings, "and the uninstall says the sync failed")
		})
	}
}
