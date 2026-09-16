package core_test

// #446: a game's active profile is the one profile file saying
// `is_default: true`. `lmm profile import --force` over the active profile's
// name cleared it, leaving the game with none, and every flow that asks
// "which profile is active" fell back to a guess. These tests pin that no
// lmm write path leaves a game with zero or two active profiles - and that
// a game a hand edit left that way is reported, not silently guessed at.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defaultsOf returns the profiles of gameID whose file says
// `is_default: true`, sorted.
func defaultsOf(t *testing.T, svc *core.Service, gameID string) []string {
	t.Helper()
	profiles, err := svc.NewProfileManager().List(context.Background(), gameID)
	require.NoError(t, err)
	var flagged []string
	for _, p := range profiles {
		if p.IsDefault {
			flagged = append(flagged, p.Name)
		}
	}
	slices.Sort(flagged)
	return flagged
}

// profileFilePath is where gameID's profile name is stored.
func profileFilePath(svc *core.Service, gameID, name string) string {
	return filepath.Join(svc.ConfigDir(), "games", gameID, "profiles", name+".yaml")
}

// twoProfiles is a service with a game whose profiles "a" and "b" exist and
// "a" is active.
func twoProfiles(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	pm := svc.NewProfileManager()
	for _, name := range []string{"a", "b"} {
		_, err := pm.Create(context.Background(), game.ID, name)
		require.NoError(t, err)
	}
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "a"))
	return svc, game
}

func TestImportForce_OverTheActiveProfileKeepsItActive(t *testing.T) {
	ctx := context.Background()
	svc, game := twoProfiles(t)
	doc := []byte("name: a\ngame_id: g1\nmods: []\n")

	plan, err := svc.PlanImport(ctx, game, doc)
	require.NoError(t, err)
	require.True(t, plan.Exists)
	_, err = svc.ApplyImport(ctx, game, plan, core.ProfileImportOptions{Force: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"a"}, defaultsOf(t, svc, game.ID), "the replaced profile is still the active one")

	// ...and one that replaces a profile that is not active leaves it so.
	_, err = svc.NewProfileManager().ImportWithOptions(ctx, []byte("name: b\ngame_id: g1\nmods: []\n"), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, defaultsOf(t, svc, game.ID))
}

// TestCreateOrResetDefault_LeavesAnotherActiveProfileActive: `lmm game add`
// or `game detect` re-run on a configured game resets its "default"
// profile, and marked it active whatever profile already was - two.
func TestCreateOrResetDefault_LeavesAnotherActiveProfileActive(t *testing.T) {
	ctx := context.Background()
	svc := newFlowsTestService(t)
	pm := svc.NewProfileManager()

	_, err := pm.CreateOrResetDefault(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, defaultsOf(t, svc, "g1"), "a new game's default profile is its active one")

	_, err = pm.Create(ctx, "g1", "alt")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g1", "alt"))
	reset, err := pm.CreateOrResetDefault(ctx, "g1")
	require.NoError(t, err)
	assert.False(t, reset.IsDefault)
	assert.Equal(t, []string{"alt"}, defaultsOf(t, svc, "g1"), "a reset does not take the active profile away from alt")

	require.NoError(t, pm.SetDefault(ctx, "g1", "default"))
	reset, err = pm.CreateOrResetDefault(ctx, "g1")
	require.NoError(t, err)
	assert.True(t, reset.IsDefault)
	assert.Equal(t, []string{"default"}, defaultsOf(t, svc, "g1"), "and keeps it when default already had it")
}

func TestDeleteProfile_TheActiveProfileIsRefused(t *testing.T) {
	ctx := context.Background()

	t.Run("with another profile", func(t *testing.T) {
		svc, game := twoProfiles(t)
		_, err := svc.DeleteProfile(ctx, game.ID, "a")
		require.ErrorIs(t, err, core.ErrProfileActive)
		assert.Contains(t, err.Error(), "lmm profile switch")
		err = svc.NewProfileManager().Delete(ctx, game.ID, "a")
		require.ErrorIs(t, err, core.ErrProfileActive, "the CLI's direct path too")
		assert.Equal(t, []string{"a"}, defaultsOf(t, svc, game.ID))

		_, err = svc.DeleteProfile(ctx, game.ID, "b")
		require.NoError(t, err, "a profile that is not active can go")
	})

	t.Run("the only profile, with mods installed", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
		seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, nil)
		seedProfileWithMod(t, svc, game.ID, "default", "src", "m1", "1.0")
		_, err := svc.DeleteProfile(ctx, game.ID, "default")
		require.ErrorIs(t, err, core.ErrProfileActive, "its mods would be left with no profile")
	})

	t.Run("the only profile, empty", func(t *testing.T) {
		svc := newFlowsTestService(t)
		_, err := svc.NewProfileManager().Create(ctx, "g1", "lonely")
		require.NoError(t, err)
		_, err = svc.DeleteProfile(ctx, "g1", "lonely")
		require.NoError(t, err, "nothing depends on it")
	})
}

// TestSetDefault_AProfileItCannotClearIsReported: set-default marks the new
// profile first, so a failure never leaves the game with no active profile;
// one it then cannot unmark leaves two, and the error says which.
func TestSetDefault_AProfileItCannotClearIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only file is writable by root")
	}
	ctx := context.Background()
	svc, game := twoProfiles(t)
	path := profileFilePath(svc, game.ID, "a")
	require.NoError(t, os.Chmod(path, 0o444))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	err := svc.NewProfileManager().SetDefault(ctx, game.ID, "b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"a"`)
	assert.Contains(t, err.Error(), "both")
	assert.Equal(t, []string{"a", "b"}, defaultsOf(t, svc, game.ID))

	listing, err := svc.ListProfiles(ctx, game.ID)
	require.NoError(t, err)
	require.Len(t, listing.Warnings, 1, "profile list says so too")
	assert.Contains(t, listing.Warnings[0], "a, b")
}

func TestListProfiles_ReportsAGameWithoutExactlyOneActiveProfile(t *testing.T) {
	ctx := context.Background()
	svc := newFlowsTestService(t)
	pm := svc.NewProfileManager()

	listing, err := svc.ListProfiles(ctx, "g1")
	require.NoError(t, err)
	assert.Empty(t, listing.Warnings, "no profiles at all is not a problem")

	for _, name := range []string{"x", "y"} {
		_, err := pm.Create(ctx, "g1", name)
		require.NoError(t, err)
	}
	listing, err = svc.ListProfiles(ctx, "g1")
	require.NoError(t, err)
	require.Len(t, listing.Warnings, 1)
	assert.Contains(t, listing.Warnings[0], "no profile")
	assert.Contains(t, listing.Warnings[0], `"x"`, "it names the profile lmm is treating as active")
	assert.Contains(t, listing.Warnings[0], "lmm profile switch")

	require.NoError(t, pm.SetDefault(ctx, "g1", "y"))
	listing, err = svc.ListProfiles(ctx, "g1")
	require.NoError(t, err)
	assert.Empty(t, listing.Warnings)
}
