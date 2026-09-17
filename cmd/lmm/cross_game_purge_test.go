package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoPurge_SaysWhichFilesItLeftForAnotherGame is #445 gate 2's G2-1 at
// the CLI: a purge of the active profile leaves a file another game sharing
// the mod directory records, and says so.
func TestDoPurge_SaysWhichFilesItLeftForAnotherGame(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	other := &domain.Game{ID: "other", Name: "Other", ModPath: game.ModPath, LinkMethod: game.LinkMethod}
	require.NoError(t, svc.SaveGame(ctx, game))
	require.NoError(t, svc.SaveGame(ctx, other))
	_, err := getProfileManager(svc).Create(ctx, other.ID, "default")
	require.NoError(t, err)
	require.NoError(t, getProfileManager(svc).SetDefault(ctx, other.ID, "default"))
	for _, g := range []*domain.Game{game, other} {
		require.NoError(t, svc.GetGameCache(g).Store(g.ID, "src", "shared", "1.0", "shared.esp", []byte(g.ID)))
		seedSyncInstalledMod(t, svc, g, "src", "shared", "shared", "1.0", "default", true, nil)
		require.NoError(t, getProfileManager(svc).AddMod(ctx, g.ID, "default", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))
		result, err := svc.DeployProfile(ctx, g, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
		require.Equal(t, 1, result.Deployed)
	}
	require.FileExists(t, filepath.Join(game.ModPath, "shared.esp"))
	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, true)

	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, game) })

	assert.Contains(t, stdout, "\nPurged: 1 mod(s)\nLeft in place (still recorded by game other): shared.esp\n")
	assert.FileExists(t, filepath.Join(game.ModPath, "shared.esp"))
}
