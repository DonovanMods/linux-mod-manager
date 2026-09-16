package core_test

// The one-time profile-document backfill (#431, fix-round F2). Every mod a
// user disabled BEFORE the `disabled:` marker existed recorded that intent
// in one place only - the installed_mods row - because the profile document
// had no key for it. A converge run reads the document, so on the first
// switch or apply after the upgrade every such mod would be switched back
// on: the owner's reported symptom, reproducing once per already-disabled
// mod. Nothing in the document can be read as "off" for them, and nothing
// in it can be read as "on" either, so the row is the only evidence of what
// the user asked for - and it is turned into an explicit marker once,
// rather than by permanently teaching every plan to prefer the row over the
// document (which would mean an apply could never enable a mod again).

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

// TestBackfillProfileDisabledMarkers_MarksAPreMarkerDisabledMod is the F2
// regression: a disabled row with an unmarked ref is a mod disabled before
// the marker shipped, and the backfill records it in the document.
func TestBackfillProfileDisabledMarkers_MarksAPreMarkerDisabledMod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()
	pm := svc.NewProfileManager()

	// Exactly what an lmm that predates the marker leaves behind: the row
	// says off, the document says nothing.
	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	seedInstalledMod(t, svc, game, "src", "on", "1.0", true, map[string][]byte{"on.esp": []byte("y")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "off", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "on", "1.0")

	marked, err := svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, marked)

	profile, err := pm.Get(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)
	byID := map[string]domain.ModReference{}
	for _, ref := range profile.Mods {
		byID[ref.ModID] = ref
	}
	assert.True(t, byID["off"].Disabled, "the row said off, so the document must now say off too")
	assert.False(t, byID["on"].Disabled, "an enabled mod is left exactly as it was")
	assert.Equal(t, "1.0", byID["off"].Version, "and the backfill touches nothing else on the ref")
}

// TestBackfillProfileDisabledMarkers_ConvergeRunsThenLeaveTheModOff is the
// symptom the backfill exists to stop, end to end: before it ran, the first
// `profile apply` or `profile switch` after the upgrade re-enabled the mod.
func TestBackfillProfileDisabledMarkers_ConvergeRunsThenLeaveTheModOff(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	ctx := context.Background()
	pm := svc.NewProfileManager()

	_, err := pm.Create(ctx, game.ID, "a")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "a"))
	_, err = pm.Create(ctx, game.ID, "b")
	require.NoError(t, err)

	// Disabled under "a" the pre-marker way, and listed by both profiles -
	// the shared-mod shape, where the plan would otherwise schedule an
	// enable under whichever profile is switched to.
	seedInstalledModUnderProfile(t, svc, game, "a", "src", "off", "Off Mod", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	require.NoError(t, pm.AddMod(ctx, game.ID, "a", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "b", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))

	_, err = svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)

	applyPlan, err := svc.PlanProfileApply(ctx, game, "a")
	require.NoError(t, err)
	assert.Empty(t, applyPlan.ToEnable, "`lmm profile apply` must not switch a pre-marker disabled mod back on")
	assert.Empty(t, applyPlan.ToInstall)
	assert.True(t, applyPlan.NoChanges)

	// The other profile lists it too and has no row of its own, so a switch
	// to "b" would have enabled and deployed it. The document that "b"
	// carries has no marker of its own, though - the backfill only speaks
	// for the profile whose row said off - so this asserts the sync path
	// instead, which is where the destructive case lives.
	syncPlan, err := svc.PlanProfileSync(ctx, game, "a")
	require.NoError(t, err)
	assert.Empty(t, syncPlan.ToRemove,
		"`lmm profile sync` must not prune the ref, its load-order position and its pinned version")
	assert.True(t, syncPlan.NoChanges)

	assert.NoFileExists(t, filepath.Join(gameDir, "off.esp"))
}

// TestBackfillProfileDisabledMarkers_RunsOnceAndOnlyOnce pins the durable
// obligation marker: the backfill is a migration, not a rule. Running it
// again must not re-mark a mod whose marker the user has since cleared by
// hand - which is the whole reason it is one-time rather than a plan-time
// preference for the row over the document.
func TestBackfillProfileDisabledMarkers_RunsOnceAndOnlyOnce(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "off", "1.0")

	marked, err := svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, marked)

	// The user edits the marker away by hand (docs/configuration.md invites
	// exactly this), leaving the row still saying off.
	require.NoError(t, pm.SetModDisabled(ctx, "g1", "default", "src", "off", false))

	marked, err = svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, marked, "the obligation is discharged: a second run must do nothing")

	profile, err := pm.Get(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Disabled, "and the hand edit must stand")
}

// TestBackfillProfileDisabledMarkers_NothingToDoLeavesEveryFileUntouched is
// the backward-compatibility half: an installation with no disabled mod has
// nothing to record, and the backfill must not rewrite a byte of any
// profile file (nor of one whose mods it does not list a row for).
func TestBackfillProfileDisabledMarkers_NothingToDoLeavesEveryFileUntouched(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()

	seedInstalledMod(t, svc, game, "src", "on", "1.0", true, map[string][]byte{"on.esp": []byte("y")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "on", "1.0")
	// A row the profile never listed, disabled: there is no desired-state
	// entry to mark, which is not a failure.
	seedInstalledMod(t, svc, game, "src", "unlisted", "1.0", false, nil)

	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	before, err := os.ReadFile(profilePath)
	require.NoError(t, err)

	marked, err := svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, marked)

	after, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "nothing to record means nothing written")
}

// TestBackfillProfileDisabledMarkers_SkipsExternalRows pins #269's rule
// through the migration: lmm cannot switch a Steam Workshop item off, so an
// external row must never gain a marker that would read as an off intent
// the user could not have expressed through lmm at all.
func TestBackfillProfileDisabledMarkers_SkipsExternalRows(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()
	pm := svc.NewProfileManager()

	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "ws", SourceID: "steamworkshop", Name: "Workshop Item", Version: "12345", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
		External:     true,
		ExternalPath: "/steam/workshop/content/1/12345",
	}))
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "steamworkshop", ModID: "ws"}))

	marked, err := svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, marked)

	profile, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Disabled)
}

// TestBackfillProfileDisabledMarkers_MarkerSurvivesTheToggleRoundTrip is
// the seam between the migration and the flows it feeds: once backfilled,
// the mod behaves exactly like one disabled by today's `lmm mod disable`,
// including being recoverable by `lmm mod enable`.
func TestBackfillProfileDisabledMarkers_MarkerSurvivesTheToggleRoundTrip(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()
	pm := svc.NewProfileManager()

	seedInstalledMod(t, svc, game, "src", "off", "1.0", false, map[string][]byte{"off.esp": []byte("x")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "off", "1.0")

	_, err := svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)

	result, err := svc.EnableMod(ctx, game, "default", "src", "off")
	require.NoError(t, err)
	assert.True(t, result.Changed)

	profile, err := pm.Get(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Disabled, "`lmm mod enable` clears a backfilled marker like any other")

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.Empty(t, plan.ToDisable)
	_ = core.ProfileApplyOptions{}
}
