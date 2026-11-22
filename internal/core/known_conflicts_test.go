package core_test

// #668: known (intended) conflicts. A directed mod pair, "winner overrides
// loser", marks every path the two share as intended; a path is known when
// its load-order winner is marked over each other provider. A reorder that
// flips the winner un-knows it.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func twinShared(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	return seedTwinConflict(t, svc,
		map[string][]byte{"shared.esp": []byte("X"), "other.esp": []byte("X")},
		map[string][]byte{"shared.esp": []byte("Y"), "other.esp": []byte("Y")},
	)
}

func knownOf(t *testing.T, svc *core.Service, game *domain.Game) []bool {
	t.Helper()
	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	var out []bool
	for _, c := range conflicts {
		out = append(out, c.Known)
	}
	return out
}

func TestKnownConflicts_AMarkedPairIsReportedKnown(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()
	assert.Equal(t, []bool{false, false}, knownOf(t, svc, game), "nothing is known until marked")

	// Bare mod ids resolve as a reorder's do.
	result, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modY", Loser: "modX"}})
	require.NoError(t, err)
	want := domain.KnownConflict{Winner: "src:modY", Loser: "src:modX", Origin: domain.KnownConflictUser}
	assert.Equal(t, domain.KnownConflicts{want}, result.Added)
	assert.Empty(t, result.Removed)
	assert.Equal(t, domain.KnownConflicts{want}, result.KnownConflicts)
	assert.Equal(t, []bool{true, true}, knownOf(t, svc, game), "one pair covers every path the two share")

	saved, err := config.LoadProfile(svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{want}, saved.KnownConflicts)

	// Marking it again changes nothing.
	result, err = svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "src:modY", Loser: "src:modX"}})
	require.NoError(t, err)
	assert.Empty(t, result.Added)
}

func TestKnownConflicts_AReorderThatFlipsTheWinnerUnknowsIt(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()
	_, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "src:modY", Loser: "src:modX"}})
	require.NoError(t, err)

	flipped := []domain.ModReference{{SourceID: "src", ModID: "modY", Version: "1.0"}, {SourceID: "src", ModID: "modX", Version: "1.0"}}
	preview, err := svc.GetProfileConflictsForOrder(ctx, game, "default", flipped)
	require.NoError(t, err)
	require.Len(t, preview, 2)
	assert.False(t, preview[0].Known, "the reorder preview shows the flipped pair as a conflict again")

	require.NoError(t, svc.ReorderProfileMods(ctx, "g1", "default", flipped))
	assert.Equal(t, []bool{false, false}, knownOf(t, svc, game), "modX wins now, which was never marked")

	// Marking the new direction replaces the old entry: a pair has one.
	result, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modX", Loser: "modY"}})
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:modY", Loser: "src:modX", Origin: domain.KnownConflictUser}}, result.Removed)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:modX", Loser: "src:modY", Origin: domain.KnownConflictUser}}, result.KnownConflicts)
	assert.Equal(t, []bool{true, true}, knownOf(t, svc, game))
}

func TestKnownConflicts_Unmark(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()
	_, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modY", Loser: "modX"}})
	require.NoError(t, err)

	// Either way round names the pair.
	result, err := svc.UnmarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modX", Loser: "modY"}})
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:modY", Loser: "src:modX", Origin: domain.KnownConflictUser}}, result.Removed)
	assert.Empty(t, result.KnownConflicts)
	assert.NotNil(t, result.KnownConflicts, "an empty list is [] on the wire")
	assert.Equal(t, []bool{false, false}, knownOf(t, svc, game))

	result, err = svc.UnmarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modX", Loser: "modY"}})
	require.NoError(t, err)
	assert.Empty(t, result.Removed, "unmarking a pair that is not marked removes nothing")
}

func TestKnownConflicts_MarkCurrentAcceptsEveryConflictAsItStands(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()
	result, err := svc.MarkCurrentConflictsKnown(ctx, game, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:modY", Loser: "src:modX", Origin: domain.KnownConflictUser}}, result.Added,
		"one entry per pair, however many paths it shares")
	assert.Equal(t, []bool{true, true}, knownOf(t, svc, game))
}

func TestKnownConflicts_APathIsKnownOnlyWhenItsWinnerIsMarkedOverEveryProvider(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	for _, id := range []string{"modA", "modB", "modC"} {
		seedNamedInstalledMod(t, svc, game, "src", id, "Mod "+id, "1.0", true, map[string][]byte{"shared.esp": []byte(id)})
		seedProfileWithMod(t, svc, "g1", "default", "src", id, "1.0")
	}
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	_, err = svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modC", Loser: "modA"}})
	require.NoError(t, err)
	assert.Equal(t, []bool{false}, knownOf(t, svc, game), "modC over modB is not marked")

	_, err = svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modC", Loser: "modB"}})
	require.NoError(t, err)
	assert.Equal(t, []bool{true}, knownOf(t, svc, game))

	// B over A, a pair the winner is not part of, changes nothing.
	_, err = svc.UnmarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modC", Loser: "modB"}})
	require.NoError(t, err)
	_, err = svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modB", Loser: "modA"}})
	require.NoError(t, err)
	assert.Equal(t, []bool{false}, knownOf(t, svc, game))
}

func TestKnownConflicts_BadPairsAreRefused(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()

	_, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modX", Loser: "modX"}})
	assert.ErrorIs(t, err, core.ErrSameModPair)
	_, err = svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modX", Loser: "nope"}})
	assert.ErrorIs(t, err, core.ErrModNotInProfile)
	_, err = svc.MarkConflictsKnown(ctx, game, "default", nil)
	assert.ErrorIs(t, err, core.ErrNoConflictPairs)
	_, err = svc.UnmarkConflictsKnown(ctx, game, "default", nil)
	assert.ErrorIs(t, err, core.ErrNoConflictPairs)

	saved, err := config.LoadProfile(svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, saved.KnownConflicts, "a refused request writes nothing")
}

func TestKnownConflicts_ARemovedModTakesItsPairsWithIt(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()
	_, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modY", Loser: "modX"}})
	require.NoError(t, err)

	require.NoError(t, svc.NewProfileManager().RemoveMod(ctx, "g1", "default", "src", "modX"))
	saved, err := config.LoadProfile(svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, saved.KnownConflicts, "the entry naming the removed mod is dropped")
}

func TestKnownConflicts_DuplicateCarriesThem(t *testing.T) {
	svc := newFlowsTestService(t)
	game := twinShared(t, svc)
	ctx := context.Background()
	_, err := svc.MarkConflictsKnown(ctx, game, "default", []core.ConflictPair{{Winner: "modY", Loser: "modX"}})
	require.NoError(t, err)
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.DuplicateProfile(ctx, game.ID, "default", "copy")
	require.NoError(t, err)
	dup, err := config.LoadProfile(svc.ConfigDir(), "g1", "copy")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:modY", Loser: "src:modX", Origin: domain.KnownConflictUser}}, dup.KnownConflicts)
}

// TestKnownConflicts_ARelinkMovesTheMarks: a re-link (#593) renames the
// mod's marks with its reference, so the next save does not drop them as
// naming a mod the profile no longer has.
func TestKnownConflicts_ARelinkMovesTheMarks(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := newModDetailTestService(t)
	seedModDetailInstalled(t, svc, game, "a", "1.5")
	game.SourceIDs["src"] = game.ID

	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	profile.Mods = append(profile.Mods, domain.ModReference{SourceID: "src", ModID: "z", Version: "1"})
	profile.KnownConflicts = domain.KnownConflicts{{Winner: "src:z", Loser: "src:a", Origin: domain.KnownConflictUser}}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	plan, err := svc.PlanRelinkMod(ctx, game, "default", "src", "a", "src", "b")
	require.NoError(t, err)
	_, err = svc.ApplyRelinkMod(ctx, game, plan, core.RelinkOptions{}, nil)
	require.NoError(t, err)

	// Any later save keeps them.
	require.NoError(t, svc.NewProfileManager().SetModDisabled(ctx, game.ID, "default", "src", "z", true))
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:z", Loser: "src:b", Origin: domain.KnownConflictUser}}, saved.KnownConflicts)
}

// TestKnownConflicts_MarkCurrentSkipsAModTheProfileNoLongerLists: a
// conflict's owner is the deployed-files record's, which can name a mod
// removed from the profile since the last deploy. --all-current marks the
// rest instead of refusing the batch.
func TestKnownConflicts_MarkCurrentSkipsAModTheProfileNoLongerLists(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	for _, id := range []string{"modX", "modZ", "modY"} {
		seedNamedInstalledMod(t, svc, game, "src", id, "Mod "+id, "1.0", true, map[string][]byte{"shared.esp": []byte(id)})
		seedProfileWithMod(t, svc, "g1", "default", "src", id, "1.0")
	}
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	// modY, the deployed owner, leaves the profile without a redeploy.
	profile, err := config.LoadProfile(svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	profile.Mods = profile.Mods[:2]
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	result, err := svc.MarkCurrentConflictsKnown(ctx, game, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.KnownConflicts{{Winner: "src:modZ", Loser: "src:modX", Origin: domain.KnownConflictUser}}, result.Added)
}
