package main

// #458: a Steam Workshop item lmm downloaded itself (Tier 3: not External,
// but its Version is the content id) still printed the id on the surfaces
// that keyed on `external` alone. Core now stamps the display fact -
// domain.Mod.DisplayVersion - on the documents it returns, and every
// surface below reads it: the listing document (which the web UI renders),
// the plan ref lines, the switch preview, verify's missing row and the
// locked-ref refusal core words itself.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkshopTier3_EverySurfaceShowsTheRevisionDate(t *testing.T) {
	ctx := context.Background()
	svc, game, src := setupDoUpdateWorkshopTest(t)
	mod := seedWorkshopInstalledMod(t, svc, game)
	// The source's own document for the item, at the installed content id,
	// so mod show has detail to render and no update is on offer.
	src.AddMod(&domain.Mod{ID: mod.ID, SourceID: mod.SourceID, GameID: game.ID, Name: mod.Name,
		Version: workshopContentID, UpdatedAt: workshopUpdatedAt}, nil)
	ref := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: workshopContentID}
	require.NoError(t, getProfileManager(svc).UpsertMod(ctx, game.ID, "default", ref))

	t.Run("the installed row and the listing document", func(t *testing.T) {
		assert.Equal(t, workshopRevision, mod.DisplayVersion)
		list, err := svc.ListMods(ctx, game, "default")
		require.NoError(t, err)
		require.Len(t, list.Mods, 1)
		data, err := json.Marshal(list)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"display_version":"`+workshopRevision+`"`, "the web UI reads this")
	})

	t.Run("mod show's document", func(t *testing.T) {
		detail, err := svc.ModDetail(ctx, game, "default", mod.SourceID, mod.ID)
		require.NoError(t, err)
		assert.Equal(t, workshopRevision, detail.Mod.DisplayVersion)
		require.NotNil(t, detail.Installed)
		assert.Equal(t, workshopRevision, detail.Installed.DisplayVersion)

		setFlag(t, &modSource, mod.SourceID)
		setFlag(t, &modProfile, "")
		setFlag(t, &jsonOutput, false)
		stdout := captureStdout(t, func() error {
			return doModShow(ctx, svc, game, mod.ID)
		})
		assert.Contains(t, stdout, "Version: revision of "+workshopRevision)
		assert.Contains(t, stdout, "Installed: revision of "+workshopRevision+" (profile: default)")
		assert.NotContains(t, stdout, "v"+workshopContentID)
	})

	t.Run("a profile import's plan lines", func(t *testing.T) {
		data := buildImportProfileData(t, game.ID, "default", []domain.ModReference{ref})
		plan, err := svc.PlanImport(ctx, game, data)
		require.NoError(t, err)
		require.Len(t, plan.Installed, 1)
		line := planRefLine(plan.Installed[0])
		assert.Equal(t, "steamworkshop:3000000001 ("+workshopRevision+")", line)
	})

	t.Run("the switch preview", func(t *testing.T) {
		pm := getProfileManager(svc)
		_, err := pm.Create(ctx, game.ID, "alt")
		require.NoError(t, err)
		require.NoError(t, pm.AddMod(ctx, game.ID, "alt", domain.ModReference{SourceID: mod.SourceID, ModID: "3000000002", Version: newWorkshopContentID}))
		setFlag(t, &profileSwitchDryRun, true)
		stdout, _, err := captureStdoutAndStderr(t, func() error {
			return doProfileSwitch(ctx, svc, game, "alt")
		})
		require.NoError(t, err)
		assert.Contains(t, stdout, "  ↓ steamworkshop:3000000002\n", "no date is known for an item not installed anywhere")
		assert.NotContains(t, stdout, newWorkshopContentID)
	})

	t.Run("the locked-ref refusal", func(t *testing.T) {
		err := core.LockedRefUnlockOnlyRefusalError(mod.Mod, "default", &domain.ModReference{Version: workshopContentID, Locked: true})
		assert.NotContains(t, err.Error(), workshopContentID)
		assert.Contains(t, err.Error(), "Workshop Item is locked in profile default")
	})

	t.Run("verify's missing row", func(t *testing.T) {
		require.NoError(t, svc.GetGameCache(game).Delete(game.ID, mod.SourceID, mod.ID, workshopContentID))
		report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		var saw bool
		for _, f := range report.Result.Findings {
			if f.Status != "missing" {
				continue
			}
			saw = true
			assert.Equal(t, workshopRevision, f.DisplayVersion)
			stdout := captureStdout(t, func() error {
				renderVerifyFinding(core.VerifyEvent{Finding: f, Version: f.Version})
				return nil
			})
			assert.Contains(t, stdout, "MISSING (revision of "+workshopRevision+" not in cache)")
			assert.NotContains(t, stdout, workshopContentID)
		}
		assert.True(t, saw, "findings were %+v", report.Result.Findings)
	})
}
