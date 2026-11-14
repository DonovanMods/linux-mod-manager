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

// #639 re-review ruling 3: default's entry {20} is gone; other must not
// refill that key with {21}, or default deploys 21 on its way back.
func TestFileSwap_AbsentEntryIsNotRefilledByAnotherSet(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	require.NoError(t, os.RemoveAll(svc.GetGameCache(game).ModPath(plain)))
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}}))
	rrSwitchTo(t, svc, game, "other")
	orow, _ := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	t.Logf("other variant=%q", orow.Variant)
	rrSwitchTo(t, svc, game, "default")
	drow, _ := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	t.Logf("default variant=%q files=%v", drow.Variant, drow.FileIDs)
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "default pins 20 only")
}
