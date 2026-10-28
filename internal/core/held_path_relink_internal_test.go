package core

// #476, the relink half: `lmm mod edit` ends with the merged-artifact
// resync, a deploy that can leave a file for another game and queue that
// line - and ApplyRelinkMod never drained the queue, so the line surfaced
// on an unrelated later command. White-box, because the queue is the
// Service's unexported originals store: what the relink ran into is put
// there directly, the way the resync's installer would.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestApplyRelinkMod_ReportsTheHeldPathsItLeaves(t *testing.T) {
	ctx := context.Background()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	game := &domain.Game{ID: "sky", Name: "Sky", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "k", SourceID: domain.SourceLocal, Name: "K", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	plan, err := svc.PlanRelinkMod(ctx, game, "default", domain.SourceLocal, "k", "", "")
	require.NoError(t, err)
	held := "Data/a.esp was not replaced: game sky2 records it too, so lmm left that game's file there"
	svc.originalsStoreFor(game.ID).note(held)
	sink, events := RecordEvents()

	result, err := svc.ApplyRelinkMod(ctx, game, plan, RelinkOptions{Name: "K renamed"}, sink)

	require.NoError(t, err)
	assert.Equal(t, []string{held}, result.Warnings)
	var emitted int
	for _, e := range *events {
		if w, ok := e.(WarningEvent); ok && w.Message == held {
			emitted++
		}
	}
	assert.Equal(t, 1, emitted)
	assert.Empty(t, svc.originalsStoreFor(game.ID).takeFailures(), "nothing is left for a later command")
}
