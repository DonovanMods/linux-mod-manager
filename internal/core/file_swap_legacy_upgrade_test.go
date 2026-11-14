package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #639 third review: a legacy (unmarked) entry survives a same-version
// update, a deploy, an apply plan, a rollback and another deploy, with one
// download.
func TestFileSwap_LegacyEntryUpdateDeployRollbackRoundTrips(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"}, map[string][]byte{"mod1-fileA.esp": []byte("A")})
	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "Old Archive", FileName: "mod1-fileA.esp", Version: "1.0", Category: "MAIN"},
			{ID: "fileB", Name: "Fixed Archive", FileName: "mod1-fileB.esp", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddDownload("fileB", []byte("B-content"))
	mock.AddDownload("fileA", []byte("A"))

	// Upgraded install, before anything: deploy and apply are no-ops.
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	pa, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, pa.NoChanges, "upgrade: apply no-op")

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.0", FileIDReplacements: map[string]string{"fileA": "fileB"}}
	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", upd)
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, deployedFile(t, game, "mod1-fileA.esp"), "deploy after update: A stays retired")
	assert.True(t, deployedFile(t, game, "mod1-fileB.esp"))
	pa, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, pa.NoChanges, "after update apply no-op")
	n := mock.DownloadCount()
	t.Logf("downloads so far: %d", n)

	rp, err := svc.PlanRollback(ctx, game, "default", "src", "mod1")
	require.NoError(t, err)
	t.Logf("rollback cacheMissing=%v", rp.CacheMissing)
	_, err = svc.ApplyRollback(ctx, game, rp, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, deployedFile(t, game, "mod1-fileA.esp"))
	assert.False(t, deployedFile(t, game, "mod1-fileB.esp"))
	pa, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, pa.NoChanges, "after rollback apply no-op")
	t.Logf("downloads total: %d", mock.DownloadCount())
}
