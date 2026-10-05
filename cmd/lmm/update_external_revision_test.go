package main

// #538: `lmm update` on a game whose Steam Workshop items Steam has already
// updated. The run records Steam's installed revision before it checks, so
// nothing Steam already applied is offered as an update; an item Steam no
// longer lists is named on its own line (and in --json's external_missing),
// never as an update.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	steamOldRevision = "1111111111111111111"
	steamNewRevision = "2222222222222222222"
)

// scanningWorkshopSource is fakeWorkshopUpdateSource with a Steam manifest
// it actually reports: the items a Steam library declares, and their
// installed revisions.
type scanningWorkshopSource struct {
	*fakeUpdateSource
	scan source.WorkshopScan
}

func (s *scanningWorkshopSource) ScanWorkshopItems(context.Context, string) (source.WorkshopScan, error) {
	return s.scan, nil
}

// setupExternalRevisionTest tracks two Workshop items at steamOldRevision:
// "kept", which Steam has since updated to steamNewRevision (the revision
// the API now offers), and "gone", which Steam no longer lists.
func setupExternalRevisionTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, src := setupDoUpdateWorkshopTest(t)
	ctx := context.Background()
	_, err := svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)

	root := t.TempDir()
	scan := source.WorkshopScan{Roots: []string{root}}
	for _, id := range []string{"3000000001", "3000000002"} {
		dir := filepath.Join(root, "steamapps", "workshop", "content", "g1", id)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mod.pak"), []byte("steam owns this"), 0o644))
		name := map[string]string{"3000000001": "Kept Item", "3000000002": "Gone Item"}[id]
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod: domain.Mod{
				ID: id, SourceID: "steamworkshop", Name: name, Version: steamOldRevision, GameID: game.ID,
				UpdatedAt: workshopUpdatedAt,
			},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
			Enabled: true, Deployed: true, External: true, ExternalPath: dir,
		}))
		require.NoError(t, svc.NewProfileManager().UpsertMod(ctx, game.ID, "default",
			domain.ModReference{SourceID: "steamworkshop", ModID: id, Version: steamOldRevision}))
		src.AddMod(&domain.Mod{ID: id, SourceID: "steamworkshop", Name: name, Version: steamNewRevision, GameID: game.ID}, nil)
		if id == "3000000001" {
			scan.Items = append(scan.Items, domain.WorkshopItem{
				FileID: id, Path: dir, Manifest: steamNewRevision, TimeUpdated: workshopUpdatedAt.Unix() + 86400,
			})
		}
	}
	svc.RegisterSource(&scanningWorkshopSource{fakeUpdateSource: src, scan: scan})
	return svc, game
}

func TestDoUpdate_ExternalRevisionSteamInstalledIsNotAnUpdate(t *testing.T) {
	svc, game := setupExternalRevisionTest(t)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "All mods are up to date.")
	assert.Contains(t, out, "1 Steam Workshop item(s) tracked by lmm are no longer installed by Steam: Gone Item")
	assert.NotContains(t, out, "update(s) available")

	row, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, steamNewRevision, row.Version, "the run records the revision Steam installed")
	gone, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000002", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, steamOldRevision, gone.Version, "an item Steam no longer lists is left as it was")
}

func TestJSONGolden_UpdateBulkExternal(t *testing.T) {
	withJSONOutput(t)
	svc, game := setupExternalRevisionTest(t)

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_external", out)
}
