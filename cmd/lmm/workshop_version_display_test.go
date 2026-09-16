package main

// #428: a Steam Workshop item's Version is its content id (hcontent_file),
// which no human-facing surface may print as a version - `lmm install`
// printed "Selected: ModMenu v493958101155293591" while `lmm list` showed a
// date. Every line naming a Workshop item now shows its revision date, for
// an item lmm downloaded itself (Tier 3) as much as for one it tracks.

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workshopContentID is the fixture API's hcontent_file, and
// workshopRevision its time_updated as a date.
const (
	workshopContentID = "7987119735124793734"
	workshopRevision  = "2025-12-03"
)

func TestDoInstall_Workshop_EveryLineShowsTheRevisionDate(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000001")

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
	assert.Contains(t, out, "Selected: Workshop Item 3000000001 (revision of "+workshopRevision+")")
	assert.Contains(t, out, "Installed: Workshop Item 3000000001 (revision of "+workshopRevision+")")

	// The item lmm downloaded is not External, and its Version is still the
	// content id - `lmm list` says the date for it too.
	installed, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", game.ID, "default")
	require.NoError(t, err)
	require.False(t, installed.External)
	require.Equal(t, workshopContentID, installed.Version, "fixture: the version identity is the content id")

	listed := listVerbose(t, svc, game, false)
	assert.NotContains(t, listed, workshopContentID)
	assert.Contains(t, lineContaining(listed, "3000000001"), workshopRevision)
}

// TestDoInstall_Workshop_TheDependencyTreeShowsTheRevisionDate covers the
// plan readout, which names the target by version too.
func TestDoInstall_Workshop_TheDependencyTreeShowsTheRevisionDate(t *testing.T) {
	svc, _ := setupWorkshopInstallTest(t, "3000000001")
	plan := &core.InstallPlan{Mod: domain.Mod{
		ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: workshopContentID,
	}}
	out := captureStdout(t, func() error { showInstallPlan(svc, plan); return nil })
	assert.NotContains(t, out, workshopContentID)
	assert.Contains(t, out, "Workshop Item (revision of unknown) (ID: 3000000001) [target]")
}

// TestCheckExternalInstallExclusivity_NamesTheItemAndTheCommand: the
// refusal for installing an lmm copy of an item Steam already loads says
// which item, and the exact command that clears the way (#428).
func TestCheckExternalInstallExclusivity_NamesTheItemAndTheCommand(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000001")
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "3000000001", SourceID: "steamworkshop", Name: "ModMenu", Version: workshopContentID, GameID: game.ID,
		},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
		External: true, ExternalPath: t.TempDir(),
	}))

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), core.ReasonExternalAlreadyTracked)
	assert.Contains(t, err.Error(), "`lmm uninstall 3000000001 --source steamworkshop --game g1 --profile default`")
	assert.False(t, strings.Contains(err.Error(), workshopContentID))
}
