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
