package core_test

// Tests for the read-only query types in internal/core/queries.go - the
// documents `lmm list`, `lmm status`, `lmm search`, `lmm game list` and
// `lmm verify` render (v2 Phase 3 Task 3, #301). Each query is asserted on
// its assembled fields here; its wire shape is pinned separately by
// TestJSONGoldens.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ListMods ---

// TestListMods_ProfileOrderAndIdentity covers the base case: the listing
// carries the game/profile it was asked for and its mods come back in
// OrderByProfile order (profile order, absent-from-profile first), not the
// DB's installed_at order.
func TestListMods_ProfileOrderAndIdentity(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", true, nil)
	// Profile order is the REVERSE of the install order, so a listing that
	// merely echoed the DB order would fail here.
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, list)

	assert.Equal(t, "g1", list.GameID)
	assert.Equal(t, "default", list.Profile)
	require.Len(t, list.Mods, 2)
	assert.Equal(t, "b", list.Mods[0].ID)
	assert.Equal(t, "a", list.Mods[1].ID)
	assert.Equal(t, "Mod B", list.Mods[0].Name, "the whole InstalledMod is embedded, not a bare reference")
}

// TestListMods_LockStateFromProfile covers the lock join: lock state lives on
// the profile YAML ref, not the DB row ListMods reads its mods from.
func TestListMods_LockStateFromProfile(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	require.NoError(t, svc.NewProfileManager().SetModLock("g1", "default", "src", "a", "1.0"))

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 2)

	byID := map[string]core.ModListing{}
	for _, m := range list.Mods {
		byID[m.ID] = m
	}
	assert.True(t, byID["a"].Locked)
	assert.Equal(t, "1.0", byID["a"].LockedVersion, "the lock's own version, from the profile ref")
	assert.False(t, byID["b"].Locked)
	assert.Empty(t, byID["b"].LockedVersion)
}

// TestListMods_ConvertPaksOnlyForCompileGames covers the tri-state
// ConvertPaks pointer: nil means "pak conversion does not apply here at
// all", which is every mod of a non-DeployCompile game.
func TestListMods_ConvertPaksOnlyForCompileGames(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 1)
	assert.Nil(t, list.Mods[0].ConvertPaks)
}

// TestListMods_MissingProfileYAMLIsNotAnError covers the tolerated case: a
// profile with no YAML on disk yet just means nothing is locked and every
// mod sorts as "absent from the load order" - not a failed listing.
func TestListMods_MissingProfileYAMLIsNotAnError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 1)
	assert.False(t, list.Mods[0].Locked)
}

// TestListMods_EmptyProfileHasEmptyMods pins that a profile with nothing
// installed still yields a listing (not a nil one) whose Mods marshals as
// [], the shape the --json contract promises.
func TestListMods_EmptyProfileHasEmptyMods(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, list)
	assert.Empty(t, list.Mods)
}

// --- Status / GameStatus ---

// TestStatus_GamesByIDWithCountsAndDefault covers the summary `lmm status`
// renders: one row per configured game, ordered by ID (#299), each carrying
// its profile names, its active profile's mod count and whether it is the
// default game.
func TestStatus_GamesByIDWithCountsAndDefault(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	// Saved out of ID order, so a report that echoed insertion (or map)
	// order would fail here.
	zeta := &domain.Game{ID: "zeta", Name: "Zeta", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	alpha := &domain.Game{ID: "alpha", Name: "Alpha", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, zeta))
	require.NoError(t, svc.SaveGame(ctx, alpha))
	require.NoError(t, svc.SetDefaultGame(ctx, "alpha"))

	seedNamedInstalledMod(t, svc, alpha, "src", "a", "Mod A", "1.0", true, nil)
	seedProfileWithMod(t, svc, "alpha", "default", "src", "a", "1.0")
	require.NoError(t, svc.NewProfileManager().SetDefault("alpha", "default"))

	report := svc.Status(ctx)
	require.NotNil(t, report)
	require.Len(t, report.Games, 2)

	assert.Equal(t, "alpha", report.Games[0].Game.ID)
	assert.Equal(t, "zeta", report.Games[1].Game.ID)
	assert.True(t, report.Games[0].IsDefault)
	assert.False(t, report.Games[1].IsDefault)
	assert.Equal(t, []string{"default"}, report.Games[0].Profiles)
	assert.Equal(t, 1, report.Games[0].ModCount)
	assert.Equal(t, 0, report.Games[1].ModCount)
	assert.Equal(t, domain.LinkSymlink, report.Games[0].LinkMethod)
}

// TestStatus_NoGamesIsEmptyNotNil pins the zero-games shape: an empty
// report, whose Games marshals as "[]" rather than "null".
func TestStatus_NoGamesIsEmptyNotNil(t *testing.T) {
	report := newFlowsTestService(t).Status(context.Background())
	require.NotNil(t, report)
	assert.Empty(t, report.Games)
}

// TestGameStatus_ActiveProfileDetail covers the per-game detail `lmm status
// --game X` renders: the profile list, the active profile's installed/
// enabled counts, and its never-deployed state.
func TestGameStatus_ActiveProfileDetail(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", false, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	require.NoError(t, svc.NewProfileManager().SetDefault("g1", "default"))

	st, err := svc.GameStatus(ctx, game)
	require.NoError(t, err)
	require.NotNil(t, st)

	assert.Equal(t, "g1", st.Game.ID)
	assert.Equal(t, "default", st.ActiveProfile)
	assert.Equal(t, 2, st.InstalledModCount)
	assert.Equal(t, 1, st.EnabledModCount)
	assert.Nil(t, st.LastDeploy, "a profile that has never been deployed reports no timestamp")
	assert.Equal(t, 0, st.ConversionFailures)
	require.Len(t, st.Profiles, 1)
	assert.Equal(t, "default", st.Profiles[0].Name)
	assert.Equal(t, 2, st.Profiles[0].ModCount)
	assert.True(t, st.Profiles[0].IsDefault)
}

// TestGameStatus_LinkMethodSource covers the three-level link-method
// resolution the detail view reports: global default, per-game override,
// per-profile override (#155/#81). LinkMethod always stays the GAME-level
// answer; only EffectiveLinkMethod follows the profile.
func TestGameStatus_LinkMethodSource(t *testing.T) {
	ctx := context.Background()

	t.Run("global", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
		require.NoError(t, svc.SaveGame(ctx, game))
		st, err := svc.GameStatus(ctx, game)
		require.NoError(t, err)
		assert.Equal(t, "global", st.LinkMethodSource)
		assert.Equal(t, st.LinkMethod, st.EffectiveLinkMethod)
	})

	t.Run("game", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
		require.NoError(t, svc.SaveGame(ctx, game))
		st, err := svc.GameStatus(ctx, game)
		require.NoError(t, err)
		assert.Equal(t, "game", st.LinkMethodSource)
		assert.Equal(t, domain.LinkCopy, st.LinkMethod)
		assert.Equal(t, domain.LinkCopy, st.EffectiveLinkMethod)
	})

	t.Run("profile", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
		require.NoError(t, svc.SaveGame(ctx, game))
		pm := svc.NewProfileManager()
		_, err := pm.Create("g1", "default")
		require.NoError(t, err)
		setProfileLinkMethod(t, svc, "g1", "default", domain.LinkHardlink)
		require.NoError(t, pm.SetDefault("g1", "default"))

		st, err := svc.GameStatus(ctx, game)
		require.NoError(t, err)
		assert.Equal(t, "profile", st.LinkMethodSource)
		assert.Equal(t, domain.LinkCopy, st.LinkMethod, "LinkMethod stays the game-level answer (#155)")
		assert.Equal(t, domain.LinkHardlink, st.EffectiveLinkMethod)
	})
}

// TestGameStatus_NoProfilesIsEmptyNotNil pins the zero-profiles shape: a
// game with no profiles yet reports an empty list (marshalling as "[]") and
// no active profile, not an error.
func TestGameStatus_NoProfilesIsEmptyNotNil(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(ctx, game))

	st, err := svc.GameStatus(ctx, game)
	require.NoError(t, err)
	assert.Empty(t, st.Profiles)
	assert.Empty(t, st.ActiveProfile)
	assert.Equal(t, svc.GetGameCachePath(game), st.CachePath)
}
