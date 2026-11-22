package core_test

// #666: an import into a profile that is not the game's active one records
// the profile and deploys nothing (#462). ProfileImportOptions.Switch then
// switches to it once the import finishes - `lmm profile switch`'s own
// Plan/Apply, so the active profile's files come down and the imported
// profile deploys and becomes active - and a switch that fails never undoes
// the import: its error is reported beside the import's result.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importDoc is a version-0 profile document for g1 named name, listing
// each mod of the fixture's source at 1.0.
func importDoc(name string, mods ...string) []byte {
	doc := "name: " + name + "\ngame_id: g1\nmods:\n"
	for _, m := range mods {
		doc += "  - source_id: src\n    mod_id: " + m + "\n    version: \"1.0\"\n"
	}
	return []byte(doc)
}

// activeProfile is the name of the game's active profile.
func activeProfile(t *testing.T, svc *core.Service, gameID string) string {
	t.Helper()
	p, err := svc.NewProfileManager().GetDefault(context.Background(), gameID)
	require.NoError(t, err)
	return p.Name
}

func TestImportSwitch_TheImportedProfileBecomesActiveAndDeployed(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)

	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly"))
	require.NoError(t, err)
	require.True(t, plan.RecordedOnly, "c is not the active profile")

	var phases []core.DeployPhase
	sink := func(e core.Event) {
		switch ev := e.(type) {
		case core.StepEvent:
			phases = append(phases, ev.Phase)
		case core.ModEvent:
			phases = append(phases, ev.Phase)
		}
	}
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Switch: true}, sink)
	require.NoError(t, err)

	assert.True(t, result.RecordedOnly, "the import itself recorded, as before")
	assert.Equal(t, 1, result.Recorded)
	require.NotNil(t, result.Switch, "the switch is reported beside the import")
	assert.True(t, result.Switch.Switched)
	assert.Equal(t, "a", result.Switch.From)
	assert.Equal(t, "c", result.Switch.To)
	assert.Empty(t, result.Switch.Error)
	require.NotNil(t, result.Switch.Result)
	assert.Equal(t, 1, result.Switch.Result.Enabled, "the recorded row is enabled and deployed")

	assert.Equal(t, "c", activeProfile(t, f.svc, f.game.ID))
	assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"), "the imported profile is deployed")
	assert.NoFileExists(t, filepath.Join(f.gameDir, "aonly.esp"), "the old profile's files are gone")
	assert.NoFileExists(t, filepath.Join(f.gameDir, "shared.esp"))
	assert.True(t, f.installedRow(t, "c", "bonly").Deployed)
	assert.False(t, f.installedRow(t, "a", "aonly").Deployed)
	assert.Contains(t, phases, core.ImportSwitching, "the switch announces itself")
	assert.Contains(t, phases, core.SwitchDisabled, "and reports its own steps")
}

func TestImportSwitch_WithoutTheOptionNothingSwitches(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)
	before := treeOf(t, f.gameDir)

	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly"))
	require.NoError(t, err)
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	assert.Nil(t, result.Switch)
	assert.Equal(t, "a", activeProfile(t, f.svc, f.game.ID))
	assert.Equal(t, before, treeOf(t, f.gameDir))
}

func TestImportSwitch_IntoTheActiveProfileHasNothingToSwitch(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)

	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("a", "aonly", "shared"))
	require.NoError(t, err)
	require.False(t, plan.RecordedOnly)
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Force: true, Switch: true}, nil)
	require.NoError(t, err)

	assert.Nil(t, result.Switch, "the import deployed into the active profile itself")
	assert.Equal(t, "a", activeProfile(t, f.svc, f.game.ID))
}

func TestImportSwitch_AFailedSwitchNeverUndoesTheImport(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)

	// "gone" is a mod no source can fetch: the import records the profile
	// and fails it, and the switch, retrying it, fails it again.
	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly", "gone"))
	require.NoError(t, err)
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Switch: true}, nil)

	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete, "the import's own outcome is unchanged by the switch")
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 1, result.Recorded)
	require.NotNil(t, result.Switch)
	assert.True(t, result.Switch.Switched, "the profile is active - an incomplete switch still makes it so")
	assert.Contains(t, result.Switch.Error, "gone", "the switch's own error is reported beside the import")
	require.NotNil(t, result.Switch.Result)
	require.Len(t, result.Switch.Result.Failed, 1)

	saved, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "c")
	require.NoError(t, err)
	assert.Len(t, saved.Mods, 2, "the imported profile stands")
	assert.Equal(t, "c", activeProfile(t, f.svc, f.game.ID))
	assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
}

func TestImportSwitch_ASwitchThatCannotRunLeavesTheImport(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)

	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly"))
	require.NoError(t, err)
	// A profile file nothing can read refuses every switch plan: broken
	// once the import is done, as the switch begins.
	sink := func(e core.Event) {
		if s, ok := e.(core.StepEvent); ok && s.Phase == core.ImportSwitching {
			f.breakProfile(t, "b")
		}
	}
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Switch: true}, sink)
	require.NoError(t, err, "the import itself succeeded")

	require.NotNil(t, result.Switch)
	assert.False(t, result.Switch.Switched)
	assert.NotEmpty(t, result.Switch.Error)
	assert.Equal(t, 1, result.Recorded)
	assert.False(t, f.installedRow(t, "c", "bonly").Deployed, "recorded, not deployed")
	assert.FileExists(t, filepath.Join(f.gameDir, "aonly.esp"), "the active profile's files stay")
}

func TestImportSwitch_AnUnfinishedCollectionSwitchesWithItsWarning(t *testing.T) {
	ctx := context.Background()
	f := newTodoFixture(t)
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, f.game.ID, "Live")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, f.game.ID, "Live"))

	plan := f.plan(t, core.SourceCollectionImportRequest{WithOptional: true})
	require.True(t, plan.RecordedOnly)
	result, err := f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Switch: true}, nil)

	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete, "the collection is left unfinished")
	require.Len(t, result.Todos, 2)
	require.NotNil(t, result.Switch)
	assert.True(t, result.Switch.Switched, "an unfinished collection still switches (#660)")
	assert.Empty(t, result.Switch.Error, "its to-dos are not the switch's failures")
	require.NotNil(t, result.Switch.Unresolved, "with the warning naming them")
	assert.Contains(t, result.Switch.Unresolved.Warning, "collection incomplete: 2 item(s) unresolved")
	assert.Equal(t, "Three Mods", activeProfile(t, f.svc, f.game.ID))
	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "10", f.game.ID, "Three Mods")
	require.NoError(t, err)
	assert.True(t, row.Deployed, "the item that installed is deployed")
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", f.game.ID, "Three Mods")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "a to-do stays to finish")
}

func TestImportSwitch_AnImportThatLeftItsModsPendingDoesNotSwitch(t *testing.T) {
	for name, opts := range map[string]core.ProfileImportOptions{
		"install not asked for": {Switch: true},
		"no install":            {Switch: true, Install: true, NoInstall: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := liveDirFixture(t)
			before := treeOf(t, f.gameDir)

			plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly"))
			require.NoError(t, err)
			result, err := f.svc.ApplyImport(ctx, f.game, plan, opts, nil)
			require.NoError(t, err)

			assert.Equal(t, 1, result.Skipped)
			require.NotNil(t, result.Switch)
			assert.False(t, result.Switch.Switched, "a switch would install what the import was told not to")
			assert.Equal(t, core.ErrImportSwitchUninstalled.Error(), result.Switch.Error)
			assert.Equal(t, "a", activeProfile(t, f.svc, f.game.ID))
			assert.Equal(t, before, treeOf(t, f.gameDir))
		})
	}
}

// #666 review: the switch is planned and applied under one mutation slot,
// so a job queued behind the import - here a disable in the outgoing
// profile - waits for it rather than leaving the switch's plan stale.
func TestImportSwitch_AJobQueuedBehindTheImportWaitsForTheSwitch(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)

	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly"))
	require.NoError(t, err)

	queued := make(chan error, 1)
	var ranInside bool
	f.svc.SetImportSwitchPlannedForTest(func() {
		go func() {
			_, err := f.svc.DisableMod(ctx, f.game, "a", "src", "aonly")
			queued <- err
		}()
		select {
		case err := <-queued:
			// It ran between the plan and the apply: the slot was not
			// held. Hand its result back for the assertions below.
			ranInside = true
			queued <- err
		case <-time.After(300 * time.Millisecond):
		}
	})
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Switch: true}, nil)
	require.NoError(t, err)

	assert.False(t, ranInside, "nothing runs between the switch's plan and its apply")
	require.NotNil(t, result.Switch)
	assert.True(t, result.Switch.Switched)
	assert.Empty(t, result.Switch.Error, "the switch is never refused as stale")
	assert.Equal(t, "c", activeProfile(t, f.svc, f.game.ID))
	require.NoError(t, <-queued, "the queued job ran once the switch was done")
}

// #666 review: a job that runs after the import's slot is released and
// before the switch takes its own is seen by the switch's plan.
func TestImportSwitch_AJobBetweenTheImportAndTheSwitchIsPlannedFor(t *testing.T) {
	ctx := context.Background()
	f := liveDirFixture(t)

	plan, err := f.svc.PlanImport(ctx, f.game, importDoc("c", "bonly"))
	require.NoError(t, err)
	sink := func(e core.Event) {
		if s, ok := e.(core.StepEvent); ok && s.Phase == core.ImportSwitching {
			_, err := f.svc.DisableMod(ctx, f.game, "a", "src", "aonly")
			require.NoError(t, err)
		}
	}
	result, err := f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Switch: true}, sink)
	require.NoError(t, err)

	require.NotNil(t, result.Switch)
	assert.True(t, result.Switch.Switched)
	assert.Empty(t, result.Switch.Error)
	assert.Equal(t, "c", activeProfile(t, f.svc, f.game.ID))
	assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
}
